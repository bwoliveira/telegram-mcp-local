package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// A local, ephemeral credential display, not another HTTP listener or a panel
// bootstrap. The browser receives only PNG bytes, never the daemon bearer.
// Same-user processes/root remain outside the filesystem protection boundary.
var errAuthQRUnavailable = errors.New("QR no longer available")

type authQRPage struct {
	path      string
	announced bool
}

func newAuthQRPage() (*authQRPage, error) {
	dir, err := os.MkdirTemp("", "telegram-mcp-qr-") // 0700 even with umask 000
	if err != nil {
		return nil, errors.New("could not create private QR directory")
	}
	return &authQRPage{path: filepath.Join(dir, "qr.html")}, nil
}

func (p *authQRPage) close() { _ = os.RemoveAll(filepath.Dir(p.path)) }

func (p *authQRPage) update(ctx context.Context, client *http.Client, cfg config) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.baseURL()+"/api/pair/qr.png", nil)
	if err != nil {
		return errors.New("could not construct QR request")
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("could not read QR from daemon")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errAuthQRUnavailable
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		return errors.New("daemon QR request refused")
	}
	const limit = 1 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(b) > limit {
		return errors.New("invalid daemon QR image")
	}
	image, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil || image.Width < 1 || image.Height < 1 || image.Width > 2048 || image.Height > 2048 {
		return errors.New("invalid daemon QR image")
	}
	// Reload the same file to follow daemon QR renewal. No network, scripts,
	// bearer, bootstrap code, or Telegram URL is included in the HTML.
	html := `<!doctype html><html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; base-uri 'none'; form-action 'none'"><meta http-equiv="refresh" content="1"><title>Telegram MCP — private QR</title></head><body><h1>Telegram MCP</h1><p>Scan with Telegram Settings → Devices → Link Desktop Device. Keep the auth command running. This file is a temporary login credential; do not share it.</p><img alt="Telegram login QR" src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(b) + `"></body></html>`
	f, err := os.CreateTemp(filepath.Dir(p.path), ".qr-*") // 0600 from creation
	if err != nil {
		return errors.New("could not write private QR page")
	}
	defer os.Remove(f.Name())
	_, writeErr := io.WriteString(f, html)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("could not write private QR page")
	}
	if err := os.Rename(f.Name(), p.path); err != nil {
		return errors.New("could not replace private QR page")
	}
	return nil
}
