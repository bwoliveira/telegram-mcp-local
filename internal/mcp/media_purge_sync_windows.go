package mcp

import "os"

// Windows does not support POSIX directory fsync on the read-only handles
// returned by os.Root.Open. Namespace operations remain atomic and recoverable
// after process termination; no power-loss durability is promised on Windows.
func syncMediaPurgeDir(root *os.Root) error { return nil }
