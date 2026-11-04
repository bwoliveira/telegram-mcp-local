package gotdengine

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tgerr"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func waitLogin(t *testing.T, e *Engine, want engine.LoginState) engine.Login {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		if l := e.Login(); l.State == want {
			return l
		}
		select {
		case <-e.Changes():
		case <-timer.C:
			t.Fatalf("waiting for %s, got %+v", want, e.Login())
		}
	}
}

func TestLoginStartNotifiesBeforeRPCAndFailureIsAtomic(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	e := loginEngine(t, func(ctx context.Context, _ bin.Encoder, _ bin.Decoder) error {
		close(entered)
		select {
		case <-release:
			return errors.New("synthetic secret code/hash/password/token must be suppressed")
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	result := make(chan error, 1)
	go func() {
		result <- e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginPhone, Phone: "synthetic-phone"})
	}()
	<-entered
	select {
	case <-e.Changes():
	default:
		close(release)
		<-result
		t.Fatal("starting login did not notify")
	}
	if l := e.Login(); l.State != engine.LoginStarting || l.UpdatedAt.IsZero() {
		close(release)
		<-result
		t.Fatalf("starting snapshot incomplete: %+v", l)
	}
	close(release)
	if err := <-result; err == nil || strings.Contains(err.Error(), "synthetic secret") {
		t.Fatal("unsanitized or missing error")
	}
	select {
	case <-e.Changes():
	default:
		t.Fatal("failure did not notify")
	}
	l := e.Login()
	if l.State != engine.LoginFailed || l.Error == "" || strings.Contains(l.Error, "synthetic secret") || l.UpdatedAt.IsZero() {
		t.Fatalf("incomplete failed snapshot: %+v", l)
	}
}

func TestLoginCanceledPhoneRequestDoesNotRemainStarting(t *testing.T) {
	entered := make(chan struct{})
	e := loginEngine(t, func(ctx context.Context, _ bin.Encoder, _ bin.Decoder) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- e.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginPhone, Phone: "synthetic-phone"})
	}()
	<-entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if l := e.Login(); l.State != engine.LoginCancelled || l.Phone != "" {
		t.Fatalf("canceled request retained active login: %+v", l)
	}
}

func TestLoginCancellationRejectsLatePhoneHashAndPasswordTransition(t *testing.T) {
	for _, phase := range []string{"send", "resend", "sign-in"} {
		t.Run(phase, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.AuthSendCodeRequest:
					if phase != "send" {
						out.(*tg.AuthSentCodeBox).SentCode = &tg.AuthSentCode{PhoneCodeHash: "old-hash", Type: &tg.AuthSentCodeTypeApp{Length: 5}}
						return nil
					}
				case *tg.AuthResendCodeRequest:
				case *tg.AuthSignInRequest:
				default:
					t.Errorf("unexpected stale RPC %T", in)
					return engine.ErrNotSupported
				}
				close(entered)
				<-release // Deliberately ignore cancellation, like a late transport completion.
				if phase == "sign-in" {
					return tgerr.New(401, "SESSION_PASSWORD_NEEDED")
				}
				out.(*tg.AuthSentCodeBox).SentCode = &tg.AuthSentCode{PhoneCodeHash: "stale-hash", Type: &tg.AuthSentCodeTypeApp{Length: 5}}
				return nil
			})
			ctx := context.Background()
			result := make(chan error, 1)
			if phase == "send" {
				go func() {
					result <- e.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginPhone, Phone: "old-phone"})
				}()
			} else {
				if err := e.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginPhone, Phone: "old-phone"}); err != nil {
					t.Fatal(err)
				}
				go func() {
					if phase == "resend" {
						result <- e.ResendCode(ctx)
					} else {
						result <- e.SubmitCode(ctx, "old-code")
					}
				}()
			}
			<-entered
			e.CancelLogin()
			continuation := engine.Secret(`{"Phone":"new-phone","Hash":"new-hash","State":"code"}`)
			if err := e.ResumePhone(ctx, continuation); !errors.Is(err, engine.ErrLoginRunning) {
				t.Fatalf("reused a key while cancelled operation was unwinding: %v", err)
			}
			close(release)
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatalf("late result: %v", err)
			}
			waitLoginWorkers(t, e)
			if err := e.ResumePhone(ctx, continuation); err != nil {
				t.Fatal(err)
			}
			if l := e.Login(); l.State != engine.LoginWantCode || l.Phone != "new-phone" {
				t.Fatalf("stale login reopened: %+v", l)
			}
			if e.phoneHash != "new-hash" {
				t.Fatal("stale hash retained")
			}
		})
	}
}

