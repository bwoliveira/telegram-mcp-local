package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var portugueseCLILiteral = regexp.MustCompile(`(?i)[ãõçáéíóúâêôà]|\b(nao|baixando|baixar|modelo|instalado|instale|programas|mudou|precisa|confere|esperado|tente|parou|arquivo|pasta|senha|porta|iniciando|concluido|falha|erro|pronto|encontrado|desconectado)\b`)

// Scan production literals, not comments or message fixtures. Panel/tray copy
// explicitly wrapped in i18n is outside the English CLI/daemon contract.
func TestCLIAndDaemonStringsEnglish(t *testing.T) {
	root := filepath.Join("..", "..")
	portuguese := portugueseCLILiteral
	for _, dir := range []string{"cmd/telegram-mcp", "internal/cli", "cmd/telegram-mcp-bridge", "internal/daemon", "internal/localasr", "internal/sidecar", "internal/service", "internal/appconfig"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			files := token.NewFileSet()
			tree, err := parser.ParseFile(files, path, nil, 0)
			if err != nil {
				return err
			}
			localized := map[*ast.BasicLit]bool{}
			ast.Inspect(tree, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := selector.X.(*ast.Ident)
				if !ok || pkg.Name != "i18n" {
					return true
				}
				// Shared panel copy keeps its source-language i18n keys.
				// The CLI selects English before invoking shared code.
				if (selector.Sel.Name == "T" || selector.Sel.Name == "Tf" || selector.Sel.Name == "N") && len(call.Args) > 0 {
					if literal, ok := call.Args[0].(*ast.BasicLit); ok {
						localized[literal] = true
					}
				}
				return true
			})
			ast.Inspect(tree, func(node ast.Node) bool {
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING || localized[literal] {
					return true
				}
				value, err := strconv.Unquote(literal.Value)
				if err == nil && portuguese.MatchString(value) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s:%d: Portuguese CLI/daemon literal: %q", rel, files.Position(literal.Pos()).Line, value)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPortugueseLiteralDetector(t *testing.T) {
	// ASCII-only Portuguese must also be covered; accents alone miss regressions.
	pattern := portugueseCLILiteral
	for _, text := range []string{"Baixando o whisper.cpp", "modelo", "programas de transcrição"} {
		if !pattern.MatchString(text) {
			t.Errorf("missed Portuguese: %q", text)
		}
	}
	for _, text := range []string{"Downloading whisper.cpp", "model", "expected checksum"} {
		if pattern.MatchString(text) {
			t.Errorf("false positive: %q", text)
		}
	}
}
