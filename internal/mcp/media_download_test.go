package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/localasr"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

type mediaSpy struct {
	engine.Engine
	downloads, native int
	downloadResult    *engine.Download
	nativeResult      engine.NativeTranscript
	nativeErr         error
}

func (e *mediaSpy) Download(ctx context.Context, r engine.DownloadRequest) (engine.Download, error) {
	e.downloads++
	if e.downloadResult != nil {
		return *e.downloadResult, nil
	}
	return e.Engine.Download(ctx, r)
}
func (e *mediaSpy) TranscribeNative(ctx context.Context, k engine.MessageKey) (engine.NativeTranscript, error) {
	e.native++
	return e.nativeResult, e.nativeErr
}

type mediaNoNetwork struct{ t *testing.T }

func (n mediaNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.t.Error("unexpected HTTP request in local transcription")
	return nil, errors.New("network forbidden")
}

func mediaFakeASR(t *testing.T) *localasr.Engine {
	t.Helper()
	data := t.TempDir()
	testutil.InstallASRTools(t, filepath.Join(data, "fake-bin"), "originalasrtoken")
	models := filepath.Join(data, "models")
	if err := os.Mkdir(models, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(models, localasr.ModelName))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(574041195); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	http.DefaultTransport = mediaNoNetwork{t}
	t.Cleanup(func() { http.DefaultTransport = old })
	return localasr.New(data)
}

func TestMediaPromptKeepsValidUTF8(t *testing.T) {
	got := mediaPrompt([]map[string]any{{"text": strings.Repeat("a", 8191) + "é"}})
	if !utf8.ValidString(got) || len(got) > 8192 || len(got) != 8191 {
		t.Fatalf("invalid prompt boundary: bytes=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestMediaLocalTranscriptionFakeBinaryAndFTS(t *testing.T) {
	s, st, key := mediaStoreServer(t)
	s.asr = mediaFakeASR(t)
	spy := &mediaSpy{Engine: s.eng}
	s.eng = spy
	raw := json.RawMessage(`{"chat_id":"user:1001","message_id":"5","language":"pt"}`)
	result, ok := s.MediaTool(context.Background(), "transcribe_audio", raw)
	if !ok || result["isError"] != false {
		t.Fatalf("transcribe tool=%+v", result)
	}
	tr, found, err := st.ReadTranscript(context.Background(), key)
	if err != nil || !found || tr.Text != "originalasrtoken" || tr.Raw != tr.Text || tr.Source != "local" {
		t.Fatalf("local transcript=%+v %v %v", tr, found, err)
	}
	if spy.downloads != 1 || spy.native != 0 {
		t.Fatalf("wrong operations: download=%d native=%d", spy.downloads, spy.native)
	}
	result, _ = s.MediaTool(context.Background(), "transcribe_audio", raw)
	if result["isError"] != false || toolBody(t, result)["cached"] != true || spy.downloads != 1 {
		t.Fatalf("cache performed download: %+v", result)
	}
	result, _ = s.MediaTool(context.Background(), "save_transcript", json.RawMessage(`{"message_id":"user:1001/5","text":"correctedasrtoken"}`))
	if result["isError"] != false {
		t.Fatal(result)
	}
	got, err := s.TranscribeMedia(context.Background(), key, "pt", true, false)
	if err != nil || got.Cached || got.Transcript.Raw != "originalasrtoken" || spy.native != 0 {
		t.Fatalf("refresh=%+v %v", got, err)
	}
	hits, err := st.SearchLocal(context.Background(), key.Account, "originalasrtoken")
	if err != nil || len(hits) != 1 {
		t.Fatalf("ASR not in FTS: %d %v", len(hits), err)
	}
}

func TestMediaLifecycleGuardsPrecedeEngineAndASR(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*engine.Message)
		want error
	}{
		{"protected", func(m *engine.Message) { m.Protected = true }, engine.ErrProtectedContent},
		{"temporary", func(m *engine.Message) { m.Media.TTLSeconds = 20 }, engine.ErrProtectedContent},
		{"expired", func(m *engine.Message) { at := m.Date.Add(-time.Second); m.ExpiresAt = &at }, engine.ErrMessageNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, st, key := mediaStoreServer(t)
			m, found, err := st.Message(context.Background(), key)
			if err != nil || !found {
				t.Fatal(err)
			}
			test.edit(&m)
			edited := s.clock()
			m.EditDate = &edited
			// Existing message TTL is immutable; exercise expiry as a fresh update.
			if test.name == "expired" {
				m.Key.ID = 105
				key = m.Key
			}
			if _, err = st.Apply(context.Background(), engine.Batch{Account: key.Account, Source: engine.SourceUpdate, Messages: []engine.Message{m}}); err != nil {
				t.Fatal(err)
			}
			spy := &mediaSpy{Engine: s.eng}
			s.eng = spy
			if _, err = s.DownloadMedia(context.Background(), key); !errors.Is(err, test.want) {
				t.Fatalf("download=%v", err)
			}
			if _, err = s.TranscribeMedia(context.Background(), key, "", true, true); !errors.Is(err, test.want) {
				t.Fatalf("transcribe=%v", err)
			}
			if _, err = s.SaveMediaTranscript(context.Background(), key, "forbidden", "", ""); !errors.Is(err, test.want) {
				t.Fatalf("save=%v", err)
			}
			if spy.downloads != 0 || spy.native != 0 {
				t.Fatal("guard executed remote call")
			}
			inv, err := s.Inventory(context.Background())
			if err != nil || inv.Files != 0 {
				t.Fatalf("guard created file: %+v %v", inv, err)
			}
		})
	}
}

func TestMediaLinkLifecycleAndNativeExplicitPending(t *testing.T) {
	s, st, key := mediaStoreServer(t)
	spy := &mediaSpy{Engine: s.eng, nativeResult: engine.NativeTranscript{Pending: true, ID: 42}}
	s.eng = spy
	got, err := s.TranscribeMedia(context.Background(), key, "", false, true)
	if err != nil || !got.Pending || spy.native != 1 || spy.downloads != 0 {
		t.Fatalf("native pending=%+v %v", got, err)
	}
	if _, found, err := st.ReadTranscript(context.Background(), key); err != nil || found {
		t.Fatalf("saved pending transcript: %v %v", found, err)
	}
	result, _ := s.MediaTool(context.Background(), "download_media", json.RawMessage(`{"message_id":"user:1001/5","link":true}`))
	if result["isError"] != false {
		t.Fatal(result)
	}
	link := toolBody(t, result)["url"].(string)
	path := link[strings.Index(link, "/media/"):]
	serve := func() int {
		rr := httptest.NewRecorder()
		s.links.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		return rr.Code
	}
	if serve() != 200 {
		t.Fatal("valid media not served")
	}
	peer := key.Peer
	if _, err := st.Apply(context.Background(), engine.Batch{Account: key.Account, Source: engine.SourceUpdate, Deletes: []engine.Deletion{{Peer: &peer, IDs: []int{key.ID}}}}); err != nil {
		t.Fatal(err)
	}
	if status := serve(); status != 404 {
		t.Fatalf("deleted message still downloadable: %d", status)
	}
}
