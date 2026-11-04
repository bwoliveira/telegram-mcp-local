//go:build darwin && !cgo

package main

// The release build enables cgo and uses dock_darwin.go. These fallbacks keep
// compile-only purego cross-builds available on non-macOS hosts; they do not
// replace the native Cocoa integration used by packaged applications.
func launchedAtLogin() bool { return false }

func quitAskedByApp() bool { return false }

func inDock() bool { return true }

func menuBarOnly() bool { return true }

func showInDock(bool) {}

func activate() {}
