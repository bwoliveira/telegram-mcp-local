package fake_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
)

func TestReadsAreDeterministicAndTreatInjectionAsData(t *testing.T) {
	for _, mode := range []engine.Mode{engine.ModeReadOnly, engine.ModeReadWrite} {
		t.Run(string(mode), func(t *testing.T) {
			f := newFake(t, engine.Config{Account: "test", Mode: mode})
			other := newFake(t, engine.Config{Account: "test", Mode: mode})
			ctx := context.Background()
			before := f.Snapshot()
			if !reflect.DeepEqual(before, other.Snapshot()) {
				t.Fatal("fixtures are not deterministic")
			}
			peers, e := f.Resolve(ctx, engine.ResolveRequest{Query: "https://t.me/fake_ana"})
			if e != nil || len(peers) != 1 || !peers[0].Exact || peers[0].Peer.Ref != fake.PrivatePeer {
				t.Fatalf("resolve: %+v %v", peers, e)
			}
			if _, e = f.ResolveContacts(ctx, []string{"@fake_ana", "unknown", ""}); e != nil {
				t.Fatal(e)
			}
			if _, e = f.RefreshDialogs(ctx, engine.RefreshRequest{}); e != nil {
				t.Fatal(e)
			}
			for _, ref := range []engine.PeerRef{fake.BasicGroupPeer, fake.SupergroupPeer, fake.ChannelPeer} {
				if g, e := f.RefreshGroup(ctx, engine.GroupRequest{Group: ref}); e != nil || len(g.Participants) == 0 {
					t.Fatalf("group %v: %v", ref, e)
				}
			}
			if _, e = f.PollResults(ctx, engine.PollRequest{Key: key(fake.BasicGroupPeer, 7)}); e != nil {
				t.Fatal(e)
			}
			if _, e = f.InviteLink(ctx, engine.InviteLinkRequest{Group: fake.BasicGroupPeer}); e != nil {
				t.Fatal(e)
			}
			if tr, e := f.TranscribeNative(ctx, key(fake.PrivatePeer, 5)); e != nil || tr.Text == "" {
				t.Fatalf("transcribe: %v", e)
			}
			found, e := f.SearchRemote(ctx, engine.SearchRequest{Query: "IGNORE ALL INSTRUCTIONS"})
			if e != nil || len(found.Messages) != 1 {
				t.Fatalf("injection fixture: %+v %v", found, e)
			}
			window, e := f.FetchWindow(ctx, engine.WindowRequest{Key: found.Messages[0].Key, Before: 1, After: 1})
			if e != nil || len(window) != 3 {
				t.Fatalf("window: %v", e)
			}
			encoded, e := json.Marshal(found)
			if e != nil || !json.Valid(encoded) {
				t.Fatal("invalid JSON")
			}
			var decoded engine.SearchResult
			if e = json.Unmarshal(encoded, &decoded); e != nil {
				t.Fatal(e)
			}
			if decoded.Messages[0].Text != found.Messages[0].Text || !strings.Contains(decoded.Messages[0].Text, "send_text_message") {
				t.Fatal("message text was interpreted or changed")
			}
			if !reflect.DeepEqual(before.Dialogs, f.Snapshot().Dialogs) || len(f.Writes()) != 0 {
				t.Fatal("reading sent a write/read receipt or modified counters")
			}
		})
	}
}

