package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Apply only historical migrations: never lower user_version on a current DB.
// In particular, v4 has none of the v5/v6/v7 columns or the v8 panel index.
func combinedMigrationFixture(t *testing.T, version int) (string, engine.MessageKey) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{writer: db, account: confAccount, now: func() time.Time { return confTime }}
	for _, migration := range storeMigrations()[:version] {
		if err := migration.apply(s, context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	key := engine.MessageKey{Account: confAccount, Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
	if _, err := db.Exec(`INSERT INTO accounts(account,updated_at) VALUES(?,1000);
INSERT INTO peers(account,peer_type,peer_id,kind,updated_at) VALUES(?,'user',10,'user',1000);
INSERT INTO messages(account,peer_type,peer_id,msg_id,date,edit_date,text,version,source,first_seen_at,updated_at)
VALUES(?,'user',10,8,1000,1200,'synthetic historical content',9,'backfill',1000,1000);
INSERT INTO reactions(row_id,counts_json,recent_json,counts_hash,updated_at)
SELECT row_id,'[{"emoji":"thumb","count":1}]','[]','legacy-hash',1000 FROM messages;`, confAccount, confAccount, confAccount); err != nil {
		t.Fatal(err)
	}
	if version >= 6 {
		if _, err := db.Exec(`UPDATE reactions SET version=17;
UPDATE messages SET server_edit_date=1200,content_hash='synthetic-content-hash'`); err != nil {
			t.Fatal(err)
		}
	}
	assertCombinedMigrationSchema(t, db, version)
	return path, key
}

func assertCombinedMigrationSchema(t *testing.T, db *sql.DB, wantVersion int) {
	t.Helper()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != wantVersion {
		t.Fatalf("schema version=%d err=%v; want %d", version, err, wantVersion)
	}
	for _, object := range []struct {
		name, query  string
		since, count int
	}{
		{"v5 revisions", `SELECT count(*) FROM pragma_table_info('messages') WHERE name IN ('server_edit_date','content_hash')`, 5, 2},
		{"v5 rights", `SELECT count(*) FROM pragma_table_info('participants') WHERE name IN ('admin_rights','banned_rights','banned_until','can_edit')`, 5, 4},
		{"v6 reactions", `SELECT count(*) FROM pragma_table_info('reactions') WHERE name='version'`, 6, 1},
		{"v7 identity", `SELECT count(*) FROM pragma_table_info('messages') WHERE name='content_identity' AND type='TEXT' AND "notnull"=1 AND dflt_value="''"`, 7, 1},
		{"v8 panel index", `SELECT count(*) FROM sqlite_schema WHERE type='index' AND tbl_name='messages' AND name='messages_panel_status'`, 8, 1},
		{"v9 terminal index", `SELECT count(*) FROM sqlite_schema WHERE type='index' AND tbl_name='gaps' AND name='gaps_access_lost_terminal'`, 9, 1},
	} {
		want := 0
		if wantVersion >= object.since {
			want = object.count
		}
		var count int
		if err := db.QueryRow(object.query).Scan(&count); err != nil || count != want {
			t.Fatalf("%s: count=%d err=%v; want %d at v%d", object.name, count, err, want, wantVersion)
		}
	}
	if wantVersion >= 8 {
		var columns string
		if err := db.QueryRow(`SELECT group_concat(name,',') FROM (SELECT name FROM pragma_index_info('messages_panel_status') ORDER BY seqno)`).Scan(&columns); err != nil || columns != "account,peer_type,peer_id,msg_id,expires_at,ttl_period,date,out,first_seen_at" {
			t.Fatalf("panel index columns=%q err=%v", columns, err)
		}
	}
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation after migration/rollback")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestCombinedMigrationV4V6ToCurrentRollbackAndReopen(t *testing.T) {
	ctx := context.Background()
	for _, start := range []int{4, 6} {
		for failVersion := start + 1; failVersion <= currentSchemaVersion; failVersion++ {
			t.Run(fmt.Sprintf("v%d/fail_v%d", start, failVersion), func(t *testing.T) {
				path, key := combinedMigrationFixture(t, start)
				injected := errors.New("interrupted combined migration")
				stage := fmt.Sprintf("migration_v%d_before_commit", failVersion)
				failed, err := Open(ctx, path, confAccount, FaultAt(func(hit string) error {
					if hit == stage {
						return injected
					}
					return nil
				}))
				if failed != nil {
					failed.Close()
					t.Fatal("interrupted Open returned a store")
				}
				if !errors.Is(err, injected) {
					t.Fatalf("fault at %s: %v", stage, err)
				}
				raw, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				// Earlier migrations commit independently; the failed migration
				// must leave neither its version nor any partial schema objects.
				assertCombinedMigrationSchema(t, raw, failVersion-1)
				if err := raw.Close(); err != nil {
					t.Fatal(err)
				}
				for reopen := 0; reopen < 2; reopen++ {
					var stages []string
					s, err := Open(ctx, path, confAccount, WithClock(func() time.Time { return confTime }), FaultAt(func(hit string) error {
						if strings.HasPrefix(hit, "migration") {
							stages = append(stages, hit)
						}
						return nil
					}))
					if err != nil {
						t.Fatal(err)
					}
					func() {
						defer s.Close()
						var wantStages []string
						if reopen == 0 {
							for v := failVersion; v <= currentSchemaVersion; v++ {
								wantStages = append(wantStages, fmt.Sprintf("migration_v%d_before_commit", v))
							}
						}
						if !reflect.DeepEqual(stages, wantStages) {
							t.Fatalf("reopen %d migration sequence=%v; want %v", reopen, stages, wantStages)
						}
						assertCombinedMigrationSchema(t, s.reader, currentSchemaVersion)
						old := confGet(t, s, key)
						wantHash, wantReactionVersion := "", int64(0)
						if start == 6 {
							wantHash, wantReactionVersion = "synthetic-content-hash", 17
						}
						if old.Text != "synthetic historical content" || old.ContentHash != wantHash || old.ContentIdentity != "" || old.ServerEditDate == nil || old.ServerEditDate.Unix() != 1200 || old.EditDate == nil || old.EditDate.Unix() != 1200 {
							t.Fatalf("historical payload changed: %+v", old)
						}
						if n := confCount(t, s, `SELECT version FROM messages WHERE msg_id=8`); n != 9 {
							t.Fatalf("historical message version=%d; want 9", n)
						}
						var reactionHash string
						if err := s.reader.QueryRow(`SELECT counts_hash FROM reactions`).Scan(&reactionHash); err != nil || reactionHash != "legacy-hash" || reactionRevision(t, s, key) != wantReactionVersion {
							t.Fatalf("historical reaction changed: hash=%q err=%v", reactionHash, err)
						}
						message := confMessage(key.Peer, 9, "combined migration write")
						message.ContentIdentity = "synthetic-canonical-identity"
						message.ContentHash = "synthetic-write-hash"
						// Read before replaying Apply: a write on the second open
						// must not hide a lost or unpersisted identity on restart.
						if reopen > 0 {
							persisted := confGet(t, s, message.Key)
							if persisted.Text != message.Text || persisted.ContentIdentity != message.ContentIdentity || persisted.ContentHash != message.ContentHash {
								t.Fatalf("reopen lost persisted identity: %+v", persisted)
							}
						}
						applied := confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{message}})
						if reopen == 0 {
							confKeys(t, applied.Inserted, []engine.MessageKey{message.Key})
						} else {
							confNoEvents(t, applied)
						}
						got := confGet(t, s, message.Key)
						if got.Text != message.Text || got.ContentIdentity != message.ContentIdentity || got.ContentHash != message.ContentHash {
							t.Fatalf("write/read identity mismatch: %+v", got)
						}
						messages, err := s.ReadMessages(ctx, confAccount)
						if err != nil || len(messages) != 2 {
							t.Fatalf("ReadMessages count=%d err=%v", len(messages), err)
						}
						history, _, err := s.ReadStatus(ctx, confAccount)
						if err != nil || history.Messages != 2 || history.Conversations != 1 {
							t.Fatalf("ReadStatus=%+v err=%v", history, err)
						}
						activity, err := s.ReadActivity(ctx, confAccount, confTime)
						if err != nil || activity.NewestAny == nil || !activity.NewestAny.Equal(message.Date) {
							t.Fatalf("ReadActivity=%+v err=%v", activity, err)
						}
					}()
				}
			})
		}
	}
}

