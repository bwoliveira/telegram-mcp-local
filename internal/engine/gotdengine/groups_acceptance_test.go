package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/gotdengine/pocstore"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Close the actual fixture SQLite only after Telegram has accepted the RPC.
// Keep its real error so errors.Is must survive the engine's warning wrappers.
func groupsCloseStore(t *testing.T, s *pocstore.Store) error {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := s.Apply(context.Background(), engine.Batch{Account: "primary"})
	if err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("expected closed SQLite, got %v", err)
	}
	for errors.Unwrap(err) != nil {
		err = errors.Unwrap(err)
	}
	return err
}

func groupsAssertAcceptedWarning(t *testing.T, result engine.WriteResult, err error) {
	t.Helper()
	for _, text := range []string{result.Note, fmt.Sprint(err)} {
		if !strings.Contains(text, "accepted") || !strings.Contains(text, "do not blindly retry") {
			t.Errorf("acceptance/retry warning missing: %q", text)
		}
	}
	if result.Preview == nil || result.DryRun {
		t.Errorf("lost acceptance evidence: %+v", result)
	}
}

func TestGroupsAcceptedMetadataSQLiteFailure(t *testing.T) {
	for _, ref := range groupsTestPeers {
		for _, action := range []string{"title", "title_and_about", "leave"} {
			t.Run(fmt.Sprintf("%v/%s", ref, action), func(t *testing.T) {
				e, s := writeFixture(t)
				calls := 0
				var localErr error
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					calls++
					if calls > 1 {
						t.Fatalf("continued after local persistence failure: %T", in)
					}
					groupsTestReply(t, out)
					localErr = groupsCloseStore(t, s)
					return nil
				}))
				title, about := "accepted title", "not attempted"
				call := func(opts engine.WriteOptions) (engine.WriteResult, error) {
					if action == "leave" {
						return e.Leave(context.Background(), engine.LeaveRequest{Group: ref, WriteOptions: opts})
					}
					r := engine.UpdateGroupRequest{Group: ref, Title: &title, WriteOptions: opts}
					if action == "title_and_about" {
						r.About = &about
					}
					return e.UpdateGroup(context.Background(), r)
				}
				preview, err := call(engine.WriteOptions{DryRun: true})
				if err != nil {
					t.Fatal(err)
				}
				got, err := call(engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint})
				if err == nil || !errors.Is(err, localErr) || calls != 1 || got.Affected != 1 || got.Done != (action != "title_and_about") {
					t.Errorf("got=%+v err=%v underlying=%v calls=%d", got, err, localErr, calls)
				}
				groupsAssertAcceptedWarning(t, got, err)
			})
		}
	}
}

func TestGroupsAcceptedParticipantsSQLiteFailure(t *testing.T) {
	for _, ref := range groupsTestPeers {
		for _, action := range []engine.ParticipantsAction{engine.ParticipantsAdd, engine.ParticipantsRemove, engine.ParticipantsBan, engine.ParticipantsPromote, engine.ParticipantsDemote} {
			// Basic-group admin RPCs return a bool, not updates to persist.
			if ref.Type == engine.PeerChat && (action == engine.ParticipantsPromote || action == engine.ParticipantsDemote) {
				continue
			}
			steps := []int{1}
			if ref.Type == engine.PeerChannel && action == engine.ParticipantsRemove {
				steps = append(steps, 2)
			}
			for _, failAt := range steps {
				for _, count := range []int{1, 2} {
					t.Run(fmt.Sprintf("%v/%s/step=%d/users=%d", ref, action, failAt, count), func(t *testing.T) {
						e, s := writeFixture(t)
						calls := 0
						var localErr error
						e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
							calls++
							if calls > failAt {
								t.Fatalf("continued destructive operation after persistence failure: %T", in)
							}
							groupsTestReply(t, out)
							if calls == failAt {
								localErr = groupsCloseStore(t, s)
							}
							return nil
						}))
						users := []engine.PeerRef{{Type: engine.PeerUser, ID: 10}, {Type: engine.PeerUser, ID: 11}}
						r := engine.ParticipantsRequest{Group: ref, Action: action, Users: users[:count], WriteOptions: engine.WriteOptions{DryRun: true}}
						preview, err := e.ManageParticipants(context.Background(), r)
						if err != nil {
							t.Fatal(err)
						}
						r.DryRun, r.ExpectFingerprint = false, preview.Preview.Fingerprint
						got, err := e.ManageParticipants(context.Background(), r)
						wantDone := count == 1 && !(ref.Type == engine.PeerChannel && action == engine.ParticipantsRemove && failAt == 1)
						if err == nil || !errors.Is(err, localErr) || got.Affected != 1 || got.Done != wantDone || calls != failAt {
							t.Errorf("got=%+v err=%v underlying=%v calls=%d wantDone=%v", got, err, localErr, calls, wantDone)
						}
						if len(got.PerUser) != 1 || !strings.Contains(got.PerUser["user:10"], "database is closed") {
							t.Errorf("lost per-user reconciliation error or continued to next user: %+v", got.PerUser)
						}
						groupsAssertAcceptedWarning(t, got.WriteResult, err)
					})
				}
			}
		}
	}
}

func TestGroupsPartialAcceptanceWarnings(t *testing.T) {
	for _, action := range []string{"metadata", "participants", "remove"} {
		t.Run(action, func(t *testing.T) {
			e, _ := writeFixture(t)
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
				calls++
				if calls == 2 {
					return tgerr.New(400, "CHAT_ADMIN_REQUIRED")
				}
				groupsTestReply(t, out)
				return nil
			}))
			title, about := "title", "about"
			call := func(opts engine.WriteOptions) (engine.WriteResult, error) {
				if action == "metadata" {
					return e.UpdateGroup(context.Background(), engine.UpdateGroupRequest{Group: groupsTestPeers[0], Title: &title, About: &about, WriteOptions: opts})
				}
				r := engine.ParticipantsRequest{Group: groupsTestPeers[0], Action: engine.ParticipantsPromote, Users: []engine.PeerRef{{Type: engine.PeerUser, ID: 10}, {Type: engine.PeerUser, ID: 11}}, WriteOptions: opts}
				if action == "remove" {
					r.Group, r.Action, r.Users = groupsTestPeers[1], engine.ParticipantsRemove, r.Users[:1]
				}
				got, err := e.ManageParticipants(context.Background(), r)
				return got.WriteResult, err
			}
			preview, err := call(engine.WriteOptions{DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			got, err := call(engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint})
			var rpc *engine.RPCError
			if !errors.Is(err, engine.ErrPermission) || !errors.As(err, &rpc) || rpc.Type != "CHAT_ADMIN_REQUIRED" || got.Done || got.Affected != 1 || calls != 2 {
				t.Errorf("got=%+v err=%v calls=%d", got, err, calls)
			}
			groupsAssertAcceptedWarning(t, got, err)
		})
	}
}
