package gotdengine

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// Inject local persistence failures only: QR token export/import and the self
// lookup still run through the production controller and real gotd QR helper.
func TestQRAuthorizedPersistenceFailureCleansRemoteSession(t *testing.T) {
	stages := []string{"BindSelf", "SetAuthState", "Apply"}
	for i, stage := range stages {
		for _, logoutFails := range []bool{false, true} {
			outcome := "confirmed"
			if logoutFails {
				outcome = "failed"
			}
			t.Run(stage+"/"+outcome, func(t *testing.T) {
				var exports, selfLookups, logouts atomic.Int32
				e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					switch r := in.(type) {
					case *tg.AuthExportLoginTokenRequest:
						switch n := exports.Add(1); n {
						case 1:
							out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{
								Token: []byte("synthetic-persistence-qr"), Expires: int(time.Now().Add(time.Minute).Unix()),
							}
						case 2:
							out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginTokenSuccess{
								Authorization: &tg.AuthAuthorization{User: &tg.User{ID: 99, Self: true}},
							}
						default:
							t.Errorf("unexpected QR token export %d", n)
							return engine.ErrNotSupported
						}
					case *tg.UsersGetUsersRequest:
						selfLookups.Add(1)
						if len(r.ID) != 1 {
							t.Errorf("self lookup has %d inputs, want 1", len(r.ID))
							return engine.ErrInvalid
						}
						if _, ok := r.ID[0].(*tg.InputUserSelf); !ok {
							t.Errorf("identity lookup input %T, want InputUserSelf", r.ID[0])
							return engine.ErrInvalid
						}
						out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
					case *tg.AuthLogOutRequest:
						logouts.Add(1)
						if logoutFails {
							return errors.New("synthetic secret remote logout failure")
						}
					default:
						t.Errorf("unexpected QR RPC %T", in)
						return engine.ErrNotSupported
					}
					return nil
				})
				persisted := []byte("synthetic-persisted-MTProto-session")
				session := &lifecycleSession{data: append([]byte(nil), persisted...)}
				e.cfg.Session = session
				store := &qrPersistenceFailure{
					Persister: e.cfg.Store,
					stage:     stage,
					err:       errors.New("synthetic local persistence failure at " + stage),
				}
				e.cfg.Store = store

				if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
					t.Fatal(err)
				}
				waitLogin(t, e, engine.LoginShowQR)
				if exports.Load() != 1 || selfLookups.Load() != 0 {
					t.Fatal("QR authorization completed before the login-token signal")
				}
				e.signalLogin(&tg.UpdateShort{Update: &tg.UpdateLoginToken{}})
				login := waitLogin(t, e, engine.LoginFailed)
				if login.Error == "" || login.UpdatedAt.IsZero() || strings.Contains(login.Error, "synthetic local persistence failure") {
					t.Fatalf("persistence failure missing error/timestamp: %+v", login)
				}
				if login.QRURL != "" || login.QRExpiresAt != nil {
					t.Fatal("failed QR login retained token details")
				}
				if exports.Load() != 2 || selfLookups.Load() != 1 || logouts.Load() != 1 {
					t.Fatalf("exports=%d self lookups=%d logouts=%d, want 2/1/1", exports.Load(), selfLookups.Load(), logouts.Load())
				}
				store.mu.Lock()
				calls := append([]string(nil), store.calls...)
				store.mu.Unlock()
				if !reflect.DeepEqual(calls, stages[:i+1]) {
					t.Fatalf("persistence calls=%v, want %v", calls, stages[:i+1])
				}
				select {
				case <-e.authReady:
					t.Fatal("local persistence failure released ingestion")
				default:
				}
				account, err := e.Account(context.Background())
				if err != nil || account.Authenticated {
					t.Fatalf("persistence failure published authenticated account: %+v, %v", account, err)
				}
				if e.revoked.Load() || !e.sessionBlocked.Load() || !e.held.Load() {
					t.Fatal("cleanup must fence ingestion/session saves without claiming revocation")
				}
				session.mu.Lock()
				remaining, deleted := append([]byte(nil), session.data...), session.deleted
				session.mu.Unlock()
				if logoutFails {
					if deleted != 0 || !bytes.Equal(remaining, persisted) {
						t.Fatal("failed remote logout destroyed the recoverable session")
					}
					if !strings.Contains(login.Error, "Devices") || !strings.Contains(login.Error, "cleanup") || strings.Contains(login.Error, "synthetic secret") {
						t.Fatalf("failed remote logout must request cleanup without secrets: %q", login.Error)
					}
					if e.Status().State == engine.StateLoggedOut {
						t.Fatal("unconfirmed logout claimed success")
					}
				} else if deleted != 1 || len(remaining) != 0 || e.Status().State != engine.StateLoggedOut {
					t.Fatalf("confirmed logout retained session: deletions=%d remaining=%d state=%s", deleted, len(remaining), e.Status().State)
				}
			})
		}
	}
}

type qrPersistenceFailure struct {
	engine.Persister
	stage string
	err   error
	mu    sync.Mutex
	calls []string
}

func (s *qrPersistenceFailure) record(stage string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stage)
	if stage == s.stage {
		return s.err
	}
	return nil
}

func (s *qrPersistenceFailure) BindSelf(ctx context.Context, acc engine.AccountID, self engine.Account) error {
	if err := s.record("BindSelf"); err != nil {
		return err
	}
	return s.Persister.BindSelf(ctx, acc, self)
}

func (s *qrPersistenceFailure) SetAuthState(ctx context.Context, acc engine.AccountID, state engine.AuthState, reason string) error {
	if err := s.record("SetAuthState"); err != nil {
		return err
	}
	return s.Persister.SetAuthState(ctx, acc, state, reason)
}

func (s *qrPersistenceFailure) Apply(ctx context.Context, batch engine.Batch) (engine.Applied, error) {
	if err := s.record("Apply"); err != nil {
		return engine.Applied{}, err
	}
	return s.Persister.Apply(ctx, batch)
}
