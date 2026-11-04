package gotdengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/session"
	"github.com/gotd/td/tg"
)

// All key material is deterministic synthetic test data, never an account key.
func syntheticSession() *session.Data {
	var key crypto.AuthKey
	for i := range key.Value {
		key.Value[i] = byte(i)
	}
	key.ID = key.Value.ID()
	d := &session.Data{DC: 2, Addr: "149.154.167.51:443", AuthKey: key.Value[:], AuthKeyID: key.ID[:], Salt: -123456789}
	d.Config = session.Config{Date: 1793600000, Expires: 1793603600, ThisDC: 2, DCTxtDomainName: "apv3.stel.com", WebfileDCID: 4}
	for i := 0; i < 20; i++ {
		d.Config.DCOptions = append(d.Config.DCOptions, tg.DCOption{ID: i%5 + 1, IPAddress: "149.154.167.51", Port: 443, Ipv6: i%2 == 0, MediaOnly: i%3 == 0})
	}
	return d
}

type sessionMemory struct {
	data   []byte
	writes int
	err    error
}

func (s *sessionMemory) LoadSession(context.Context) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.data == nil {
		return nil, engine.ErrNoSession
	}
	return bytes.Clone(s.data), nil
}
func (s *sessionMemory) StoreSession(_ context.Context, b []byte) error {
	if s.err != nil {
		return s.err
	}
	s.data = bytes.Clone(b)
	s.writes++
	return nil
}
func (s *sessionMemory) DeleteSession(context.Context) error { s.data = nil; return nil }

func legacySession(t *testing.T, d *session.Data) []byte {
	t.Helper()
	s := &sessionMemory{}
	if err := (&session.Loader{Storage: s}).Save(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return s.data
}

func requireSessionIdentity(t *testing.T, want, got *session.Data) {
	t.Helper()
	wantCopy, gotCopy := *want, *got
	wantCopy.Config, gotCopy.Config = session.Config{}, session.Config{}
	if !reflect.DeepEqual(wantCopy, gotCopy) {
		t.Fatal("session identity did not round-trip")
	}
}

func TestSessionCompactRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := &sessionMemory{}
	loader := &session.Loader{Storage: sessAdapter{store}}
	want := syntheticSession()
	if err := loader.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	if len(store.data) == 0 || len(store.data) > 1800 {
		t.Fatalf("portable session size = %d, want 1..1800", len(store.data))
	}
	var envelope struct{ Version int }
	if err := json.Unmarshal(store.data, &envelope); err != nil {
		t.Fatal("invalid compact envelope")
	}
	if envelope.Version != 2 {
		t.Fatalf("persisted version = %d, want 2", envelope.Version)
	}
	got, err := loader.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	requireSessionIdentity(t, want, got)
	if !reflect.DeepEqual(got.Config, session.Config{}) {
		t.Fatal("refreshable config persisted")
	}
	// gotd normally saves no Addr; keep this valid shape compatible too.
	want.Addr = ""
	if err := loader.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = loader.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	requireSessionIdentity(t, want, got)
}

func TestSessionLegacyMigratesOnlyOnSave(t *testing.T) {
	ctx := context.Background()
	want := syntheticSession()
	legacy := legacySession(t, want)
	if len(legacy) < 3800 || len(legacy) > 5500 {
		t.Fatalf("unrealistic legacy fixture size = %d", len(legacy))
	}
	store := &sessionMemory{data: bytes.Clone(legacy)}
	loader := &session.Loader{Storage: sessAdapter{store}}
	got, err := loader.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("legacy session changed on load")
	}
	if store.writes != 0 || !bytes.Equal(store.data, legacy) {
		t.Fatal("load rewrote the existing session")
	}
	if err := loader.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	if len(store.data) > 1800 {
		t.Fatalf("migrated session size = %d, want <=1800", len(store.data))
	}
	t.Logf("synthetic serialized session: before=%d bytes after=%d bytes limit=1800", len(legacy), len(store.data))
	// Recreate the adapter/loader to prove persisted restart compatibility.
	got, err = (&session.Loader{Storage: sessAdapter{store}}).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	requireSessionIdentity(t, want, got)
}

