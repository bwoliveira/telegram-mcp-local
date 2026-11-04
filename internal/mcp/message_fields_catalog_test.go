package mcp

import (
	"reflect"
	"testing"
)

func TestToolsListMessageFieldsMatchValidator(t *testing.T) {
	wanted := map[string]bool{"get_chat_messages": true, "search_messages": true, "get_message_context": true}
	for _, raw := range toolDefinitions() {
		def := raw.(map[string]any)
		name := def["name"].(string)
		if !wanted[name] {
			continue
		}
		delete(wanted, name)
		t.Run(name, func(t *testing.T) {
			schema := def["inputSchema"].(map[string]any)
			fields := schema["properties"].(map[string]any)["fields"].(map[string]any)
			advertised := fields["items"].(map[string]any)["enum"].([]string)
			if !reflect.DeepEqual(advertised, messageFields) {
				t.Fatalf("tools/list fields = %v; validator accepts %v", advertised, messageFields)
			}
			for _, field := range advertised {
				if _, err := newShaping(arguments{Fields: []string{field}}, messageFields); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if len(wanted) > 0 {
		t.Fatalf("missing tools: %v", wanted)
	}
}
