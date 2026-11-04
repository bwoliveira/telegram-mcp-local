package gotdengine

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestContactsNativeReadsPersistBeforeReturning(t *testing.T) {
	e, s := offlineEngine(t)
	ctx := context.Background()
	var calls []string
	user := engine.PeerRef{Type: engine.PeerUser, ID: 123}
	phoneUser := engine.PeerRef{Type: engine.PeerUser, ID: 456}
	channel := engine.PeerRef{Type: engine.PeerChannel, ID: 789}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			calls = append(calls, "username:"+r.Username)
			*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{
				Peer:  &tg.PeerUser{UserID: user.ID},
				Users: []tg.UserClass{&tg.User{ID: 999}, &tg.User{ID: user.ID, Username: "demo_user", FirstName: "Demo", AccessHash: 101}},
				Chats: []tg.ChatClass{&tg.Channel{ID: channel.ID, Title: "Related", AccessHash: 202}},
			}
		case *tg.ContactsResolvePhoneRequest:
			calls = append(calls, "phone:"+r.Phone)
			if _, ok, err := s.LookupPeer(ctx, "primary", user); err != nil || !ok {
				t.Fatalf("previous query was not persisted before the next RPC: ok=%v err=%v", ok, err)
			}
			*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: phoneUser.ID}, Users: []tg.UserClass{&tg.User{ID: phoneUser.ID, FirstName: "Phone", AccessHash: 303}}}
		default:
			t.Fatalf("forbidden RPC (including importContacts): %T", in)
		}
		return nil
	}))
	queries := []string{"@demo_user", "+5511999999999"}
	got, err := e.ResolveContacts(ctx, queries)
	if err != nil || len(got) != len(queries) {
		t.Fatalf("results=%+v err=%v", got, err)
	}
	for i, ref := range []engine.PeerRef{user, phoneUser} {
		if got[i].Query != queries[i] || !got[i].Found || got[i].Reason != "" || got[i].Peer == nil || got[i].Peer.Ref != ref {
			t.Fatalf("result %d: %+v", i, got[i])
		}
		stored, ok, err := s.LookupPeer(ctx, "primary", ref)
		if err != nil || !ok || !reflect.DeepEqual(stored, *got[i].Peer) {
			t.Fatalf("result was not read from durable store: %+v ok=%v err=%v", stored, ok, err)
		}
	}
	for ref, want := range map[engine.PeerRef]int64{user: 101, channel: 202, phoneUser: 303} {
		hash, ok, err := s.AccessHash(ctx, "primary", ref)
		if err != nil || !ok || hash != want {
			t.Fatalf("lost learned hash for %+v: %d ok=%v err=%v", ref, hash, ok, err)
		}
	}
	if !reflect.DeepEqual(calls, []string{"username:demo_user", "phone:+5511999999999"}) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestContactsProductionStoreLookupAndPersistence(t *testing.T) {
	e, _ := offlineEngine(t)
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "production.db"), "primary")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.BindSelf(ctx, "primary", e.account); err != nil {
		t.Fatal(err)
	}
	e.cfg.Store = s
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		r, ok := in.(*tg.ContactsResolveUsernameRequest)
		if !ok || r.Username != "demo_user" {
			t.Fatalf("forbidden RPC %T: %+v", in, in)
		}
		*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: 123}, Users: []tg.UserClass{&tg.User{ID: 123, Username: "demo_user", AccessHash: 42}}}
		return nil
	}))
	got, err := e.ResolveContacts(ctx, []string{"@demo_user", "user:123"})
	if err != nil || len(got) != 2 || !got[0].Found || !got[1].Found {
		t.Fatalf("production store rejected lookup/persistence: %+v err=%v", got, err)
	}
	hash, ok, err := s.AccessHash(ctx, "primary", engine.PeerRef{Type: engine.PeerUser, ID: 123})
	if err != nil || !ok || hash != 42 {
		t.Fatalf("lost durable hash: %d ok=%v err=%v", hash, ok, err)
	}
}

