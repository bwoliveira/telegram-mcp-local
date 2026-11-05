package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Exercise visible copy in all three locales and both build modes. Release
// rendering here uses the existing test-only bypass, not launch approval.
func TestStage4Content(t *testing.T) {
	credentials := map[string][]string{
		"pt-BR": {"próprio", "formulário", "cofre", "primeira inicialização"},
		"en":    {"own", "form", "vault", "first launch"},
		"es":    {"propio", "formulario", "almacén", "primer inicio"},
	}
	protections := map[string][]string{
		"pt-BR": {"não desative", "integridade", "não a identidade", "ainda não foi validada"},
		"en":    {"do not disable", "integrity", "not the publisher", "not yet been validated"},
		"es":    {"no desactives", "integridad", "no la identidad", "aún no se ha validado"},
	}
	smartscreen := map[string][]string{
		"pt-BR": {"O Windows protegeu o computador", "Mais informações", "Executar assim mesmo", "notarizado"},
		"en":    {"Windows protected your PC", "More info", "Run anyway", "notarized"},
		"es":    {"Windows protegió su PC", "Más información", "Ejecutar de todas formas", "notarizado"},
	}
	remote := map[string][]string{
		"pt-BR": {"Quem tiver a URL entra", "um minuto", "uma vez", "Não compartilhe", "logs"},
		"en":    {"Anyone with the URL", "one minute", "once", "Do not share", "log"},
		"es":    {"Quien tenga la URL", "al minuto", "una vez", "No la compartas", "registros"},
	}
	launch := map[string][]string{
		"pt-BR": {"só após o lançamento", "repositório ainda é privado"},
		"en":    {"only after launch", "repository is still private"},
		"es":    {"solo después del lanzamiento", "repositorio sigue siendo privado"},
	}
	both(t, func(t *testing.T, b built, mode string) {
		if len(b.site.Languages) != 3 {
			t.Fatalf("want three locales, got %d", len(b.site.Languages))
		}
		for _, lang := range b.site.Languages {
			t.Run(lang.Code, func(t *testing.T) {
				faq := reviewFAQ(t, b.pages[outFile(lang, "index")], "faq-api-id")
				reviewRequire(t, faq, credentials[lang.Code])
				reviewRequire(t, faq, []string{"my.telegram.org/apps", "api_id", "api_hash", "TELEGRAM_API_ID", "TELEGRAM_API_HASH"})
				dl := b.pages[outFile(lang, "download")]
				reqs := stage4Section(t, dl, "requirements-title")
				reviewRequire(t, reqs, []string{"macOS", "11.0", "Apple silicon", "Intel", "Windows", "x64", "WebView2", "Linux", "amd64", "arm64", "GTK3", "WebKitGTK 4.1", "glibc", "AppImage", ".deb", "Gatekeeper", "SmartScreen", "SHA-256"})
				reviewRequire(t, reqs, protections[lang.Code])
				// v1.0.0 platform support: Linux floor, signed/notarized Mac,
				// unsigned Windows with the SmartScreen steps, headless CLI.
				reviewRequire(t, reqs, append([]string{"Ubuntu 24.04", "Debian 13", "Developer ID", "Authenticode", "Windows 10", "11", "telegram-mcp serve", ".tar.gz"}, smartscreen[lang.Code]...))
				for _, stale := range []string{"22.04", "20.04", "Debian 12"} {
					if strings.Contains(dl, stale) {
						t.Errorf("stale Linux support claim %q", stale)
					}
				}
				first := stage4Section(t, dl, "first-title")
				reviewRequire(t, first, []string{"api_id", "api_hash", "my.telegram.org/apps", "QR"})
				onboarding := map[string][]string{
					"pt-BR": {"primeiro uso", "formulário", "cofre local"},
					"en":    {"first use", "form", "local vault"},
					"es":    {"primer uso", "formulario", "almacén local de credenciales"},
				}
				reviewRequire(t, first, onboarding[lang.Code])
				if strings.Contains(first, "TELEGRAM_API_") {
					t.Error("desktop setup must use the form, not environment variables")
				}
				headless := b.pages[outFile(lang, "headless")]
				reviewRequire(t, stage4Section(t, headless, "remote-title"), append([]string{"ssh -N -L 47831:127.0.0.1:47831", "telegram-mcp open --print"}, remote[lang.Code]...))
				install := stage4Section(t, headless, "install-title")
				reviewRequire(t, install, []string{"curl -fsSL https://raw.githubusercontent.com/bwoliveira/telegram-mcp-local/main/install.sh | sh", "SHA-256", "~/.local/bin", "--prefix", "sudo", "TELEGRAM_API_ID", "TELEGRAM_API_HASH"})
				for _, warning := range launch[lang.Code] {
					if mode == ModePrelaunch {
						reviewRequire(t, install, []string{warning})
					} else if strings.Contains(install, warning) {
						t.Error("prelaunch-only installer warning leaked into release output")
					}
				}
				for _, page := range []string{faq, dl, headless} {
					for _, claim := range []string{"exit 1", "não tem formulário", "has no credential-entry form", "no tiene formulario", "não há fluxo documentado", "no documented flow", "no hay un flujo documentado", "NOTARY_KEY", "SIGN_IDENTITY", "ad-hoc"} {
						if strings.Contains(page, claim) {
							t.Errorf("obsolete setup claim or build-script detail: %s", claim)
						}
					}
				}
			})
		}
	})
}