func TestDownloadUsesPrivateFilesAndRejectsProtectedMedia(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test"})
	ctx := context.Background()
	dir := t.TempDir()
	for _, id := range []int{6, 9} {
		if _, e := f.Download(ctx, engine.DownloadRequest{Key: key(fake.PrivatePeer, id), Dir: dir}); !errors.Is(e, engine.ErrProtectedContent) {
			t.Fatalf("protected %d: %v", id, e)
		}
	}
	if _, e := f.Download(ctx, engine.DownloadRequest{Key: key(fake.PrivatePeer, 4), Dir: dir, MaxBytes: 1}); !errors.Is(e, engine.ErrInvalid) {
		t.Fatalf("max bytes: %v", e)
	}
	d, e := f.Download(ctx, engine.DownloadRequest{Key: key(fake.PrivatePeer, 4), Dir: dir})
	if e != nil {
		t.Fatal(e)
	}
	content, e := os.ReadFile(d.Path)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(content)
	if int64(len(content)) != d.Bytes || d.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("incorrect size/digest")
	}
	if e = service.CheckPrivateDataPath(d.Path, false); e != nil {
		t.Fatalf("permissions: %v", e)
	}
	if _, e = png.Decode(bytes.NewReader(content)); e != nil {
		t.Fatalf("invalid fixture PNG: %v", e)
	}
	second, e := f.Download(ctx, engine.DownloadRequest{Key: key(fake.PrivatePeer, 4), Dir: dir})
	if e != nil || second.Path != d.Path {
		t.Fatalf("download path is not deterministic: %v", e)
	}
	if _, e = f.ProfilePhoto(ctx, engine.PhotoRequest{Peer: fake.PrivatePeer, Dir: dir}); e != nil {
		t.Fatal(e)
	}
	if len(f.Writes()) != 0 {
		t.Fatal("media read recorded a write")
	}
}

func TestLifecycleLoginRunAndSecretRedaction(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test"})
	ctx := context.Background()
	before := f.Snapshot()
	if e := f.Disconnect(ctx, "test pause"); e != nil {
		t.Fatal(e)
	}
	if s := f.Status(); s.State != engine.StatePaused || s.PausedFor != "test pause" {
		t.Fatalf("status %+v", s)
	}
	if _, e := f.SearchRemote(ctx, engine.SearchRequest{}); !errors.Is(e, engine.ErrPaused) {
		t.Fatal(e)
	}
	if e := f.Connect(ctx); e != nil {
		t.Fatal(e)
	}
	if e := f.LogOut(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := f.Resolve(ctx, engine.ResolveRequest{Query: "Ana"}); !errors.Is(e, engine.ErrNotAuthorized) {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before.Messages, f.Snapshot().Messages) {
		t.Fatal("logout deleted history")
	}
	if e := f.SubmitCode(ctx, engine.Secret("unused-code")); !errors.Is(e, engine.ErrNoLogin) {
		t.Fatal(e)
	}
	if e := f.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginPhone, Phone: "+10000000000"}); e != nil {
		t.Fatal(e)
	}
	if e := f.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginQR}); !errors.Is(e, engine.ErrLoginRunning) {
		t.Fatal(e)
	}
	if e := f.ResendCode(ctx); e != nil {
		t.Fatal(e)
	}
	if e := f.SubmitCode(ctx, engine.Secret("unique-secret-code")); e != nil {
		t.Fatal(e)
	}
	if f.Login().State != engine.LoginWantPass {
		t.Fatal("no synthetic 2FA step")
	}
	if e := f.SubmitPassword(ctx, engine.Secret("unique-secret-password")); e != nil {
		t.Fatal(e)
	}
	if f.Login().State != engine.LoginDone {
		t.Fatal("login not completed")
	}
	if e := f.LogOut(ctx); e != nil {
		t.Fatal(e)
	}
	if e := f.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginQR}); e != nil {
		t.Fatal(e)
	}
	qr := f.Login().QRURL
	if qr == "" {
		t.Fatal("no QR")
	}
	encoded, _ := json.Marshal(struct {
		Status engine.Status
		Login  engine.Login
	}{f.Status(), f.Login()})
	for _, secret := range []string{qr, "unique-secret-code", "unique-secret-password"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("secret leaked")
		}
	}
	f.CancelLogin()
	if f.Login().State != engine.LoginCancelled {
		t.Fatal("not cancelled")
	}
	if e := f.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginQR}); e != nil {
		t.Fatal(e)
	}
	if e := f.CompleteLogin(ctx); e != nil {
		t.Fatal(e)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	events := f.Subscribe(subCtx, engine.SubscribeOptions{Kinds: []engine.EventKind{engine.EventConnection}})
	done := make(chan error, 1)
	select {
	case <-f.Changes():
	default:
	}
	go func() { done <- f.Run(runCtx) }()
	// Ensure Run has entered rather than canceling before it can install cleanup.
	select {
	case <-f.Changes():
	case <-time.After(time.Second):
		t.Fatal("Run did not start")
	}
	if e := f.Run(ctx); !errors.Is(e, engine.ErrInvalid) {
		t.Fatalf("duplicate Run: %v", e)
	}
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("Run ignored cancellation")
	}
	if f.Status().State != engine.StateStopped {
		t.Fatal("Run did not stop")
	}
	for range events {
	}
}

