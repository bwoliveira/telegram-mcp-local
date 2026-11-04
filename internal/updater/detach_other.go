//go:build !windows

package updater

import (
	"os"
	"os/exec"

	"github.com/bwoliveira/telegram-mcp-local/internal/platform"
)

// startDetached starts cmd in a session of its own, so it outlives the app.
func startDetached(cmd *exec.Cmd) (*os.Process, error) {
	platform.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}
