package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

// Structural assertions intentionally do not import mcp.
var _ interface {
	ReadMessages(context.Context, engine.AccountID) ([]engine.Message, error)
	SearchLocal(context.Context, engine.AccountID, string) ([]engine.Message, error)
	ReadTranscript(context.Context, engine.MessageKey) (state.Transcript, bool, error)
} = (*Store)(nil)

func TestReadStoreMessagesAndSearchDoNotTruncate(t *testing.T) {
	s := queryStore(t)
	queryPeer(t, s, "user", 1, "user")
	querySQL(t, s, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1005)
 INSERT INTO messages(account,peer_type,peer_id,msg_id,date,text,source,first_seen_at,updated_at)
 SELECT 'primary','user',1,i,1000+i,'reunião amanhã','update',1000,1000 FROM n`)
	ctx := context.Background()
	for _, auth := range []engine.AuthState{engine.AuthLoggedOut, engine.AuthRevoked} {
		if err := s.SetAuthState(ctx, "primary", auth, "offline test"); err != nil {
			t.Fatal(err)
		}
		all, err := s.ReadMessages(ctx, "primary")
		if err != nil || len(all) != 1005 {
			t.Fatalf("snapshot count=%d err=%v", len(all), err)
		}
		hits, err := s.SearchLocal(ctx, "primary", `"reuniao amanha"`)
		if err != nil || len(hits) != 1005 {
			t.Fatalf("FTS count=%d err=%v", len(hits), err)
		}
		if !reflect.DeepEqual(all, hits) {
			t.Fatal("search changed snapshot projection")
		}
	}
	for _, query := range []string{"", " ", "\"unterminated", "' OR 1=1 --"} {
		if _, err := s.SearchLocal(ctx, "primary", query); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("bad query %q: %v", query, err)
		}
	}
	hits, err := s.SearchLocal(ctx, "primary", "reuni*")
	if err != nil || len(hits) != 1005 {
		t.Fatalf("prefix: %d %v", len(hits), err)
	}
}

func TestReadStoreMessageVisibilityTranscriptAndProjection(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	for _, p := range []struct{ typ, kind string }{{"user", "user"}, {"chat", "group"}, {"channel", "channel"}} {
		queryPeer(t, s, p.typ, 1, p.kind)
	}
	for i := 1; i <= 9; i++ {
		queryMsg(t, s, "user", 1, i, 1900, "matchtoken")
	}
	queryMsg(t, s, "chat", 1, 1, 1900, "matchtoken")
	queryMsg(t, s, "channel", 1, 1, 1900, "matchtoken")
	querySQL(t, s, `UPDATE messages SET expires_at=2000 WHERE peer_type='user' AND msg_id=2`)
	querySQL(t, s, `UPDATE messages SET ttl_period=100,expires_at=NULL WHERE peer_type='user' AND msg_id=3`)
	querySQL(t, s, `UPDATE messages SET ttl_period=100,expires_at=3000 WHERE peer_type='user' AND msg_id=4`)
	querySQL(t, s, `UPDATE messages SET expires_at=1999 WHERE peer_type='user' AND msg_id=5`)
	querySQL(t, s, `INSERT INTO message_tombstones VALUES('primary','user',1,6,'deleted',1900)`)
	// An ambiguous delete must not affect the same message ID in a channel.
	querySQL(t, s, `INSERT INTO pending_deletes VALUES('primary',1,1900)`)
	querySQL(t, s, `UPDATE messages SET expires_at=2001 WHERE peer_type='user' AND msg_id=7`)
	querySQL(t, s, `UPDATE messages SET media_type='photo',media_json='{"type":"photo","downloadable":true}',media_ttl=1 WHERE peer_type='user' AND msg_id=8`)
	// Message 9 has a transcript-only FTS hit and a rich nested projection.
	querySQL(t, s, `UPDATE messages SET text='ordinary',sender_name='senderonly',media_type='voice',media_json='{"type":"voice","downloadable":true}',media_remote=x'010203',entities_json='[{"type":"bold","offset":0,"length":3}]',sender_type='user',sender_id=2,poll_json='{"question":"q","options":[{"text":"x"}]}',service_type='test',service_json='{"data":{"key":"value"}}' WHERE peer_type='user' AND msg_id=9`)
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1}, ID: 9}
	if err := s.SaveTranscript(ctx, key, Transcript{Text: "orçamento matchtoken", RawText: "original", Language: "pt", Model: "fixture", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	// Insert stale transcript rows to prove TTL and tombstones are checked at read time.
	querySQL(t, s, `INSERT INTO transcripts(row_id,text,source,created_at) SELECT row_id,'matchtoken','manual',1000 FROM messages WHERE msg_id IN (2,3,4,5,6,7)`)
	all, err := s.ReadMessages(ctx, "primary")
	if err != nil || len(all) != 4 {
		t.Fatalf("visible: %+v err=%v", all, err)
	}
	hits, err := s.SearchLocal(ctx, "primary", "matchtoken")
	if err != nil || !reflect.DeepEqual(hits, all) {
		t.Fatalf("search visibility: %+v %v", hits, err)
	}
	hits, err = s.SearchLocal(ctx, "primary", "orcamento")
	if err != nil || len(hits) != 1 || hits[0].Key != key {
		t.Fatalf("transcript-only FTS: %+v %v", hits, err)
	}
	if hits[0].Media == nil || hits[0].Media.Remote != nil {
		t.Fatalf("remote leaked: %+v", hits)
	}
	hits[0].Media.Filename = "mutated"
	hits[0].Sender.ID = 999
	hits[0].Entities[0].Length = 999
	hits[0].Poll.Options[0].Text = "mutated"
	hits[0].Service.Data["key"] = "mutated"
	again, err := s.SearchLocal(ctx, "primary", "orcamento")
	if err != nil || again[0].Media.Filename != "" || again[0].Sender.ID != 2 || again[0].Entities[0].Length != 3 || again[0].Poll.Options[0].Text != "x" || again[0].Service.Data["key"] != "value" {
		t.Fatalf("alias: %+v %v", again, err)
	}
	if hits, err = s.SearchLocal(ctx, "primary", "senderonly"); err != nil || len(hits) != 0 {
		t.Fatalf("sender indexed as content: %+v %v", hits, err)
	}
	if hits, err = s.SearchLocal(ctx, "primary", "sender:senderonly"); err != nil || len(hits) != 0 {
		t.Fatalf("sender column bypass: %+v %v", hits, err)
	}
	if _, err = s.SearchLocal(ctx, "primary", "nomatch) OR sender:senderonly OR (nomatch"); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("unbalanced query escaped content columns: %v", err)
	}
	tr, ok, err := s.ReadTranscript(ctx, key)
	if err != nil || !ok || tr.ChatJID != "user:1" || tr.MessageID != "9" || tr.Text != "orçamento matchtoken" || tr.Raw != "original" || tr.Language != "pt" || tr.Model != "fixture" || tr.Source != "manual" || tr.CreatedAt.Unix() != 2000 {
		t.Fatalf("transcript: %+v %v %v", tr, ok, err)
	}
	for _, id := range []int{1, 2, 3, 4, 5, 6, 8, 99} {
		k := key
		k.ID = id
		if tr, ok, err = s.ReadTranscript(ctx, k); err != nil || ok || tr != (state.Transcript{}) {
			t.Fatalf("invisible transcript %d: %+v %v %v", id, tr, ok, err)
		}
	}
	// No janitor runs: both message and transcript disappear exactly at the TTL boundary.
	s.now = func() time.Time { return time.Unix(2001, 0).UTC() }
	all, err = s.ReadMessages(ctx, "primary")
	if err != nil || len(all) != 3 {
		t.Fatalf("boundary snapshot: %+v %v", all, err)
	}
	k := key
	k.ID = 7
	if _, ok, err = s.ReadTranscript(ctx, k); err != nil || ok {
		t.Fatalf("boundary transcript: %v %v", ok, err)
	}
	// Cached local reads survive access loss, and return no transport bytes.
	querySQL(t, s, `UPDATE peers SET access='lost',access_hash=987654321`)
	tr, ok, err = s.ReadTranscript(ctx, key)
	if err != nil || !ok {
		t.Fatalf("offline cache: %+v %v %v", tr, ok, err)
	}
	raw, _ := json.Marshal(all)
	if strings.Contains(string(raw), "remote") || strings.Contains(string(raw), "access_hash") || strings.Contains(string(raw), "987654321") {
		t.Fatalf("private projection: %s", raw)
	}
}

func TestReadStoreAccountAndContextGuards(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1}, ID: 1}
	for _, acc := range []engine.AccountID{"", "other"} {
		if _, err := s.ReadMessages(ctx, acc); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatalf("messages: %v", err)
		}
		if _, err := s.SearchLocal(ctx, acc, "needle"); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatalf("search: %v", err)
		}
		k := key
		k.Account = acc
		if _, _, err := s.ReadTranscript(ctx, k); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatalf("transcript: %v", err)
		}
	}
	for _, k := range []engine.MessageKey{{Account: "primary"}, {Account: "primary", Peer: key.Peer, ID: -1}, {Account: "primary", Peer: engine.PeerRef{Type: "unknown", ID: 1}, ID: 1}} {
		if _, _, err := s.ReadTranscript(ctx, k); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("key: %v", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	expired, finish := context.WithDeadline(ctx, time.Unix(1, 0))
	defer finish()
	for _, c := range []context.Context{canceled, expired} {
		if _, err := s.ReadMessages(c, "primary"); !errors.Is(err, c.Err()) {
			t.Fatal(err)
		}
		if _, err := s.SearchLocal(c, "primary", "needle"); !errors.Is(err, c.Err()) {
			t.Fatal(err)
		}
		if _, _, err := s.ReadTranscript(c, key); !errors.Is(err, c.Err()) {
			t.Fatal(err)
		}
	}
}