func TestLoginCancelClearsSecretsAndContinuationNotifies(t *testing.T) {
	e := loginEngine(t, func(context.Context, bin.Encoder, bin.Decoder) error { return engine.ErrNotSupported })
	if err := e.ResumePhone(context.Background(), engine.Secret(`{"Phone":"phone","Hash":"sensitive-hash","State":"password"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.Changes():
	default:
		t.Fatal("resuming did not notify")
	}
	if _, err := e.PhoneContinuation(); err != nil {
		t.Fatal(err)
	}
	e.CancelLogin()
	l := e.Login()
	if e.phoneHash != "" || l.Phone != "" || l.PasswordHint != "" || l.QRURL != "" || l.QRExpiresAt != nil || l.UpdatedAt.IsZero() {
		t.Fatalf("cancel retained login details: %+v", l)
	}
	if _, err := e.PhoneContinuation(); !errors.Is(err, engine.ErrNoLogin) {
		t.Fatal(err)
	}
}

func TestLoginQRRenewalAndCompletion(t *testing.T) {
	var exports atomic.Int32
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.AuthExportLoginTokenRequest:
			if r.APIID != 1 || r.APIHash != "synthetic-test-only" {
				t.Error("QR credentials missing")
			}
			n := exports.Add(1)
			switch n {
			case 1:
				out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte("expired-synthetic-token"), Expires: int(time.Now().Add(-time.Second).Unix())}
			case 2:
				out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte("current-synthetic-token"), Expires: int(time.Now().Add(time.Minute).Unix())}
			case 3:
				out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginTokenSuccess{Authorization: &tg.AuthAuthorization{User: &tg.User{ID: 99}}}
			default:
				t.Errorf("unexpected export number %d", n)
				return engine.ErrNotSupported
			}
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		default:
			t.Errorf("unexpected QR RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	var l engine.Login
	for {
		l = e.Login()
		if l.State == engine.LoginShowQR && l.QRVersion == 2 {
			break
		}
		select {
		case <-e.Changes():
		case <-timer.C:
			t.Fatalf("QR did not renew: %+v", l)
		}
	}
	if l.QRURL == "" || l.QRExpiresAt == nil {
		t.Fatal("missing refreshed QR")
	}
	b, _ := json.Marshal(l)
	if strings.Contains(string(b), "token=") {
		t.Fatal("QR credential serialized")
	}
	e.signalLogin(&tg.UpdateShort{Update: &tg.UpdateLoginToken{}})
	waitLogin(t, e, engine.LoginDone)
	if l = e.Login(); l.QRURL != "" || l.QRExpiresAt != nil {
		t.Fatal("completed QR retained credential")
	}
	select {
	case <-e.authReady:
	case <-time.After(2 * time.Second):
		t.Fatal("bound login did not release ingestion")
	}
	a, _ := e.Account(context.Background())
	if !a.Authenticated || a.SelfID != 99 {
		t.Fatal("identity missing before ingestion")
	}
}

func TestLoginQRStaleExportCannotReopenCanceledAttempt(t *testing.T) {
	entered, release, returned := make(chan context.Context, 1), make(chan struct{}), make(chan struct{})
	e := loginEngine(t, func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.AuthExportLoginTokenRequest); !ok {
			t.Errorf("unexpected stale QR RPC %T", in)
			return engine.ErrNotSupported
		}
		entered <- ctx
		<-release
		defer close(returned)
		out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte("stale-synthetic-token"), Expires: int(time.Now().Add(time.Minute).Unix())}
		return nil
	})
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		t.Fatal(err)
	}
	oldctx := <-entered
	e.CancelLogin()
	<-oldctx.Done()
	continuation := engine.Secret(`{"Phone":"new-phone","Hash":"new-hash","State":"code"}`)
	if err := e.ResumePhone(context.Background(), continuation); !errors.Is(err, engine.ErrLoginRunning) {
		t.Fatalf("reused key before cancelled RPC joined: %v", err)
	}
	close(release)
	<-returned
	waitLoginWorkers(t, e)
	if err := e.ResumePhone(context.Background(), continuation); err != nil {
		t.Fatal(err)
	}
	// The actual show callback sees the canceled old context and generation. It
	// cannot publish its token even if the transport ignores cancellation.
	if l := e.Login(); l.State != engine.LoginWantCode || l.QRURL != "" || l.Phone != "new-phone" {
		t.Fatalf("stale export reopened login: %+v", l)
	}
}

func TestLoginQR2FAAndCanceledImport(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "2fa", true: "canceled"}[canceled], func(t *testing.T) {
			var exports atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				if _, ok := in.(*tg.AuthExportLoginTokenRequest); !ok {
					t.Errorf("unexpected QR RPC %T", in)
					return engine.ErrNotSupported
				}
				if exports.Add(1) == 1 {
					out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte("synthetic-token"), Expires: int(time.Now().Add(time.Minute).Unix())}
					return nil
				}
				close(entered)
				<-release
				return tgerr.New(401, "SESSION_PASSWORD_NEEDED")
			})
			// Expose lifetime cancellation without connecting the Telegram client.
			lifetime, stop := context.WithCancel(context.Background())
			defer stop()
			e.active = lifetime
			if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
				t.Fatal(err)
			}
			waitLogin(t, e, engine.LoginShowQR)
			e.signalLogin(&tg.UpdatesCombined{Updates: []tg.UpdateClass{&tg.UpdateLoginToken{}}})
			<-entered
			if canceled {
				stop()
			}
			close(release)
			want := engine.LoginWantPass
			if canceled {
				// Parent transport death is not an explicit user cancellation.
				want = engine.LoginFailed
			}
			l := waitLogin(t, e, want)
			if l.QRURL != "" || l.QRExpiresAt != nil {
				t.Fatal("2FA/cancellation retained QR credential")
			}
		})
	}
}

func TestLoginIdentityMismatchNeverReleasesIngestion(t *testing.T) {
	var exports atomic.Int32
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.AuthExportLoginTokenRequest:
			if exports.Add(1) == 1 {
				out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte("synthetic-token"), Expires: int(time.Now().Add(time.Minute).Unix())}
			} else {
				out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginTokenSuccess{Authorization: &tg.AuthAuthorization{User: &tg.User{ID: 100}}}
			}
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 100, Self: true}}
		case *tg.AuthLogOutRequest:
			return nil
		default:
			t.Errorf("unexpected identity RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		t.Fatal(err)
	}
	waitLogin(t, e, engine.LoginShowQR)
	e.signalLogin(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateLoginToken{}}})
	l := waitLogin(t, e, engine.LoginFailed)
	if l.Error == "" || l.QRURL != "" {
		t.Fatal("failed identity snapshot incomplete")
	}
	a, _ := e.Account(context.Background())
	if a.Authenticated {
		t.Fatal("mismatched identity became authenticated")
	}
	select {
	case <-e.authReady:
		t.Fatal("identity mismatch released ingestion")
	default:
	}
}

func TestLoginSnapshotDoesNotExposeMutableTimestamps(t *testing.T) {
	e := loginEngine(t, func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
		s := &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{Length: 5}, PhoneCodeHash: "hash"}
		s.SetTimeout(60)
		out.(*tg.AuthSentCodeBox).SentCode = s
		return nil
	})
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginPhone, Phone: "phone"}); err != nil {
		t.Fatal(err)
	}
	l := e.Login()
	before := *l.ResendAfter
	*l.ResendAfter = time.Time{}
	if !e.Login().ResendAfter.Equal(before) {
		t.Fatal("caller mutated live login timestamp")
	}
}

func TestLoginAttemptLifetimeCancellationClearsWaitingPhone(t *testing.T) {
	for _, state := range []engine.LoginState{engine.LoginWantCode, engine.LoginWantPass} {
		t.Run(string(state), func(t *testing.T) {
			e := loginEngine(t, func(context.Context, bin.Encoder, bin.Decoder) error {
				t.Error("waiting login called RPC")
				return engine.ErrInvalid
			})
			lifetime, stop := context.WithCancel(context.Background())
			defer stop()
			e.active = lifetime
			data, _ := json.Marshal(struct {
				Phone, Hash string
				State       engine.LoginState
			}{"phone", "sensitive-hash", state})
			if err := e.ResumePhone(context.Background(), engine.Secret(data)); err != nil {
				t.Fatal(err)
			}
			stop()
			l := waitLogin(t, e, engine.LoginCancelled)
			e.mu.Lock()
			hash := e.phoneHash
			e.mu.Unlock()
			if l.Phone != "" || hash != "" {
				t.Fatal("canceled lifetime retained phone credential")
			}
		})
	}
}

func TestLoginCanceledOperationNeverCallsRPC(t *testing.T) {
	e := loginEngine(t, func(context.Context, bin.Encoder, bin.Decoder) error {
		t.Error("canceled operation called RPC")
		return engine.ErrInvalid
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, state := range []string{"code", "password"} {
		if err := e.ResumePhone(context.Background(), engine.Secret(`{"Phone":"phone","Hash":"hash","State":"`+state+`"}`)); err != nil {
			t.Fatal(err)
		}
		if state == "code" {
			if err := e.SubmitCode(ctx, "code"); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if err := e.ResendCode(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		} else if err := e.SubmitPassword(ctx, "password"); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

func TestLoginCanceledSelfCannotBindOrReleaseIngestion(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.AuthSignInRequest:
			out.(*tg.AuthAuthorizationBox).Authorization = &tg.AuthAuthorization{User: &tg.User{ID: 99}}
		case *tg.UsersGetUsersRequest:
			close(entered)
			<-release
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		default:
			if _, ok := in.(*tg.AuthLogOutRequest); ok {
				return nil
			}
			t.Errorf("unexpected self RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})
	if err := e.ResumePhone(context.Background(), engine.Secret(`{"Phone":"old-phone","Hash":"hash","State":"code"}`)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- e.SubmitCode(context.Background(), "12345") }()
	<-entered
	e.CancelLogin()
	if err := e.ResumePhone(context.Background(), engine.Secret(`{"Phone":"new-phone","Hash":"new-hash","State":"code"}`)); !errors.Is(err, engine.ErrLoginRunning) {
		t.Fatalf("reused key while accepted login was awaiting compensation: %v", err)
	}
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("late self response: %v", err)
	}
	waitLoginWorkers(t, e)
	if l := e.Login(); l.State != engine.LoginCancelled || l.Phone != "" {
		t.Fatalf("self response reopened cancelled attempt: %+v", l)
	}
	if !e.sessionBlocked.Load() || e.Status().State != engine.StateLoggedOut {
		t.Fatal("late accepted login was not compensated")
	}
	a, _ := e.Account(context.Background())
	if a.Authenticated {
		t.Fatal("late self authenticated old attempt")
	}
	select {
	case <-e.authReady:
		t.Fatal("late self released ingestion")
	default:
	}
}

func TestLoginPhoneSentCodeSuccessBindsIdentity(t *testing.T) {
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.AuthSendCodeRequest:
			out.(*tg.AuthSentCodeBox).SentCode = &tg.AuthSentCodeSuccess{Authorization: &tg.AuthAuthorization{User: &tg.User{ID: 99}}}
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		default:
			t.Errorf("unexpected sent-success RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginPhone, Phone: "phone"}); err != nil {
		t.Fatal(err)
	}
	if l := e.Login(); l.State != engine.LoginDone || l.Phone != "" {
		t.Fatalf("sent-code success incomplete: %+v", l)
	}
	a, _ := e.Account(context.Background())
	if !a.Authenticated || a.SelfID != 99 {
		t.Fatal("identity not bound")
	}
}

func loginSRPParameters(t *testing.T) *tg.AccountPassword {
	t.Helper()
	// Public Telegram 2048-bit safe prime, not a session or account credential.
	p, err := hex.DecodeString("C71CAEB9C6B1C9048E6C522F70F13F73980D40238E3E21C14934D037563D930F" +
		"48198A0AA7C14058229493D22530F4DBFA336F6E0AC925139543AED44CCE7C37" +
		"20FD51F69458705AC68CD4FE6B6B13ABDC9746512969328454F18FAF8C595F64" +
		"2477FE96BB2A941D5BCD1D4AC8CC49880708FA9B378E3C4F3A9060BEE67CF9A4" +
		"A4A695811051907E162753B56B0F6B410DBA74D8A84B2A14B3144E0EF1284754" +
		"FD17ED950D5965B4B9DD46582DB1178D169C6BC465B0D6FF9CA3928FEF5B9AE4" +
		"E418FC15E83EBEA0F87FA9FF5EED70050DED2849F47BF959D956850CE929851F" +
		"0D8115F635B105EE2E4E15D04B2454BF6F4FADF034B10403119CD8E3B92FCC5B")
	if err != nil {
		t.Fatal(err)
	}
	return &tg.AccountPassword{SRPID: 42, SRPB: bytes.Repeat([]byte{0x55}, 256), SecureRandom: bytes.Repeat([]byte{0x33}, 256), CurrentAlgo: &tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow{G: 3, P: p, Salt1: []byte("synthetic-salt-one"), Salt2: []byte("synthetic-salt-two")}}
}

func TestLoginPhone2FAUsesRealPasswordProofAndAllowsRetry(t *testing.T) {
	params := loginSRPParameters(t)
	const password = "synthetic-password-only"
	expected, err := auth.PasswordHash([]byte(password), params.SRPID, params.SRPB, params.SecureRandom, params.CurrentAlgo)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.AuthSendCodeRequest:
			out.(*tg.AuthSentCodeBox).SentCode = &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{Length: 5}, PhoneCodeHash: "hash"}
		case *tg.AuthSignInRequest:
			return tgerr.New(401, "SESSION_PASSWORD_NEEDED")
		case *tg.AccountGetPasswordRequest:
			*out.(*tg.AccountPassword) = *params
		case *tg.AuthCheckPasswordRequest:
			checks++
			proof, ok := r.Password.(*tg.InputCheckPasswordSRP)
			if !ok || proof.SRPID != 42 || len(proof.A) != 256 || len(proof.M1) != 32 {
				t.Error("invalid SRP answer")
				return engine.ErrInvalid
			}
			if !bytes.Equal(proof.M1, expected.M1) {
				return tgerr.New(400, "PASSWORD_HASH_INVALID")
			}
			if !bytes.Equal(proof.A, expected.A) {
				t.Error("wrong SRP A")
			}
			out.(*tg.AuthAuthorizationBox).Authorization = &tg.AuthAuthorization{User: &tg.User{ID: 99}}
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		default:
			t.Errorf("unexpected 2FA RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})
	ctx := context.Background()
	if err := e.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginPhone, Phone: "synthetic-phone"}); err != nil {
		t.Fatal(err)
	}
	if err := e.SubmitCode(ctx, "12345"); err != nil {
		t.Fatal(err)
	}
	if l := e.Login(); l.State != engine.LoginWantPass || l.QRURL != "" {
		t.Fatalf("missing 2FA transition: %+v", l)
	}
	if err := e.SubmitPassword(ctx, "incorrect-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if l := e.Login(); l.State != engine.LoginWantPass || l.Error == "" {
		t.Fatalf("password retry not visible: %+v", l)
	}
	if err := e.SubmitPassword(ctx, password); err != nil {
		t.Fatal(err)
	}
	if l := e.Login(); l.State != engine.LoginDone || l.Error != "" || l.Phone != "" || e.phoneHash != "" {
		t.Fatalf("successful 2FA retained state: %+v", l)
	}
	if checks != 2 {
		t.Fatalf("password checks=%d", checks)
	}
}

// The production controller uses a real gotd auth helper with an offline RPC
// transport. No client.Run, sessions on disk, or Telegram connections are used.
func loginEngine(t *testing.T, f invokeFunc) *Engine {
	t.Helper()
	e, _ := offlineEngine(t)
	e.account.Authenticated = false
	ctx, cancel := context.WithCancel(context.Background())
	e.active = ctx
	e.api = tg.NewClient(f)
	t.Cleanup(func() { e.CancelLogin(); cancel() })
	return e
}

func TestLoginPhoneSendResendAndComplete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		switch r := in.(type) {
		case *tg.AuthSendCodeRequest:
			if r.PhoneNumber != "+15550000000" || r.APIID != 1 || r.APIHash != "synthetic-test-only" {
				t.Errorf("wrong send-code credentials")
			}
			s := &tg.AuthSentCode{PhoneCodeHash: "hash-one", Type: &tg.AuthSentCodeTypeApp{Length: 5}}
			s.SetTimeout(30)
			out.(*tg.AuthSentCodeBox).SentCode = s
		case *tg.AuthResendCodeRequest:
			if r.PhoneCodeHash != "hash-one" {
				t.Errorf("wrong resend hash")
			}
			out.(*tg.AuthSentCodeBox).SentCode = &tg.AuthSentCode{PhoneCodeHash: "hash-two", Type: &tg.AuthSentCodeTypeSMS{Length: 6}}
		case *tg.AuthSignInRequest:
			if r.PhoneCode != "123456" || r.PhoneCodeHash != "hash-two" {
				t.Errorf("wrong sign-in values")
			}
			out.(*tg.AuthAuthorizationBox).Authorization = &tg.AuthAuthorization{User: &tg.User{ID: 99, Self: true}}
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		default:
			t.Errorf("unexpected offline RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})
	var c engine.Controller = e
	if err := c.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginPhone, Phone: "+15550000000"}); err != nil {
		t.Fatal(err)
	}
	l := c.Login()
	if l.State != engine.LoginWantCode || l.CodeType != "app" || l.CodeLength != 5 || l.ResendAfter == nil {
		t.Fatalf("missing code details: %+v", l)
	}
	if err := c.ResendCode(ctx); err != nil {
		t.Fatal(err)
	}
	if l = c.Login(); l.CodeType != "sms" || l.CodeLength != 6 || l.ResendAfter != nil {
		t.Fatalf("stale resend details: %+v", l)
	}
	if err := c.SubmitCode(ctx, "123456"); err != nil {
		t.Fatal(err)
	}
	if c.Login().State != engine.LoginDone || calls != 4 {
		t.Fatalf("login=%+v calls=%d", c.Login(), calls)
	}
	a, _ := c.Account(ctx)
	if !a.Authenticated || a.SelfID != 99 {
		t.Fatal("identity not bound")
	}
	if _, err := e.PhoneContinuation(); !errors.Is(err, engine.ErrNoLogin) {
		t.Fatal("completed login retained continuation")
	}
}
