package cli

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/appconfig"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

type cliInstallTransport func(*http.Request) (*http.Response, error)

func (f cliInstallTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTranscriptionInstallEnglish(t *testing.T) {
	if os.Getenv("CLI_ENGLISH_INSTALL_HELPER") == "1" {
		// No network, real tools, model, service, secrets or database. Delay
		// a rejected HTTP response so InstallNow prints its progress tick.
		http.DefaultClient = &http.Client{Transport: cliInstallTransport(func(*http.Request) (*http.Response, error) {
			time.Sleep(1200 * time.Millisecond)
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Body: io.NopCloser(strings.NewReader(""))}, nil
		})}
		os.Args = []string{"telegram-mcp", "transcription", "install"}
		run()
		t.Fatal("expected the stub download to fail")
	}
	for _, locale := range []string{"pt-BR", "es", "saved"} {
		t.Run(locale, func(t *testing.T) {
			data := t.TempDir()
			t.Setenv("TELEGRAM_MCP_DATA", data)
			t.Setenv("TELEGRAM_MCP_LANG", locale)
			if locale == "saved" {
				t.Setenv("TELEGRAM_MCP_LANG", "")
				if err := appconfig.Save(data, appconfig.Config{Port: appconfig.DefaultPort, Language: "pt-BR"}); err != nil {
					t.Fatal(err)
				}
			}
			tools := t.TempDir()
			testutil.InstallASRTools(t, tools, "unused")
			t.Setenv("CLI_ENGLISH_INSTALL_HELPER", "1")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command(executable, "-test.run=^TestTranscriptionInstallEnglish$").CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				t.Fatalf("helper error = %v, output = %s", err, output)
			}
			text := string(output)
			for _, want := range []string{"setting up local transcription", "Downloading the transcription model", "the model download returned 503 Service Unavailable"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q in CLI output: %s", want, text)
				}
			}
			for _, unwanted := range []string{"Baixando", "baixando", "transcrição", "Descargando", "descarga"} {
				if strings.Contains(text, unwanted) {
					t.Errorf("localized text %q in CLI output: %s", unwanted, text)
				}
			}
		})
	}
}
