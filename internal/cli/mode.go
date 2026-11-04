package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
)

// config set is deliberately a local human CLI action, not an MCP tool. It
// establishes the same ephemeral session and CSRF proof as the panel, never a
// bearer-only mode bypass. The panel owns persistence and Controller.SetMode.
func configCmd(ctx context.Context, cfg config, args []string) error {
	if len(args) == 0 {
		printConfig(cfg)
		return nil
	}
	if (len(args) != 3 && len(args) != 4) || args[0] != "set" || args[1] != "mode" || (len(args) == 4 && args[3] != "--confirm") || (args[2] != "read_only" && args[2] != "read_write") {
		return errors.New("usage: telegram-mcp config set mode read_only|read_write --confirm")
	}
	var err error
	cfg, err = credentialConfig(cfg, false)
	if err != nil {
		return err
	}
	client := daemonClient()
	defer client.CloseIdleConnections()
	client.Jar, _ = cookiejar.New(nil)
	return openPanel(ctx, cfg, func(link string) error {
		req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
		if err != nil {
			return errors.New("invalid bootstrap")
		}
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("panel bootstrap failed")
		}
		resp.Body.Close()
		if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
			return errors.New("panel bootstrap refused")
		}
		req, _ = http.NewRequestWithContext(ctx, "GET", cfg.baseURL()+"/api/session", nil)
		resp, err = client.Do(req)
		if err != nil {
			return errors.New("panel session failed")
		}
		var session struct {
			CSRF string `json:"csrf_token"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&session)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || session.CSRF == "" {
			return errors.New("panel session refused")
		}
		body, _ := json.Marshal(map[string]any{"mode": args[2], "confirm": true})
		req, _ = http.NewRequestWithContext(ctx, "POST", cfg.baseURL()+"/api/mode", strings.NewReader(string(body)))
		req.Header.Set("Origin", cfg.baseURL())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", session.CSRF)
		resp, err = client.Do(req)
		if err != nil {
			return errors.New("mode change failed")
		}
		resp.Body.Close()
		// Do not leave a CLI-created browser session behind.
		logout, _ := http.NewRequestWithContext(ctx, "POST", cfg.baseURL()+"/api/session/logout", strings.NewReader("{}"))
		logout.Header = req.Header.Clone()
		if result, e := client.Do(logout); e == nil {
			result.Body.Close()
		}
		if resp.StatusCode != 200 && resp.StatusCode != 204 {
			return errors.New("mode change refused by panel")
		}
		return nil
	})
}
