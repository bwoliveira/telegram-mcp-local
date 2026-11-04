package panel

import (
	"html/template"

	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// The panel uses its own blue mark, never Telegram's official logo or paper
// plane. Non-affiliation is explained in the footer and Help, not in the name.
var productName = i18n.N("Telegram MCP")

// upstreamURL is the project this one is based on, credited in every footer.
// This attribution is separate from the panel's own product identity.
const upstreamURL = "https://github.com/BrOrlandi/whatsapp-mcp-local"

// shortName is the same product name on compact surfaces.
const shortName = "Telegram MCP"

// markSVG is a speech bubble holding three linked nodes: messages reaching
// tools through MCP. It is inlined in every page and served as the favicon.
const markSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" aria-hidden="true" focusable="false">
<defs><linearGradient id="tgmcp-mark" x1="6" y1="6" x2="58" y2="58" gradientUnits="userSpaceOnUse"><stop offset="0" stop-color="#5b7cfa"/><stop offset="1" stop-color="#2a3fb0"/></linearGradient></defs>
<rect x="4" y="4" width="56" height="56" rx="16" fill="url(#tgmcp-mark)"/>
<path d="M19 17h26a7 7 0 0 1 7 7v13a7 7 0 0 1-7 7H29l-9 7v-7h-1a7 7 0 0 1-7-7V24a7 7 0 0 1 7-7Z" fill="none" stroke="#fff" stroke-width="3.4" stroke-linejoin="round"/>
<path d="M23 34l9-7 9 7" fill="none" stroke="#fff" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/>
<circle cx="23" cy="34" r="3.2" fill="#fff"/><circle cx="32" cy="27" r="3.2" fill="#fff"/><circle cx="41" cy="34" r="3.2" fill="#fff"/>
</svg>`

func panelMark() template.HTML { return template.HTML(markSVG) }
