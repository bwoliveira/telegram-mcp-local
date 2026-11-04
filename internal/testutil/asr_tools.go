package testutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// InstallASRTools puts native helper executables on PATH, never a shell script
// masquerading as an .exe. Each package using it calls RunASRHelper in TestMain.
func InstallASRTools(t testing.TB, bin, transcript string) {
	t.Helper()
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ffmpeg", "whisper-cli"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		src, err := os.Open(executable)
		if err != nil {
			t.Fatal(err)
		}
		dst, err := os.OpenFile(filepath.Join(bin, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			src.Close()
			t.Fatal(err)
		}
		_, err = io.Copy(dst, src)
		src.Close()
		closeErr := dst.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("copy native helper: %v %v", err, closeErr)
		}
	}
	t.Setenv("TELEGRAM_MCP_TEST_ASR_HELPER", "1")
	t.Setenv("TELEGRAM_MCP_TEST_ASR_TEXT", transcript)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// RunASRHelper only intercepts explicitly marked, renamed test executables.
// The production localasr code still discovers and launches real subprocesses.
func RunASRHelper() {
	if os.Getenv("TELEGRAM_MCP_TEST_ASR_HELPER") != "1" {
		return
	}
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	var path string
	var body []byte
	switch name {
	case "ffmpeg":
		if len(os.Args) < 2 {
			os.Exit(2)
		}
		path = os.Args[len(os.Args)-1]
		body = []byte("wav")
	case "whisper-cli":
		for i, arg := range os.Args {
			if arg == "-of" && i+1 < len(os.Args) {
				path = os.Args[i+1] + ".txt"
			}
		}
		body = []byte(os.Getenv("TELEGRAM_MCP_TEST_ASR_TEXT") + "\n")
	default:
		return
	}
	if path == "" {
		os.Exit(2)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}
