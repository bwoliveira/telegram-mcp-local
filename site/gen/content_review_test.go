package main

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

// These regressions check the rendered disclosures, not template comments or
// unrelated footer text. Both modes must carry the same localized caveats.
func reviewContentText(markup string) string {
	return strings.Join(strings.Fields(html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(markup, " "))), " ")
}

func reviewFAQ(t *testing.T, page, id string) string {
	t.Helper()
	matches := regexp.MustCompile(`(?s)<details\s+id="`+regexp.QuoteMeta(id)+`">(.*?)</details>`).FindAllStringSubmatch(page, -1)
	if len(matches) != 1 {
		t.Fatalf("want exactly one %s disclosure, got %d", id, len(matches))
	}
	return reviewContentText(matches[0][1])
}

func reviewRequire(t *testing.T, text string, concepts []string) {
	t.Helper()
	for _, concept := range concepts {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(concept)) {
			t.Errorf("missing disclosure %q", concept)
		}
	}
}

func TestReviewC2ApprovalIsAClientResponsibility(t *testing.T) {
	want := map[string][]string{
		"pt-BR": {"começa em modo só leitura", "prévia vinculada", "preview_fingerprint", "confirm=true", "mesmo cliente MCP", "pedir a prévia e confirmar", "não autentica uma aprovação humana separada", "configure o cliente", "OK explícito antes de cada escrita", "não autorize confirmação automática"},
		"en":    {"starts in read-only mode", "bound preview", "preview_fingerprint", "confirm=true", "same MCP client", "request the preview and confirm", "does not authenticate separate human approval", "configure the client", "explicit OK before every write", "do not allow automatic confirmation"},
		"es":    {"empieza en modo solo lectura", "vista previa vinculada", "preview_fingerprint", "confirm=true", "mismo cliente MCP", "pedir la vista previa y confirmar", "no autentica una aprobación humana separada", "configura el cliente", "OK explícito antes de cada escritura", "no autorices confirmación automática"},
	}
	oldGuarantees := []string{
		"Nada é enviado sem uma prévia e a sua aprovação", "não envia nada sem a sua aprovação", "A mensagem só sai depois da sua aprovação explícita", "cada envio passa por uma prévia que você aprova",
		"Nothing is sent without a preview and your approval", "sends nothing without your approval", "The message only goes out after your explicit approval", "every send goes through a preview you approve",
		"No se envía nada sin una vista previa y tu aprobación", "no envía nada sin tu aprobación", "El mensaje solo sale después de tu aprobación explícita", "cada envío pasa por una vista previa que apruebas",
	}
	both(t, func(t *testing.T, b built, _ string) {
		for _, lang := range b.site.Languages {
			t.Run(lang.Code, func(t *testing.T) {
				page := b.pages[outFile(lang, "index")]
				text := reviewFAQ(t, page, "faq-sending")
				reviewRequire(t, text, want[lang.Code])
				for _, categoricalNo := range []string{"Não.", "No."} {
					if strings.Contains(text, "? "+categoricalNo) {
						t.Error("FAQ must not categorically deny autonomous sends")
					}
				}
				// The nearby account-risk answer must not repeat the old guarantee.
				reviewRequire(t, reviewFAQ(t, page, "faq-restriction"), []string{"MCP"})
				for file, markup := range b.pages {
					if langOf(b.site, file) != lang.Code {
						continue
					}
					for _, claim := range oldGuarantees {
						if strings.Contains(reviewContentText(markup), claim) {
							t.Errorf("%s: unconditional human-approval guarantee %q", file, claim)
						}
					}
				}
			})
		}
	})
}

func TestReviewC3BrandQualifiedInTitlesAndHeadings(t *testing.T) {
	both(t, func(t *testing.T, b built, _ string) {
		for file, markup := range b.pages {
			qualified := b.strs[langOf(b.site, file)]["brand.qualifier"] + " " + b.site.Brand
			for _, tag := range []string{"title", "h1", "h2"} {
				re := regexp.MustCompile(`(?s)<` + tag + `(?:\s[^>]*)?>(.*?)</` + tag + `>`)
				for _, match := range re.FindAllStringSubmatch(markup, -1) {
					text := reviewContentText(match[1])
					// Descriptive mentions of Telegram chats are not the app brand.
					// Count every occurrence, so one qualifier cannot mask a second name.
					if strings.Count(text, b.site.Brand) != strings.Count(text, qualified) {
						t.Errorf("%s: <%s> %q must qualify every %q as %q", file, tag, text, b.site.Brand, qualified)
					}
				}
			}
		}
	})
}

func TestReviewC1TermsDiscloseInferenceRisk(t *testing.T) {
	// Keep semantic anchors small: this is a disclosure contract, not a prose golden.
	want := map[string][]string{
		"pt-BR": {"1.5", "deployment", "pode abranger", "LLM", "Whisper local", "mesmo sem treino", "uso privado", "consentimento do titular", "modelo local", "api_id próprio", "não criam exceção automática", "dados de outras pessoas", "não elimina o risco contratual", "não há confirmação do Telegram", "não consultou o Telegram", "você decide", "assume o risco", "ajustado ou retirado"},
		"en":    {"1.5", "deployment", "may cover", "LLM", "local Whisper", "even without training", "private use", "account holder's consent", "local model", "your own api_id", "do not create an automatic exception", "other people's data", "does not remove the contractual risk", "no confirmation from Telegram", "has not consulted Telegram", "you decide", "accept the risk", "adjusted or removed"},
		"es":    {"1.5", "deployment", "puede abarcar", "LLM", "Whisper local", "incluso sin entrenamiento", "uso privado", "consentimiento del titular", "modelo local", "api_id propio", "no crean una excepción automática", "datos de otras personas", "no elimina el riesgo contractual", "no hay confirmación de Telegram", "no ha consultado a Telegram", "tú decides", "asumes el riesgo", "ajustarse o retirarse"},
	}
	both(t, func(t *testing.T, b built, _ string) {
		for _, lang := range b.site.Languages {
			t.Run(lang.Code, func(t *testing.T) {
				text := reviewFAQ(t, b.pages[outFile(lang, "index")], "faq-terms")
				reviewRequire(t, text, want[lang.Code])
			})
		}
	})
}
