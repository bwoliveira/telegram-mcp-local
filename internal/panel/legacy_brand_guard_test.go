// Legacy literals are isolated here to verify that inherited branding and APIs stay absent.
package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The inherited addresses are gone behind the real boundary too.
func TestIntegrationWhatsAppAliasesGone(t *testing.T) {
	b := newBrowserPanel(t)
	b.login()
	if code, _, _ := b.do("GET", "/whatsapp", "", "", map[string]string{"Sec-Fetch-Site": "same-origin"}); code != http.StatusNotFound {
		t.Fatalf("GET /whatsapp: %d", code)
	}
	for _, path := range []string{"/whatsapp/sair", "/whatsapp/nome"} {
		if code, _, _ := b.page("POST", path, "application/x-www-form-urlencoded", "label=x", true); code != http.StatusNotFound {
			t.Fatalf("POST %s: %d", path, code)
		}
	}
}

// The inherited WhatsApp addresses are gone; Conta is the only account tab.
func TestPanelWhatsAppAliasesRemoved(t *testing.T) {
	_, h := panelWithFake(t)
	for _, c := range []struct{ method, path string }{{"GET", "/whatsapp"}, {"POST", "/whatsapp/sair"}, {"POST", "/whatsapp/nome"}} {
		r := httptest.NewRequest(c.method, "http://127.0.0.1:47831"+c.path, strings.NewReader("label=x"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: want 404, got %d", c.method, c.path, w.Code)
		}
	}
	for _, path := range []string{"/ajuda", "/conta", "/status"} {
		if page := pageBody(t, h, path); strings.Contains(page, `href='/whatsapp'`) || strings.Contains(page, `href="/whatsapp"`) {
			t.Errorf("%s still links to /whatsapp", path)
		}
	}
}

// The prompt the connect flow suggests only reads, about Telegram.
func TestVerificationPromptIsTelegramAndReadOnly(t *testing.T) {
	for _, want := range []string{"Telegram", "@usuário", "quantas mensagens", "desde quando", "5 conversas", "Não envie nada"} {
		if !strings.Contains(verificationPrompt, want) {
			t.Errorf("verification prompt lacks %q", want)
		}
	}
	if legacyWord.MatchString(verificationPrompt) {
		t.Error("verification prompt names WhatsApp")
	}
}

// Every page carries the panel's own name, the non-affiliation notice and
// the Secret Chats caveat, and none carries the inherited brand.
func TestPanelBrandAndDisclaimer(t *testing.T) {
	p, h := panelWithFake(t)
	for _, path := range []string{"/conta", "/status", "/funcoes", "/configuracoes"} {
		page := pageBody(t, h, path)
		mustContain(t, path, page, "Telegram MCP", "data-disclaimer",
			"não é uma ferramenta do Telegram nem tem endosso dele", "Secret Chats ficam de fora", "tgmcp-mark")
		for _, banned := range []string{"WhatsApp MCP</span>", "wamcp-mark", "#128c7e"} {
			if strings.Contains(page, banned) {
				t.Errorf("%s still shows the inherited brand: %q", path, banned)
			}
		}
	}
	if err := p.Engine.LogOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	page := pageBody(t, h, "/instalacao")
	mustContain(t, "/instalacao", page, "Conecte o seu Telegram", "Conectar dispositivo", "data-pair-password",
		`type="password"`, "Secret Chats ficam de fora", "não é uma ferramenta do Telegram nem tem endosso dele")
	w := panelRequest(t, h, "GET", "/favicon.svg", "")
	if !strings.Contains(w.Body.String(), "tgmcp-mark") {
		t.Fatalf("favicon is not the panel's own mark")
	}
}
