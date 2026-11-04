package panel

import (
	"net/http/httptest"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// A program on this computer may act through the API; a browser page from
// elsewhere still may not, and a form cannot pass for a program.
func TestFromAgent(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		headers map[string]string
		want    bool
	}{
		{"curl with JSON", "", map[string]string{"Content-Type": "application/json"}, false},
		{"curl with JSON and a charset", "", map[string]string{"Content-Type": "application/json; charset=utf-8"}, false},
		{"a form from an old browser", "", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, false},
		{"no body type", "", nil, false},
		{"a page from another site", "", map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, false},
		{"token required, none sent", "s3cret", map[string]string{"Content-Type": "application/json"}, false},
		{"token required and sent", "s3cret", map[string]string{"Content-Type": "application/json", "Authorization": "Bearer s3cret"}, true},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "http://127.0.0.1:47831/api/pair", nil)
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		if got := (&Panel{Token: c.token}).fromAgent(r); got != c.want {
			t.Errorf("%s: fromAgent = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNextStep(t *testing.T) {
	paired := engine.Account{Authenticated: true}
	for _, c := range []struct {
		name string
		s    snapshot
		want string
	}{
		{"fresh install", snapshot{Sync: engine.Status{State: "not_authorized"}, Pairing: engine.Login{State: "idle"}}, "pair"},
		{"pairing starting", snapshot{Sync: engine.Status{State: "not_authorized"}, Pairing: engine.Login{State: "starting"}}, "wait"},
		{"QR on screen", snapshot{Sync: engine.Status{State: "not_authorized"}, Pairing: engine.Login{State: "qr"}}, "scan"},
		{"code to enter", snapshot{Sync: engine.Status{State: engine.StateNotAuthorized}, Pairing: engine.Login{State: engine.LoginWantCode}}, "code"},
		{"history arriving", snapshot{Account: paired, Sync: engine.Status{State: "starting"}, Pairing: engine.Login{State: "syncing"}}, "syncing"},
		{"opening", snapshot{Account: paired, Sync: engine.Status{State: "starting"}, Pairing: engine.Login{State: "idle"}}, "wait"},
		{"connected is ready", snapshot{Account: paired, Sync: engine.Status{State: engine.StateConnected}, Pairing: engine.Login{State: engine.LoginIdle}}, "ready"},
		{"ready", snapshot{Account: paired, Sync: engine.Status{State: "connected"}, Pairing: engine.Login{State: "idle"}}, "ready"},
		{"logged out by the phone", snapshot{Sync: engine.Status{State: "logged_out"}, Pairing: engine.Login{State: "idle"}}, "pair"},
		{"reconnecting", snapshot{Account: paired, Sync: engine.Status{State: "reconnecting"}, Pairing: engine.Login{State: "idle"}}, "wait"},
		{"stopped", snapshot{Account: paired, Sync: engine.Status{State: "stopped"}, Pairing: engine.Login{State: "idle"}}, "error"},
		{"password required", snapshot{Sync: engine.Status{State: engine.StateNotAuthorized}, Pairing: engine.Login{State: engine.LoginWantPass}}, "password"},
		{"manually disconnected", snapshot{Account: paired, Sync: engine.Status{State: engine.StatePaused}}, "connect"},
		{"revoked", snapshot{Account: paired, Sync: engine.Status{State: engine.StateRevoked}}, "pair"},
	} {
		if got := nextStep(c.s); got != c.want {
			t.Errorf("%s: next = %q, want %q", c.name, got, c.want)
		}
	}
}
