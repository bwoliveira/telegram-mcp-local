package mcp

import (
	"regexp"
	"testing"
)

func TestToolDescriptionsAvoidConcealmentPhrases(t *testing.T) {
	patterns := []string{`(?i)\bdo not (reveal|tell|mention|disclose)\b`, `(?i)\b(hide|conceal|secret)\b`, `(?i)\bignore previous\b`, `(?i)\bnever tell the user\b`}
	defs := toolDefinitions()
	if len(defs) != 40 {
		t.Fatalf("catalog size = %d, want 40", len(defs))
	}
	for _, raw := range defs {
		def := raw.(map[string]any)
		description := def["description"].(string)
		for _, pattern := range patterns {
			if regexp.MustCompile(pattern).MatchString(description) {
				t.Errorf("%s: description matches %q", def["name"], pattern)
			}
		}
	}
}