func TestPanelStatusMigrationV8BodyIdempotent(t *testing.T) {
	path, _ := combinedMigrationFixture(t, 8)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	s := &Store{writer: raw}
	if err := s.migrateV8(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCombinedMigrationSchema(t, raw, 8)
}

func TestPanelStatusExplain(t *testing.T) {
	s := queryStore(t)
	queryPeer(t, s, "user", 1, "user")
	queryMsg(t, s, "user", 1, 1, 1000, "synthetic")
	statusQuery := panelStatusCountsQuery
	for _, phase := range []string{"before", "after"} {
		if phase == "before" {
			querySQL(t, s, `DROP INDEX messages_panel_status`)
		} else {
			querySQL(t, s, panelStatusIndexSQL)
		}
		for _, q := range []struct {
			name, sql string
			args      []any
		}{
			{"status", statusQuery, []any{"primary", 2000, 2000, "primary"}},
			{"activity", panelStatusActivityQuery, []any{0, 2000, 0, 2000, "primary", 2000, 2000}},
		} {
			// Use the DDL connection so EXPLAIN cannot reuse a stale reader schema.
			query := q.sql
			if phase == "before" && q.name == "status" {
				query = panelStatusLegacyCountsQuery
			}
			rows, err := s.writer.Query("EXPLAIN QUERY PLAN "+query, q.args...)
			if err != nil {
				t.Fatal(err)
			}
			var details []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				details = append(details, detail)
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			plan := strings.Join(details, "\n")
			t.Logf("%s %s:\n%s", phase, q.name, plan)
			if phase == "after" && !strings.Contains(plan, "COVERING INDEX messages_panel_status") {
				t.Errorf("%s is not covered: %s", q.name, plan)
			}
		}
	}
}