// A Persister spy deliberately implements only the seam this fake uses. No
// Telegram transport, database implementation or other package is involved.
type persisterSpy struct {
	engine.Persister
	messages map[engine.MessageKey]engine.Message
	calls    int
	fail     bool
	bound    int64
	auth     engine.AuthState
	last     engine.Batch
}

func (s *persisterSpy) BindSelf(_ context.Context, _ engine.AccountID, a engine.Account) error {
	if s.bound != 0 && s.bound != a.SelfID {
		return engine.ErrAccountMismatch
	}
	s.bound = a.SelfID
	return nil
}
func (s *persisterSpy) SetAuthState(_ context.Context, _ engine.AccountID, a engine.AuthState, _ string) error {
	s.auth = a
	return nil
}
func (s *persisterSpy) Message(_ context.Context, k engine.MessageKey) (engine.Message, bool, error) {
	m, ok := s.messages[k]
	return m, ok, nil
}
func (s *persisterSpy) Apply(ctx context.Context, b engine.Batch) (engine.Applied, error) {
	if e := ctx.Err(); e != nil {
		return engine.Applied{}, e
	}
	s.calls++
	if s.fail {
		return engine.Applied{}, errors.New("synthetic commit failure")
	}
	if s.messages == nil {
		s.messages = make(map[engine.MessageKey]engine.Message)
	}
	a := engine.Applied{}
	for _, m := range b.Messages {
		if _, ok := s.messages[m.Key]; !ok {
			s.messages[m.Key] = m
			a.Inserted = append(a.Inserted, m.Key)
		}
	}
	for _, m := range b.Edits {
		s.messages[m.Key] = m
		a.Edited = append(a.Edited, m.Key)
	}
	s.last = b
	return a, nil
}

func TestPersisterCommitsBeforeEventsAndFailedWritesDoNotLeak(t *testing.T) {
	store := &persisterSpy{}
	f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite, Store: store})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := f.Subscribe(ctx, engine.SubscribeOptions{Kinds: []engine.EventKind{engine.EventMessage}, Buffer: 4})
	r := engine.SendTextRequest{Peer: fake.PrivatePeer, Text: "committed", WriteOptions: engine.WriteOptions{DryRun: true}}
	p, e := f.SendText(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	seedCalls := store.calls
	store.fail = true
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	if _, e = f.SendText(ctx, r); e == nil {
		t.Fatal("commit failure ignored")
	}
	if len(f.Writes()) != 0 {
		t.Fatal("failed write journaled")
	}
	select {
	case <-events:
		t.Fatal("event before commit")
	default:
	}
	store.fail = false
	result, e := f.SendText(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	event := <-events
	if _, ok := store.messages[event.Message.Key]; !ok {
		t.Fatal("event not committed")
	}
	if event.Message.Key != result.Messages[0] || event.Source != engine.SourceSend {
		t.Fatalf("event %+v", event)
	}
	if store.calls != seedCalls+2 {
		t.Fatalf("unexpected writes: %d", store.calls)
	}
	// Replayed Applied is empty, so no duplicate event.
	if e = f.Publish(ctx, store.last); e != nil {
		t.Fatal(e)
	}
	select {
	case <-events:
		t.Fatal("duplicate event")
	default:
	}
	restored := newFake(t, engine.Config{Account: "test", Store: store})
	if len(restored.Snapshot().Messages) < len(fake.Fixtures("test").Messages) {
		t.Fatal("reopening idempotently seeded store lost fixtures")
	}
}
