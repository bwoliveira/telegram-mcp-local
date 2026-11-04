package panel

import (
	"html/template"
	"net/http"

	"github.com/bwoliveira/telegram-mcp-local/internal/webhook"
)

// RegisterWebhookDocs adds only the documentation page. The daemon registers
// httpserver.RegisterMediaAPIs separately on its private authenticated mux;
// the panel must not shadow or duplicate those JSON APIs.
func (p *Panel) RegisterWebhookDocs(mux *http.ServeMux) {
	template.Must(p.pages.Parse(webhookDocsSource))
	mux.HandleFunc("GET /webhooks/documentacao", p.page(p.webhookDocs))
}

// RegisterWebhooks is the source-compatible documentation registration for
// older daemon wiring. It deliberately registers no webhook or media APIs.
// Deprecated: use RegisterWebhookDocs and httpserver.RegisterMediaAPIs.
func (p *Panel) RegisterWebhooks(mux *http.ServeMux, _ *webhook.Manager, _ bool) {
	p.RegisterWebhookDocs(mux)
}
