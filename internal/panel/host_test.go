package panel

// The desktop app's settings through a fake Host: the port, open at login,
// close to the tray, quitting and erasing everything, each behind the
// session and its CSRF token.

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type fakeHost struct {
	mu        sync.Mutex
	settings  HostSettings
	port      int
	quit      int
	erased    int
	eraseErr  error
	languages []string
}

func (h *fakeHost) Settings() HostSettings {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.settings
}
func (h *fakeHost) SetAutostart(on bool) error {
	h.mu.Lock()
	h.settings.Autostart = on
	h.mu.Unlock()
	return nil
}
func (h *fakeHost) SetCloseToTray(on bool) error {
	h.mu.Lock()
	h.settings.CloseToTray = on
	h.mu.Unlock()
	return nil
}
func (h *fakeHost) SetLanguage(code string) error {
	h.mu.Lock()
	h.languages = append(h.languages, code)
	h.settings.Language = code
	h.mu.Unlock()
	return nil
}
func (h *fakeHost) SetPort(port int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if port == 1234 {
		return errors.New("a porta 1234 já está em uso por outro programa")
	}
	h.port = port
	return nil
}
func (h *fakeHost) OpenURL(string) error  { return nil }
func (h *fakeHost) OpenDataFolder() error { return nil }
func (h *fakeHost) Update() UpdateState   { return UpdateState{State: "current", Current: "0.0.0-test"} }
func (h *fakeHost) CheckUpdate()          {}
func (h *fakeHost) InstallUpdate() error  { return nil }
func (h *fakeHost) ShowWindow(string)     {}
func (h *fakeHost) Quit()                 { h.mu.Lock(); h.quit++; h.mu.Unlock() }
func (h *fakeHost) EraseEverything() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.erased++
	return h.eraseErr
}
func (h *fakeHost) counts() (int, int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.port, h.quit, h.erased
}
func (h *fakeHost) autostart() (bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.settings.Autostart, h.settings.CloseToTray
}

func TestIntegrationHostSettingsNeedCSRF(t *testing.T) {
	host := &fakeHost{settings: HostSettings{CanHide: true, Version: "0.0.0-test"}}
	b := newSettingsPanelWith(t, host)
	b.login()
	form := "application/x-www-form-urlencoded"

	for _, m := range []struct{ path, ctype, body string }{
		{"/configuracoes/porta", form, "port=48001"},
		{"/configuracoes/encerrar", form, ""},
		{"/configuracoes/apagar", form, "confirm=apagar"},
		{"/api/configuracoes", "application/json", `{"autostart":true}`},
	} {
		if code, _, _ := b.page("POST", m.path, m.ctype, m.body, false); code != http.StatusForbidden {
			t.Errorf("%s without CSRF: %d", m.path, code)
		}
	}
	if port, quit, erased := host.counts(); port != 0 || quit != 0 || erased != 0 {
		t.Fatalf("a refused request acted: port %d quit %d erased %d", port, quit, erased)
	}

	// Port: out of range, taken, then a good one that sends to the
	// reconfigure step.
	if code, hdr, _ := b.page("POST", "/configuracoes/porta", form, "port=80", true); code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "erro=") {
		t.Errorf("port 80: %d %s", code, hdr.Get("Location"))
	}
	if code, hdr, _ := b.page("POST", "/configuracoes/porta", form, "port=1234", true); code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "erro=") {
		t.Errorf("taken port: %d %s", code, hdr.Get("Location"))
	}
	code, hdr, _ := b.page("POST", "/configuracoes/porta", form, "port=48001", true)
	if code != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/configuracoes?porta=1") {
		t.Fatalf("port: %d %s", code, hdr.Get("Location"))
	}
	if port, _, _ := host.counts(); port != 48001 {
		t.Fatalf("port not set: %d", port)
	}
	if code, _, page := b.do("GET", hdr.Get("Location"), "", "", map[string]string{"Sec-Fetch-Site": "same-origin"}); code != 200 || !strings.Contains(page, "data-reconfigure") {
		t.Errorf("after the port change the page does not offer to update the tools: %d", code)
	}

	// Open at login and close to the tray.
	if code, _, body := b.page("POST", "/api/configuracoes", "application/json", `{"autostart":true,"close_to_tray":true}`, true); code != 200 {
		t.Fatalf("settings: %d %s", code, body)
	}
	if a, c := host.autostart(); !a || !c {
		t.Fatalf("autostart %v close to tray %v", a, c)
	}

	// Erase everything needs the word, then answers with its own page.
	if code, hdr, _ := b.page("POST", "/configuracoes/apagar", form, "confirm=nao", true); code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "erro=") {
		t.Errorf("erase without the word: %d %s", code, hdr.Get("Location"))
	}
	if _, _, erased := host.counts(); erased != 0 {
		t.Fatal("erased without the word")
	}
	code, _, page := b.page("POST", "/configuracoes/apagar", form, "confirm="+url.QueryEscape("apagar"), true)
	if code != 200 || !strings.Contains(page, "Tudo apagado") || legacyWord.MatchString(page) {
		t.Fatalf("erase: %d", code)
	}
	if _, _, erased := host.counts(); erased != 1 {
		t.Fatal("not erased")
	}
	// Quit answers with the Encerrando page.
	code, _, page = b.page("POST", "/configuracoes/encerrar", form, "", true)
	if code != 200 || !strings.Contains(page, "Telegram MCP está fechando") {
		t.Fatalf("quit: %d", code)
	}
	if _, quit, _ := host.counts(); quit != 1 {
		t.Fatal("did not quit")
	}

	// In the app the settings page shows the app's own forms.
	if code, _, page := b.do("GET", "/configuracoes", "", "", map[string]string{"Sec-Fetch-Site": "same-origin"}); code != 200 {
		t.Fatalf("configuracoes: %d", code)
	} else {
		mustContain(t, "app settings", page, `action="/configuracoes/porta"`, `data-setting="autostart"`, `data-setting="close_to_tray"`, `href="#encerrar-app"`, `action="/configuracoes/apagar"`, `data-update-check`)
	}
}
