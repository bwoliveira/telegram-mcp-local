package fake

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
	"weak"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

var _ triageStore = (*Controller)(nil)
var _ triageStore = (*store.Store)(nil)

func triageController(t *testing.T, attached bool) (*Controller, *store.Store) {
	t.Helper()
	cfg := engine.Config{Account: "test", Mode: engine.ModeReadOnly}
	var db *store.Store
	if attached {
		var err error
		db, err = store.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), "test")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		cfg.Store = db
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c.(*Controller), db
}

func triageCalls(f *Controller, at time.Time) map[string]func(context.Context, engine.AccountID, engine.PeerRef) error {
	return map[string]func(context.Context, engine.AccountID, engine.PeerRef) error{
		"read": func(ctx context.Context, account engine.AccountID, _ engine.PeerRef) error {
			_, err := f.ReadTriageMarks(ctx, account)
			return err
		},
		"handled": func(ctx context.Context, account engine.AccountID, peer engine.PeerRef) error {
			return f.MarkTriageHandled(ctx, account, peer, "changed", at)
		},
		"snooze": func(ctx context.Context, account engine.AccountID, peer engine.PeerRef) error {
			return f.SnoozeTriage(ctx, account, peer, "changed", at, at.Add(time.Hour))
		},
		"clear": f.ClearTriageMark,
	}
}

func TestTriageLocalValidationAndCancellation(t *testing.T) {
	for _, attached := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "sqlite"}[attached], func(t *testing.T) {
			f, _ := triageController(t, attached)
			ctx := context.Background()
			at := Epoch()
			if err := f.MarkTriageHandled(ctx, "test", PrivatePeer, "original", at); err != nil {
				t.Fatal(err)
			}
			before, err := f.ReadTriageMarks(ctx, "test")
			if err != nil {
				t.Fatal(err)
			}
			for name, call := range triageCalls(f, at) {
				t.Run(name, func(t *testing.T) {
					for _, account := range []engine.AccountID{"", "other"} {
						if err := call(ctx, account, PrivatePeer); !errors.Is(err, engine.ErrAccountMismatch) {
							t.Fatalf("account %q = %v", account, err)
						}
					}
					if err := call(nil, "test", PrivatePeer); !errors.Is(err, engine.ErrInvalid) {
						t.Fatalf("nil context = %v", err)
					}
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					if err := call(cancelled, "test", PrivatePeer); !errors.Is(err, context.Canceled) {
						t.Fatalf("cancelled = %v", err)
					}
					expired, finish := context.WithDeadline(ctx, at.Add(-time.Hour))
					defer finish()
					if err := call(expired, "test", PrivatePeer); !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("deadline = %v", err)
					}
					waiting, stop := context.WithCancel(ctx)
					defer stop()
					f.mu.Lock()
					started := make(chan struct{})
					done := make(chan error, 1)
					go func() {
						close(started)
						done <- call(waiting, "test", PrivatePeer)
					}()
					<-started
					stop()
					f.mu.Unlock()
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Fatalf("cancel waiting for lock = %v", err)
					}
					if name == "read" {
						return
					}
					for _, peer := range []engine.PeerRef{{}, {Type: engine.PeerUser, ID: 0}, {Type: engine.PeerChat, ID: -1}, {Type: "invalid", ID: 1}} {
						if err := call(ctx, "test", peer); !errors.Is(err, engine.ErrInvalid) {
							t.Fatalf("invalid peer %+v = %v", peer, err)
						}
					}
					if err := call(ctx, "test", engine.PeerRef{Type: engine.PeerUser, ID: 987654321}); !errors.Is(err, engine.ErrPeerNotFound) {
						t.Fatalf("missing peer = %v", err)
					}
				})
			}
			after, err := f.ReadTriageMarks(ctx, "test")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected call changed marks = %v, %v", after, err)
			}
		})
	}
}

func TestTriageRemainsLocalAfterAccessAndAuthLoss(t *testing.T) {
	for _, attached := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "sqlite"}[attached], func(t *testing.T) {
			f, _ := triageController(t, attached)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events := f.Subscribe(ctx, engine.SubscribeOptions{Buffer: 64})
			// No transport is started. Marking is allowed in every local state.
			f.mu.Lock()
			peer := f.peers[PrivatePeer]
			peer.Access = engine.AccessLost
			f.peers[PrivatePeer] = peer
			seq := f.seq
			f.mu.Unlock()
			for {
				select {
				case <-f.Changes():
				default:
					goto drained
				}
			}
		drained:
			for _, state := range []engine.ConnState{engine.StatePaused, engine.StateLoggedOut, engine.StateRevoked, engine.StateNotAuthorized, engine.StateStopped} {
				f.mu.Lock()
				f.status.State = state
				f.mu.Unlock()
				for name, call := range triageCalls(f, Epoch()) {
					if err := call(ctx, "test", PrivatePeer); err != nil {
						t.Fatalf("%s in %s = %v", name, state, err)
					}
				}
			}
			if len(f.Writes()) != 0 || f.seq != seq {
				t.Fatal("local triage emitted writes/events")
			}
			select {
			case event := <-events:
				t.Fatalf("triage event = %+v", event)
			default:
			}
			select {
			case <-f.Changes():
				t.Fatal("triage signalled a Telegram/status change")
			default:
			}
		})
	}
}

