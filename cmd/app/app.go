package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/bwoliveira/telegram-mcp-local/internal/appconfig"
	"github.com/bwoliveira/telegram-mcp-local/internal/daemon"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/panel"
	"github.com/bwoliveira/telegram-mcp-local/internal/platform"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
	"github.com/bwoliveira/telegram-mcp-local/internal/updater"
)

// bundleID names the app to the system: single instance, login item,
// notifications.
const bundleID = platform.AppID

type App struct {
	logger   *slog.Logger
	logFile  *os.File
	logPath  string
	logDir   string
	dataDir  string
	storeDir string
	bridge   string
	hidden   bool

	cfgMu      sync.Mutex
	cfg        appconfig.Config
	portLocked bool

	wails  *application.App
	winMu  sync.Mutex
	window *application.WebviewWindow
	// showMu keeps the window's coming and going in order; dockedAt is when
	// the app last came into the Dock.
	showMu   sync.Mutex
	dockedAt time.Time
	tray     *tray
	appMenu  appMenu
	notifier *notifications.NotificationService
	handler  swapHandler
	updates  *updates

	startMu sync.Mutex // Serialize startup and first-use credential retries.
	dmu     sync.Mutex
	daemon  *daemon.Daemon
	follow  context.CancelFunc

	// canHide is false where there is no tray to hide the window into.
	canHide  bool
	quitting atomic.Bool
}

func newApp(hidden bool) (*App, error) {
	dataDir, err := platform.DataDir()
	if err != nil {
		return nil, err
	}
	logDir, err := platform.LogDir()
	if err != nil {
		return nil, err
	}
	logger, logFile, err := openLog(logDir)
	if err != nil {
		return nil, err
	}
	mcp.Version = version
	a := &App{logger: logger, logFile: logFile, logPath: logFile.Name(), logDir: logDir, dataDir: dataDir, hidden: hidden}
	a.storeDir = os.Getenv("TELEGRAM_MCP_STORE_DIR")
	if a.storeDir == "" {
		a.storeDir = filepath.Join(dataDir, "store")
	}
	a.bridge = platform.Bundled("telegram-mcp-bridge")
	if a.cfg, err = appconfig.Load(dataDir); err != nil {
		logger.Warn("config.json could not be read; using the defaults", "error", err)
	}
	if port, ok, err := appconfig.PortFromEnv(); ok && err == nil {
		a.cfg.Port, a.portLocked = port, true
	}
	i18n.Set(a.cfg.Language)
	a.canHide = trayAvailable()
	a.updates = newUpdates(a)
	if err := updater.ReconcileStartup(a.updates.u.Dir, strings.TrimPrefix(version, "v")); err != nil {
		logger.Error("pending update recovery failed", "error", err)
		return nil, err
	}
	logger.Info("starting", "version", version, "data", dataDir, "engine", os.Getenv("TELEGRAM_MCP_ENGINE"), "bridge", a.bridge, "hidden", hidden)
	return a, nil
}

func (a *App) config() appconfig.Config {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.cfg
}

// updateConfig changes config.json. A port fixed by the environment is the
// app's to use, never to keep.
func (a *App) updateConfig(f func(*appconfig.Config)) error {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	f(&a.cfg)
	saved := a.cfg
	if a.portLocked {
		saved.Port = appconfig.DefaultPort
		if disk, err := appconfig.Load(a.dataDir); err == nil {
			saved.Port = disk.Port
		}
	}
	return appconfig.Save(a.dataDir, saved)
}

func (a *App) run() error {
	a.notifier = notifications.New()
	a.wails = application.New(application.Options{
		Name:        platform.AppName,
		Description: i18n.T("Seu Telegram nas suas ferramentas de IA, neste computador. Não oficial."),
		Icon:        appIcon,
		Logger:      a.logger,
		Services:    []application.Service{application.NewService(a.notifier)},
		Assets:      application.AssetOptions{Handler: &a.handler, DisableLogging: true},
		Mac: application.MacOptions{
			// The app starts out of the Dock; the window brings it in.
			ActivationPolicy: application.ActivationPolicyAccessory,
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		Linux:   application.LinuxOptions{DisableQuitOnLastWindowClosed: true, ProgramName: "telegram-mcp"},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID:               instanceID(),
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { go a.showWindow("") },
		},
		ShouldQuit: a.shouldQuit,
		OnShutdown: a.shutdown,
	})

	// The tray first: the gateway, started next, reports to it at once.
	a.setMenu()
	a.tray = newTray(a)
	// Native menus require GTK/Cocoa to be initialized. Starting the daemon
	// and login registration before Run races their first tray updates.
	a.wails.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			a.prepare()
			a.firstRun()
			go a.updates.loop()
		}()
	})

	a.wails.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) { a.showWindow("") })
	if runtime.GOOS == "darwin" {
		// macOS says whether it opened the app at login only once the app has
		// started, so the window is made then, if at all.
		a.wails.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
			a.waitOutOfDock()
			if a.hidden || launchedAtLogin() {
				a.logger.Info("opened at login: staying in the tray")
				return
			}
			a.showWindow("")
		})
	} else {
		// Elsewhere --hidden says so from the start, and GTK wants its windows
		// made before the application runs.
		a.createWindow(a.hidden && a.canHide, "/")
	}
	return a.wails.Run()
}

