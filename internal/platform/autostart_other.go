//go:build !windows

package platform

import "errors"

func setRunAtLogin(bool, string) error { return errors.New("Windows Run entry unavailable") }
func runAtLoginEnabled(string) (bool, error) {
	return false, errors.New("Windows Run entry unavailable")
}
