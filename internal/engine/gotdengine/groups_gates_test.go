package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

var groupsTestWrites = []string{"participants", "update_group", "invite_link", "leave"}

func groupsTestWrite(ctx context.Context, e *Engine, action string, ref engine.PeerRef, opts engine.WriteOptions) (engine.WriteResult, error) {
	switch action {
	case "participants":
		got, err := e.ManageParticipants(ctx, engine.ParticipantsRequest{Group: ref, Action: engine.ParticipantsAdd, Users: []engine.PeerRef{{Type: engine.PeerUser, ID: 10}}, WriteOptions: opts})
		return got.WriteResult, err
	case "update_group":
		title := "Approved title"
		return e.UpdateGroup(ctx, engine.UpdateGroupRequest{Group: ref, Title: &title, WriteOptions: opts})
	case "invite_link":
		got, err := e.InviteLink(ctx, engine.InviteLinkRequest{Group: ref, Revoke: true, WriteOptions: opts})
		return got.WriteResult, err
	case "leave":
		return e.Leave(ctx, engine.LeaveRequest{Group: ref, WriteOptions: opts})
	default:
		panic("unknown test action")
	}
}

func TestGroupsWriteGates(t *testing.T) {
	for _, action := range groupsTestWrites {
		for _, ref := range groupsTestPeers {
			for _, gate := range []string{"cancelled", "read-only", "not-authorized", "paused", "revoked", "missing-preview", "forged-preview", "expired-preview", "changed-key", "changed-target", "changed-hash"} {
				t.Run(fmt.Sprintf("%s/%v/%s", action, ref, gate), func(t *testing.T) {
					e, s := writeFixture(t)
					now := time.Unix(1000, 0)
					e.cfg.Clock = func() time.Time { return now }
					e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
						t.Fatal("gate must prevent every RPC")
						return nil
					}))
					ctx := context.Background()
					p, err := groupsTestWrite(ctx, e, action, ref, engine.WriteOptions{DryRun: true})
					if err != nil {
						t.Fatal(err)
					}
					opts := engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
					want := engine.ErrPreviewMismatch
					requestRef := ref
					switch gate {
					case "cancelled":
						var cancel context.CancelFunc
						ctx, cancel = context.WithCancel(ctx)
						cancel()
						want = context.Canceled
					case "read-only":
						if err := e.SetMode(ctx, engine.ModeReadOnly); err != nil {
							t.Fatal(err)
						}
						want = engine.ErrReadOnly
					case "not-authorized":
						e.account.Authenticated = false
						want = engine.ErrNotAuthorized
					case "paused":
						e.paused = true
						want = engine.ErrPaused
					case "revoked":
						e.revoked.Store(true)
						want = engine.ErrRevoked
					case "missing-preview":
						opts.ExpectFingerprint = ""
					case "forged-preview":
						e.mu.Lock()
						delete(e.previews, opts.ExpectFingerprint)
						e.mu.Unlock()
					case "expired-preview":
						now = now.Add(10 * time.Minute)
					case "changed-key":
						opts.IdempotencyKey = "different-intent"
					case "changed-target":
						// Identity, not a display-title change: titles are unrelated
						// to participant, invite-reset and leave RPCs.
						if ref.Type == engine.PeerChat {
							requestRef = groupsTestPeers[1]
						} else {
							requestRef = groupsTestPeers[0]
						}
					case "changed-hash":
						p, _, err := s.LookupPeer(ctx, "primary", ref)
						if err != nil {
							t.Fatal(err)
						}
						hash := int64(3030)
						if ref.ID == 40 {
							hash = 4040
						}
						if ref.Type == engine.PeerChat { // Bind a participant hash, or self identity: basic groups have no hash.
							if action != "participants" {
								e.account.SelfID++
							} else {
								refUser := engine.PeerRef{Type: engine.PeerUser, ID: 10}
								up, _, _ := s.LookupPeer(ctx, "primary", refUser)
								if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: up, AccessHash: 2020}}}); err != nil {
									t.Fatal(err)
								}
							}
						} else {
							hash = 9090
						}
						if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: p, AccessHash: hash}}}); err != nil {
							t.Fatal(err)
						}
					}
					_, err = groupsTestWrite(ctx, e, action, requestRef, opts)
					if !errors.Is(err, want) {
						t.Fatalf("got %v, want %v", err, want)
					}
				})
			}
		}
	}
}