func TestContactsPerQueryFailuresContinue(t *testing.T) {
	e, _ := offlineEngine(t)
	var calls []string
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			calls = append(calls, r.Username)
			switch r.Username {
			case "not_here":
				return tgerr.New(400, "USERNAME_NOT_OCCUPIED")
			case "bad_user":
				return tgerr.New(400, "USERNAME_INVALID")
			case "private_user":
				return tgerr.New(403, "USER_PRIVACY_RESTRICTED")
			case "last_user":
				*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: 123}, Users: []tg.UserClass{&tg.User{ID: 123, Username: "last_user"}}}
			default:
				t.Fatalf("unexpected username %q", r.Username)
			}
		case *tg.ContactsResolvePhoneRequest:
			calls = append(calls, r.Phone)
			switch r.Phone {
			case "+551100000001":
				return tgerr.New(400, "PHONE_NOT_OCCUPIED")
			case "+551100000002":
				return tgerr.New(400, "PHONE_NUMBER_INVALID")
			case "+551100000003":
				return tgerr.New(403, "USER_PRIVACY_RESTRICTED")
			default:
				t.Fatalf("unexpected phone %q", r.Phone)
			}
		default:
			t.Fatalf("forbidden RPC %T", in)
		}
		return nil
	}))
	queries := []string{"", "  ", "@", "@@bad", "+55abc", "+", "user:nope", "user:0", "@not_here", "@bad_user", "@private_user", "+551100000001", "+551100000002", "+551100000003", "@last_user"}
	wantReasons := []string{"invalid", "invalid", "invalid", "invalid", "invalid", "invalid", "invalid", "invalid", "not_found", "invalid", "privacy", "not_found", "invalid", "privacy", ""}
	got, err := e.ResolveContacts(context.Background(), queries)
	if err != nil || len(got) != len(queries) {
		t.Fatalf("results=%+v err=%v", got, err)
	}
	for i, r := range got {
		if r.Query != queries[i] || r.Reason != wantReasons[i] || r.Found != (i == len(queries)-1) || (r.Peer != nil) != r.Found {
			t.Errorf("result %d: %+v want reason %q", i, r, wantReasons[i])
		}
	}
	if len(calls) != 7 {
		t.Fatalf("invalid input issued RPCs or batch stopped: calls=%v", calls)
	}
}

func TestContactsLinksPreserveQueriesAndDuplicates(t *testing.T) {
	for _, query := range []string{"t.me/demo_user", "https://t.me/demo_user", "http://t.me/demo_user", " @demo_user "} {
		t.Run(query, func(t *testing.T) {
			e, _ := offlineEngine(t)
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				r, ok := in.(*tg.ContactsResolveUsernameRequest)
				if !ok || r.Username != "demo_user" {
					t.Fatalf("unexpected RPC %T: %+v", in, in)
				}
				calls++
				*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 123}, Chats: []tg.ChatClass{&tg.Channel{ID: 123, Username: "demo_user", Title: "Channel", AccessHash: 42}}}
				return nil
			}))
			got, err := e.ResolveContacts(context.Background(), []string{query, query})
			if err != nil || len(got) != 2 || calls < 1 {
				t.Fatalf("results=%+v calls=%d err=%v", got, calls, err)
			}
			for _, r := range got {
				if r.Query != query || !r.Found || r.Peer == nil || r.Peer.Ref.Type != engine.PeerChannel || r.Peer.Ref.ID != 123 {
					t.Fatalf("result=%+v", r)
				}
			}
		})
	}
}

func TestContactsLocalResolutionNeverGuesses(t *testing.T) {
	e, s := offlineEngine(t)
	ctx := context.Background()
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 1}, Kind: engine.KindUser, FirstName: "Ana", LastName: "Silva", Username: "ana_silva"}},
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 2}, Kind: engine.KindUser, FirstName: "Ana", LastName: "Souza"}},
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 3}, Kind: engine.KindUser, FirstName: "Same Name"}},
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 4}, Kind: engine.KindUser, FirstName: "Same Name"}},
	}}); err != nil {
		t.Fatal(err)
	}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
		t.Fatalf("unexpected RPC for local query %T", in)
		return nil
	}))
	queries := []string{"Ana", "Ana Silva", "Same Name", "user:1", "user:999", "@ana_silva"}
	got, err := e.ResolveContacts(ctx, queries)
	if err != nil || len(got) != len(queries) {
		t.Fatalf("results=%+v err=%v", got, err)
	}
	for i, r := range got {
		wantFound := i == 1 || i == 3 || i == 5
		if r.Found != wantFound || (r.Peer != nil) != wantFound || r.Query != queries[i] {
			t.Errorf("result %d=%+v", i, r)
		}
		if !wantFound && r.Reason != "not_found" {
			t.Errorf("result %d=%+v", i, r)
		}
	}
}

type contactsSaturatedStore struct{ engine.Persister }

func (s contactsSaturatedStore) FindPeers(_ context.Context, _ engine.AccountID, _ string, n int) ([]engine.Peer, error) {
	if n > 1000 {
		return nil, engine.ErrInvalid
	}
	peers := make([]engine.Peer, n)
	for i := range peers {
		peers[i] = engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: int64(i + 1)}, FirstName: "Ana Other"}
	}
	peers[0].FirstName = "Ana"
	return peers, nil
}

func TestContactsTruncatedNameSearchNeverGuesses(t *testing.T) {
	e, s := offlineEngine(t)
	e.cfg.Store = contactsSaturatedStore{Persister: s}
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("name lookup issued RPC"); return nil }))
	got, err := e.ResolveContacts(context.Background(), []string{"Ana"})
	if err != nil || len(got) != 1 || got[0].Found || got[0].Reason != "not_found" {
		t.Fatalf("truncated candidates were treated as unique: %+v err=%v", got, err)
	}
}

