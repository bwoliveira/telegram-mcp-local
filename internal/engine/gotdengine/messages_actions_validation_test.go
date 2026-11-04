package gotdengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestMessageActionsInvalidInputNoRPC(t *testing.T) {
	e, key := actionFixture(t)
	ctx := context.Background()
	opts := engine.WriteOptions{DryRun: true}
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		t.Fatal("invalid input reached RPC")
		return nil
	}))
	past := e.now().Add(-time.Hour)
	overflow := time.Unix(2147483648, 0)
	for name, run := range map[string]func() (engine.WriteResult, error){
		"edit empty": func() (engine.WriteResult, error) {
			return e.Edit(ctx, engine.EditRequest{Key: key, WriteOptions: opts})
		},
		"edit UTF8": func() (engine.WriteResult, error) {
			return e.Edit(ctx, engine.EditRequest{Key: key, Text: string([]byte{255}), WriteOptions: opts})
		},
		"delete empty": func() (engine.WriteResult, error) {
			return e.Delete(ctx, engine.DeleteRequest{Peer: key.Peer, WriteOptions: opts})
		},
		"delete duplicate": func() (engine.WriteResult, error) {
			return e.Delete(ctx, engine.DeleteRequest{Peer: key.Peer, IDs: []int{71, 71}, WriteOptions: opts})
		},
		"delete zero": func() (engine.WriteResult, error) {
			return e.Delete(ctx, engine.DeleteRequest{Peer: key.Peer, IDs: []int{0}, WriteOptions: opts})
		},
		"delete overflow": func() (engine.WriteResult, error) {
			return e.Delete(ctx, engine.DeleteRequest{Peer: key.Peer, IDs: []int{2147483648}, WriteOptions: opts})
		},
		"reaction custom empty": func() (engine.WriteResult, error) {
			return e.React(ctx, engine.ReactRequest{Key: key, Emoji: "custom:", WriteOptions: opts})
		},
		"reaction custom negative": func() (engine.WriteResult, error) {
			return e.React(ctx, engine.ReactRequest{Key: key, Emoji: "custom:-1", WriteOptions: opts})
		},
		"reaction whitespace": func() (engine.WriteResult, error) {
			return e.React(ctx, engine.ReactRequest{Key: key, Emoji: " 👍", WriteOptions: opts})
		},
		"forward duplicate": func() (engine.WriteResult, error) {
			return e.Forward(ctx, engine.ForwardRequest{From: key.Peer, To: key.Peer, IDs: []int{71, 71}, WriteOptions: opts})
		},
		"forward topic nonforum": func() (engine.WriteResult, error) {
			return e.Forward(ctx, engine.ForwardRequest{From: key.Peer, To: key.Peer, IDs: []int{71}, TopicID: 1, WriteOptions: opts})
		},
		"typing unknown": func() (engine.WriteResult, error) {
			return e.SetTyping(ctx, engine.TypingRequest{Peer: key.Peer, Action: "online", WriteOptions: opts})
		},
		"typing negative topic": func() (engine.WriteResult, error) {
			return e.SetTyping(ctx, engine.TypingRequest{Peer: key.Peer, Action: engine.TypingText, TopicID: -1, WriteOptions: opts})
		},
		"typing nonforum topic": func() (engine.WriteResult, error) {
			return e.SetTyping(ctx, engine.TypingRequest{Peer: key.Peer, Action: engine.TypingText, TopicID: 2, WriteOptions: opts})
		},
		"organise unknown": func() (engine.WriteResult, error) {
			return e.Organise(ctx, engine.OrganiseRequest{Peer: key.Peer, Action: "delete_chat", WriteOptions: opts})
		},
		"mute past": func() (engine.WriteResult, error) {
			return e.Organise(ctx, engine.OrganiseRequest{Peer: key.Peer, Action: engine.OrganiseMute, MuteUntil: &past, WriteOptions: opts})
		},
		"mute overflow": func() (engine.WriteResult, error) {
			return e.Organise(ctx, engine.OrganiseRequest{Peer: key.Peer, Action: engine.OrganiseMute, MuteUntil: &overflow, WriteOptions: opts})
		},
		"unmute extra date": func() (engine.WriteResult, error) {
			return e.Organise(ctx, engine.OrganiseRequest{Peer: key.Peer, Action: engine.OrganiseUnmute, MuteUntil: &past, WriteOptions: opts})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := run(); !errors.Is(err, engine.ErrInvalid) {
				t.Fatal(err)
			}
		})
	}
}

func TestMessageActionsAccountAndPermission(t *testing.T) {
	e, key := actionFixture(t)
	ctx := context.Background()
	opts := engine.WriteOptions{DryRun: true}
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("invalid key reached RPC"); return nil }))
	other := key
	other.Account = "other"
	if _, err := e.Edit(ctx, engine.EditRequest{Key: other, Text: "new", WriteOptions: opts}); !errors.Is(err, engine.ErrAccountMismatch) {
		t.Fatal(err)
	}
	if _, err := e.React(ctx, engine.ReactRequest{Key: other, Emoji: "👍", WriteOptions: opts}); !errors.Is(err, engine.ErrAccountMismatch) {
		t.Fatal(err)
	}
	m, _, _ := e.cfg.Store.Message(ctx, key)
	m.Out = false
	e.cfg.Store = actionSnapshotStore{Persister: e.cfg.Store, message: m}
	if _, err := e.Edit(ctx, engine.EditRequest{Key: key, Text: "new", WriteOptions: opts}); !errors.Is(err, engine.ErrPermission) {
		t.Fatal(err)
	}
}

func TestMessageActionsBooleanRefusalAndNotifyFlags(t *testing.T) {
	for _, action := range []string{"typing", "pin", "mute", "unmute"} {
		t.Run(action, func(t *testing.T) {
			e, key := actionFixture(t)
			ctx := context.Background()
			until := time.Unix(1900000000, 0)
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				if r, ok := in.(*tg.AccountUpdateNotifySettingsRequest); ok {
					if !r.Settings.Flags.Has(2) || r.Settings.Flags.Has(0) || r.Settings.Flags.Has(1) || r.Settings.Flags.Has(3) {
						t.Fatalf("other notify fields overwritten: %+v", r.Settings)
					}
					want := int(until.Unix())
					if action == "unmute" {
						want = 0
					}
					if r.Settings.MuteUntil != want {
						t.Fatalf("wrong mute_until %d", r.Settings.MuteUntil)
					}
				}
				out.(*tg.BoolBox).Bool = &tg.BoolFalse{}
				return nil
			}))
			run := func(opts engine.WriteOptions) (engine.WriteResult, error) {
				if action == "typing" {
					return e.SetTyping(ctx, engine.TypingRequest{Peer: key.Peer, Action: engine.TypingText, WriteOptions: opts})
				}
				r := engine.OrganiseRequest{Peer: key.Peer, Action: engine.OrganiseAction(action), WriteOptions: opts}
				if action == "mute" {
					r.MuteUntil = &until
				}
				return e.Organise(ctx, r)
			}
			p, err := run(engine.WriteOptions{DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := run(engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint})
			if !errors.Is(err, engine.ErrPermission) || result.Done {
				t.Fatalf("false acceptance ignored %+v %v", result, err)
			}
		})
	}
}
