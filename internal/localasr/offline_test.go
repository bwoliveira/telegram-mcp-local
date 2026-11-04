package localasr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/sidecar"
)

const privateMarker = "PRIVATE-message-transcript-conversation-context"

// TestASRProcess is re-executed by fake whisper/ffmpeg launchers. No real
// model, codec, network or external service is used by the offline tests.
func TestASRProcess(t *testing.T) {
	if os.Getenv("ASR_TEST_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 3 {
		os.Exit(90)
	}
	stage, args := args[1], args[2:]
	raw, _ := json.Marshal(args)
	if err := os.WriteFile(filepath.Join(os.Getenv("ASR_TEST_RECORDS"), stage+".json"), raw, 0o600); err != nil {
		os.Exit(91)
	}
	env, _ := json.Marshal(map[string]string{"TMPDIR": os.Getenv("TMPDIR"), "TMP": os.Getenv("TMP"), "TEMP": os.Getenv("TEMP")})
	_ = os.WriteFile(filepath.Join(os.Getenv("ASR_TEST_RECORDS"), stage+".env.json"), env, 0o600)
	fmt.Fprintln(os.Stdout, privateMarker)
	fmt.Fprintln(os.Stderr, privateMarker)
	mode := os.Getenv("ASR_TEST_" + strings.ToUpper(stage))
	var output string
	if stage == "ffmpeg" {
		output = args[len(args)-1]
	} else {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-of" {
				output = args[i+1] + ".txt"
			}
		}
	}
	if mode != "missing" {
		content := []byte(" Olá, equipe.\n[BLANK_AUDIO]\n[MUSIC]\n Reunião amanhã. \n")
		if mode == "empty" {
			content = nil
		}
		if err := os.WriteFile(output, content, 0o666); err != nil {
			os.Exit(92)
		}
	}
	if mode == "block" {
		_ = os.WriteFile(filepath.Join(os.Getenv("ASR_TEST_RECORDS"), stage+".ready"), []byte("ready"), 0o600)
		time.Sleep(30 * time.Second)
	}
	if mode == "fail" {
		os.Exit(7)
	}
	os.Exit(0)
}

func fakeEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	denyASRNetwork(t)
	if runtime.GOOS == "windows" {
		t.Skip("fake binary launchers require /bin/sh")
	}
	root := t.TempDir()
	records := filepath.Join(root, "records")
	if err := os.Mkdir(records, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASR_TEST_EXECUTABLE", executable)
	t.Setenv("ASR_TEST_PROCESS", "1")
	t.Setenv("ASR_TEST_RECORDS", records)
	tools := sidecar.Tools{Accel: "cpu"}
	for stage, dst := range map[string]*string{"ffmpeg": &tools.FFmpeg, "whisper": &tools.Whisper} {
		path := filepath.Join(root, stage)
		script := "#!/bin/sh\nexec \"$ASR_TEST_EXECUTABLE\" -test.run='^TestASRProcess$' -- " + stage + " \"$@\"\n"
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		*dst = path
	}
	old := systemTools
	systemTools = func() sidecar.Tools { return tools }
	t.Cleanup(func() { systemTools = old })
	e := New(filepath.Join(root, "data"))
	if err := os.MkdirAll(e.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Sparse placeholder: Status checks the local model size, not its content.
	f, err := os.Create(e.modelPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(modelSize); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return e, records
}

func TestTranscribeSubprocessErrorsArePrivate(t *testing.T) {
	for _, stage := range []string{"ffmpeg", "whisper"} {
		t.Run(stage, func(t *testing.T) {
			e, _ := fakeEngine(t)
			t.Setenv("ASR_TEST_"+strings.ToUpper(stage), "fail")
			text, err := e.Transcribe(context.Background(), privateMarker+".ogg", "pt", privateMarker)
			if err == nil || text != "" {
				t.Fatalf("want an empty transcript and failure; text=%q err=%v", text, err)
			}
			if strings.Contains(err.Error(), privateMarker) {
				t.Fatal("subprocess output leaked into returned error")
			}
			assertASRCleaned(t, e, os.Getenv("ASR_TEST_RECORDS"))
		})
	}
}

func waitForASRFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fake subprocess did not reach its synchronization point")
}

func startTranscribe(e *Engine, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := e.Transcribe(ctx, "private.ogg", "", "private context")
		done <- err
	}()
	return done
}

