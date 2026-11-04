package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

type statsRegisteredFake struct {
	ReadStore
	StatsExportRegistry
}
type statsFailRegistry struct{ StatsExportRegistry }

func (statsFailRegistry) CommitStatsExport(context.Context, engine.AccountID, string, int, int64) error {
	return errors.New("registry unavailable")
}

func statsReal(t *testing.T) (*Server, *store.Store, *fake.Controller, *time.Time) {
	t.Helper()
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "accounts", "people", "store.db"), "people", store.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := peopleFake(t)
	s := New(Config{Engine: e, ReadStore: st, ExportDir: filepath.Join(dir, "exports"), Clock: func() time.Time { return now }})
	return s, st, e, &now
}
func statsReadLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row map[string]any
		if err = json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	return out
}
func statsApply(t *testing.T, st *store.Store, b engine.Batch) {
	t.Helper()
	b.Account = "people"
	b.Source = engine.SourceUpdate
	if _, err := st.Apply(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

func TestStatsExportRealHeaderTTLProtectedAndRetention(t *testing.T) {
	s, st, e, now := statsReal(t)
	p := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	at := now.Add(-time.Hour)
	expiry := now.Add(time.Hour)
	makeMessage := func(id int, text string) engine.Message {
		return engine.Message{Key: engine.MessageKey{Account: "people", Peer: p, ID: id}, Date: at.Add(time.Duration(id) * time.Second), Text: text}
	}
	plain := makeMessage(1, "normal text")
	protected := makeMessage(2, "protected text")
	protected.Protected = true
	protected.Media = &engine.Media{Type: engine.MediaDocument, Filename: "private-file", MimeType: "secret/mime", Remote: []byte("remote-secret")}
	ttl := makeMessage(3, "omit-ttl")
	ttl.TTLPeriod = 7200
	timed := makeMessage(4, "omit-timed-media")
	timed.Media = &engine.Media{Type: engine.MediaPhoto, TTLSeconds: 20}
	expires := makeMessage(5, "omit-expiry")
	expires.ExpiresAt = &expiry
	statsApply(t, st, engine.Batch{Messages: []engine.Message{expires, protected, timed, plain, ttl}, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: p, Kind: engine.KindUser, FirstName: "Ana", Access: engine.AccessOK}}}, Cursor: &engine.Cursor{Peer: p, OldestID: 1, OldestDate: &at, Complete: true, State: "idle"}, Gaps: []engine.GapMark{{Peer: &p, Reason: engine.GapStateLost, Detected: *now}}})
	if cursor, found, err := st.Cursor(context.Background(), "people", p); err != nil || !found || cursor.OldestDate == nil {
		t.Fatalf("cursor: %+v %v %v", cursor, found, err)
	}
	body := toolBody(t, s.statsMessages(context.Background(), arguments{ChatJID: "user:1"}))
	if body["total"] != float64(5) || body["coverage_complete"] != false || body["gaps_known"] != true {
		t.Fatal(body)
	}
	result := s.exportStoreMessages(context.Background(), arguments{ChatJID: "user:1"})
	if result["isError"] != false {
		t.Fatal(result)
	}
	b := toolBody(t, result)
	if b["count"] != float64(2) || b["retention_registered"] != true {
		t.Fatal(b)
	}
	path := b["path"].(string)
	rows := statsReadLines(t, path)
	if len(rows) != 3 || rows[0]["type"] != "header" || rows[1]["id"] != "1" || rows[2]["id"] != "2" || rows[2]["text"] != "protected text" || rows[2]["protected"] != true {
		t.Fatal(rows)
	}
	coverage := rows[0]["coverage"].(map[string]any)
	if coverage["coverage_complete"] != false || len(coverage["gaps"].([]any)) != 1 || coverage["history_since"] == nil {
		t.Fatal(coverage)
	}
	data, _ := os.ReadFile(path)
	for _, forbidden := range []string{"omit-ttl", "omit-timed-media", "omit-expiry", "remote-secret", "private-file", "secret/mime", "access_hash", "media_remote"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("export contains %s", forbidden)
		}
	}
	info, err := os.Stat(path)
	if err != nil || service.CheckPrivateDataPath(path, false) != nil || float64(info.Size()) != b["bytes"] {
		t.Fatalf("file metadata: %v %v %v", info, err, b)
	}
	record, err := st.ReadStatsExport(context.Background(), "people", path)
	if err != nil || record.Count != 2 || record.Bytes != info.Size() || !record.CreatedAt.Equal(*now) {
		t.Fatalf("registration %+v %v", record, err)
	}
	for _, media := range []string{"any", "document"} {
		filtered := s.exportStoreMessages(context.Background(), arguments{ChatJID: "user:1", MediaType: media})
		if filtered["isError"] != false {
			t.Fatal(filtered)
		}
		got := toolBody(t, filtered)
		lines := statsReadLines(t, got["path"].(string))
		if got["count"] != float64(1) || len(lines) != 2 || lines[1]["text"] != "protected text" || lines[1]["protected"] != true {
			t.Fatal(got, lines)
		}
	}
	// Same clock/timestamp must produce another independent file, not overwrite.
	second := toolBody(t, s.exportStoreMessages(context.Background(), arguments{ChatJID: "user:1"}))
	if second["path"] == path {
		t.Fatal(second)
	}
	*now = now.Add(8 * 24 * time.Hour)
	if err := st.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(second["path"].(string)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := st.ReadStatsExport(context.Background(), "people", path); err == nil {
		t.Fatal("registration survived retention")
	}
	if len(e.Writes()) != 0 {
		t.Fatal(e.Writes())
	}
}

