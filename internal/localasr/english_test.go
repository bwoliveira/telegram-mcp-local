package localasr

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/sidecar"
)

type englishTestTransport func(*http.Request) (*http.Response, error)

func (f englishTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTranscriptionDiagnosticsEnglish(t *testing.T) {
	oldLang := i18n.Lang()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(oldLang) })
	oldTools := systemTools
	systemTools = func() sidecar.Tools { return sidecar.Tools{} }
	t.Cleanup(func() { systemTools = oldTools })
	e := New(t.TempDir())
	if got := strings.Join(e.Status().Missing, ", "); got != "whisper.cpp, ffmpeg, model" {
		t.Errorf("missing components = %q", got)
	}
	_, err := e.Transcribe(context.Background(), "unused.ogg", "", "")
	if err == nil || err.Error() != "local transcription is not installed (missing: whisper.cpp, ffmpeg, model)" {
		t.Errorf("not-ready error = %v", err)
	}

	// Stub the transport: exercise the inherited download progress/error path
	// without a network request, model, sidecar installation or real account.
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: englishTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = oldClient })
	err = e.download(context.Background())
	if err == nil || err.Error() != "the model download returned 503 Service Unavailable" {
		t.Errorf("model error = %v", err)
	}
	if got := e.Status().Install.Step; got != "Downloading the transcription model" {
		t.Errorf("model progress = %q", got)
	}
	if got := i18n.Lang(); got != "en" {
		t.Errorf("backend changed panel language to %q", got)
	}
}
