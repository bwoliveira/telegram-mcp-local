package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// These fixtures contain only synthetic metadata. A MIME type or file extension
// is deliberately not evidence of Telegram's document-attribute classification.
func TestDocumentSubtypeMigrationV4AtomicIdempotent25K(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	clock := func() time.Time { return time.Unix(2000, 0) }
	s, err := Open(ctx, path, "primary", WithClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	queryPeer(t, s, "user", 10, "user")
	type fixture struct {
		mediaType, json, want string
		ttl, expiry           int
		tombstone, pending    bool
	}
	fixtures := []fixture{
		{"document", `{"type":"video","filename":"keep.bin","downloadable":false,"future_field":"keep"}`, "video", 0, 0, false, false},
		{"document", `{"type":"document","document_attributes":{"video":true}}`, "video", 0, 0, false, false},
		{"document", `{"type":"document","document_attributes":{"video":true,"round_message":true}}`, "video_note", 0, 0, false, false},
		{"document", `{"type":"document","document_attributes":{"audio":true}}`, "audio", 0, 0, false, false},
		{"document", `{"type":"document","document_attributes":{"audio":true,"voice":true}}`, "voice", 0, 5000, false, false},
		{"document", `{"type":"document","document_attributes":{"video":true,"animated":true}}`, "animation", 0, 0, false, false},
		{"document", `{"type":"document","document_attributes":{"video":true,"animated":true,"sticker":true}}`, "sticker", 0, 0, false, false},
		{"document", `{"type":"document","mime_type":"video/mp4","filename":"clip.mp4","duration_seconds":10,"width":20}`, "document", 0, 0, false, false},
		{"document", `{"type":"document","mime_type":"audio/ogg","filename":"voice.ogg"}`, "document", 0, 0, false, false},
		{"document", `{"type":"document"}`, "document", 0, 0, false, false},
		{"document", `{"type":"video","ttl_seconds":30}`, "document", 30, 0, false, false},
		{"document", `{"type":"video"}`, "document", 0, 1900, false, false},
		{"photo", `{"type":"video"}`, "photo", 0, 0, false, false},
		{"voice", `{"type":"video"}`, "voice", 0, 0, false, false},
		{"document", `{"type":"photo","document_attributes":{"video":true}}`, "document", 0, 0, false, false},
		{"document", `not-json`, "document", 0, 0, false, false},
		{"document", `{"type":"document","document_attributes":{"voice":true,"round_message":true}}`, "document", 0, 0, false, false},
		{"document", `{"type":"video"}`, "document", 0, 0, true, false},
		{"document", `{"type":"video"}`, "document", 0, 0, false, true},
		{"document", `{"type":"video","ttl_seconds":30}`, "document", 0, 0, false, false},
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	const count = 25000
	stmt, err := tx.Prepare(`INSERT INTO messages(account,peer_type,peer_id,msg_id,date,edit_date,text,media_type,media_json,media_remote,media_ttl,expires_at,ttl_period,protected,version,source,first_seen_at,updated_at) VALUES('primary','user',10,?,1000,1500,'synthetic caption',?,?,X'010203',?,?,4000,1,7,'backfill',1200,1600)`)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]int{}
	for i := 1; i <= count; i++ {
		f := fixtures[(i-1)%len(fixtures)]
		var expiry any
		if f.expiry > 0 {
			expiry = f.expiry
		}
		if _, err := stmt.Exec(i, f.mediaType, f.json, f.ttl, expiry); err != nil {
			t.Fatal(err)
		}
		expected[f.want]++
		if f.tombstone {
			if _, err := tx.Exec(`INSERT INTO message_tombstones VALUES('primary','user',10,?,'deleted',1800)`, i); err != nil {
				t.Fatal(err)
			}
		}
		if f.pending {
			if _, err := tx.Exec(`INSERT INTO pending_deletes VALUES('primary',?,1800)`, i); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`CREATE TABLE migration_original AS SELECT * FROM messages; PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("v4 interrupted")
	start := time.Now()
	failed, err := Open(ctx, path, "primary", WithClock(clock), FaultAt(func(stage string) error {
		if stage == "migration_v4_before_commit" {
			return injected
		}
		return nil
	}))
	if failed != nil {
		failed.Close()
	}
	if !errors.Is(err, injected) {
		t.Fatalf("migration did not reach transactional fault: %v", err)
	}
	t.Logf("25k migration rollback duration: %s", time.Since(start))
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version, changed int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM (SELECT * FROM messages EXCEPT SELECT * FROM migration_original)`).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if version != 3 || changed != 0 {
		t.Fatalf("partial migration version=%d changed=%d", version, changed)
	}
	for restart := 0; restart < 2; restart++ {
		start = time.Now()
		s, err = Open(ctx, path, "primary", WithClock(clock), FaultAt(func(stage string) error {
			if restart > 0 && stage == "migration_v4_before_commit" {
				return errors.New("v4 reran on reopen")
			}
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		t.Logf("25k migration/reopen %d duration: %s", restart, elapsed)
		if elapsed > 30*time.Second {
			t.Fatalf("25k migration/reopen exceeded 30s budget: %s", elapsed)
		}
		if err := s.reader.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
			t.Fatalf("version=%d err=%v; want %d", version, err, currentSchemaVersion)
		}
		actual := map[string]int{}
		rows, err := s.reader.Query(`SELECT media_type,count(*) FROM messages GROUP BY media_type`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				t.Fatal(err)
			}
			actual[k] = n
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		for k, want := range expected {
			if actual[k] != want {
				t.Errorf("%s count=%d want %d", k, actual[k], want)
			}
		}
		var total int
		for _, n := range actual {
			total += n
		}
		if total != count {
			t.Fatalf("count=%d want %d", total, count)
		}
		// Check every non-classification column, not only a selected payload subset.
		rows, err = s.reader.Query(`PRAGMA table_info(messages)`)
		if err != nil {
			t.Fatal(err)
		}
		cols := []string{}
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var def any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
				t.Fatal(err)
			}
			if name != "media_type" && name != "media_json" {
				cols = append(cols, name)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		colList := strings.Join(cols, ",")
		if err := s.reader.QueryRow(`SELECT count(*) FROM (SELECT ` + colList + ` FROM messages EXCEPT SELECT ` + colList + ` FROM migration_original)`).Scan(&changed); err != nil || changed != 0 {
			t.Fatalf("non-type metadata changed=%d err=%v", changed, err)
		}
		rows, err = s.reader.Query(`SELECT m.msg_id,m.media_type,m.media_json,o.media_json FROM messages m JOIN migration_original o USING(row_id) ORDER BY m.msg_id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int
			var kind, got, before string
			if err := rows.Scan(&id, &kind, &got, &before); err != nil {
				t.Fatal(err)
			}
			want := fixtures[(id-1)%len(fixtures)].want
			if kind != want {
				t.Fatalf("id %d kind=%s want=%s", id, kind, want)
			}
			if kind == fixtures[(id-1)%len(fixtures)].mediaType {
				if got != before {
					t.Fatalf("unknown/blocked id %d payload changed", id)
				}
				continue
			}
			var original, current map[string]any
			if err := json.Unmarshal([]byte(before), &original); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(got), &current); err != nil {
				t.Fatal(err)
			}
			if current["type"] != want {
				t.Fatalf("id %d JSON type=%v want=%s", id, current["type"], want)
			}
			original["type"] = want
			a, _ := json.Marshal(original)
			b, _ := json.Marshal(current)
			if string(a) != string(b) {
				t.Fatalf("id %d payload changed: %s vs %s", id, a, b)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		// Reapplying v4 explicitly is also a no-op, even with preserved legacy rows.
		// Keep an exact durable snapshot across both the forced migration and
		// the subsequent ordinary reopen.
		if restart == 0 {
			if _, err := s.writer.Exec(`CREATE TABLE migration_repaired AS SELECT * FROM messages; PRAGMA user_version=3`); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(ctx, path, "primary", WithClock(clock))
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := s.reader.QueryRow(`SELECT count(*) FROM (SELECT * FROM messages EXCEPT SELECT * FROM migration_repaired)`).Scan(&changed); err != nil || changed != 0 {
			t.Fatalf("idempotent rerun/reopen changed rows: %d %v", changed, err)
		}
		var integrity string
		if err := s.reader.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatal(fmt.Sprintf("integrity=%s err=%v", integrity, err))
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDocumentAttributeEvidenceRoundTripsWithoutSecrets(t *testing.T) {
	s := queryStore(t)
	var media engine.Media
	if err := json.Unmarshal([]byte(`{"type":"video_note","document_attributes":{"video":true,"round_message":true},"downloadable":false}`), &media); err != nil {
		t.Fatal(err)
	}
	m := engine.Message{Key: engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}, Date: time.Unix(1000, 0), Media: &media}
	if _, err := s.Apply(context.Background(), engine.Batch{Account: "primary", Messages: []engine.Message{m}}); err != nil {
		t.Fatal(err)
	}
	var retained string
	if err := s.reader.QueryRow(`SELECT media_json FROM messages WHERE msg_id=8`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(retained), &got); err != nil {
		t.Fatal(err)
	}
	attrs, ok := got["document_attributes"].(map[string]any)
	if !ok || attrs["video"] != true || attrs["round_message"] != true {
		t.Fatalf("document attribute evidence lost: %s", retained)
	}
}
