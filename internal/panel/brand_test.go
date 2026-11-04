package panel

// The panel speaks of Telegram only. WhatsApp appears solely where the
// project credits Bruno Orlandi's WhatsApp MCP Local, which it is based on.

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// legacyWord is any user-facing trace of the inherited product.
var legacyWord = regexp.MustCompile(`(?i)whats\s*app|wacli|\bzap\b|wamcp|s\.whatsapp\.net|@g\.us`)

// credit is the only sentence allowed to name WhatsApp: the footer's credit,
// in each language. Its link to the upstream repository is a credit too.
var credit = map[string]bool{
	"Baseado no WhatsApp MCP Local, de Bruno Orlandi (licença MIT)": true,
	"https://github.com/BrOrlandi/whatsapp-mcp-local":               true,
}

func creditTranslations(t *testing.T) map[string]bool {
	t.Helper()
	ok := map[string]bool{}
	for k := range credit {
		ok[k] = true
	}
	for _, l := range i18n.Languages[1:] {
		raw, err := os.ReadFile(filepath.Join("..", "i18n", "locales", l.Code+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var cat map[string]string
		if err := json.Unmarshal(raw, &cat); err != nil {
			t.Fatal(err)
		}
		for k := range credit {
			if v, found := cat[k]; found {
				ok[v] = true
			}
		}
	}
	return ok
}

// withoutCredits blanks the allowed credits, so what is left must be clean.
func withoutCredits(s string, allowed map[string]bool) string {
	for c := range allowed {
		s = strings.ReplaceAll(s, c, "")
	}
	return s
}

// TestNoLegacyBrandInPanelTemplatesOrCatalogues fails when a template, a script
// of the panel, a page as rendered, or a translation the panel uses names
// WhatsApp (or wacli) outside the credits.
func TestNoLegacyBrandInPanelTemplatesOrCatalogues(t *testing.T) {
	allowed := creditTranslations(t)
	check := func(where, text string) {
		t.Helper()
		if m := legacyWord.FindString(withoutCredits(text, allowed)); m != "" {
			t.Errorf("%s names %q outside the credits", where, m)
		}
	}

	// Template sources and the panel's scripts and styles.
	check("pageSource", pageSource)
	check("webhookDocsSource", webhookDocsSource)
	check("stylesheet", stylesheet)
	check("recipes", strings.Join(append(append([]string{verificationPrompt, agentPrompt("http://127.0.0.1:1/mcp", "{}")}, suggestedPrompts...), recipeText()...), "\n"))
	check("webhook examples", webhookExampleText())
	_ = fs.WalkDir(assets, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") || strings.HasSuffix(path, ".svg")) {
			return err
		}
		raw, _ := assets.ReadFile(path)
		check(path, string(raw))
		return nil
	})

	// Every translation of every text the panel and the desktop app use: a
	// translation can bring the old name back even when the Portuguese is
	// clean. Texts of other packages are their owners' (docs/contratos).
	sources := panelSources(t)
	for _, l := range i18n.Languages[1:] {
		raw, err := os.ReadFile(filepath.Join("..", "i18n", "locales", l.Code+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var cat map[string]string
		if err := json.Unmarshal(raw, &cat); err != nil {
			t.Fatal(err)
		}
		for k, v := range cat {
			if allowed[k] || !strings.Contains(sources, k) {
				continue
			}
			check("locales/"+l.Code+".json key "+k, k)
			check("locales/"+l.Code+".json value of "+k, v)
		}
	}
	// The desktop app's own texts and the packaging around it.
	for _, dir := range []string{filepath.Join("..", "..", "cmd", "app"), filepath.Join("..", "..", "build")} {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(path, "_test.go") {
				return err
			}
			switch filepath.Ext(path) {
			case ".go", ".desktop", ".plist", ".json", ".svg", ".nsi", ".py":
				raw, _ := os.ReadFile(path)
				check(path, string(raw))
			}
			return nil
		})
	}
}

// panelSources is the text of every file whose strings the panel and the
// desktop app show: the panel's Go and scripts and cmd/app.
func panelSources(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, dir := range []string{".", "assets", filepath.Join("..", "..", "cmd", "app")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || strings.HasSuffix(name, "_test.go") || !(strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".js")) {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			b.Write(raw)
		}
	}
	return b.String()
}