// prepare starts the gateway the window shows.
func (a *App) prepare() {
	a.startDaemon()
}

// instanceID is one per data folder: an app pointed at another folder with
// TELEGRAM_MCP_DATA, for development or a demo, runs beside the real one.
func instanceID() string {
	dir := os.Getenv("TELEGRAM_MCP_DATA")
	if dir == "" {
		return bundleID
	}
	sum := sha256.Sum256([]byte(dir))
	// D-Bus forbids a component starting with a digit, including hash suffixes.
	return bundleID + ".data" + hex.EncodeToString(sum[:8])
}

func (a *App) startDaemon() {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	if a.current() != nil || a.quitting.Load() {
		return
	}
	if os.Getenv("TELEGRAM_MCP_ENGINE") != "fake" && !a.credentialsReady() {
		return
	}
	desktop := panel.DesktopCommand{Command: a.bridge}
	if appImage := os.Getenv("APPIMAGE"); appImage != "" {
		desktop = panel.DesktopCommand{Command: appImage, Args: []string{"bridge"}}
	} else if desktop.Command == "" {
		// A development build: the bridge is built beside the app.
		desktop.Command = filepath.Join(filepath.Dir(platform.Executable()), platform.ExeName("telegram-mcp-bridge"))
	}
	if err := service.PrepareData(a.dataDir); err != nil {
		a.logger.Error("the data folder could not be prepared", "error", err)
		a.handler.set(startFailure(err, a.logPath))
		return
	}
	token, err := service.ResolveToken(a.dataDir, os.Getenv("TELEGRAM_MCP_TOKEN"), true)
	if err != nil {
		a.logger.Error("the HTTP credential could not be loaded", "error", err)
		a.handler.set(startFailure(err, a.logPath))
		return
	}
	d, err := daemon.Start(daemon.Config{
		Port: a.config().Port, Token: token,
		StoreDir: a.storeDir, DataDir: a.dataDir,
		LogPath: a.logPath, Version: version, Logger: a.logger, Desktop: desktop,
		Panel: func(p *panel.Panel) { p.Host = a },
	})
	if err != nil {
		a.logger.Error("the gateway did not start", "error", err)
		a.handler.set(startFailure(err, a.logPath))
		return
	}
	a.dmu.Lock()
	if a.quitting.Load() {
		a.dmu.Unlock()
		d.Stop()
		return
	}
	a.daemon = d
	ctx, cancel := context.WithCancel(context.Background())
	a.follow = cancel
	a.dmu.Unlock()
	a.handler.set(d.AppHandler())
	if a.updates != nil {
		if err := a.updates.markHealthy(); err != nil {
			a.logger.Error("update health marker could not be written", "error", err)
		} else {
			a.logger.Info("update startup is healthy", "version", version)
		}
	}
	go a.followStatus(ctx, d)
}

func (a *App) current() *daemon.Daemon {
	a.dmu.Lock()
	defer a.dmu.Unlock()
	return a.daemon
}

// followStatus keeps the tray in step with the gateway, and tells the person
// when Telegram needs them.
func (a *App) followStatus(ctx context.Context, d *daemon.Daemon) {
	updates, stop := d.Watch()
	defer stop()
	last := engine.ConnState("")
	for {
		select {
		case <-ctx.Done():
			return
		case st := <-updates:
			if a.tray != nil {
				a.tray.render(st)
			}
			state := engine.ConnState(st.Sync)
			if (state == engine.StateLoggedOut || state == engine.StateRevoked) && last != "" && last != state {
				a.notify("telegram-logged-out", i18n.T("Telegram desconectado"),
					i18n.T("A sessão deste computador foi encerrada no Telegram. Abra o Telegram MCP para entrar de novo."))
			}
			last = state
		}
	}
}

// firstRun turns "open at login" on, as the first window offers it checked;
// the checkbox there and in the tray turns it off. On later runs it makes
// sure the registration still stands: a new version, signed anew or moved,
// can lose it.
func (a *App) firstRun() {
	if a.quitting.Load() {
		return
	}
	first := !a.config().HasSeen("first-run")
	if first || (a.config().Autostart && !a.autostartRegistered()) {
		if err := a.SetAutostart(a.config().Autostart); err != nil {
			a.logger.Warn("could not register the app to open at login", "error", err)
		}
	}
	if first {
		_ = a.updateConfig(func(c *appconfig.Config) { c.Seen = append(c.Seen, "first-run") })
	}
}

