package panel

import (
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
)

// function is one tool as the Funções tab shows it: the catalogue entry and
// what it touches, so a person sees at a glance which ones change Telegram.
type function struct {
	mcp.Tool
	Category string
	// Label is the category in the person's language.
	Label string
	// Changes is true for tools that change something other people or the
	// account can see: they always show a preview first, and in read-only
	// mode stop there.
	Changes bool
}

var categoryLabels = map[string]string{
	mcp.CategoryRead:     i18n.N("Leitura"),
	mcp.CategoryLocal:    i18n.N("Só neste computador"),
	mcp.CategorySend:     i18n.N("Envia"),
	mcp.CategoryDelete:   i18n.N("Apaga"),
	mcp.CategoryGroups:   i18n.N("Grupos"),
	mcp.CategoryOrganise: i18n.N("Organiza"),
}

func functionList(tools []mcp.Tool) []function {
	out := make([]function, 0, len(tools))
	for _, t := range tools {
		c := mcp.ToolCategory(t.Name)
		label, ok := categoryLabels[c]
		if !ok {
			c, label = "", ""
		}
		out = append(out, function{Tool: t, Category: c, Label: label,
			Changes: c == mcp.CategorySend || c == mcp.CategoryDelete || c == mcp.CategoryGroups || c == mcp.CategoryOrganise})
	}
	return out
}