// The test above must notice a WhatsApp outside the credits: a guard that
// never fails guards nothing.
func TestNoLegacyBrandGuardCatchesALeak(t *testing.T) {
	allowed := creditTranslations(t)
	for _, leak := range []string{`<p>{{t "Abra o WhatsApp no celular"}}</p>`, `wacli sync`, "5511@s.whatsapp.net", `localStorage "wamcp-theme"`} {
		if legacyWord.FindString(withoutCredits(leak, allowed)) == "" {
			t.Errorf("the guard misses %q", leak)
		}
	}
	if m := legacyWord.FindString(withoutCredits(`<a>{{t "Baseado no WhatsApp MCP Local, de Bruno Orlandi (licença MIT)"}}</a>`, allowed)); m != "" {
		t.Errorf("the credit itself is flagged: %q", m)
	}
}

// The rendered pages, in every language, carry the brand and the notice of
// non-affiliation, and never the old name outside the credits.
func TestRenderedPagesSayTelegramInEveryLanguage(t *testing.T) {
	allowed := creditTranslations(t)
	defer i18n.Set(i18n.Source)
	for _, l := range i18n.Languages {
		i18n.Set(l.Code)
		var b strings.Builder
		if err := parseTemplates().ExecuteTemplate(&b, "configuracoes", settingsPage{layout: layout{Title: "Configurações", Active: "configuracoes"}}); err != nil {
			t.Fatal(err)
		}
		page := b.String()
		if m := legacyWord.FindString(withoutCredits(page, allowed)); m != "" {
			t.Errorf("%s: Configurações names %q outside the credits", l.Code, m)
		}
		for _, want := range []string{"Telegram MCP", "data-credits", "https://github.com/BrOrlandi/whatsapp-mcp-local", i18n.T("Este app é do Telegram?")} {
			if want == i18n.T("Este app é do Telegram?") {
				continue // Ajuda, checked below
			}
			if !strings.Contains(page, want) {
				t.Errorf("%s: Configurações lacks %q", l.Code, want)
			}
		}
		for _, stale := range []string{"TG MCP Local", "Telegram MCP Local", "para Telegram (não oficial)", "for Telegram (unofficial)", "para Telegram (no oficial)"} {
			if strings.Contains(page, stale) {
				t.Errorf("%s: the page contains superseded branding %q", l.Code, stale)
			}
		}
		if !strings.Contains(page, i18n.T("Projeto independente; não é uma ferramenta do Telegram nem tem endosso dele. Telegram é marca dos seus donos.")) {
			t.Errorf("%s: the footer lacks the independence notice", l.Code)
		}
		b.Reset()
		if err := parseTemplates().ExecuteTemplate(&b, "ajuda", struct {
			layout
			Prompts []string
		}{layout{Title: "Ajuda", Active: "ajuda"}, suggestedPrompts}); err != nil {
			t.Fatal(err)
		}
		help := b.String()
		if m := legacyWord.FindString(withoutCredits(help, allowed)); m != "" {
			t.Errorf("%s: Ajuda names %q outside the credits", l.Code, m)
		}
		for _, want := range []string{i18n.T("Este app é do Telegram?"), i18n.T("Projeto independente; não é uma ferramenta do Telegram nem tem endosso dele. Telegram é marca dos seus donos."), `id="documentacao"`, `href="/webhooks/documentacao"`} {
			if !strings.Contains(help, want) {
				t.Errorf("%s: Ajuda lacks %q", l.Code, want)
			}
		}
	}
}

func recipeText() []string {
	var out []string
	for _, r := range recipeBook() {
		out = append(out, r.Title, r.Summary, r.Prompt, r.Schedule, r.Caveat)
	}
	return out
}

func webhookExampleText() string {
	var out []string
	for _, g := range webhookExamples() {
		out = append(out, g.Title, g.Intro)
		for _, e := range g.Examples {
			out = append(out, e.Title, e.Note, e.JSON)
		}
	}
	return strings.Join(out, "\n")
}
