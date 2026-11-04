package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

type triageMentionSpy struct {
	*store.Store
	calls int
	fail  bool
}

func (st *triageMentionSpy) ListMentions(ctx context.Context, a engine.AccountID, o store.MentionOptions) (store.MentionPage, error) {
	st.calls++
	if st.fail {
		return store.MentionPage{}, errors.New("mention read failed")
	}
	return st.Store.ListMentions(ctx, a, o)
}
func TestTriageStoreHistoricalMentionsSQLiteAndFake(t *testing.T) {
	for _, real := range []bool{false, true} {
		t.Run(map[bool]string{false: "fake", true: "SQLite"}[real], func(t *testing.T) {
			ctx := context.Background()
			now := fake.Epoch()
			var st *store.Store
			var spy *triageMentionSpy
			cfg := engine.Config{Clock: func() time.Time { return now }}
			if real {
				var err error
				st, err = store.Open(ctx, filepath.Join(t.TempDir(), "store.db"), "primary", store.WithClock(func() time.Time { return now }))
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				cfg.Store = st
			}
			e0, err := fake.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			e := e0.(*fake.Controller)
			var rs ReadStore = e
			if real {
				spy = &triageMentionSpy{Store: st}
				rs = spy
			}
			s := New(Config{Engine: e, ReadStore: rs, Clock: func() time.Time { return now }})
			p := engine.PeerRef{Type: engine.PeerChannel, ID: 6001}
			other := engine.PeerRef{Type: engine.PeerChannel, ID: 6002}
			at := now.Add(-time.Hour)
			mk := func(id, topic int, seconds int, text string) engine.Message {
				m := triageMessage(p, id, at.Add(time.Duration(seconds)*time.Second), text)
				m.TopicID = topic
				return m
			}
			own := mk(1, 10, -10, "out before mention")
			own.Out = true
			user := mk(2, 10, 0, "😀 @FAKE_SELF review this")
			user.Entities = []engine.Entity{{Type: engine.EntityMention, Offset: 3, Length: 10}}
			wrongTopic := mk(3, 20, 1, "out different topic")
			wrongTopic.Out = true
			named := mk(4, 20, 2, "😀 Ana review")
			named.Entities = []engine.Entity{{Type: engine.EntityMentionName, Offset: 3, Length: 3, UserID: fake.SelfPeer.ID}}
			answer := mk(5, 20, 3, "answer")
			answer.Out = true
			flag := mk(6, 30, 4, "flag only")
			flag.Mentioned = true
			reply := mk(7, 10, 5, "reply to you")
			reply.ReplyTo = &engine.ReplyRef{ID: 1}
			plain := mk(8, 10, 6, "@fake_self without entities is plain text")
			invalid := mk(9, 10, 7, "@fake_self")
			invalid.Entities = []engine.Entity{{Type: engine.EntityMention, Offset: 999, Length: 10}}
			foreign := own
			foreign.Key.Peer = other
			cross := mk(10, 10, 8, "cross peer reply")
			cross.ReplyTo = &engine.ReplyRef{ID: 1, Peer: &other}
			wrongUser := mk(11, 10, 9, "not me")
			wrongUser.Entities = []engine.Entity{{Type: engine.EntityMentionName, UserID: 555, Length: 3}}
			later := mk(13, 30, 11, "last mention")
			later.Mentioned = true
			early := mk(14, 30, 10, "reply earlier than mention")
			early.Out = true
			early.ReplyTo = &engine.ReplyRef{ID: 13}
			expires := now.Add(time.Second)
			ttl := mk(15, 10, 12, "expired answer")
			ttl.Out = true
			ttl.ExpiresAt = &expires
			expiringTarget := mk(17, 40, -30, "reply target that expires")
			expiringTarget.Out = true
			expiringTarget.ExpiresAt = &expires
			expiredReply := mk(18, 40, -20, "reply-derived mention loses its target")
			expiredReply.ReplyTo = &engine.ReplyRef{ID: 17}
			batch := engine.Batch{Account: "primary", Source: engine.SourceBackfill, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: p, Kind: engine.KindSupergroup, Forum: true}}, {Peer: engine.Peer{Ref: other, Kind: engine.KindSupergroup}}}, Messages: []engine.Message{own, user, wrongTopic, named, answer, flag, reply, plain, invalid, foreign, cross, wrongUser, later, early, ttl, expiringTarget, expiredReply}}
			if err = e.Publish(ctx, batch); err != nil {
				t.Fatal(err)
			}
			now = now.Add(2 * time.Second)
			if err = e.Disconnect(ctx, "offline local reads"); err != nil {
				t.Fatal(err)
			}
			result := s.listMentions(ctx, arguments{ChatJID: peerID(p)})
			if result["isError"] != false {
				t.Fatal(result)
			}
			body := toolBody(t, result)
			mentions := body["mentions"].([]any)
			if len(mentions) != 5 {
				t.Fatalf("%d mentions: %v", len(mentions), body)
			}
			answers := map[string]bool{}
			for _, v := range mentions {
				row := v.(map[string]any)
				message := row["message"].(map[string]any)
				answers[message["id"].(string)] = row["answered"].(bool)
				if row["server_unread"] != false {
					t.Fatal(row)
				}
			}
			for _, id := range []string{"2", "7", "13"} {
				if got, ok := answers[id]; !ok || got {
					t.Fatal(answers)
				}
			}
			if !answers["4"] || !answers["6"] {
				t.Fatal(answers)
			}
			result = s.listMentions(ctx, arguments{ChatJID: peerID(p), OnlyUnanswered: true, Limit: 2})
			if result["isError"] != false {
				t.Fatal(result)
			}
			body = toolBody(t, result)
			if body["count"] != float64(2) || body["truncated"] != true || body["since"] == nil {
				t.Fatal(body)
			}
			chats := triageChats(t, s, arguments{IncludeGroupMentions: true})
			if chats[peerID(p)] == nil || chats[peerID(p)]["mention"] == nil {
				t.Fatal(chats)
			}
			if real {
				if spy.calls == 0 {
					t.Fatal("durable mention index not called")
				}
				spy.fail = true
				if result = s.listMentions(ctx, arguments{}); result["isError"] != true {
					t.Fatal(result)
				}
			}
			if len(e.Writes()) != 0 {
				t.Fatal(e.Writes())
			}
		})
	}
}
