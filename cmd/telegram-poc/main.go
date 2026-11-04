package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/gotdengine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/gotdengine/pocstore"
	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
	"github.com/gotd/td/telegram/dcs"
	"golang.org/x/net/proxy"
	"golang.org/x/term"
)

const defaultState = "/root/.local/state/telegram-poc"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func printJSON(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println("telegram-poc {login-qr|login-phone|status|dialogs|sync|backfill|send-saved|logout} [--state DIR] [options]\nOffline by default: help and status never connect. Other commands connect only when explicitly invoked.")
		return nil
	}
	command := args[0]
	known := map[string]bool{"login-qr": true, "login-phone": true, "status": true, "dialogs": true, "sync": true, "backfill": true, "send-saved": true, "logout": true}
	if !known[command] {
		return errors.New("unknown command")
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	state := f.String("state", defaultState, "private state directory (outside repository)")
	listen := f.String("listen", "127.0.0.1:47900", "QR loopback listener")
	phone := f.String("phone", "", "international phone number")
	code := f.String("code", "", "login code (prefer hidden terminal prompt)")
	passwordEnv := f.String("password-env", "TELEGRAM_2FA_PASSWORD", "2FA environment variable (only TELEGRAM_2FA_PASSWORD)")
	limit := f.Int("limit", 100, "maximum dialogs/history messages")
	chat := f.String("chat", "me", "peer: me, @username, type:id or numeric id")
	text := f.String("text", "", "Saved Messages text")
	dry := f.Bool("dry-run", true, "preview only")
	confirm := f.Bool("confirm", false, "enable terminal approval for Saved Messages send or logout")
	follow := f.Bool("follow", false, "follow updates")
	duration := f.Duration("duration", 10*time.Minute, "sync duration")
	socks := f.String("socks", "", "optional local SOCKS5 proxy, e.g. 127.0.0.1:1080")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return engine.ErrInvalid
	}
	if *limit <= 0 || *duration <= 0 || *passwordEnv != "TELEGRAM_2FA_PASSWORD" {
		return engine.ErrInvalid
	}
	if command == "logout" && !*confirm {
		return errors.New("logout revokes the Telegram session; requires --confirm")
	}
	if command == "send-saved" && !*dry && !*confirm {
		return errors.New("real send requires --dry-run=false --confirm and interactive approval")
	}
	abs, err := filepath.Abs(*state)
	if err != nil {
		return err
	}
	if err := privateDir(abs); err != nil {
		return err
	}
	// Exclusive CLI ownership: two processes may not share one gotd auth key.
	lock, err := os.OpenFile(filepath.Join(abs, "poc.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another PoC process owns this state directory")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	s, err := secrets.Open("primary", secrets.Options{Mode: "file", DataDir: filepath.Join(abs, "secrets"), KeyDir: abs + "-keys"})
	if err != nil {
		return err
	}
	session := gotdengine.SecretsSession{Store: s}
	store, err := pocstore.Open(filepath.Join(abs, "store.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if command == "status" {
		b, err := session.LoadSession(ctx)
		if err != nil && !errors.Is(err, engine.ErrNoSession) {
			return err
		}
		id, err := store.SelfID(ctx, "primary")
		if err != nil {
			return err
		}
		st, ok, err := store.LoadUpdatesState(ctx, "primary")
		if err != nil {
			return err
		}
		counts, err := store.Metrics(ctx, "primary")
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"store_counts": counts, "offline": true, "session_present": len(b) > 0, "session_bytes": len(b), "session_le_1800": len(b) > 0 && len(b) <= 1800, "bound_self_id": id, "checkpoint_present": ok, "checkpoint": st, "authorization": "not checked offline"})
	}
	idText := os.Getenv("TELEGRAM_API_ID")
	if idText == "" {
		b, err := s.Get(secrets.APIID)
		if err != nil {
			return errors.New("TELEGRAM_API_ID or secrets api_id is required")
		}
		idText = string(b)
	}
	id, err := strconv.Atoi(idText)
	if err != nil || id <= 0 {
		return errors.New("invalid API ID")
	}
	hash := os.Getenv("TELEGRAM_API_HASH")
	if hash == "" {
		b, err := s.Get(secrets.APIHash)
		if err != nil {
			return errors.New("TELEGRAM_API_HASH or secrets api_hash is required")
		}
		hash = string(b)
	}
	var opt gotdengine.Options
	if *socks != "" {
		dialer, err := proxy.SOCKS5("tcp", *socks, nil, proxy.Direct)
		if err != nil {
			return errors.New("invalid SOCKS proxy")
		}
		cd, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return errors.New("proxy has no context support")
		}
		opt.Resolver = dcs.Plain(dcs.PlainOptions{Dial: cd.DialContext})
	}
	e, err := gotdengine.NewWithOptions(engine.Config{Account: "primary", APIID: id, APIHash: engine.Secret(hash), Store: store, Session: session, Device: engine.DeviceInfo{Model: "Telegram MCP", System: "Linux", AppVersion: "spike-s1", LangCode: "pt"}}, opt)
	if err != nil {
		return err
	}
	if command != "login-qr" && command != "login-phone" {
		if _, err := session.LoadSession(ctx); err != nil {
			return engine.ErrNotAuthorized
		}
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	defer func() { cancel(); <-done }()
	ready, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	if err := e.WaitReady(ready); err != nil {
		return errors.New("client did not become ready within 90 seconds")
	}
	switch command {
	case "login-qr":
		return loginQR(ctx, e, abs, *listen, *passwordEnv)
	case "login-phone":
		return loginPhone(ctx, e, s, abs, *phone, *code, *passwordEnv)
	}
	if err := waitConnected(ready, e); err != nil {
		return err
	}
	switch command {
	case "dialogs":
		_, err := e.RefreshDialogs(ctx, engine.RefreshRequest{Limit: *limit})
		if err != nil {
			return err
		}
		ds, err := store.Dialogs(ctx, "primary")
		if err != nil {
			return err
		}
		if len(ds) > *limit {
			ds = ds[:*limit]
		}
		return printJSON(ds)
	case "sync":
		start := time.Now()
		if *follow {
			timer := time.NewTimer(*duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
			}
		}
		counts, err := store.Metrics(context.WithoutCancel(ctx), "primary")
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"elapsed_seconds": time.Since(start).Seconds(), "metrics": e.Metrics(), "store_counts": counts, "status": e.Status()})
	case "backfill":
		p, err := resolveChat(ctx, e, store, *chat)
		if err != nil {
			return err
		}
		start := time.Now()
		fetched, added, rounds := 0, 0, 0
		var last engine.BackfillResult
		for fetched < *limit {
			last, err = e.Backfill(ctx, engine.BackfillRequest{Peer: p, Count: min(100, *limit-fetched), Rounds: 1})
			fetched += last.Fetched
			added += last.Added
			rounds += last.Rounds
			if err != nil || last.Complete || last.Fetched == 0 {
				break
			}
		}
		last.Fetched, last.Added, last.Rounds = fetched, added, rounds
		seconds := time.Since(start).Seconds()
		if encErr := printJSON(map[string]any{"backfill": last, "elapsed_seconds": seconds, "messages_per_minute": float64(fetched) * 60 / max(seconds, 0.001), "metrics": e.Metrics()}); encErr != nil {
			return encErr
		}
		return err
	case "send-saved":
		a, err := e.Account(ctx)
		if err != nil {
			return err
		}
		r := engine.SendTextRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: a.SelfID}, Text: *text, WriteOptions: engine.WriteOptions{DryRun: true}}
		preview, err := e.SendText(ctx, r)
		if err != nil {
			return err
		}
		if err := printJSON(preview); err != nil {
			return err
		}
		if *dry {
			return nil
		}
		answer, err := readSecret(ctx, "Type SEND to send ONLY to Saved Messages: ")
		if err != nil {
			return err
		}
		if answer != "SEND" {
			return errors.New("send cancelled")
		}
		if err := e.SetMode(ctx, engine.ModeReadWrite); err != nil {
			return err
		}
		r.DryRun = false
		r.ExpectFingerprint = preview.Preview.Fingerprint
		result, err := e.SendText(ctx, r)
		if err != nil {
			return err
		}
		return printJSON(result)
	case "logout":
		err := e.LogOut(ctx)
		if err != nil {
			return errors.New("local logout attempted; close this session in Telegram Devices too if remote revocation failed")
		}
		fmt.Println("Session revoked; local history retained.")
		return nil
	}
	return engine.ErrInvalid
}
func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe state directory permissions (requires 0700)")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return errors.New("state directory must not contain symlinks")
	}
	return nil
}
func readSecret(ctx context.Context, prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("interactive terminal required; do not pass secrets through chat or logs")
	}
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return "", err
	}
	next := *old
	next.Lflag &^= unix.ECHO | unix.ECHONL
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &next); err != nil {
		return "", err
	}
	defer unix.IoctlSetTermios(fd, unix.TCSETS, old)
	fmt.Fprint(os.Stderr, prompt)
	defer fmt.Fprintln(os.Stderr)
	buf := make([]byte, 4096)
	defer clear(buf)
	used := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return "", err
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			return "", errors.New("terminal disconnected")
		}
		n, err = unix.Read(fd, buf[used:])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", errors.New("terminal closed")
		}
		used += n
		if buf[used-1] == '\n' {
			return strings.TrimRight(string(buf[:used]), "\r\n"), nil
		}
		if used == len(buf) {
			return "", errors.New("terminal secret too long")
		}
	}
}

