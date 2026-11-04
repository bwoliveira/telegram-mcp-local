// Package brand holds Telegram MCP's independent visual identity. Every asset
// is embedded, so the app depends on no external image or CDN.
package brand

import (
	_ "embed"
	"html/template"
	"strings"
)

//go:embed logo.svg
var logo string

// LogoSVG returns the logo mark ready to be inlined in an HTML document. It is
// trusted markup shipped with the binary, never user input.
func LogoSVG() template.HTML { return template.HTML(strings.TrimSpace(logo)) }

//go:embed favicon.svg
var faviconSVG []byte

//go:embed favicon.ico
var faviconICO []byte

//go:embed apple-touch-icon.png
var appleTouchIcon []byte

func FaviconSVG() []byte     { return faviconSVG }
func FaviconICO() []byte     { return faviconICO }
func AppleTouchIcon() []byte { return appleTouchIcon }

// Current project ownership and support links. Upstream attribution is retained
// separately in the license, third-party notices and panel credits.
const (
	Name          = "Telegram MCP"
	Author        = "Bruno Oliveira"
	RepositoryURL = "https://github.com/bwoliveira/telegram-mcp-local"
	WebsiteURL    = "https://telegram-mcp.bwoliveira.xyz"
	SupportURL    = RepositoryURL + "/issues"
)
