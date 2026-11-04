package i18n

import "testing"

func TestInKeepsProcessLanguage(t *testing.T) {
	before := Lang()
	for code, want := range map[string]string{
		Source: "Acesso ao painel",
		"en":   "Panel access",
		"es":   "Acceso al panel",
		"fr":   "Acesso ao painel",
	} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			for i := 0; i < 100; i++ {
				if got := In(code, "Acesso ao painel"); got != want {
					t.Fatalf("translation = %q, want %q", got, want)
				}
				if got := Lang(); got != before {
					t.Fatalf("request language changed the app language: %q", got)
				}
				if got := In(code, "synthetic missing catalogue entry"); got != "synthetic missing catalogue entry" {
					t.Fatalf("missing-entry fallback = %q", got)
				}
			}
		})
	}
}
