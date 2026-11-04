package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/gotdengine"
	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
	"os"
	"path/filepath"
)

func loginPhone(ctx context.Context, e *gotdengine.Engine, _ secrets.Store, state, phone, code, env string) error {
	pending, err := secrets.Open("primary-phone", secrets.Options{Mode: "file", DataDir: filepath.Join(state, "secrets"), KeyDir: state + "-keys"})
	if err != nil {
		return err
	}
	if phone != "" && code == "" {
		if err := e.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginPhone, Phone: phone}); err != nil {
			return err
		}
	} else {
		b, err := pending.Get(secrets.Session)
		if err != nil {
			return errors.New("no pending phone login; run login-phone --phone first")
		}
		if err := e.ResumePhone(ctx, engine.Secret(b)); err != nil {
			return err
		}
	}
	if e.Login().State == engine.LoginWantCode && code != "" {
		if code == "-" {
			code, err = readSecret(ctx, "Telegram code (hidden): ")
			if err != nil {
				return err
			}
		}
		if err := e.SubmitCode(ctx, engine.Secret(code)); err != nil {
			return err
		}
	}
	if e.Login().State == engine.LoginWantPass {
		continuation, err := e.PhoneContinuation()
		if err != nil {
			return err
		}
		if err := pending.Set(secrets.Session, []byte(continuation.Reveal())); err != nil {
			return err
		}
	}
	if e.Login().State == engine.LoginWantPass && (phone == "" || os.Getenv(env) != "") {
		if err := password(ctx, e, env); err != nil {
			return err
		}
	}
	if e.Login().State == engine.LoginDone {
		if err := pending.DeleteAll(); err != nil {
			return err
		}
		fmt.Println("conectado")
		return nil
	}
	continuation, err := e.PhoneContinuation()
	if err != nil {
		return err
	}
	if err := pending.Set(secrets.Session, []byte(continuation.Reveal())); err != nil {
		return err
	}
	return printJSON(map[string]any{"login_state": e.Login().State, "continuation": "encrypted in internal/secrets; resume with --code - or --password-env TELEGRAM_2FA_PASSWORD"})
}
