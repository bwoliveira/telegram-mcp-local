package cli

import (
	"os"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.RunASRHelper()
	os.Exit(m.Run())
}
