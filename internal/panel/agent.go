package panel

import (
	"crypto/subtle"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
)

// The API an AI agent on this computer uses to install, pair and check the
// app: GET /api/status says where things stand and what comes next, and the
// panel's own actions (pairing, connecting a client, opening the window)
// accept a program's request as well as the page's. docs/api.md describes it.

// fromAgent reports whether a request comes from a program on this computer
// rather than from a browser. Browsers mark every request with Sec-Fetch-Site
// or Origin, and a page cannot send JSON to another site without both; a
// program sends neither. A program must always present the daemon token,
// as on /mcp.
func (p *Panel) fromAgent(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") != "" || r.Header.Get("Origin") != "" {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
			return false
		}
	}
	if p.Token == "" {
		return false
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(p.Token)) == 1
}

// agentStatus is GET /api/status. Its fields are kept stable for the prompts
// and agents that read them.
type agentStatus struct {
	App     string `json:"app"`
	Version string `json:"version"`
	// Desktop is true in the app, false in the command line version.
	Desktop  bool          `json:"desktop"`
	MCPURL   string        `json:"mcp_url"`
	Telegram agentTelegram `json:"telegram"`
	Pairing  agentPairing  `json:"pairing"`
	Health   agentHealth   `json:"health"`
	Next     string        `json:"next"`
}

type agentTelegram struct {
	// State is sync's: starting, connected, paused, reconnecting,
	// not_paired, logged_out or stopped.
	State  string `json:"state"`
	Paired bool   `json:"paired"`
	// Connected is receiving and ready to send.
	Connected     bool       `json:"connected"`
	Phone         string     `json:"phone,omitempty"`
	Name          string     `json:"name,omitempty"`
	Label         string     `json:"label,omitempty"`
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
}

type agentPairing struct {
	// State is idle, starting, qr, code, syncing, done, error or cancelled.
	State string `json:"state"`
	// QRPNG is the QR code to read with the phone, while State is qr.
	QRPNG string `json:"qr_png,omitempty"`
	Error string `json:"error,omitempty"`
}

type agentHealth struct {
	Status  string      `json:"status"`
	Summary string      `json:"summary"`
	Checks  []mcp.Check `json:"checks"`
}

// apiStatus is where the app stands and what an agent does next.
func (p *Panel) apiStatus(r *http.Request) (any, error) {
	s := p.snapshot(r.Context())
	st := agentStatus{
		App: "Telegram MCP", Version: strings.TrimPrefix(mcp.Version, "v"),
		Desktop: p.Host != nil, MCPURL: p.endpoint(),
		Telegram: agentTelegram{
			State: string(s.Sync.State), Paired: s.Account.Authenticated,
			Connected: s.Sync.State == engine.StateConnected,
			Phone:     s.Phone, Name: s.Name, Label: s.Label,
		},
		Pairing: agentPairing{State: string(s.Pairing.State), Error: s.Pairing.Error},
		Health:  agentHealth{Status: s.Health.Status, Summary: s.Health.Summary, Checks: s.Health.Checks},
		Next:    nextStep(s),
	}
	if t := s.Activity.NewestIncoming; t != nil && !t.IsZero() {
		st.Telegram.LastMessageAt = t
	}
	if s.Pairing.State == "qr" {
		st.Pairing.QRPNG = strings.TrimSuffix(p.endpoint(), "/mcp") + "/api/pair/qr.png"
	}
	return st, nil
}

// nextStep is what an agent does next:
//
//	ready    Telegram is connected, receiving and ready to send
//	scan     the person scans the QR code with Telegram
//	code     the person enters the code sent by Telegram
//	password the person enters their two-factor password
//	connect  the person resumes a manually disconnected account
//	pair     no Telegram session and no login: open the app, or POST /api/pair
//	syncing  paired; the phone is sending the history, which takes minutes
//	wait     starting or reconnecting: ask again shortly
//	error    sync stopped: health says why
func nextStep(s snapshot) string {
	switch {
	case s.Account.Authenticated && s.Sync.State == engine.StateConnected:
		return "ready"
	case s.Pairing.State == engine.LoginShowQR:
		return "scan"
	case s.Pairing.State == engine.LoginWantCode:
		return "code"
	case s.Pairing.State == engine.LoginWantPass:
		return "password"
	case s.Pairing.State == "starting":
		return "wait"
	case s.Sync.State == engine.StateRevoked || s.Sync.State == engine.StateLoggedOut || s.Sync.State == engine.StateNotAuthorized:
		return "pair"
	case s.Sync.State == engine.StatePaused:
		return "connect"
	case s.Pairing.State == engine.LoginSyncing || (s.Account.Authenticated && s.Arriving):
		return "syncing"
	case !s.Account.Authenticated:
		return "pair"
	case settling(s.Sync) || s.Sync.State == "reconnecting":
		return "wait"
	}
	return "error"
}

// apiOpenApp brings the app's window up, on the page given (the first one by
// default).
func (p *Panel) apiOpenApp(r *http.Request) (any, error) {
	var body struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Path != "" && (!strings.HasPrefix(body.Path, "/") || strings.HasPrefix(body.Path, "//")) {
		return nil, userError{"path must be a page of the app, such as /status"}
	}
	p.Host.ShowWindow(body.Path)
	return map[string]bool{"ok": true}, nil
}