func TestTranscribeCancellationWhileQueued(t *testing.T) {
	e, records := fakeEngine(t)
	t.Setenv("ASR_TEST_FFMPEG", "block")
	active, stop := context.WithCancel(context.Background())
	first := startTranscribe(e, active)
	t.Cleanup(func() { stop(); <-first })
	waitForASRFile(t, filepath.Join(records, "ffmpeg.ready"))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	queued := startTranscribe(e, ctx)
	select {
	case err := <-queued:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued cancellation = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request remained queued behind an active transcription")
	}
}

func TestTranscribeCancellationDuringSubprocess(t *testing.T) {
	for _, stage := range []string{"ffmpeg", "whisper"} {
		t.Run(stage, func(t *testing.T) {
			e, records := fakeEngine(t)
			t.Setenv("ASR_TEST_"+strings.ToUpper(stage), "block")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := startTranscribe(e, ctx)
			waitForASRFile(t, filepath.Join(records, stage+".ready"))
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("subprocess cancellation = %v, want context canceled", err)
				}
				assertASRCleaned(t, e, records)
			case <-time.After(time.Second):
				t.Fatal("subprocess did not stop promptly")
			}
			t.Setenv("ASR_TEST_"+strings.ToUpper(stage), "")
			if _, err := e.Transcribe(context.Background(), "next.ogg", "", ""); err != nil {
				t.Fatalf("canceled transcription did not release its queue permit: %v", err)
			}
		})
	}
}

func asrArgs(t *testing.T, records, stage string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(records, stage+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	return args
}

func assertASRCleaned(t *testing.T, e *Engine, records string) {
	t.Helper()
	args := asrArgs(t, records, "ffmpeg")
	dir := filepath.Dir(args[len(args)-1])
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary artifact directory survived: %v", err)
	}
	entries, err := os.ReadDir(e.data)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "models" {
			t.Errorf("unexpected artifact left in data directory: %s", entry.Name())
		}
	}
}

func TestTranscribePrivateArtifacts(t *testing.T) {
	e, records := fakeEngine(t)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("ASR_TEST_WHISPER", "block")
	ctx, cancel := context.WithCancel(context.Background())
	done := startTranscribe(e, ctx)
	t.Cleanup(func() {
		cancel()
		<-done
		assertASRCleaned(t, e, records)
	})
	waitForASRFile(t, filepath.Join(records, "whisper.ready"))
	args := asrArgs(t, records, "ffmpeg")
	wav := args[len(args)-1]
	dir := filepath.Dir(wav)
	if filepath.Dir(dir) != e.data {
		t.Errorf("audio artifacts are not inside the data directory")
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, wav: 0o600, filepath.Join(dir, "out.txt"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("artifact %s permissions = %o, want %o", filepath.Base(path), info.Mode().Perm(), want)
		}
	}
	for _, stage := range []string{"ffmpeg", "whisper"} {
		raw, err := os.ReadFile(filepath.Join(records, stage+".env.json"))
		if err != nil {
			t.Fatal(err)
		}
		var env map[string]string
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		for key, value := range env {
			if value != dir {
				t.Errorf("%s subprocess %s does not point to its private directory", stage, key)
			}
		}
	}
}

type deniedASRTransport struct{ calls atomic.Int64 }

func (d *deniedASRTransport) RoundTrip(*http.Request) (*http.Response, error) {
	d.calls.Add(1)
	return nil, errors.New("network prohibited by offline ASR test")
}

func denyASRNetwork(t *testing.T) {
	t.Helper()
	client, transport := http.DefaultClient, http.DefaultTransport
	denied := &deniedASRTransport{}
	http.DefaultClient = &http.Client{Transport: denied}
	http.DefaultTransport = denied
	t.Cleanup(func() {
		http.DefaultClient, http.DefaultTransport = client, transport
		if n := denied.calls.Load(); n != 0 {
			t.Errorf("offline transcription attempted %d network requests", n)
		}
	})
}

