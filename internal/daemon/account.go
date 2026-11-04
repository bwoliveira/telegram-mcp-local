package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/account"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/localasr"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/bwoliveira/telegram-mcp-local/internal/webhook"
)

// Account owns one controller, local state, MCP tools and webhook deliveries.
// Store, session and engine lifetimes are isolated by account slot.
type Account struct {
	// Slug is empty for the primary account and fixed for additional slots.
	Slug     string
	StoreDir string
	DataDir  string

	logger      *slog.Logger
	cancel      context.CancelFunc
	eng         engine.Controller
	store       *store.Store
	janitorDone chan struct{}
	st          *state.State
	server      *mcp.Server
	hooks       *webhook.Manager
	webhooksOn  bool
	syncDone    chan struct{}
}

// AccountSpec locates an account slot's local data.
type AccountSpec struct {
	Slug     string
	StoreDir string
	DataDir  string
}

// startAccount constructs an isolated controller and runs it until cancellation.
func (d *Daemon) startAccount(ctx context.Context, spec AccountSpec, asr *localasr.Engine) (*Account, error) {
	for _, dir := range []string{spec.StoreDir, spec.DataDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("cannot create %s: %w", dir, err)
		}
	}
	logger := d.logger
	if spec.Slug != "" {
		logger = logger.With("account", spec.Slug)
	}
	a := &Account{Slug: spec.Slug, StoreDir: spec.StoreDir, DataDir: spec.DataDir, logger: logger, syncDone: make(chan struct{})}
	var err error
	if a.st, err = state.Open(spec.DataDir); err != nil {
		return nil, err
	}
	id := engine.AccountID(spec.Slug)
	if id == "" {
		id = "primary"
	}
	ctx, a.cancel = context.WithCancel(ctx)
	if a.store, err = store.Open(ctx, filepath.Join(spec.DataDir, "accounts", string(id), "store.db"), id, store.WithRetentionSettings(a.st)); err != nil {
		a.cancel()
		a.st.Close()
		return nil, err
	}
	fail := func(err error) (*Account, error) {
		a.cancel()
		a.store.Close()
		a.st.Close()
		return nil, err
	}
	if err := a.store.JanitorOnce(ctx, nil); err != nil {
		return fail(err)
	}
	mode := engine.ModeReadOnly
	if saved, e := a.st.Setting(ctx, "engine.mode"); e != nil {
		return fail(e)
	} else if saved == string(engine.ModeReadWrite) && modeIntent(filepath.Join(spec.DataDir, "engine.mode")) == engine.ModeReadWrite {
		mode = engine.ModeReadWrite
	}
	ecfg := engine.Config{Account: id, Mode: mode, Logger: logger, Store: a.store, Device: engine.DeviceInfo{Model: "Telegram MCP", AppVersion: d.cfg.Version, LangCode: "pt-BR"}}
	if d.cfg.production {
		ecfg.APIID, ecfg.APIHash, ecfg.Session, err = PrepareAccountCredentials(d.cfg.DataDir, id)
		if err != nil {
			return fail(fmt.Errorf("cannot configure Telegram engine: %w", err))
		}
	}
	c, err := d.cfg.Factory(ecfg)
	if err != nil {
		return fail(fmt.Errorf("cannot create account engine: %w", err))
	}
	if c == nil {
		return fail(errors.New("engine factory returned a nil controller"))
	}
	managed := newManagedController(ctx, d.cfg.Factory, ecfg, c)
	managed.modeFile = filepath.Join(spec.DataDir, "engine.mode")
	a.eng = managed
	a.janitorDone = make(chan struct{})
	go func() {
		defer close(a.janitorDone)
		if err := a.store.RunJanitor(ctx, managed.publishTTL); err != nil && ctx.Err() == nil {
			logger.Error("account janitor stopped", "error", err)
			a.cancel()
		}
	}()
	a.server = mcp.New(mcp.Config{Engine: a.eng, ReadStore: a.store, State: a.st, Logger: logger,
		BaseURL: baseURL(d.Port()), MediaDir: filepath.Join(spec.DataDir, "media"), ExportDir: filepath.Join(spec.DataDir, "exports"), ASR: asr,
		Account: a.Info, Accounts: d.accountCount})
	a.startWebhooks(ctx, d.webhookLimiter())
	go func() {
		defer close(a.syncDone)
		if err := a.eng.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("account engine stopped", "error", err)
		}
	}()
	return a, nil
}

// stop waits for the controller to finish before closing its local state.
func (a *Account) stop() {
	a.cancel()
	<-a.syncDone
	<-a.janitorDone
	a.store.Close()
	a.st.Close()
}

// Info combines the locally chosen label with the engine's account identity.
func (a *Account) Info(ctx context.Context) account.Info {
	info := account.Info{Slug: a.Slug}
	info.Label, _ = a.st.Setting(ctx, account.LabelSetting)
	identity, err := a.eng.Account(ctx)
	if err == nil {
		info.Phone = account.FormatPhone(identity.Phone)
		info.Name = strings.TrimSpace(identity.FirstName + " " + identity.LastName)
		if info.Name == "" {
			info.Name = identity.Username
		}
	}
	return info
}

// SetLabel names an account; an empty label clears it.
func (a *Account) SetLabel(ctx context.Context, label string) error {
	return a.st.SetSetting(ctx, account.LabelSetting, account.CleanLabel(label))
}

// Server is this account's MCP server.
func (a *Account) Server() *mcp.Server { return a.server }

// Engine is this account's lifecycle controller.
func (a *Account) Engine() engine.Controller { return a.eng }

func (d *Daemon) webhookLimiter() *webhook.Limiter {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.webhookLimits == nil {
		d.webhookLimits = webhook.NewLimiter()
	}
	return d.webhookLimits
}

// startWebhooks consumes committed engine events directly, without an HTTP relay.
func (a *Account) startWebhooks(ctx context.Context, limits *webhook.Limiter) {
	a.hooks = webhook.NewWithLimiter(a.st, a.logger, mcp.Version, nil, limits)
	a.hooks.SetAccount(a.Info)
	a.hooks.BindEngine(ctx, a.eng)
	if err := a.hooks.Start(ctx); err != nil {
		a.logger.Warn("could not load the webhooks", "error", err)
		return
	}
	a.webhooksOn = true
}
