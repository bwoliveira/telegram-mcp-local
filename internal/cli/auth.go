package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"golang.org/x/term"
)

type authReader func(prompt string, secret bool) (string, error)

func terminalReader(input *os.File, output io.Writer) authReader {
	reader := bufio.NewReader(input)
	return func(prompt string, secret bool) (string, error) {
		fmt.Fprint(output, prompt)
		if secret && term.IsTerminal(int(input.Fd())) {
			b, err := term.ReadPassword(int(input.Fd()))
			fmt.Fprintln(output)
			return string(b), err
		}
		s, err := reader.ReadString('\n')
		if err == io.EOF && len(s) > 0 {
			err = nil
		}
		return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r"), err
	}
}

// Drive only the running daemon's Controller. Opening a second local engine
// would race its MTProto session and store. Credentials are read from stdin,
// never command arguments/environment and never echoed into output/errors.
func runAuth(ctx context.Context, cfg config, args []string, read authReader, out io.Writer) error {
	method := engine.LoginPhone
	if len(args) == 1 && args[0] == "--qr" {
		method = engine.LoginQR
	} else if len(args) != 0 && !(len(args) == 1 && args[0] == "--phone") {
		return errors.New("usage: telegram-mcp auth [--phone | --qr] (credentials only at protected prompts; --phone takes no value)")
	}
	request := engine.LoginRequest{Method: method}
	if method == engine.LoginPhone {
		phone, err := read("Phone number (international format): ", true)
		if err != nil || strings.TrimSpace(phone) == "" {
			return errors.New("could not read phone number")
		}
		request.Phone = strings.TrimSpace(phone)
	}
	client := daemonClient()
	defer client.CloseIdleConnections()
	var login engine.Login
	if err := daemonJSON(ctx, client, cfg, "POST", "/api/pair", request, &login); err != nil {
		return err
	}
	if login.Method != method {
		return errors.New("another login method is active; finish or cancel it in the panel first")
	}
	var page *authQRPage
	completed := false
	defer func() {
		if page != nil {
			page.close()
		}
		if !completed {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = daemonJSON(cancelCtx, client, cfg, "POST", "/api/pair/cancel", struct{}{}, nil)
		}
	}()
	for {
		if login.State != engine.LoginShowQR && page != nil {
			page.close()
			page = nil
		}
		if login.State == engine.LoginShowQR {
			if method != engine.LoginQR {
				return errors.New("unexpected QR login state")
			}
			if page == nil {
				var err error
				page, err = newAuthQRPage()
				if err != nil {
					return err
				}
			}
			if err := page.update(ctx, client, cfg); errors.Is(err, errAuthQRUnavailable) {
				// Scanning may advance to 2FA/done between state and image GET.
				page.close()
				page = nil
			} else if err != nil {
				return err
			}
			if page != nil && !page.announced {
				if _, err := fmt.Fprintf(out, "QR page (private; open locally): %s\n", page.path); err != nil {
					return errors.New("could not report private QR page")
				}
				page.announced = true
			}
		}
		switch login.State {
		case engine.LoginWantCode, engine.LoginWantPass:
			password := login.State == engine.LoginWantPass
			prompt, path, key := "Telegram login code: ", "/api/pair/code", "code"
			if password {
				prompt, path, key = "Telegram 2FA password: ", "/api/pair/password", "password"
			}
			value, err := read(prompt, true)
			if err != nil || value == "" {
				return errors.New("could not read login credential")
			}
			if err := daemonJSON(ctx, client, cfg, "POST", path, map[string]string{key: value}, &login); err != nil {
				return err
			}
		case engine.LoginDone, engine.LoginSyncing:
			completed = true
			fmt.Fprintln(out, "Telegram authenticated; the daemon continues synchronization.")
			return nil
		case engine.LoginStarting, engine.LoginShowQR:
			select {
			case <-ctx.Done():
				return errors.New("login cancelled or timed out")
			case <-time.After(300 * time.Millisecond):
			}
			var state struct {
				Pairing engine.Login `json:"pairing"`
			}
			if err := daemonJSON(ctx, client, cfg, "GET", "/api/state", nil, &state); err != nil {
				return err
			}
			login = state.Pairing
		case engine.LoginFailed:
			completed = true // terminal failure belongs to the daemon, not CancelLogin
			if login.Error != "" {
				return fmt.Errorf("Telegram login failed: %s", login.Error)
			}
			return errors.New("Telegram login failed; retry `telegram-mcp auth`")
		case engine.LoginCancelled:
			completed = true
			return errors.New("Telegram login cancelled")
		default:
			return errors.New("unexpected login state; use `telegram-mcp open` for QR login")
		}
	}
}
