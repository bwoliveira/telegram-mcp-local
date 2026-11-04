package brand

import "testing"

func TestProjectOwnershipAndSupport(t *testing.T) {
	if Name != "Telegram MCP" {
		t.Errorf("project name = %q", Name)
	}
	if Author != "Bruno Oliveira" {
		t.Errorf("project author = %q", Author)
	}
	if SupportURL != RepositoryURL+"/issues" {
		t.Errorf("support URL = %q, want repository issues", SupportURL)
	}
}
