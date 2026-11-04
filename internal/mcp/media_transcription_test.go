package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	fakeengine "github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

func mediaStoreServer(t *testing.T) (*Server, *store.Store, engine.MessageKey) {
	t.Helper()
	ctx := context.Background()
	data := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(data, "store.db"), "primary", store.WithClock(fakeengine.Epoch))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctrl, err := fakeengine.New(engine.Config{Account: "primary", Mode: engine.ModeReadOnly, Clock: fakeengine.Epoch, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1001}, ID: 5}
	s := New(Config{Engine: ctrl, ReadStore: st, MediaDir: filepath.Join(data, "media"), Clock: fakeengine.Epoch})
	return s, st, key
}

func TestMediaTranscriptCorrectionPreservesRawAndUpdatesFTS(t *testing.T) {
	s, st, key := mediaStoreServer(t)
	ctx := context.Background()
	if err := st.SaveTranscript(ctx, key, store.Transcript{Text: "rawheardtoken", RawText: "rawheardtoken", Language: "pt", Model: "offline", Source: "local"}); err != nil {
		t.Fatal(err)
	}
	tr, err := s.SaveMediaTranscript(ctx, key, "correctedtoken", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Raw != "rawheardtoken" || tr.Text != "correctedtoken" || tr.Model != "offline" || tr.Language != "pt" {
		t.Fatalf("incorrect correction: %+v", tr)
	}
	tr, err = s.SaveMediaTranscript(ctx, key, "finaltoken", "", "")
	if err != nil || tr.Raw != "rawheardtoken" {
		t.Fatalf("second correction: %+v %v", tr, err)
	}
	for query, want := range map[string]int{"correctedtoken": 0, "rawheardtoken": 0, "finaltoken": 1} {
		hits, err := st.SearchLocal(ctx, key.Account, query)
		if err != nil || len(hits) != want {
			t.Fatalf("FTS %s: %d %v", query, len(hits), err)
		}
	}
}

func TestMediaTranscriptionCachedOfflineAndMissingEngine(t *testing.T) {
	s, st, key := mediaStoreServer(t)
	ctx := context.Background()
	if _, err := s.TranscribeMedia(ctx, key, "", false, false); !errors.Is(err, ErrNoEngine) {
		t.Fatalf("missing ASR: %v", err)
	}
	inv, err := s.Inventory(ctx)
	if err != nil || inv.Files != 0 {
		t.Fatalf("downloaded before checking ASR: %+v %v", inv, err)
	}
	if err := st.SaveTranscript(ctx, key, store.Transcript{Text: "cachedtoken", RawText: "originaltoken", Source: "local"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.TranscribeMedia(ctx, key, "", false, false)
	if err != nil || !got.Cached || got.Transcript.Text != "cachedtoken" {
		t.Fatalf("cached: %+v %v", got, err)
	}
	wrong := key
	wrong.Account = "other"
	if _, err := s.TranscribeMedia(ctx, wrong, "", false, false); !errors.Is(err, engine.ErrAccountMismatch) {
		t.Fatalf("account mismatch: %v", err)
	}
	if _, err := s.SaveMediaTranscript(ctx, key, "  ", "", ""); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("empty correction: %v", err)
	}
}