func password(ctx context.Context, e *gotdengine.Engine, env string) error {
	p := os.Getenv(env)
	if p == "" {
		var err error
		p, err = readSecret(ctx, "2FA password (hidden): ")
		if err != nil {
			return err
		}
	}
	return e.SubmitPassword(ctx, engine.Secret(p))
}
func waitConnected(ctx context.Context, e *gotdengine.Engine) error {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		switch e.Status().State {
		case engine.StateConnected:
			return nil
		case engine.StateNotAuthorized:
			return engine.ErrNotAuthorized
		case engine.StateRevoked:
			return engine.ErrRevoked
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
func resolveChat(ctx context.Context, e *gotdengine.Engine, s *pocstore.Store, q string) (engine.PeerRef, error) {
	if id, err := strconv.ParseInt(q, 10, 64); err == nil {
		var found []engine.Peer
		for _, kind := range []engine.PeerType{engine.PeerUser, engine.PeerChat, engine.PeerChannel} {
			p, ok, err := s.LookupPeer(ctx, "primary", engine.PeerRef{Type: kind, ID: id})
			if err != nil {
				return engine.PeerRef{}, err
			}
			if ok {
				found = append(found, p)
			}
		}
		if len(found) != 1 {
			return engine.PeerRef{}, errors.New("numeric ID is missing or ambiguous; use type:id")
		}
		return found[0].Ref, nil
	}
	cs, err := e.Resolve(ctx, engine.ResolveRequest{Query: q, Network: true, Limit: 2})
	if err != nil {
		return engine.PeerRef{}, err
	}
	if len(cs) != 1 {
		return engine.PeerRef{}, engine.ErrPeerNotFound
	}
	return cs[0].Peer.Ref, nil
}
