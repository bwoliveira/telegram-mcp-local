package pocstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

var ctx = context.Background()
var peer = engine.PeerRef{Type: engine.PeerUser, ID: 10}

func message(id int, text string) engine.Message {
	return engine.Message{Key: engine.MessageKey{Account: "primary", Peer: peer, ID: id}, Date: time.Unix(100, 0).UTC(), Text: text}
}
func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func apply(t *testing.T, s *Store, b engine.Batch) engine.Applied {
	t.Helper()
	if b.Account == "" {
		b.Account = "primary"
	}
	r, err := s.Apply(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func mustMessage(t *testing.T, s *Store, m engine.Message) engine.Message {
	t.Helper()
	got, ok, err := s.Message(ctx, m.Key)
	if err != nil || !ok {
		t.Fatalf("Message: %v %v", ok, err)
	}
	return got
}
func missing(t *testing.T, s *Store, k engine.MessageKey) {
	t.Helper()
	_, ok, err := s.Message(ctx, k)
	if err != nil || ok {
		t.Fatalf("Message: %v %v", ok, err)
	}
}
func noEvents(t *testing.T, r engine.Applied) {
	t.Helper()
	r.Skipped = 0
	if !reflect.DeepEqual(r, engine.Applied{}) {
		t.Fatalf("replay generated events: %+v", r)
	}
}

func TestReplayAndStaleEdits(t *testing.T) {
	s := openStore(t)
	m := message(1, "old")
	b := engine.Batch{Messages: []engine.Message{m}}
	r := apply(t, s, b)
	if len(r.Inserted) != 1 {
		t.Fatalf("insert: %+v", r)
	}
	var before int64
	if err := s.db.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	noEvents(t, apply(t, s, b))
	var after int64
	if err := s.db.QueryRow("SELECT total_changes()").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("replay changed rows: %d -> %d", before, after)
	}
	edit := m
	at := time.Unix(200, 0).UTC()
	edit.EditDate = &at
	edit.Text = "new"
	r = apply(t, s, engine.Batch{Edits: []engine.Message{edit}})
	if len(r.Edited) != 1 {
		t.Fatalf("edit: %+v", r)
	}
	noEvents(t, apply(t, s, engine.Batch{Source: engine.SourceBackfill, Messages: []engine.Message{m}}))
	if got := mustMessage(t, s, m); got.Text != "new" {
		t.Fatalf("regressed: %+v", got)
	}
	noEvents(t, apply(t, s, engine.Batch{Edits: []engine.Message{edit}}))
	// Full edit arriving before the original is still the authoritative row.
	edit.Key.ID = 2
	apply(t, s, engine.Batch{Edits: []engine.Message{edit}})
	m.Key.ID = 2
	apply(t, s, engine.Batch{Messages: []engine.Message{m}})
	if got := mustMessage(t, s, m); got.Text != "new" {
		t.Fatal(got.Text)
	}
}

func TestDeletesAndPendingAreAccountAndNamespaceScoped(t *testing.T) {
	s := openStore(t)
	user := message(8, "user")
	chat := user
	chat.Key.Peer.Type = engine.PeerChat
	channel := user
	channel.Key.Peer.Type = engine.PeerChannel
	apply(t, s, engine.Batch{Messages: []engine.Message{user, chat, channel}})
	other := user
	other.Key.Account = "other"
	apply(t, s, engine.Batch{Account: "other", Messages: []engine.Message{other}})
	keys, err := s.ResolveMessageIDs(ctx, "primary", []int{8})
	if err != nil || len(keys) != 2 {
		t.Fatalf("resolve: %+v %v", keys, err)
	}
	b := engine.Batch{Deletes: []engine.Deletion{{IDs: []int{8, 99}}}}
	r := apply(t, s, b)
	if len(r.Deleted) != 2 || len(r.UnresolvedDeletes) != 1 {
		t.Fatalf("delete: %+v", r)
	}
	missing(t, s, user.Key)
	missing(t, s, chat.Key)
	mustMessage(t, s, channel)
	mustMessage(t, s, other)
	noEvents(t, apply(t, s, b))
	noEvents(t, apply(t, s, engine.Batch{Messages: []engine.Message{user, chat}}))
	unknown := message(99, "late copy")
	apply(t, s, engine.Batch{Messages: []engine.Message{unknown}})
	missing(t, s, unknown.Key)
	// Pending global deletes must not affect the channel namespace.
	unknown.Key.Peer.Type = engine.PeerChannel
	apply(t, s, engine.Batch{Messages: []engine.Message{unknown}})
	mustMessage(t, s, unknown)
	b = engine.Batch{Deletes: []engine.Deletion{{Peer: &channel.Key.Peer, IDs: []int{8}}}}
	if r = apply(t, s, b); len(r.Deleted) != 1 {
		t.Fatal(r)
	}
	noEvents(t, apply(t, s, b))
	apply(t, s, engine.Batch{Messages: []engine.Message{channel}})
	missing(t, s, channel.Key)
}

func TestPageAndCursorRollbackTogether(t *testing.T) {
	s := openStore(t)
	m := message(1, "page")
	cursor := engine.Cursor{Peer: peer, OldestID: 1, NewestID: 10, State: "idle", Fetched: 10}
	bad := message(2, "bad")
	bad.Service = &engine.ServiceAction{Type: "invalid", Data: map[string]any{"not_json": make(chan int)}}
	_, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{m, bad}, Cursor: &cursor})
	if err == nil {
		t.Fatal("expected transaction failure")
	}
	missing(t, s, m.Key)
	if _, ok, err := s.Cursor(ctx, "primary", peer); err != nil || ok {
		t.Fatalf("cursor committed without page: %v %v", ok, err)
	}
	apply(t, s, engine.Batch{Messages: []engine.Message{m}, Cursor: &cursor})
	got, ok, err := s.Cursor(ctx, "primary", peer)
	if err != nil || !ok || got.OldestID != 1 {
		t.Fatalf("cursor: %+v %v %v", got, ok, err)
	}
	// A concurrent older in-flight page cannot move the cursor backwards.
	cursor.OldestID = 5
	cursor.NewestID = 8
	cursor.Fetched = 3
	apply(t, s, engine.Batch{Cursor: &cursor})
	got, _, err = s.Cursor(ctx, "primary", peer)
	if err != nil || got.OldestID != 1 || got.NewestID != 10 || got.Fetched != 10 {
		t.Fatalf("regressing cursor: %+v %v", got, err)
	}
}

