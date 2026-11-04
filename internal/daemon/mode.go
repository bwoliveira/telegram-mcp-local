package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// modeIntent is an independent durability fence. A state-db failure while
// disabling writes must not restore its stale read_write setting at restart.
func modeIntent(path string) engine.Mode {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) || info.Size() > 16 {
		return engine.ModeReadOnly
	}
	b, err := os.ReadFile(path)
	if err == nil && string(b) == string(engine.ModeReadWrite) {
		return engine.ModeReadWrite
	}
	return engine.ModeReadOnly
}
func (m *managedController) persistMode(mode engine.Mode) error {
	if m.modeFile == "" {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(m.modeFile), ".mode-*")
	if err != nil {
		return errors.New("cannot persist write mode intent")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.WriteString(string(mode)); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, m.modeFile)
	}
	if err == nil && runtime.GOOS != "windows" {
		d, e := os.Open(filepath.Dir(m.modeFile))
		if e == nil {
			err = d.Sync()
			d.Close()
		} else {
			err = e
		}
	}
	if err != nil {
		return errors.New("cannot persist write mode intent")
	}
	return nil
}