// Inject storage failures without replacing any unrelated Persister behavior.
type contactsFailureStore struct {
	engine.Persister
	applyErr, lookupErr, findErr error
}

func (s contactsFailureStore) Apply(ctx context.Context, b engine.Batch) (engine.Applied, error) {
	if s.applyErr != nil {
		return engine.Applied{}, s.applyErr
	}
	return s.Persister.Apply(ctx, b)
}
func (s contactsFailureStore) LookupPeer(ctx context.Context, a engine.AccountID, p engine.PeerRef) (engine.Peer, bool, error) {
	if s.lookupErr != nil {
		return engine.Peer{}, false, s.lookupErr
	}
	return s.Persister.LookupPeer(ctx, a, p)
}
func (s contactsFailureStore) FindPeers(ctx context.Context, a engine.AccountID, q string, n int) ([]engine.Peer, error) {
	if s.findErr != nil {
		return nil, s.findErr
	}
	return s.Persister.FindPeers(ctx, a, q, n)
}

func TestContactsStorageFailuresPropagate(t *testing.T) {
	for _, phase := range []string{"apply", "lookup", "find"} {
		t.Run(phase, func(t *testing.T) {
			e, s := offlineEngine(t)
			failure := errors.New("synthetic storage failure")
			store := contactsFailureStore{Persister: s}
			switch phase {
			case "apply":
				store.applyErr = failure
			case "lookup":
				store.lookupErr = failure
			case "find":
				store.findErr = failure
			}
			e.cfg.Store = store
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				if _, ok := in.(*tg.ContactsResolveUsernameRequest); !ok {
					t.Fatalf("forbidden RPC %T", in)
				}
				*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: 123}, Users: []tg.UserClass{&tg.User{ID: 123, Username: "demo_user"}}}
				return nil
			}))
			got, err := e.ResolveContacts(context.Background(), []string{"@demo_user"})
			if !errors.Is(err, failure) || len(got) != 0 {
				t.Fatalf("storage failure hidden or unpersisted result returned: %+v err=%v", got, err)
			}
			if phase == "apply" {
				if _, ok, err := s.LookupPeer(context.Background(), "primary", engine.PeerRef{Type: engine.PeerUser, ID: 123}); err != nil || ok {
					t.Fatalf("failed transaction learned a peer: ok=%v err=%v", ok, err)
				}
			}
		})
	}
}

func TestContactsOperationalErrorsAbort(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		rpcType string
	}{
		{"flood", tgerr.New(420, "FLOOD_WAIT_30"), ""},
		{"rpc", tgerr.New(500, "INTERNAL_SERVER_ERROR"), "INTERNAL_SERVER_ERROR"},
		{"auth", engine.ErrNotAuthorized, ""},
		{"cancel", context.Canceled, ""},
		{"deadline", context.DeadlineExceeded, ""},
		{"transport", errors.New("synthetic connection failure"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := offlineEngine(t)
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { calls++; return tc.err }))
			got, err := e.ResolveContacts(context.Background(), []string{"@first_user", "@second_user"})
			if err == nil || len(got) != 0 || calls != 1 {
				t.Fatalf("error became per-query absence: got=%+v calls=%d err=%v", got, calls, err)
			}
			if tc.name == "flood" {
				var flood *engine.FloodWaitError
				if !errors.As(err, &flood) {
					t.Fatalf("lost flood wait: %v", err)
				}
			} else if tc.rpcType != "" {
				var rpc *engine.RPCError
				if !errors.As(err, &rpc) || rpc.Type != tc.rpcType {
					t.Fatalf("lost RPC details: %v", err)
				}
			} else if tc.name != "transport" && !errors.Is(err, tc.err) {
				t.Fatalf("lost error identity: %v", err)
			}
		})
	}
}

func TestContactsLifecycleAndCancellationPreventRPC(t *testing.T) {
	for _, state := range []string{"unauthorized", "paused", "revoked", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			e, _ := offlineEngine(t)
			ctx := context.Background()
			var want error
			switch state {
			case "unauthorized":
				e.account.Authenticated = false
				want = engine.ErrNotAuthorized
			case "paused":
				e.paused = true
				want = engine.ErrPaused
			case "revoked":
				e.revoked.Store(true)
				want = engine.ErrRevoked
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
				t.Fatal("lifecycle gate issued RPC")
				return nil
			}))
			got, err := e.ResolveContacts(ctx, []string{"@demo_user"})
			if !errors.Is(err, want) || len(got) != 0 {
				t.Fatalf("got=%+v err=%v want=%v", got, err, want)
			}
		})
	}
}