func TestConservativeProtectedAndTTLPolicy(t *testing.T) {
	s := openStore(t)
	expired := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	cases := []engine.Message{message(1, "secret"), message(2, "secret"), message(3, "secret"), message(4, "secret"), message(5, "secret")}
	cases[0].Protected = true
	cases[1].TTLPeriod = 60
	cases[2].ExpiresAt = &expired
	cases[3].ExpiresAt = &future
	cases[4].Media = &engine.Media{Type: engine.MediaPhoto, TTLSeconds: 30, Remote: []byte("secret"), Downloadable: true}
	r := apply(t, s, engine.Batch{Messages: cases})
	if r.Skipped != len(cases) || len(r.Inserted) != 0 {
		t.Fatalf("unsafe content: %+v", r)
	}
	for _, m := range cases {
		missing(t, s, m.Key)
		m.Protected = false
		m.TTLPeriod = 0
		m.ExpiresAt = nil
		m.Media = nil
		apply(t, s, engine.Batch{Messages: []engine.Message{m}})
		missing(t, s, m.Key)
	}
	protectedPeer := engine.Peer{Ref: peer, Kind: engine.KindUser, Protected: true}
	apply(t, s, engine.Batch{Peers: []engine.PeerUpsert{{Peer: protectedPeer}}, Messages: []engine.Message{message(7, "secret")}})
	missing(t, s, message(7, "").Key)
}

func TestHashMinAndBindIdentity(t *testing.T) {
	s := openStore(t)
	p := engine.Peer{Ref: peer, Kind: engine.KindUser, FirstName: "Ana", Username: "ana"}
	apply(t, s, engine.Batch{Peers: []engine.PeerUpsert{{Peer: p, AccessHash: 11, HashMin: true}}})
	if _, ok, err := s.AccessHash(ctx, "primary", peer); err != nil || ok {
		t.Fatalf("min hash exposed: %v %v", ok, err)
	}
	apply(t, s, engine.Batch{Peers: []engine.PeerUpsert{{Peer: p, AccessHash: 22}}})
	p.FirstName = ""
	p.Username = ""
	apply(t, s, engine.Batch{Peers: []engine.PeerUpsert{{Peer: p, AccessHash: 33, HashMin: true}}})
	if hash, ok, err := s.AccessHash(ctx, "primary", peer); err != nil || !ok || hash != 22 {
		t.Fatalf("hash: %d %v %v", hash, ok, err)
	}
	got, ok, err := s.LookupPeer(ctx, "primary", peer)
	if err != nil || !ok || got.FirstName != "Ana" || got.Username != "ana" {
		t.Fatalf("peer: %+v %v %v", got, ok, err)
	}
	found, err := s.FindPeers(ctx, "primary", "@ANA", 5)
	if err != nil || len(found) != 1 {
		t.Fatalf("find: %+v %v", found, err)
	}
	if err := s.BindSelf(ctx, "primary", engine.Account{SelfID: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindSelf(ctx, "primary", engine.Account{SelfID: 11}); !errors.Is(err, engine.ErrAccountMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	id, err := s.SelfID(ctx, "primary")
	if err != nil || id != 10 {
		t.Fatalf("self: %d %v", id, err)
	}
}
