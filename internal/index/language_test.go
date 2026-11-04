package index

import (
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// The name of a chat without one reaches the tools and the webhooks the same
// in every language; only the panel shows it in its own (ShownName).
func TestFallbackNamesDoNotFollowTheLanguage(t *testing.T) {
	defer i18n.Set(i18n.Source)
	for _, lang := range []string{i18n.Source, "es", "en"} {
		i18n.Set(lang)
		for jid, want := range map[string]string{"chat:120363": "Grupo sem nome", "channel:120363": "Canal do Telegram", "user:120363": "user:120363"} {
			if got := formatPeer(jid); got != want {
				t.Errorf("%s: formatPeer(%s) = %q, want %q", lang, jid, got, want)
			}
		}
	}
	i18n.Set("en")
	if got := ShownName("Grupo sem nome"); got != "Unnamed group" {
		t.Errorf("the panel in English shows %q", got)
	}
	if got := ShownName("Maria Silva"); got != "Maria Silva" {
		t.Errorf("a chat's own name changed to %q", got)
	}
}