func TestGroupsDryRunsAreLocalAndReadOnlySafe(t *testing.T) {
	for _, action := range groupsTestWrites {
		t.Run(action, func(t *testing.T) {
			e, _ := writeFixture(t)
			e.account.Authenticated = false
			if err := e.SetMode(context.Background(), engine.ModeReadOnly); err != nil {
				t.Fatal(err)
			}
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("preview must be local"); return nil }))
			got, err := groupsTestWrite(context.Background(), e, action, groupsTestPeers[1], engine.WriteOptions{DryRun: true})
			if err != nil || !got.DryRun || got.Done || got.Preview == nil || len(got.Preview.Warnings) == 0 {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestGroupsApprovalsAreConsumedAndCannotBeRevived(t *testing.T) {
	for _, action := range groupsTestWrites {
		t.Run(action, func(t *testing.T) {
			e, _ := writeFixture(t)
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
				calls++
				groupsTestReply(t, out)
				return nil
			}))
			ctx := context.Background()
			p, err := groupsTestWrite(ctx, e, action, groupsTestPeers[0], engine.WriteOptions{DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			opts := engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
			got, err := groupsTestWrite(ctx, e, action, groupsTestPeers[0], opts)
			if err != nil || !got.Done || calls != 1 {
				t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
			}
			if _, err := groupsTestWrite(ctx, e, action, groupsTestPeers[0], engine.WriteOptions{DryRun: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := groupsTestWrite(ctx, e, action, groupsTestPeers[0], opts); !errors.Is(err, engine.ErrPreviewMismatch) || calls != 1 {
				t.Fatalf("approval revived: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestGroupsConcurrentApprovalIsSingleUse(t *testing.T) {
	for _, action := range groupsTestWrites {
		t.Run(action, func(t *testing.T) {
			e, _ := writeFixture(t)
			var calls atomic.Int64
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
				calls.Add(1)
				groupsTestReply(t, out)
				return nil
			}))
			p, err := groupsTestWrite(context.Background(), e, action, groupsTestPeers[0], engine.WriteOptions{DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := groupsTestWrite(context.Background(), e, action, groupsTestPeers[0], engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint})
					errs <- err
				}()
			}
			wg.Wait()
			close(errs)
			successes, mismatches := 0, 0
			for err := range errs {
				if err == nil {
					successes++
				} else if errors.Is(err, engine.ErrPreviewMismatch) {
					mismatches++
				} else {
					t.Fatal(err)
				}
			}
			if successes != 1 || mismatches != 1 || calls.Load() != 1 {
				t.Fatalf("successes=%d mismatches=%d calls=%d", successes, mismatches, calls.Load())
			}
		})
	}
}

func TestGroupsInvalidInputsNeverCallRPC(t *testing.T) {
	e, s := writeFixture(t)
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		t.Fatal("invalid input reached RPC")
		return nil
	}))
	ctx := context.Background()
	for _, action := range groupsTestWrites {
		for _, ref := range []engine.PeerRef{{Type: engine.PeerUser, ID: 10}, {Type: engine.PeerChat, ID: -1}, {Type: engine.PeerChannel, ID: 0}} {
			if _, err := groupsTestWrite(ctx, e, action, ref, engine.WriteOptions{DryRun: true}); !errors.Is(err, engine.ErrInvalid) {
				t.Fatalf("%s/%v: %v", action, ref, err)
			}
		}
	}
	for _, tc := range []struct {
		action engine.ParticipantsAction
		users  []engine.PeerRef
	}{
		{engine.ParticipantsAdd, nil},
		{"unknown", []engine.PeerRef{{Type: engine.PeerUser, ID: 10}}},
		{engine.ParticipantsAdd, []engine.PeerRef{{Type: engine.PeerChannel, ID: 30}}},
		{engine.ParticipantsAdd, []engine.PeerRef{{Type: engine.PeerUser, ID: 10}, {Type: engine.PeerUser, ID: 10}}},
	} {
		if _, err := e.ManageParticipants(ctx, engine.ParticipantsRequest{Group: groupsTestPeers[0], Action: tc.action, Users: tc.users, WriteOptions: engine.WriteOptions{DryRun: true}}); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("invalid participants=%+v err=%v", tc, err)
		}
	}
	empty, spaces, badUTF8, longTitle, longAbout := "", "  ", string([]byte{255}), strings.Repeat("x", 129), strings.Repeat("x", 256)
	for _, r := range []engine.UpdateGroupRequest{{}, {Title: &empty}, {Title: &spaces}, {Title: &badUTF8}, {Title: &longTitle}, {About: &badUTF8}, {About: &longAbout}} {
		r.Group, r.DryRun = groupsTestPeers[0], true
		if _, err := e.UpdateGroup(ctx, r); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("invalid metadata=%+v err=%v", r, err)
		}
	}
	p, _, _ := s.LookupPeer(ctx, "primary", groupsTestPeers[0])
	p.MigratedTo = &groupsTestPeers[1]
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: p}}}); err != nil {
		t.Fatal(err)
	}
	for _, action := range groupsTestWrites {
		if _, err := groupsTestWrite(ctx, e, action, groupsTestPeers[0], engine.WriteOptions{DryRun: true}); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("migrated group silently followed: %s %v", action, err)
		}
	}
}