func TestStatsExportFakeReadStoreAndFailClosed(t *testing.T) {
	s, st, e, _ := statsReal(t)
	s.readStore = statsRegisteredFake{ReadStore: e, StatsExportRegistry: st}
	r := s.exportStoreMessages(context.Background(), arguments{ChatJID: "user:1001", Since: "2026-01-02T11:00:01Z", Until: "2026-01-02T11:00:04Z", Direction: "received"})
	if r["isError"] != false {
		t.Fatal(r)
	}
	b := toolBody(t, r)
	rows := statsReadLines(t, b["path"].(string))
	if b["count"] != float64(2) || len(rows) != 3 {
		t.Fatal(b, rows)
	}
	if rows[0]["coverage"].(map[string]any)["gaps_known"] != false {
		t.Fatal(rows)
	}
	if _, err := st.ReadStatsExport(context.Background(), "people", b["path"].(string)); err != nil {
		t.Fatal(err)
	}
	// No registry must fail before creating any directory or file.
	s.readStore = e
	s.exportDir = filepath.Join(t.TempDir(), "missing")
	if r = s.exportStoreMessages(context.Background(), arguments{}); r["isError"] != true {
		t.Fatal(r)
	}
	if _, err := os.Stat(s.exportDir); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// A registration failure must remove both final and partial files.
	failed, failedRegistry, _, _ := statsReal(t)
	s = failed
	s.readStore = statsRegisteredFake{ReadStore: e, StatsExportRegistry: statsFailRegistry{StatsExportRegistry: failedRegistry}}
	if r = s.exportStoreMessages(context.Background(), arguments{}); r["isError"] != true {
		t.Fatal(r)
	}
	entries, err := os.ReadDir(s.exportDir)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	if len(e.Writes()) != 0 {
		t.Fatal(e.Writes())
	}
}

func TestStatsExportErrorsAndEmptyHeader(t *testing.T) {
	s, st, _, _ := statsReal(t)
	r := s.exportStoreMessages(context.Background(), arguments{ChatJID: "channel:999"})
	if r["isError"] != false {
		t.Fatal(r)
	}
	b := toolBody(t, r)
	if b["count"] != float64(0) || len(statsReadLines(t, b["path"].(string))) != 1 {
		t.Fatal(b)
	}
	s.exportDir = filepath.Join(t.TempDir(), "wrong-root")
	if r = s.exportStoreMessages(context.Background(), arguments{}); r["isError"] != true {
		t.Fatal(r)
	}
	if _, err := os.Stat(s.exportDir); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	privateDir, _ := st.StatsExportDir(context.Background(), "people")
	s.exportDir = privateDir
	restore := testutil.LoosenPermissions(t, privateDir)
	if r = s.exportStoreMessages(context.Background(), arguments{}); r["isError"] != true {
		t.Fatal(r)
	}
	restore()
	// Account mismatch never falls back to all data or another account.
	e, err := fake.New(engine.Config{Account: "other"})
	if err != nil {
		t.Fatal(err)
	}
	s.eng = e
	if r = s.statsMessages(context.Background(), arguments{}); r["isError"] != true {
		t.Fatal(r)
	}
	if r = s.exportStoreMessages(context.Background(), arguments{}); r["isError"] != true {
		t.Fatal(r)
	}
	s.readStore = st
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r = s.exportStoreMessages(ctx, arguments{}); r["isError"] != true {
		t.Fatal(r)
	}
}