func stage4Section(t *testing.T, page, id string) string {
	t.Helper()
	match := regexp.MustCompile(`(?s)<section\b[^>]*aria-labelledby="` + regexp.QuoteMeta(id) + `"[^>]*>(.*?)</section>`).FindStringSubmatch(page)
	if len(match) != 2 {
		t.Fatalf("missing section %s", id)
	}
	return reviewContentText(match[1])
}

// TestStage6ParityGateResolved replaces the old "gate must remain" check.
// The TODO(stage6) marker was removed after every home-page claim was checked
// against docs/PARIDADE.md and the code; the release build must now succeed
// without the test bypass, and the corrected parity wording must stay.
func TestStage6ParityGateResolved(t *testing.T) {
	err := filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, marker := range []string{"TODO(stage4)", "TODO(stage6)", "todo.apiid"} {
			if strings.Contains(string(data), marker) {
				t.Errorf("%s: unresolved marker/key %s", path, marker)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var site Site
	if err := readJSON(filepath.Join(srcDir, "site.json"), &site); err != nil {
		t.Fatal(err)
	}
	strs, err := loadStrings(srcDir, site)
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range site.Languages {
		approval := strs[lang.Code]["todo.review"]
		if lang.Code == "pt-BR" {
			if strings.Contains(approval, "TODO(") || !strings.Contains(approval, "2026-11-02") || !strings.Contains(approval, "2026-11-03") || !strings.Contains(approval, "download, credenciais e headless") || !strings.Contains(approval, "Todo o texto pt-BR da 1.0.0 está aprovado por Bruno em 2026-11-04") || strings.Contains(approval, "precisam de nova aprovação") || strings.Contains(approval, "aguardam o OK") {
				t.Errorf("pt-BR must record owner approval of all 1.0.0 copy on 2026-11-04, with the earlier approval dates")
			}
		} else if strings.Contains(approval, "TODO(") || !strings.Contains(approval, "2026-11-02") || (!strings.Contains(approval, "pending owner approval") && !strings.Contains(approval, "pendientes de aprobación")) {
			t.Errorf("%s: must retain the prior review date and record pending source approval", lang.Code)
		}
		if _, ok := strs[lang.Code]["todo.apiid"]; ok {
			t.Errorf("%s: obsolete todo.apiid key", lang.Code)
		}
	}
	out := filepath.Join(t.TempDir(), "release")
	if _, err := Build(Options{Src: srcDir, Out: out, Mode: ModeRelease, Version: "v1.2.3", Date: "2026-12-01"}); err != nil {
		t.Fatalf("release build without test bypass: %v", err)
	}
	// Parity corrections from the Stage 6 review, per language.
	parity := map[string][]string{
		"pt-BR": {"Toda ação que muda o Telegram exige prévia", "verificados com uma conta real em um grupo de teste", "por padrão", "não confiável"},
		"en":    {"Every action that changes Telegram needs a preview", "verified with a real account in a test group", "by default", "untrusted data"},
		"es":    {"Cada acción que cambia Telegram exige vista previa", "verificaron con una cuenta real en un grupo de prueba", "por defecto", "no confiable"},
	}
	for _, lang := range site.Languages {
		index, err := os.ReadFile(filepath.Join(out, outFile(lang, "index")))
		if err != nil {
			t.Fatal(err)
		}
		reviewRequire(t, reviewContentText(string(index)), parity[lang.Code])
		for _, stale := range []string{"um envio de texto com prévia. Outras funções estão em desenvolvimento", "More is in development and will only be listed here", "Hay más en desarrollo", "conta real ainda está pendente", "real account is still pending", "cuenta real sigue pendiente"} {
			if strings.Contains(string(index), stale) {
				t.Errorf("%s: stale parity claim %q", lang.Code, stale)
			}
		}
	}
}