func TestGroupsInviteReadConnectionGates(t *testing.T) {
	for _, gate := range []string{"cancelled", "not-authorized", "paused", "revoked"} {
		t.Run(gate, func(t *testing.T) {
			e, _ := writeFixture(t)
			ctx := context.Background()
			want := engine.ErrNotAuthorized
			switch gate {
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "not-authorized":
				e.account.Authenticated = false
			case "paused":
				e.paused = true
				want = engine.ErrPaused
			case "revoked":
				e.revoked.Store(true)
				want = engine.ErrRevoked
			}
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("gate must prevent RPC"); return nil }))
			if _, err := e.InviteLink(ctx, engine.InviteLinkRequest{Group: groupsTestPeers[0]}); !errors.Is(err, want) {
				t.Fatalf("got=%v want=%v", err, want)
			}
		})
	}
}

func TestGroupsParticipantsFloodWaitStopsBatchAndConsumesApproval(t *testing.T) {
	e, _ := writeFixture(t)
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		return tgerr.New(420, "FLOOD_WAIT_120")
	}))
	r := engine.ParticipantsRequest{Group: groupsTestPeers[1], Action: engine.ParticipantsAdd, Users: []engine.PeerRef{{Type: engine.PeerUser, ID: 10}, {Type: engine.PeerUser, ID: 11}}, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.ManageParticipants(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun, r.ExpectFingerprint = false, p.Preview.Fingerprint
	got, err := e.ManageParticipants(context.Background(), r)
	var flood *engine.FloodWaitError
	if !errors.As(err, &flood) || flood.Method != "channels.inviteToChannel" || calls != 1 || got.Done || got.Affected != 0 || len(got.PerUser) != 1 {
		t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
	}
	if _, err := e.ManageParticipants(context.Background(), r); !errors.Is(err, engine.ErrPreviewMismatch) || calls != 1 {
		t.Fatalf("RPC error allowed a replay: %v calls=%d", err, calls)
	}
}