func autostartExecutable() string {
	if appImage := os.Getenv("APPIMAGE"); runtime.GOOS == "linux" && appImage != "" {
		return appImage
	}
	return platform.Executable()
}

func (a *App) autostartRegistered() bool {
	on, err := platform.AutostartEnabled(autostartExecutable())
	return err == nil && on
}

func (a *App) onClose(w *application.WebviewWindow, e *application.WindowEvent) {
	// Quitting, or the window hideWindow let go: it closes.
	if a.quitting.Load() || a.win() != w {
		return
	}
	e.Cancel()
	go a.closeWindow()
}

// closeWindow is what closing the window does, and on macOS ⌘Q too: the app
// stays in the tray, working, unless the person turned that off.
func (a *App) closeWindow() {
	switch {
	case !a.canHide:
		if w := a.win(); w != nil {
			w.Minimise()
		}
	case a.config().CloseToTray:
		a.hideWindow()
		if !a.config().HasSeen("close") {
			_ = a.updateConfig(func(c *appconfig.Config) { c.Seen = append(c.Seen, "close") })
			a.notify("close", i18n.T("O Telegram MCP continua rodando"),
				stillRunning())
		}
	default:
		a.quit()
	}
}

// shouldQuit answers a request to quit the app. Encerrar quits, and so do
// logging out, shutting down and a signal; Encerrar in the macOS Dock closes
// the window, as ⌘Q does, since the app keeps working without one.
func (a *App) shouldQuit() bool {
	if a.quitting.Load() || !quitAskedByApp() {
		return true
	}
	go a.closeWindow()
	return false
}

// quitWarning is what quitting costs, said before the person confirms.
var quitWarning = i18n.N("O MCP é desligado: as ferramentas de IA perdem o acesso ao Telegram, e este computador para de receber mensagens enquanto o app estiver fechado.")

// confirmQuit asks before quitting, from the tray and the app menu.
func (a *App) confirmQuit() {
	application.InvokeSync(activate)
	d := a.wails.Dialog.Question().SetTitle(i18n.Tf("Encerrar o %s?", platform.AppName)).SetMessage(i18n.T(quitWarning))
	// Windows only has its own Sim and Não, which Wails names in English.
	no, yes := i18n.T("Cancelar"), i18n.T("Encerrar")
	if runtime.GOOS == "windows" {
		no, yes = "No", "Yes"
	}
	d.AddButton(no).SetAsCancel()
	d.SetDefaultButton(d.AddButton(yes).OnClick(func() { go a.quit() }))
	d.Show()
}

func (a *App) createWindow(hidden bool, url string) *application.WebviewWindow {
	w := a.wails.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            platform.AppName,
		Width:            1000,
		Height:           760,
		MinWidth:         720,
		MinHeight:        560,
		URL:              url,
		Hidden:           hidden,
		BackgroundColour: application.NewRGB(246, 248, 247),
	})
	w.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) { a.onClose(w, e) })
	a.winMu.Lock()
	a.window = w
	a.winMu.Unlock()
	return w
}

func (a *App) win() *application.WebviewWindow {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	return a.window
}

// dockDelay is how long the app gives macOS to finish bringing it into the
// Dock before it makes the window.
const dockDelay = 200 * time.Millisecond

// showWindow brings the window up, at path when one is given. It can wait,
// so it must not run on the main thread.
func (a *App) showWindow(path string) {
	a.showMu.Lock()
	defer a.showMu.Unlock()
	w := a.win()
	if w == nil {
		if runtime.GOOS != "darwin" {
			return
		}
		// macOS treats a window made while the app is out of the Dock as a
		// menu bar app's for as long as it lives, and shows it over other
		// apps in full screen. So the window is made fresh each time, once
		// the app is in the Dock, and goes when it closes.
		var docked bool
		application.InvokeSync(func() {
			docked = inDock()
			showInDock(true)
		})
		if !docked {
			a.dockedAt = time.Now()
			time.Sleep(dockDelay)
		}
		if path == "" {
			path = "/"
		}
		w, path = a.createWindow(false, path), ""
	}
	application.InvokeSync(func() {
		showInDock(true)
		activate()
		if path != "" {
			w.SetURL(path)
		}
		w.Show()
		w.UnMinimise()
		w.Focus()
	})
}