func TestSessionRejectsInvalidWithoutLeakingInput(t *testing.T) {
	short := syntheticSession()
	short.AuthKey = []byte("SECRET_MARKER")
	mismatch := syntheticSession()
	mismatch.AuthKeyID = bytes.Repeat([]byte{1}, 8)
	badDC := syntheticSession()
	badDC.DC = -1
	longAddr := syntheticSession()
	longAddr.Addr = strings.Repeat("SECRET_MARKER", 200)
	compact, err := compactSession(legacySession(t, syntheticSession()))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"oversized compact":     append(bytes.Repeat([]byte(" "), 1800), compact...),
		"unknown compact field": bytes.Replace(compact, []byte(`"Data":{`), []byte(`"Data":{"Secret":"SECRET_MARKER",`), 1),
		"malformed":             []byte(`{"Version":2,"Data":"SECRET_MARKER`),
		"unknown version":       []byte(`{"Version":999,"Data":"SECRET_MARKER"}`),
		"missing identity":      []byte(`{"Version":2,"Data":{}}`),
		"short key":             legacySession(t, short),
		"wrong key ID":          legacySession(t, mismatch),
		"invalid DC":            legacySession(t, badDC),
		"oversized address":     legacySession(t, longAddr),
		"empty":                 {},
		"null":                  []byte(`null`),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			store := &sessionMemory{data: bytes.Clone(input)}
			adapter := sessAdapter{store}
			_, loadErr := adapter.LoadSession(context.Background())
			saveErr := adapter.StoreSession(context.Background(), input)
			for _, err := range []error{loadErr, saveErr} {
				if err == nil {
					t.Fatal("invalid session accepted")
				}
				if strings.Contains(err.Error(), "SECRET_MARKER") {
					t.Fatal("session bytes leaked in error")
				}
				if errors.Is(err, session.ErrNotFound) {
					t.Fatal("corruption must not silently start a new login")
				}
				if !strings.Contains(err.Error(), "session") {
					t.Fatal("error does not identify the session")
				}
			}
			if store.writes != 0 || !bytes.Equal(store.data, input) {
				t.Fatal("invalid session overwrote storage")
			}
		})
	}
}

func TestSessionLegacyZeroDCRoundTrip(t *testing.T) {
	want := syntheticSession()
	want.DC = 0 // gotd restores this sentinel using its configured DC.
	store := &sessionMemory{data: legacySession(t, want)}
	loader := &session.Loader{Storage: sessAdapter{store}}
	got, err := loader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	requireSessionIdentity(t, want, got)
	if err := loader.Save(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	got, err = loader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	requireSessionIdentity(t, want, got)
}

func TestSessionCanonicalizesLegacyDuplicateFields(t *testing.T) {
	want := syntheticSession()
	legacy := legacySession(t, want)
	// RawMessage accepts the last Data; gotd's typed decoder would otherwise
	// report the earlier overflow value (which may contain session material).
	input := append([]byte(`{"Data":{"Salt":999999999999999999999999999999999999},`), legacy[1:]...)
	store := &sessionMemory{data: bytes.Clone(input)}
	got, err := (&session.Loader{Storage: sessAdapter{store}}).Load(context.Background())
	if err != nil {
		t.Fatal("canonical legacy session did not load")
	}
	requireSessionIdentity(t, want, got)
	if store.writes != 0 || !bytes.Equal(store.data, input) {
		t.Fatal("canonicalization rewrote stored session")
	}
}

func TestSessionAdapterPropagatesStorageErrors(t *testing.T) {
	ctx := context.Background()
	store := &sessionMemory{}
	adapter := sessAdapter{store}
	if _, err := adapter.LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("missing session not mapped")
	}
	store.err = errors.New("synthetic storage failure")
	if _, err := adapter.LoadSession(ctx); !errors.Is(err, store.err) {
		t.Fatal("load error lost")
	}
	if err := adapter.StoreSession(ctx, legacySession(t, syntheticSession())); !errors.Is(err, store.err) {
		t.Fatal("save error lost")
	}
}
