//go:build !windows

package mcp

import "os"

func tightenMediaDirectory(path string) error              { return os.Chmod(path, 0700) }
func tightenMediaFile(f *os.File) error                    { return f.Chmod(0600) }
func tightenMediaStaging(root *os.Root, path string) error { return root.Chmod(path, 0700) }
