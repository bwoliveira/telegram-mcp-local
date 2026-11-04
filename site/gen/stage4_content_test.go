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

func TestStage4ResolvedStage6StillGatesRelease(t *testing.T) {
	err := filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "TODO(stage4)") || strings.Contains(string(data), "todo.apiid") {
			t.Errorf("%s: unresolved Stage 4 marker/key", path)
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
		index, err := os.ReadFile(filepath.Join(srcDir, "pages", lang.Code, "index.html"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(index), "TODO(stage6)") {
			t.Errorf("%s: Stage 6 parity gate must remain", lang.Code)
		}
		approval := strs[lang.Code]["todo.review"]
		if lang.Code == "pt-BR" {
			if strings.Contains(approval, "TODO(stage6)") || !strings.Contains(approval, "Todo o texto pt-BR está aprovado por Bruno") || !strings.Contains(approval, "2026-11-02") || !strings.Contains(approval, "2026-11-03") || !strings.Contains(approval, "download, credenciais e headless") || strings.Contains(approval, "precisam de nova aprovação") {
				t.Errorf("pt-BR must record owner approval of all copy, with both existing and setup approval dates")
			}
		} else if strings.Contains(approval, "TODO(stage6)") || !strings.Contains(approval, "2026-11-02") || (!strings.Contains(approval, "pending owner approval") && !strings.Contains(approval, "pendientes de aprobación")) {
			t.Errorf("%s: must retain the prior review date and record pending source approval", lang.Code)
		}
		if _, ok := strs[lang.Code]["todo.apiid"]; ok {
			t.Errorf("%s: obsolete todo.apiid key", lang.Code)
		}
	}
	_, err = Build(Options{Src: srcDir, Out: filepath.Join(t.TempDir(), "release"), Mode: ModeRelease, Version: "v1.2.3", Date: "2026-12-01"})
	if err == nil || !strings.Contains(err.Error(), "TODO") {
		t.Fatalf("release without test bypass must remain blocked by Stage 6: %v", err)
	}
}
