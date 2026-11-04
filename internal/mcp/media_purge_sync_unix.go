//go:build !windows

package mcp

import (
	"errors"
	"os"
)

func syncMediaPurgeDir(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
