package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// Local credentials must never follow redirects or environment HTTP proxies.
func daemonClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func daemonJSON(ctx context.Context, client *http.Client, cfg config, method, path string, input, output any) error {
	return daemonJSONWithCredential(ctx, client, cfg, method, path, "Authorization", "Bearer "+cfg.Token, input, output)
}

func daemonJSONWithCredential(ctx context.Context, client *http.Client, cfg config, method, path, credentialHeader, credential string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return errors.New("could not encode daemon request")
		}
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.baseURL()+path, body)
	if err != nil {
		return errors.New("could not construct daemon request")
	}
	req.Header.Set(credentialHeader, credential)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("the Telegram MCP daemon is not answering; start `telegram-mcp serve` or `telegram-mcp service install`")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("daemon request refused (HTTP " + http.StatusText(resp.StatusCode) + ")")
	}
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(output); err != nil {
		return errors.New("invalid daemon response")
	}
	return nil
}

// openCommand keeps process arguments and terminal output at the CLI boundary.
func openCommand(ctx context.Context, cfg config, open func(string) error) error {
	return runOpen(ctx, cfg, os.Args[2:], runtime.GOOS, os.Getenv, os.Stdout, os.Stderr, open)
}

func runOpen(ctx context.Context, cfg config, args []string, goos string, getenv func(string) string, out, warnings io.Writer, open func(string) error) error {
	printURL := len(args) == 1 && args[0] == "--print"
	if len(args) != 0 && !printURL {
		return errors.New("usage: telegram-mcp open [--print]")
	}
	if goos == "linux" && getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" && !printURL {
		return errors.New(i18n.T("Nenhuma tela de navegador está disponível. Execute `telegram-mcp open --print` para imprimir um link de uso único do painel."))
	}
	if !printURL {
		return openPanel(ctx, cfg, open)
	}
	return openPanel(ctx, cfg, func(target string) error {
		// This is an explicit capability disclosure, not a log entry. The
		// persistent panel bootstrap authority never leaves the local request.
		if _, err := fmt.Fprintf(warnings, i18n.T("Aviso: qualquer pessoa que possua esta URL de uso único obtém acesso ao painel. Ela expira em um minuto. Não compartilhe nem registre esta URL.\nPara acesso remoto, primeiro encaminhe a mesma porta de loopback:\n  ssh -L %d:127.0.0.1:%d user@server\nDepois abra a URL no navegador local.\n"), cfg.Port, cfg.Port); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, target)
		return err
	})
}

func openPanel(ctx context.Context, cfg config, open func(string) error) error {
	if cfg.PanelBootstrapSecret == "" {
		secret, err := httpserver.ReadPanelBootstrapSecret(cfg.DataDir)
		if err != nil {
			return fmt.Errorf("cannot load panel bootstrap authority (start `telegram-mcp serve` first): %w", err)
		}
		cfg.PanelBootstrapSecret = secret
	}
	client := daemonClient()
	defer client.CloseIdleConnections()
	var result struct {
		URL string `json:"url"`
	}
	if err := daemonJSONWithCredential(ctx, client, cfg, http.MethodPost, "/api/session/bootstrap", httpserver.PanelBootstrapHeader, cfg.PanelBootstrapSecret, struct{}{}, &result); err != nil {
		return err
	}
	u, err := url.Parse(result.URL)
	expected, _ := url.Parse(cfg.baseURL())
	if err != nil || u.Scheme != expected.Scheme || u.Host != expected.Host || u.User != nil || u.Path != "/bootstrap" || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" {
		return errors.New("invalid panel bootstrap URL")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["c"]) != 1 || q.Get("c") == "" {
		return errors.New("invalid panel bootstrap URL")
	}
	if err := open(result.URL); err != nil {
		return errors.New("could not open the browser; run `telegram-mcp open` again")
	}
	return nil
}