// hideWindow leaves the app in the tray alone. It can wait, so it must not
// run on the main thread.
func (a *App) hideWindow() {
	a.showMu.Lock()
	defer a.showMu.Unlock()
	w := a.win()
	if w == nil {
		return
	}
	if runtime.GOOS != "darwin" {
		application.InvokeSync(func() { w.Hide() })
		return
	}
	a.winMu.Lock()
	a.window = nil
	a.winMu.Unlock()
	application.InvokeSync(func() { w.Hide() })
	w.Close()
	// Leaving the Dock right after coming in can leave a second icon there,
	// so a window closed at once waits out a second.
	if wait := time.Until(a.dockedAt.Add(time.Second)); wait > 0 {
		time.Sleep(wait)
	}
	application.InvokeSync(func() { showInDock(false) })
}

// waitOutOfDock waits for Wails to take the app out of the Dock as it
// starts, which it does from another goroutine: a window brought in before
// that would be left without the Dock.
func (a *App) waitOutOfDock() {
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var out bool
		application.InvokeSync(func() { out = menuBarOnly() })
		if out {
			return
		}
	}
	a.logger.Warn("the app did not leave the Dock as it started")
}

func (a *App) quit() {
	a.quitting.Store(true)
	a.wails.Quit()
}

// shutdown stops the gateway with care, whatever ends the app: Encerrar,
// logging out, shutting the computer down.
func (a *App) shutdown() {
	a.quitting.Store(true)
	a.dmu.Lock()
	d, cancel := a.daemon, a.follow
	a.daemon = nil
	a.dmu.Unlock()
	if cancel != nil {
		cancel()
	}
	if d != nil {
		a.logger.Info("stopping the gateway")
		d.Stop()
	}
	a.logger.Info("stopped")
}

// notify shows a system notification. It is best effort: the person may
// have turned them off, and a development build has no bundle to send from.
func (a *App) notify(id, title, body string) {
	if a.notifier == nil {
		return
	}
	go func() {
		if ok, err := a.notifier.CheckNotificationAuthorization(); err == nil && !ok {
			if ok, err = a.notifier.RequestNotificationAuthorization(); err != nil || !ok {
				return
			}
		}
		if err := a.notifier.SendNotification(notifications.NotificationOptions{ID: id + "-" + fmt.Sprint(time.Now().Unix()), Title: title, Body: body}); err != nil {
			a.logger.Debug("notification not sent", "error", err)
		}
	}()
}

// stillRunning says where the app went when its window closed, in the words
// of each system.
func stillRunning() string {
	switch runtime.GOOS {
	case "darwin":
		return i18n.T("Ele fica na barra de menus, e as ferramentas de IA continuam usando o Telegram. Para desligar de vez, use Encerrar no ícone dele.")
	case "windows":
		return i18n.T("Ele fica na área de notificação, e as ferramentas de IA continuam usando o Telegram. Para desligar de vez, use Encerrar no ícone dele.")
	}
	return i18n.T("Ele fica na bandeja do sistema, e as ferramentas de IA continuam usando o Telegram. Para desligar de vez, use Encerrar no ícone dele.")
}

// swapHandler is what the window loads: the panel once the gateway is up,
// or a page saying why it could not start.
type swapHandler struct {
	h atomic.Value
}

func (s *swapHandler) set(h http.Handler) { s.h.Store(&h) }

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h, ok := s.h.Load().(*http.Handler); ok {
		(*h).ServeHTTP(w, r)
		return
	}
	// The window may navigate before asynchronous vault/daemon preparation
	// mounts its handler. Replacing that handler cannot update a document that
	// WebKit has already loaded, so retry the same URL until it is mounted.
	// A refresh without a URL also preserves deep links and query parameters.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprintf(w, `<!doctype html><html lang="%s"><head><meta charset="utf-8">
<meta http-equiv="refresh" content="1"><title>Telegram MCP</title></head>
<body style="font:16px/1.6 -apple-system,system-ui,sans-serif;max-width:560px;margin:80px auto;padding:0 24px;color:#121733">
<h1>Telegram MCP</h1><p role="status">%s</p></body></html>`, htmlEscape(i18n.Lang()), htmlEscape(i18n.T("Carregando…")))
}

// startFailure is the window's page when the gateway cannot start.
func startFailure(err error, logPath string) http.Handler {
	msg := err.Error()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html lang="%s"><meta charset="utf-8"><title>Telegram MCP</title>
<body style="font:16px/1.6 -apple-system,system-ui,sans-serif;max-width:560px;margin:80px auto;padding:0 24px;color:#121733">
<h1 style="font-size:1.4rem">%s</h1><p>%s</p>
<p style="color:#545c7e">%s</p></body></html>`, htmlEscape(i18n.Lang()), htmlEscape(i18n.T("O Telegram MCP não conseguiu iniciar")), htmlEscape(msg),
			i18n.Tf("Detalhes no log: <code>%s</code>", htmlEscape(logPath)))
	})
}
