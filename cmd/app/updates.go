package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/brand"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/panel"
	"github.com/bwoliveira/telegram-mcp-local/internal/platform"
	"github.com/bwoliveira/telegram-mcp-local/internal/updater"
)

const internalRollbackCommand = "--internal-update-rollback"

func init() {
	if len(os.Args) != 3 || os.Args[1] != internalRollbackCommand {
		return
	}
	if err := updater.RollbackAndRelaunch(os.Args[2]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "update rollback failed:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// updates looks for a new version once a day, tells the person, and installs
// it when they ask.
type updates struct {
	app *App
	u   *updater.Updater

	mu        sync.Mutex
	announced string
}

func newUpdates(a *App) *updates {
	u := &updater.Updater{
		Repo:    strings.TrimPrefix(brand.RepositoryURL, "https://github.com/"),
		Version: strings.TrimPrefix(version, "v"),
		Logger:  a.logger,
		Dir:     filepath.Join(a.dataDir, "updates"),
		Target:  updater.DetectTarget(platform.Executable()),
	}
	// TELEGRAM_MCP_UPDATES_API and TELEGRAM_MCP_UPDATES_PUBLIC_KEY point the
	// real app at a local test feed signed with a disposable key, to rehearse
	// an update and rollback before publishing. They are honored only with
	// TELEGRAM_MCP_DEV=1 and logged loudly; otherwise they are refused and
	// update checks stay off until they are unset. The error is already
	// logged and shown by the next check.
	_ = u.ConfigureDevFeed(os.Getenv)
	return &updates{app: a, u: u}
}

func (u *updates) state() panel.UpdateState {
	s := u.u.State()
	return panel.UpdateState{State: s.Phase, Current: version, Latest: s.Latest, Page: s.Page, Progress: s.Progress, Error: s.Error, CheckedAt: s.CheckedAt}
}

// markHealthy is called only after the daemon is serving the embedded panel.
// The rollback copy remains until ReconcileStartup observes this marker on the
// following process start.
func (u *updates) markHealthy() error {
	return updater.MarkHealthy(u.u.Dir, strings.TrimPrefix(version, "v"))
}

// check looks now and returns with the answer, so the page can say whether
// there is a new version.
func (u *updates) check() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if found, _ := u.u.Check(ctx); found {
		u.announce()
	}
}

// install downloads, checks and installs the new version in the background,
// then quits so the new one can start. The page follows the progress.
func (u *updates) install() error {
	if st := u.u.State(); st.Phase == updater.Downloading || st.Phase == updater.Ready {
		return updater.ErrBusy
	}
	started := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		close(started)
		if err := u.u.Apply(ctx); err != nil {
			return
		}
		u.app.logger.Info("restarting into the new version", "version", u.u.State().Latest)
		time.Sleep(time.Second)
		u.app.quit()
	}()
	<-started
	time.Sleep(100 * time.Millisecond)
	return nil
}

// loop checks a minute after start and then twice a day.
func (u *updates) loop() {
	if !u.u.Enabled() {
		return
	}
	time.Sleep(time.Minute)
	for {
		if found, _ := u.u.Check(context.Background()); found {
			u.announce()
		}
		time.Sleep(12 * time.Hour)
	}
}

// announce tells the person once per version: a notification, and a line in
// the tray's menu.
func (u *updates) announce() {
	s := u.u.State()
	u.mu.Lock()
	seen := u.announced == s.Latest
	u.announced = s.Latest
	u.mu.Unlock()
	if seen {
		return
	}
	u.app.tray.offerUpdate(s.Latest)
	u.app.notify("update-"+s.Latest, i18n.Tf("Versão %s do Telegram MCP disponível", s.Latest), i18n.T("Abra Configurações para instalar."))
}