func TestTranscribeOffline(t *testing.T) {
	for _, tc := range []struct{ name, language, prompt, wantLanguage, wantPrompt string }{
		{"automatic", "", "", "auto", ""},
		{"explicit", "pt", "  Conversa com José; $(never execute)  ", "pt", "Conversa com José; $(never execute)"},
		{"blank_prompt", "en", " \n\t ", "en", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, records := fakeEngine(t)
			if st := e.Status(); !st.Ready || st.Model != e.modelPath() || st.Install.State != "idle" {
				t.Fatalf("unexpected ready status: %+v", st)
			}
			text, err := e.Transcribe(context.Background(), "audio with spaces.ogg", tc.language, tc.prompt)
			if err != nil || text != "Olá, equipe. Reunião amanhã." {
				t.Fatalf("text=%q err=%v", text, err)
			}
			ffmpeg := asrArgs(t, records, "ffmpeg")
			for flag, want := range map[string]string{"-i": "audio with spaces.ogg", "-ar": "16000", "-ac": "1", "-c:a": "pcm_s16le", "-protocol_whitelist": "file,pipe"} {
				if got := asrArg(ffmpeg, flag); got != want {
					t.Errorf("ffmpeg %s=%q, want %q", flag, got, want)
				}
			}
			whisper := asrArgs(t, records, "whisper")
			for flag, want := range map[string]string{"-m": e.modelPath(), "-f": ffmpeg[len(ffmpeg)-1], "-l": tc.wantLanguage, "--prompt": tc.wantPrompt} {
				if got := asrArg(whisper, flag); got != want {
					t.Errorf("whisper %s=%q, want %q", flag, got, want)
				}
			}
			assertASRCleaned(t, e, records)
		})
	}
}

func asrArg(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func TestTranscribeDoesNotInstallMissingDependencies(t *testing.T) {
	for _, missing := range []string{"model", "tools", "all"} {
		t.Run(missing, func(t *testing.T) {
			e, records := fakeEngine(t)
			if missing != "tools" {
				if err := os.Remove(e.modelPath()); err != nil {
					t.Fatal(err)
				}
			}
			if missing != "model" {
				systemTools = func() sidecar.Tools { return sidecar.Tools{} }
			}
			text, err := e.Transcribe(context.Background(), privateMarker, "", privateMarker)
			if err == nil || text != "" || strings.Contains(err.Error(), privateMarker) {
				t.Fatalf("expected private missing-dependency failure, got %q %v", text, err)
			}
			if st := e.Status(); st.Ready || st.Install.State != "idle" {
				t.Fatalf("Transcribe changed installation state: %+v", st)
			}
			entries, err := os.ReadDir(records)
			if err != nil || len(entries) != 0 {
				t.Fatalf("a subprocess ran without dependencies: %v", err)
			}
			if _, err := os.Stat(e.modelPath() + ".part"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected model download artifact: %v", err)
			}
		})
	}
}

func TestTranscribeMissingTextIsPrivateAndCleaned(t *testing.T) {
	e, records := fakeEngine(t)
	t.Setenv("ASR_TEST_WHISPER", "missing")
	text, err := e.Transcribe(context.Background(), privateMarker, "", privateMarker)
	if err == nil || text != "" || strings.Contains(err.Error(), e.data) || strings.Contains(err.Error(), privateMarker) {
		t.Fatalf("missing transcript returned %q %v", text, err)
	}
	assertASRCleaned(t, e, records)
}

func TestTranscribeSilenceIsSuccessful(t *testing.T) {
	e, records := fakeEngine(t)
	t.Setenv("ASR_TEST_WHISPER", "empty")
	if text, err := e.Transcribe(context.Background(), "silence.ogg", "", ""); text != "" || err != nil {
		t.Fatalf("empty transcript = %q %v", text, err)
	}
	assertASRCleaned(t, e, records)
}

func TestTranscribeAlreadyCanceledDoesNotRun(t *testing.T) {
	e, records := fakeEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Transcribe(ctx, "audio.ogg", "", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	entries, err := os.ReadDir(records)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled request ran a subprocess: %v", err)
	}
}

func TestTranscribeRefusesDamagedInstalledTools(t *testing.T) {
	for _, damage := range []string{"missing", "tampered"} {
		t.Run(damage, func(t *testing.T) {
			e, records := fakeEngine(t)
			bin := sidecar.Dir(e.data)
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			files := make(map[string]string)
			for _, name := range []string{"whisper-cli", "ffmpeg"} {
				contents := []byte("installed fake " + name)
				if err := os.WriteFile(filepath.Join(bin, name), contents, 0o700); err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(contents)
				files[name] = hex.EncodeToString(sum[:])
			}
			raw, err := json.Marshal(map[string]any{"accel": "cpu", "files": files})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "manifest.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if damage == "missing" {
				err = os.Remove(filepath.Join(bin, "ffmpeg"))
			} else {
				err = os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte("tampered"), 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if st := e.Status(); st.Ready || st.Install.State != "error" {
				t.Errorf("damaged installed tools were replaced by unchecked system tools: %+v", st)
			}
			if _, err := e.Transcribe(context.Background(), "audio.ogg", "", ""); err == nil {
				t.Error("transcribed despite damaged installed tools")
			}
			entries, err := os.ReadDir(records)
			if err != nil || len(entries) != 0 {
				t.Errorf("system tools ran after installed tool damage: %v", err)
			}
		})
	}
}
