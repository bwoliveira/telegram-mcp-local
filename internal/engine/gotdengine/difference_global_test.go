package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

func TestGlobalDifferenceResponseBarrier(t *testing.T) {
	for _, slice := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("slice=%v/failure=%v", slice, fail), func(t *testing.T) {
				var armed atomic.Bool
				s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"), store.FaultAt(func(stage string) error {
					if fail && armed.Load() && stage == "apply_before_commit" {
						return errors.New("synthetic disk-full")
					}
					return nil
				}))
				ctx := context.Background()
				p := engine.PeerRef{Type: engine.PeerUser, ID: 99}
				key := func(id int) engine.MessageKey { return engine.MessageKey{Account: barrierAccount, Peer: p, ID: id} }
				if _, err := s.Apply(ctx, engine.Batch{Account: barrierAccount, Messages: []engine.Message{
					{Key: key(7), Date: time.Unix(1000, 0), Text: "oldglobalmarker"}, {Key: key(8), Date: time.Unix(1000, 0), Text: "deletedglobalmarker"},
				}}); err != nil {
					t.Fatal(err)
				}
				if err := s.SaveUpdatesState(ctx, barrierAccount, engine.UpdatesState{Pts: 1, Date: 1000}); err != nil {
					t.Fatal(err)
				}
				e := newBarrierEngine(t, s)
				e.api = tg.NewClient(productionRPC(e, invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					r, ok := in.(*tg.UpdatesGetDifferenceRequest)
					if !ok {
						t.Errorf("forbidden offline RPC %T", in)
						return engine.ErrNotSupported
					}
					if r.Pts != 1 {
						out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1001}
						return nil
					}
					others := []tg.UpdateClass{
						&tg.UpdateEditMessage{Pts: 2, PtsCount: 1, Message: &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, EditDate: 1001, Message: "newglobalmarker"}},
						&tg.UpdateDeleteMessages{Messages: []int{8}, Pts: 3, PtsCount: 1},
					}
					messages := []tg.MessageClass{&tg.Message{ID: 9, PeerID: &tg.PeerUser{UserID: 99}, Date: 1001, Message: "newglobalmessage"}}
					state := tg.UpdatesState{Pts: 4, Date: 1001}
					if slice {
						out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceSlice{OtherUpdates: others, NewMessages: messages, IntermediateState: state}
					} else {
						out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifference{OtherUpdates: others, NewMessages: messages, State: state}
					}
					armed.Store(true)
					return nil
				})))
				runctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				done := make(chan error, 1)
				go func() { done <- e.manager.Run(runctx, e.api, 99, updates.AuthOptions{}) }()
				defer func() { cancel(); <-done; e.manager.Reset() }()
				waitBarrier(t, func() bool {
					st, _, _ := s.LoadUpdatesState(ctx, barrierAccount)
					return (fail && e.held.Load()) || (!fail && st.Pts == 4)
				})
				st, _, err := s.LoadUpdatesState(ctx, barrierAccount)
				wantPTS := 4
				if fail {
					wantPTS = 1
				}
				if err != nil || st.Pts != wantPTS {
					t.Errorf("PTS=%d want=%d err=%v", st.Pts, wantPTS, err)
				}
				edited, ok, err := s.Message(ctx, key(7))
				wantText := "newglobalmarker"
				if fail {
					wantText = "oldglobalmarker"
				}
				if err != nil || !ok || edited.Text != wantText {
					t.Errorf("edit=%+v exists=%v err=%v", edited, ok, err)
				}
				_, exists, err := s.Message(ctx, key(8))
				if err != nil || exists != fail {
					t.Errorf("deleted exists=%v err=%v", exists, err)
				}
				_, exists, err = s.Message(ctx, key(9))
				if err != nil || exists == fail {
					t.Errorf("new exists=%v err=%v", exists, err)
				}
				hits, err := s.SearchLocal(ctx, barrierAccount, "deletedglobalmarker")
				wantHits := 0
				if fail {
					wantHits = 1
				}
				if err != nil || len(hits) != wantHits {
					t.Errorf("deleted FTS=%d want=%d err=%v", len(hits), wantHits, err)
				}
				if !fail && (e.Metrics().Messages != 1 || e.Metrics().Edits != 1 || e.Metrics().Deletes != 1) {
					t.Errorf("duplicate events: %+v", e.Metrics())
				}
			})
		}
	}
}