func TestTriageConcurrentSnapshotsAndNamespaceIsolation(t *testing.T) {
	for _, attached := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "sqlite"}[attached], func(t *testing.T) {
			f, db := triageController(t, attached)
			ctx := context.Background()
			refs := []engine.PeerRef{{Type: engine.PeerUser, ID: 999}, {Type: engine.PeerChat, ID: 999}, {Type: engine.PeerChannel, ID: 999}}
			batch := engine.Batch{Account: "test"}
			for _, ref := range refs {
				batch.Peers = append(batch.Peers, engine.PeerUpsert{Peer: engine.Peer{Ref: ref, Kind: engine.KindUser, Access: engine.AccessOK}})
			}
			if err := f.Publish(ctx, batch); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for worker := range 12 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					peer := refs[worker%len(refs)]
					for range 12 {
						var err error
						switch worker % 4 {
						case 0:
							err = f.MarkTriageHandled(ctx, "test", peer, "handled", Epoch())
						case 1:
							err = f.SnoozeTriage(ctx, "test", peer, "snoozed", Epoch(), Epoch().Add(time.Hour))
						case 2:
							err = f.ClearTriageMark(ctx, "test", peer)
						case 3:
							var marks map[engine.PeerRef]store.TriageMark
							marks, err = f.ReadTriageMarks(ctx, "test")
							for ref, mark := range marks {
								if mark.HandledAt != nil && (mark.SnoozedAt != nil || mark.SnoozeUntil != nil) {
									t.Errorf("mixed decision = %+v", mark)
								}
								for _, pointer := range []*time.Time{mark.HandledAt, mark.SnoozedAt, mark.SnoozeUntil} {
									if pointer != nil {
										*pointer = time.Time{}
									}
								}
								delete(marks, ref)
							}
						}
						if err != nil {
							t.Errorf("concurrent operation = %v", err)
							return
						}
					}
				}()
			}
			wg.Wait()
			for _, ref := range refs {
				if err := f.MarkTriageHandled(ctx, "test", ref, string(ref.Type), Epoch()); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.ClearTriageMark(ctx, "test", refs[0]); err != nil {
				t.Fatal(err)
			}
			marks, err := f.ReadTriageMarks(ctx, "test")
			if err != nil || len(marks) != 2 || marks[refs[1]].Note != string(refs[1].Type) || marks[refs[2]].Note != string(refs[2].Type) {
				t.Fatalf("peer namespaces collide = %v, %v", marks, err)
			}
			if attached {
				durable, err := db.ReadTriageMarks(ctx, "test")
				if err != nil || !reflect.DeepEqual(marks, durable) {
					t.Fatalf("durable namespace snapshot = %v, %v", durable, err)
				}
			}
		})
	}
}

func TestTriageAttachedStoreErrorsDoNotFallBackToMemory(t *testing.T) {
	f, db := triageController(t, true)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for name, call := range triageCalls(f, Epoch()) {
		if err := call(context.Background(), "test", PrivatePeer); err == nil {
			t.Errorf("%s swallowed attached store error", name)
		}
		// Even with a failed store, validate the fake's own local scope first.
		if err := call(context.Background(), "other", PrivatePeer); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Errorf("%s skipped local scope: %v", name, err)
		}
	}
}

func TestTriageRegistryDoesNotRetainController(t *testing.T) {
	key := func() weak.Pointer[Controller] {
		f, _ := triageController(t, false)
		if err := f.MarkTriageHandled(context.Background(), "test", PrivatePeer, "ephemeral", Epoch()); err != nil {
			t.Fatal(err)
		}
		key := weak.Make(f)
		if _, exists := triageRegistry.Load(key); !exists {
			t.Fatal("test did not create standalone registry state")
		}
		return key
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if _, exists := triageRegistry.Load(key); !exists {
			if key.Value() != nil {
				t.Fatal("cleanup removed a live Controller's state")
			}
			return
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("standalone triage registry retained Controller or was not cleaned up")
}
