// Package panel is the local control page: log in to Telegram by QR code or
// by phone number (with the 2FA password), check the connection and the
// history, and connect an AI tool, step by step. The panel's shell and flow
// come from Bruno Orlandi's WhatsApp MCP Local panel (MIT), credited in the
// footer and in THIRD_PARTY_NOTICES.md.
//
// Its actions run commands and edit client configuration on this machine, so
// every state-changing request must come from the page itself: same-origin,
// never from another site or another localhost app open in the browser.
package panel

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/bwoliveira/telegram-mcp-local/internal/account"
	"github.com/bwoliveira/telegram-mcp-local/internal/brand"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
	"github.com/bwoliveira/telegram-mcp-local/internal/localasr"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

//go:embed assets
var assets embed.FS

type Panel struct {
	Server *mcp.Server
	Engine engine.Controller
	// ReadStore supplies local coverage only; no Telegram refresh on page reads.
	ReadStore mcp.ReadStore
	AccountID engine.AccountID
	// Index is retained for caller compatibility only. Telegram handlers must
	// not read the inherited upstream schema; they await store integration.
	Index *index.Index
	State *state.State
	Token string
	// Endpoint is the MCP's address now: it changes when the port does.
	Endpoint func() string
	// Desktop is what Claude Desktop runs to reach the MCP: the command
	// line's bridge subcommand, or the app's bridge program.
	Desktop DesktopCommand
	// LogPath is where the daemon writes its log, shown when sync stops.
	LogPath string
	// MCPProblem says why the MCP is not answering, or nil when it is.
	MCPProblem func() *PortProblem
	// Host is the desktop app around the panel; nil on the command line.
	Host Host
	// DataDir is the data folder, whose config.json keeps the language
	// picked on the command line, where there is no Host to keep it.
	DataDir string

	loginAttempts loginAttempts
	qrw           qrWatch
	pages         *template.Template
	code          codeCache
	codex         codeCache
	modeMu        sync.Mutex
	cache         panelCache
}

// PortProblem is the MCP's port taken by another program: Telegram keeps
// running, and the MCP waits for a free port.
type PortProblem struct {
	Port int `json:"port"`
	// OtherGateway is set when what holds the port is another Telegram MCP,
	// such as the command-line service.
	OtherGateway bool `json:"other_gateway"`
	// Suggest is a free port, already tried, or 0 when none was found.
	Suggest int `json:"suggest,omitempty"`
}

// DesktopCommand is the stdio server entry written into Claude Desktop.
type DesktopCommand struct {
	Command string
	Args    []string
	Env     map[string]string
}

func (p *Panel) endpoint() string { return p.Endpoint() }

type internalKey struct{}

// Internal marks every request as coming from the app's own window, which
// reaches the panel in memory rather than over the network: no other page
// can send a request down that path, and the window's webview sends neither
// Sec-Fetch-Site nor, on a fetch, Origin.
//
// The window's webview does not follow an HTTP redirect from the app, so a
// redirect is answered with a page that navigates there itself.
func Internal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &redirectWriter{ResponseWriter: w}
		next.ServeHTTP(rw, r.WithContext(context.WithValue(r.Context(), internalKey{}, true)))
	})
}

type redirectWriter struct {
	http.ResponseWriter
	redirected bool
}

func (w *redirectWriter) WriteHeader(code int) {
	to := w.Header().Get("Location")
	if code < 300 || code >= 400 || to == "" {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.redirected = true
	h := w.Header()
	h.Del("Location")
	h.Del("Content-Length")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(http.StatusOK)
	to = template.HTMLEscapeString(to)
	_, _ = io.WriteString(w.ResponseWriter, `<!doctype html><meta http-equiv="refresh" content="0;url=`+to+`"><a href="`+to+`">…</a>`)
}

func (w *redirectWriter) Write(b []byte) (int, error) {
	if w.redirected {
		return len(b), nil // the redirect's own body
	}
	return w.ResponseWriter.Write(b)
}

// IsInternal recognizes only the desktop context, never a request header.
func IsInternal(r *http.Request) bool { return isInternal(r) }

func isInternal(r *http.Request) bool {
	v, _ := r.Context().Value(internalKey{}).(bool)
	return v
}

const (
	setupSetting     = "setup_step" // "done" once the first run is over
	recentChats      = 10
	conversationSize = 10
)

func (p *Panel) Register(mux *http.ServeMux) {
	p.pages = parseTemplates()

	mux.HandleFunc("GET /{$}", p.page(p.conectar))
	mux.HandleFunc("GET /instalacao", p.page(p.instalacao))
	mux.HandleFunc("POST /instalacao/avancar", p.form(p.advance))
	mux.HandleFunc("GET /conectar", p.page(p.connectChoose))
	mux.HandleFunc("GET /conectar/{tool}", p.page(p.connectTool))
	mux.HandleFunc("GET /conta", p.page(p.conta))
	mux.HandleFunc("POST /conta/sair", p.form(p.logout))
	mux.HandleFunc("POST /conta/nome", p.form(p.nameNumber))
	mux.HandleFunc("POST /conta/desconectar", p.form(p.pause))
	mux.HandleFunc("POST /conta/reconectar", p.form(p.resume))
	mux.HandleFunc("POST /api/mode", p.apiMode)
	mux.HandleFunc("GET /status", p.page(p.status))
	mux.HandleFunc("GET /api/asr", p.api(p.apiASR))
	mux.HandleFunc("POST /api/asr/install", p.api(p.apiASRInstall))
	mux.HandleFunc("GET /funcoes", p.page(p.funcoes))
	mux.HandleFunc("GET /receitas", p.page(p.receitas))
	mux.HandleFunc("GET /ajuda", p.page(p.ajuda))
	mux.HandleFunc("GET /configuracoes", p.page(p.configuracoes))
	// The pages' former addresses, still in links and in older answers of
	// the MCP: transcription is a section of Configurações now.
	for from, to := range map[string]string{"/estado": "/status", "/documentacao": "/funcoes", "/transcricao": "/configuracoes#transcricao"} {
		mux.Handle("GET "+from, http.RedirectHandler(to, http.StatusFound))
	}
	mux.HandleFunc("POST /conexoes/remover", p.form(p.removeConnection))
	mux.HandleFunc("POST /conexoes/renomear", p.form(p.renameConnection))

	mux.HandleFunc("GET /api/status", p.api(p.apiStatus))
	mux.HandleFunc("GET /api/state", p.api(p.apiState))
	mux.HandleFunc("GET /api/pulse", p.api(p.apiPulse))
	mux.HandleFunc("GET /api/panel/status", p.api(p.apiPanelStatus))
	mux.HandleFunc("GET /api/panel/health", p.api(p.apiPanelHealth))
	mux.HandleFunc("GET /api/panel/clients", p.api(p.apiPanelClients))
	mux.HandleFunc("GET /api/chats", p.api(p.apiChats))
	mux.HandleFunc("GET /api/chats/{jid}", p.api(p.apiConversation))
	mux.HandleFunc("GET /api/search", p.api(p.apiSearch))
	mux.HandleFunc("POST /api/history", p.api(p.apiHistory))
	mux.HandleFunc("GET /api/pair/qr.png", p.qr)
	mux.HandleFunc("POST /api/pair", p.api(p.apiPair))
	mux.HandleFunc("POST /api/pair/refresh", p.api(p.apiRefreshQR))
	mux.HandleFunc("POST /api/pair/cancel", p.api(p.apiCancelPair))
	mux.HandleFunc("POST /api/pair/code", p.api(p.apiCode))
	mux.HandleFunc("POST /api/pair/password", p.api(p.apiPassword))
	mux.HandleFunc("POST /api/pair/resend", p.api(p.apiResendCode))
	mux.HandleFunc("POST /api/account/connect", p.api(p.apiConnect))
	mux.HandleFunc("POST /api/account/disconnect", p.api(p.apiDisconnect))
	mux.HandleFunc("POST /api/account/logout", p.api(p.apiLogout))
	mux.HandleFunc("POST /api/clients/{client}", p.api(p.apiAddClient))
	mux.HandleFunc("POST /api/idioma", p.api(p.apiLanguage))
	if p.Host != nil {
		p.registerApp(mux)
	}
	registerAssets(mux)
}

// registerAssets serves the scripts and icons every page loads.
func registerAssets(mux *http.ServeMux) {
	sub, _ := fs.Sub(assets, "assets")
	mux.HandleFunc("GET /assets/i18n.js", translations)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(sub))))
	icon := func(ctype string, body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ctype)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(body)
		}
	}
	// The panel's own mark: never the Telegram logo (see brandmark.go).
	mux.HandleFunc("GET /favicon.svg", icon("image/svg+xml", []byte(markSVG)))
	// Rendered from build/icons/tile.svg, the same mark.
	ico, _ := assets.ReadFile("assets/favicon.ico")
	touch, _ := assets.ReadFile("assets/apple-touch-icon.png")
	mux.HandleFunc("GET /favicon.ico", icon("image/x-icon", ico))
	mux.HandleFunc("GET /apple-touch-icon.png", icon("image/png", touch))
}

func parseTemplates() *template.Template {
	return template.Must(template.New("panel").Funcs(funcs()).Parse(pageSource))
}

// ---- plumbing ----

// sameOrigin reports whether a request was made by this page. Browsers mark
// every fetch and form post with Sec-Fetch-Site and send Origin on POST; a
// request from any other site, or from another app on another localhost port,
// fails one of the two. A POST carrying neither is not from a browser page of
// ours, so it is refused too.
func sameOrigin(r *http.Request) bool {
	if isInternal(r) {
		return true
	}
	site := r.Header.Get("Sec-Fetch-Site")
	origin := r.Header.Get("Origin")
	if site != "" && site != "same-origin" && !(site == "none" && r.Method == http.MethodGet) {
		return false
	}
	if origin != "" && origin != "http://"+r.Host {
		return false
	}
	if r.Method == http.MethodPost && site == "" && origin == "" {
		return false
	}
	return true
}

var errNotImplemented = errors.New("not implemented yet")

type userError struct{ msg string }

func (e userError) Error() string { return e.msg }

func (p *Panel) page(f func(http.ResponseWriter, *http.Request) (string, any)) http.HandlerFunc {
	// A page is a plain navigation, which may come from anywhere (the
	// installer opening the browser, a link in the README): another origin can
	// neither read nor frame it, so only the requests that act are guarded.
	return func(w http.ResponseWriter, r *http.Request) {
		name, data := f(w, r)
		if name == "" {
			return // f answered already, with a redirect
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		// Render first, so a template error is a clean 500 rather than half a
		// page followed by an error.
		var body bytes.Buffer
		if err := p.pages.ExecuteTemplate(&body, name, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body.Bytes())
	}
}

// form handles a plain HTML form post and redirects to where f says.
func (p *Panel) form(f func(*http.Request) (string, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		to, err := f(r)
		if err != nil {
			sep := "?"
			if strings.Contains(to, "?") {
				sep = "&"
			}
			to += sep + "erro=" + url.QueryEscape(err.Error())
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
	}
}

func (p *Panel) api(f func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !sameOrigin(r) && !p.fromAgent(r) {
			http.Error(w, "this API answers its own page and programs on this computer", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		v, err := f(r)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errNotImplemented) {
				status = http.StatusNotImplemented
			}
			if errors.Is(err, errLoginRateLimited) {
				status = http.StatusTooManyRequests
			}
			if errors.Is(err, engine.ErrInvalid) {
				status = http.StatusBadRequest
			}
			if errors.Is(err, engine.ErrNoLogin) || errors.Is(err, engine.ErrAccountMismatch) || errors.Is(err, engine.ErrLoginRunning) {
				status = http.StatusConflict
			}
			var ue userError
			if errors.As(err, &ue) {
				status = http.StatusConflict
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	}
}

// ---- shared page data ----

type layout struct {
	Title      string
	Active     string
	HealthTone string
	// LiveKey is the connection the page was made with: the page follows it
	// in the background, more closely while LiveBusy. Empty, it does not.
	LiveKey  string
	LiveBusy bool
	Error    string
	OK       string
	// App is set inside the desktop app, whose window has no address bar and
	// opens links in the browser.
	App bool
	// Language is the language the person picked, empty when the panel
	// follows the system's.
	Language   string
	MCPProblem *PortProblem
	Settings   HostSettings
	Update     UpdateState
}

// snapshot is what most pages need about the account, gathered once.
type snapshot struct {
	Account engine.Account
	Name    string
	Phone   string
	// Label is the name the person gave this number, such as Trabalho.
	Label     string
	Sync      engine.Status
	SyncTone  string
	SyncLabel string
	// SyncBusy is a state that passes on its own (loading, reconnecting, a
	// pause of seconds): the page shows a spinner and refreshes itself.
	SyncBusy bool
	Health   mcp.Health
	// Tone is the health the panel shows: a connection still settling is no
	// cause for alarm.
	Tone     string
	Checks   []check
	Activity index.Activity
	Coverage index.Coverage
	Gaps     []index.Gap
	Pairing  engine.Login
	// QRSeq changes every time a new QR code is to be drawn.
	QRSeq    int
	ReadOnly bool
	History  *mcp.HistoryJob
	// Arriving says the phone is still sending the history it sends after
	// pairing, with how many messages have come in so far.
	Arriving      bool
	ArrivingCount int64
	Endpoint      string
	LogPath       string
	InApp         bool
	SnapshotReady bool
	SnapshotAt    time.Time
}

type check struct {
	Name   string
	Status string
	Title  string
	Text   string
}

func (p *Panel) snapshot(ctx context.Context) snapshot {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	s := snapshot{Endpoint: p.endpoint(), LogPath: p.LogPath, InApp: p.Host != nil, History: p.Server.HistoryStatus()}
	s.Pairing = p.Engine.Login()
	s.Account, _ = p.Engine.Account(ctx)
	p.observeAccount(s.Account)
	s.Label, _ = p.State.Setting(ctx, account.LabelSetting)
	s.Sync = p.lightStatus()
	s.ReadOnly = s.Sync.Mode != engine.ModeReadWrite
	if h, at, fresh := p.cache.health.peek(); fresh {
		s.Health, s.Activity = h.Health, h.Health.Activity
		s.Sync.History = h.Sync.History
		s.SnapshotReady, s.SnapshotAt = true, at
	}
	// Legacy index reads are not compatible with the Telegram store yet.
	if s.Account.Authenticated {
		s.Name = strings.TrimSpace(s.Account.FirstName + " " + s.Account.LastName)
		if s.Name == "" {
			s.Name = s.Account.Username
		}
		if s.Account.Phone != "" {
			s.Phone = account.FormatPhone(s.Account.Phone)
		}
	}
	s.SyncTone, s.SyncLabel = syncLabel(s.Sync)
	s.SyncBusy = settling(s.Sync) || s.Sync.State == engine.StateReconnecting
	if s.Pairing.State == "syncing" || (!s.Sync.History.LastAt.IsZero() && time.Since(s.Sync.History.LastAt) < 90*time.Second) {
		s.Arriving = true
		s.ArrivingCount = s.Pairing.Synced + s.Sync.History.Messages
	}
	s.readChecks()
	if !s.SnapshotReady {
		s.Tone = ""
	}
	return s
}

// readChecks says the health checks for the page, and how worried to be.
func (s *snapshot) readChecks() {
	s.Tone = tone(s.Health.Checks, s.Sync)
	for _, c := range s.Health.Checks {
		title, text := describeCheck(c, *s)
		status := c.Status
		switch {
		case quietCheck(c, s.Sync):
			status = "busy"
		case informationalCheck(c):
			status = "info"
		}
		s.Checks = append(s.Checks, check{Name: c.Name, Status: status, Title: title, Text: text})
	}
}

// settling is a transient connection state. A manually paused engine stays
// paused until Connect; a connected engine has no delegated sending process.
func settling(s engine.Status) bool {
	switch s.State {
	case engine.StateStarting, engine.StateConnecting, engine.StateResyncing:
		return true
	}
	return false
}

// quietCheck is the connection's warning while it settles, which the panel
// shows as on its way rather than as a problem: the connection itself, and
// the updates being replayed while it resynchronizes.
func quietCheck(c mcp.Check, sync engine.Status) bool {
	return (c.Name == "sync" || c.Name == "updates") && c.Status == "warn" && settling(sync)
}

// informationalCheck is history coverage. Partial history (backfill limits)
// and gaps that replay can never fill (conversations that became
// inaccessible, ranges Telegram truncated) are facts to show, not a sync or
// configuration problem: they never raise the Status tab's warning mark.
// Recoverable gaps and a held checkpoint keep warning through "updates".
func informationalCheck(c mcp.Check) bool {
	return c.Status == "info"
}

// tone is the worst of the health checks, leaving out a settling connection.
func tone(checks []mcp.Check, sync engine.Status) string {
	t := "ok"
	for _, c := range checks {
		if quietCheck(c, sync) || informationalCheck(c) {
			continue
		}
		switch c.Status {
		case "fail":
			return "fail"
		case "warn":
			t = "warn"
		}
	}
	return t
}

// syncKey changes whenever the connection a page shows does.
func syncKey(s engine.Status) string {
	return string(s.State) + "/" + s.PausedFor
}

func (p *Panel) layout(r *http.Request, title, active string, s snapshot) layout {
	l := layout{Title: title, Active: active, HealthTone: s.Tone, Error: r.URL.Query().Get("erro"), OK: r.URL.Query().Get("ok"), Language: p.chosenLanguage()}
	if s.Sync.State != "" {
		l.LiveKey, l.LiveBusy = syncKey(s.Sync), settling(s.Sync) || s.Sync.State == "reconnecting"
	}
	if p.MCPProblem != nil {
		l.MCPProblem = p.MCPProblem()
	}
	if p.Host != nil {
		l.App = true
		l.Settings = p.Host.Settings()
		l.Update = p.Host.Update()
	}
	return l
}

func syncLabel(s engine.Status) (tone, label string) {
	switch s.State {
	case "connected":
		return "ok", i18n.N("Conectado")
	case engine.StatePaused:
		return "warn", i18n.N("Desconectado")
	case engine.StateConnecting:
		return "warn", i18n.N("Conectando")
	case engine.StateResyncing:
		return "warn", i18n.N("Sincronizando")
	case "starting":
		return "warn", i18n.N("Carregando")
	case "reconnecting":
		return "warn", i18n.N("Reconectando")
	case engine.StateNotAuthorized:
		return "off", i18n.N("Não conectado")
	case engine.StateLoggedOut:
		return "off", i18n.N("Sem sessão")
	case engine.StateRevoked:
		return "off", i18n.N("Sessão encerrada")
	case engine.StateError:
		return "fail", i18n.N("Erro")
	}
	return "off", i18n.N("Parado")
}

// describeCheck says each health check in the person's language, from the
// report's own data, so the panel does not show the model-facing English.
func describeCheck(c mcp.Check, s snapshot) (string, string) {
	title, text := describeCheckPT(c, s)
	return i18n.T(title), text
}

// describeCheckPT is the check's title, still the Portuguese key, and its text
// already in the person's language.
func describeCheckPT(c mcp.Check, s snapshot) (string, string) {
	switch c.Name {
	case "daemon":
		return i18n.N("Telegram MCP"), i18n.T("Funcionando neste computador.")
	case "engine":
		return i18n.N("Motor"), i18n.T("Não foi possível ler o diagnóstico do motor.")
	case "paired":
		switch {
		case c.Status == "ok":
			return i18n.N("Sessão do Telegram"), i18n.T("Este computador tem uma sessão autorizada da sua conta.")
		case s.Sync.State == engine.StateRevoked:
			return i18n.N("Sessão do Telegram"), i18n.T("A sessão foi encerrada fora deste app, por exemplo em Configurações → Dispositivos. Entre de novo.")
		case s.Sync.State == engine.StatePaused:
			return i18n.N("Sessão do Telegram"), i18n.T("Desconectado por você. A sessão continua guardada; reconecte na aba Conta.")
		}
		return i18n.N("Sessão do Telegram"), i18n.T("Nenhuma conta conectada. Entre pelo QR code ou pelo número de telefone.")
	case "sync":
		switch s.Sync.State {
		case engine.StateConnected:
			return i18n.N("Conexão"), i18n.T("Conectado ao Telegram e recebendo atualizações em tempo real.")
		case engine.StatePaused:
			return i18n.N("Conexão"), i18n.T("Desconectado por você. Reconecte na aba Conta para continuar.")
		case engine.StateConnecting:
			return i18n.N("Conexão"), i18n.T("Conectando.")
		case engine.StateResyncing:
			return i18n.N("Conexão"), i18n.T("Recuperando as atualizações que chegaram enquanto estava fora.")
		case engine.StateStarting:
			return i18n.N("Conexão"), i18n.T("Carregando.")
		case engine.StateReconnecting:
			return i18n.N("Conexão"), i18n.T("Reconectando ao Telegram. Confira a internet deste computador.")
		case engine.StateNotAuthorized:
			return i18n.N("Conexão"), i18n.T("Aguardando o login no Telegram.")
		case engine.StateLoggedOut, engine.StateRevoked:
			return i18n.N("Conexão"), i18n.T("Sem sessão do Telegram. Entre de novo pela Instalação.")
		}
		var text string
		switch {
		case s.Sync.LastError != "" && s.LogPath != "":
			text = i18n.Tf("Parada: %s. Veja o log em %s.", s.Sync.LastError, s.LogPath)
		case s.Sync.LastError != "":
			text = i18n.Tf("Parada: %s.", s.Sync.LastError)
		case s.LogPath != "":
			text = i18n.Tf("Parada. Veja o log em %s.", s.LogPath)
		default:
			text = i18n.T("Parada.")
		}
		return i18n.N("Conexão"), text
	case "updates":
		return i18n.N("Atualizações"), describeUpdates(s.Sync)
	case "receiving":
		return i18n.N("Recebendo mensagens"), i18n.T("Ainda não medido: depende do banco do Telegram, que chega em outra frente desta etapa.")
	case "coverage":
		_, text := coverageText(s.Sync, nil)
		return i18n.N("Cobertura"), text
	case "history_request":
		return i18n.N("Pedido de histórico"), i18n.T("O último pedido de histórico falhou em algumas conversas. Confira a conexão e tente de novo mais tarde.")
	}
	return c.Name, c.Detail
}

func pauseReason(r string) string {
	if strings.HasPrefix(r, "history backfill") {
		return i18n.T("buscar histórico antigo no celular")
	}
	if v, ok := pauseReasons[r]; ok {
		return i18n.T(v)
	}
	return i18n.T("uma operação")
}

// pairingError says a login error from the engine's state in the person's
// language when it names a known Telegram error type.
func pairingError(err string) string {
	for typ, pt := range loginErrors {
		if strings.Contains(err, typ) {
			return i18n.T(pt)
		}
	}
	return err
}

// pauseReasons describes historical operation names, completing "Pausada por alguns
// segundos para …".
var pauseReasons = map[string]string{"revoke message": i18n.N("apagar uma mensagem"), "check numbers": i18n.N("verificar números"), "profile picture": i18n.N("buscar uma foto de perfil"),
	"group info": i18n.N("atualizar um grupo"), "logout": i18n.N("desconectar"), "pairing": i18n.N("entrar no Telegram"),
	"archive chat": i18n.N("arquivar uma conversa"), "unarchive chat": i18n.N("desarquivar uma conversa"), "pin chat": i18n.N("fixar uma conversa"),
	"unpin chat": i18n.N("desafixar uma conversa"), "mute chat": i18n.N("silenciar uma conversa"), "unmute chat": i18n.N("reativar uma conversa")}

// ---- the setup flow ----

type wizardStep struct {
	Number int
	Label  string
	State  string // done, now, todo
}

// setupStep is where the first run stands: 1 until Telegram is logged in, 2
// while a client is connected, 0 once it is over. The history keeps arriving
// in the background meanwhile; it does not need a step of its own.
func (p *Panel) setupStep(ctx context.Context, s snapshot) int {
	if !s.Account.Authenticated {
		return 1
	}
	if v, _ := p.State.Setting(ctx, setupSetting); v == "done" {
		return 0
	}
	return 2
}

func steps(now int) []wizardStep {
	labels := []string{i18n.N("Telegram"), i18n.N("Ferramenta de IA")}
	out := make([]wizardStep, len(labels))
	for i, l := range labels {
		st := "todo"
		switch {
		case i+1 < now:
			st = "done"
		case i+1 == now:
			st = "now"
		}
		out[i] = wizardStep{Number: i + 1, Label: l, State: st}
	}
	return out
}

func (p *Panel) instalacao(w http.ResponseWriter, r *http.Request) (string, any) {
	s := p.snapshot(r.Context())
	step := p.setupStep(r.Context(), s)
	if step == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return "", nil
	}
	set := p.cheapSetup(r.Context())
	conns := p.connections(r.Context(), set)
	live := 0
	for _, c := range conns {
		if c.Live {
			live++
		}
	}
	l := p.layout(r, i18n.N("Instalação"), "", s)
	// Setting up is not a problem to be alarmed about, and the wizard follows
	// the pairing on its own.
	l.HealthTone, l.LiveKey = "", ""
	return "instalacao", struct {
		layout
		snapshot
		Step         int
		Steps        []wizardStep
		Setup        setup
		Connections  []Connection
		LiveCount    int
		Verification string
		Tools        []toolChoice
	}{l, s, step, steps(step), set, conns, live, verificationPrompt, toolChoices(conns)}
}

func (p *Panel) advance(r *http.Request) (string, error) {
	if r.FormValue("to") == "done" {
		return "/", p.State.SetSetting(r.Context(), setupSetting, "done")
	}
	return "/instalacao", nil
}

// ---- pages ----

func (p *Panel) conectar(w http.ResponseWriter, r *http.Request) (string, any) {
	s := p.snapshot(r.Context())
	if p.setupStep(r.Context(), s) != 0 {
		http.Redirect(w, r, "/instalacao", http.StatusSeeOther)
		return "", nil
	}
	set := p.cheapSetup(r.Context())
	conns := p.connections(r.Context(), set)
	live := 0
	var last time.Time
	for _, c := range conns {
		if c.Live {
			live++
			if c.LastUsed.After(last) {
				last = c.LastUsed
			}
		}
	}
	return "conectar", struct {
		layout
		snapshot
		Setup       setup
		Connections []Connection
		LiveCount   int
		LastUse     time.Time
		Prompts     []string
		Newcomer    bool
	}{p.layout(r, i18n.N("Conectar"), "conectar", s), s, set, conns, live, last, suggestedPrompts, p.newcomer(r.Context(), time.Now())}
}

// newcomerDays is how long the first page keeps its examples, counted from
// the first time an AI tool used the MCP.
const newcomerDays = 3

func (p *Panel) newcomer(ctx context.Context, now time.Time) bool {
	seen, err := p.State.Clients(ctx)
	if err != nil {
		return false
	}
	var first time.Time
	for _, c := range seen {
		if first.IsZero() || c.FirstSeen.Before(first) {
			first = c.FirstSeen
		}
	}
	return !first.IsZero() && now.Sub(first) < newcomerDays*24*time.Hour
}

func (p *Panel) ajuda(w http.ResponseWriter, r *http.Request) (string, any) {
	s := p.shellSnapshot()
	return "ajuda", struct {
		layout
		Prompts []string
	}{p.layout(r, i18n.N("Ajuda"), "ajuda", s), suggestedPrompts}
}

// guide is an AI tool the connect flow walks through.
type guide struct {
	Key  string // its page, and its row in Suas conexões
	Name string
	Hint string
	Mark string // its logo, or an icon for any other tool
	// Client is how the tool's name starts when it connects; empty for any
	// other tool.
	Client    string
	Title     string // the page's heading
	Who       string // the tool in a sentence: "o Codex"
	Connected string // the heading once it is: "Codex conectado"
}

var guides = []guide{
	{"claude-desktop", i18n.N("Claude Desktop"), i18n.N("Chat e Cowork, no app do Claude para computador."), "claude", "claude-ai",
		i18n.N("Conectar o Claude Desktop"), i18n.N("o Claude Desktop"), i18n.N("Claude Desktop conectado")},
	{"claude-code", i18n.N("Claude Code"), i18n.N("No terminal ou no editor de código."), "claude-code", "claude-code",
		i18n.N("Conectar o Claude Code"), i18n.N("o Claude Code"), i18n.N("Claude Code conectado")},
	{"codex", i18n.N("ChatGPT / Codex"), i18n.N("O Codex no app do ChatGPT, no app do Codex, no terminal ou no editor."), "openai", codexClient,
		i18n.N("Conectar o ChatGPT / Codex"), i18n.N("o Codex"), i18n.N("Codex conectado")},
	{"cursor", i18n.N("Cursor"), i18n.N("O editor de código com IA."), "cursor", cursorClient,
		i18n.N("Conectar o Cursor"), i18n.N("o Cursor"), i18n.N("Cursor conectado")},
	{"hermes", i18n.N("Hermes Agent"), i18n.N("O agente da Nous Research, no terminal ou nos apps de mensagem."), "code", "",
		i18n.N("Conectar o Hermes Agent"), i18n.N("o Hermes Agent"), i18n.N("Hermes Agent conectado")},
	{"outra", i18n.N("Outra ferramenta"), i18n.N("Windsurf e outras que aceitem MCP."), "grid", "",
		i18n.N("Conectar outra ferramenta"), i18n.N("a ferramenta"), i18n.N("Ferramenta conectada")},
}

// toolChoice is a guide with where its tool stands: live, configured or "".
type toolChoice struct {
	guide
	State string
}

func toolChoices(conns []Connection) []toolChoice {
	out := make([]toolChoice, len(guides))
	for i, g := range guides {
		out[i] = toolChoice{guide: g, State: toolState(g, conns)}
	}
	return out
}

func toolState(g guide, conns []Connection) string {
	st := ""
	for _, c := range conns {
		other := g.Key == "outra" && strings.HasPrefix(c.Key, "client:") && !strings.HasPrefix(c.Key, "client:hermes")
		hermes := g.Key == "hermes" && strings.HasPrefix(c.Key, "client:hermes")
		if c.Key != g.Key && !other && !hermes {
			continue
		}
		if c.Live {
			return "live"
		}
		if c.Configured {
			st = "configured"
		}
	}
	return st
}

// connectChoose is the start of the connect flow: which tool.
func (p *Panel) connectChoose(w http.ResponseWriter, r *http.Request) (string, any) {
	s := p.snapshot(r.Context())
	step := p.setupStep(r.Context(), s)
	if step == 1 {
		http.Redirect(w, r, "/instalacao", http.StatusSeeOther)
		return "", nil
	}
	conns := p.connections(r.Context(), p.cheapSetup(r.Context()))
	l := p.layout(r, i18n.N("Conectar ferramenta de IA"), "conectar", s)
	l.LiveKey = ""
	return "conectarescolha", struct {
		layout
		Tools   []toolChoice
		InSetup bool
	}{l, toolChoices(conns), step == 2}
}

// connectTool is the steps for one tool, which end when it connects.
func (p *Panel) connectTool(w http.ResponseWriter, r *http.Request) (string, any) {
	var g guide
	for _, c := range guides {
		if c.Key == r.PathValue("tool") {
			g = c
		}
	}
	s := p.snapshot(r.Context())
	step := p.setupStep(r.Context(), s)
	if g.Key == "" || step == 1 {
		to := "/conectar"
		if step == 1 {
			to = "/instalacao"
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return "", nil
	}
	set := p.cheapSetup(r.Context())
	conns := p.connections(r.Context(), set)
	// Any other tool is one that was not here when the page was made.
	var known []string
	if seen, err := p.State.Clients(r.Context()); err == nil {
		for _, c := range seen {
			known = append(known, c.Name)
		}
	}
	l := p.layout(r, g.Title, "conectar", s)
	l.LiveKey = ""
	live := toolState(g, conns) == "live"
	return "conectarferramenta", struct {
		layout
		Tool  guide
		Setup setup
		Live  bool
		// Done is the page coming back once the tool connected: only the
		// good news then, and the way back.
		Done         bool
		InSetup      bool
		Known        string
		Since        int64
		Verification string
	}{l, g, set, live, live && r.URL.Query().Get("conectado") == "1", step == 2, strings.Join(known, " "), time.Now().Unix(), verificationPrompt}
}

// conta is the Telegram account this computer is logged into: who, how it is
// connected, and the actions on the session (P23, P24, P29).
func (p *Panel) conta(w http.ResponseWriter, r *http.Request) (string, any) {
	s := p.snapshot(r.Context())
	if !s.Account.Authenticated {
		http.Redirect(w, r, "/instalacao", http.StatusSeeOther)
		return "", nil
	}
	return "conta", struct {
		layout
		snapshot
		Paused bool
	}{p.layout(r, i18n.N("Conta"), "conta", s), s, s.Sync.State == engine.StatePaused}
}

// statusView is what the Status tab shows beyond the snapshot: the engine's
// own diagnostics and an honest word on how complete the history is.
type statusView struct {
	Diagnostics          engine.Diagnostics
	DiagnosticsErr       bool
	CoverageText         string
	CoverageTone         string
	Recent               []engine.LifecycleEvent
	FloodWait            *time.Time
	ConversationCoverage []conversationCoverage
	CoverageUnavailable  bool
}

func (p *Panel) status(w http.ResponseWriter, r *http.Request) (string, any) {
	s := p.snapshot(r.Context())
	v, _, ready := p.cache.details.peek()
	v.CoverageTone, v.CoverageText = statusCoverageText(s.Sync, v, s.History)
	return "status", statusPage{p.layout(r, i18n.N("Status"), "status", s), s, v, ready && s.SnapshotReady}
}

type statusPage struct {
	layout
	snapshot
	statusView
	DetailsReady bool
}

// coverageText says how complete the history is, without promising more than
// is measured. It tells three situations apart:
//   - sync: updates are still being replayed (held checkpoint, resync, or
//     recoverable gaps). Until that ends the account is not ready.
//   - info: ready, yet the stored history is partial — conversations fetched
//     only down to the backfill limit, or gaps replay can never fill
//     (inaccessible conversations, ranges Telegram truncated). Information,
//     never a warning, and never a claim of complete history.
//   - ok: nothing known to be missing; the old history's depth is still not
//     a proof of completeness.
//
// statusCoverageText shares the API's conservative diagnostic prerequisites.
// A connected transport alone does not prove a usable authorized session.
func statusCoverageText(st engine.Status, v statusView, job *mcp.HistoryJob) (string, string) {
	if st.LastError != "" || v.DiagnosticsErr || !v.Diagnostics.Authorized || !v.Diagnostics.Connected || v.CoverageUnavailable {
		return "unknown", i18n.T("Não foi possível comprovar o estado das atualizações agora; consulte as verificações abaixo.")
	}
	if job != nil && job.FinishedAt == nil {
		return "sync", i18n.T("Baixando histórico acessível agora; a profundidade e a cobertura ainda estão sendo medidas.")
	}
	st.Updates.Held = st.Updates.Held || v.Diagnostics.LocalUpdates.Held
	st.Updates.OpenGaps = max(st.Updates.OpenGaps, v.Diagnostics.LocalUpdates.OpenGaps)
	return coverageText(st, v.ConversationCoverage)
}

func coverageText(st engine.Status, conversations []conversationCoverage) (string, string) {
	switch {
	case st.State != engine.StateConnected && st.State != engine.StateResyncing:
		return "off", i18n.T("Sem medida enquanto não estiver conectado.")
	case st.Updates.OpenGaps > 0:
		return "sync", i18n.Tf("Sincronizando: %d lacunas recuperáveis nas atualizações estão sendo reprocessadas. Até terminar, a cobertura das atualizações está incompleta.", st.Updates.OpenGaps)
	case st.Updates.Held || st.State == engine.StateResyncing:
		return "sync", i18n.T("Em recuperação: as atualizações de quando o computador estava fora estão sendo reprocessadas.")
	}
	if st.History.Running != nil {
		return "sync", i18n.T("Baixando histórico acessível agora; a profundidade e a cobertura ainda estão sendo medidas.")
	}
	partial := 0
	for _, c := range conversations {
		if !c.Complete {
			partial++
		}
	}
	terminal := max(st.Updates.TerminalGaps, st.Updates.CoverageGaps-st.Updates.OpenGaps)
	if terminal == 0 && partial == 0 {
		return "ok", i18n.T("Sem lacunas conhecidas nas atualizações. A profundidade do histórico antigo ainda não é medida.")
	}
	text := i18n.T("Pronto: as atualizações estão em dia.")
	if terminal > 0 {
		text += " " + i18n.Tf("%d lacunas não podem ser preenchidas pelas atualizações (por exemplo, conversas que ficaram inacessíveis) e ficam só registradas.", terminal)
	}
	if partial > 0 {
		text += " " + i18n.Tf("%d de %d conversas têm só parte do histórico antigo guardado, por exemplo pelo limite de busca.", partial, len(conversations))
	}
	return "info", text + " " + i18n.T("O que está guardado não comprova histórico completo no Telegram.")
}

// describeUpdates is the "updates" check: the checkpoint held or the
// recoverable gaps being replayed. Terminal gaps never appear here.
func describeUpdates(st engine.Status) string {
	if st.Updates.OpenGaps > 0 {
		return i18n.Tf("Sincronizando: %d lacunas recuperáveis nas atualizações estão sendo reprocessadas. Até terminar, a cobertura das atualizações está incompleta.", st.Updates.OpenGaps)
	}
	return i18n.T("Em recuperação: as atualizações de quando o computador estava fora estão sendo reprocessadas.")
}

// gapCause is one line of the terminal gaps' breakdown on the Status tab.
type gapCause struct {
	Cause engine.GapRecoveryCause
	Label string
	Count int
}

// gapCauseOrder lists the causes replay cannot fix, most common first, with
// the Portuguese key of each. Pending replay is counted apart, as open gaps.
var gapCauseOrder = []gapCause{
	{Cause: engine.GapRecoveryAccessLost, Label: i18n.N("registros de perda de acesso")},
	{Cause: engine.GapRecoveryChannelTooLong, Label: i18n.N("canal com intervalo longo demais")},
	{Cause: engine.GapRecoveryDifferenceTooLong, Label: i18n.N("conta com intervalo longo demais")},
	{Cause: engine.GapRecoveryStreamTruncated, Label: i18n.N("depois de um intervalo longo demais")},
	{Cause: engine.GapRecoveryUnknownBaseline, Label: i18n.N("ponto de partida desconhecido")},
}

// gapCauses is the breakdown of the gaps update replay cannot fill.
func gapCauses(u engine.UpdatesStatus) []gapCause {
	var out []gapCause
	for _, c := range gapCauseOrder {
		if n := u.GapCauses[c.Cause]; n > 0 {
			c.Count = n
			out = append(out, c)
		}
	}
	return out
}

// recentEvents is the newest n lifecycle events, newest first.
func recentEvents(in []engine.LifecycleEvent, n int) []engine.LifecycleEvent {
	out := make([]engine.LifecycleEvent, 0, n)
	for i := len(in) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, in[i])
	}
	return out
}

// configuracoes is the settings page. Appearance and audio transcription are
// there everywhere; the rest only in the desktop app.
func (p *Panel) configuracoes(w http.ResponseWriter, r *http.Request) (string, any) {
	s := p.shellSnapshot()
	var local localasr.Status
	if asr := p.Server.ASR(); asr != nil {
		local = asr.Status()
	}
	total, corrected, _ := p.State.TranscriptCount(r.Context())
	l := p.layout(r, i18n.N("Configurações"), "configuracoes", s)
	set := p.cheapSetup(r.Context())
	dataDir := p.DataDir
	if l.App && l.Settings.DataDir != "" {
		dataDir = l.Settings.DataDir
	}
	return "configuracoes", settingsPage{l, set, portOf(p.endpoint()), r.URL.Query().Get("porta") == "1",
		set.Code.Stale || set.Codex.Stale || set.Cursor.Stale, local, total, corrected, dataDir}
}

// settingsPage is what Configurações shows: port, webhooks, transcription,
// retention, language and theme, version, and the ways out.
type settingsPage struct {
	layout
	Setup       setup
	Port        int
	PortChanged bool
	// Stale is a tool still configured with this MCP's address on an older
	// port: the page offers to update it, in the app and on the command line.
	Stale     bool
	Local     localasr.Status
	Total     int
	Corrected int
	DataDir   string
}

func (p *Panel) apiASR(r *http.Request) (any, error) {
	asr := p.Server.ASR()
	if asr == nil {
		return localasr.Status{}, nil
	}
	return map[string]any{"status": asr.Status()}, nil
}

func (p *Panel) apiASRInstall(r *http.Request) (any, error) {
	asr := p.Server.ASR()
	if asr == nil {
		return nil, userError{i18n.T("a transcrição local não está disponível")}
	}
	if err := asr.StartInstall(); err != nil && !errors.Is(err, localasr.ErrInstalling) {
		return nil, err
	}
	return asr.Status(), nil
}

func (p *Panel) funcoes(w http.ResponseWriter, r *http.Request) (string, any) {
	s := p.shellSnapshot()
	tools := functionList(mcp.CatalogueForLanguage(i18n.Lang()))
	writes := 0
	for _, t := range tools {
		if t.Changes {
			writes++
		}
	}
	return "funcoes", struct {
		layout
		Tools    []function
		Count    int
		Writes   int
		ReadOnly bool
	}{p.layout(r, i18n.N("Funções"), "funcoes", s), tools, len(tools), writes, s.ReadOnly}
}

func (p *Panel) receitas(w http.ResponseWriter, r *http.Request) (string, any) {
	s := p.shellSnapshot()
	return "receitas", struct {
		layout
		Recipes []recipe
	}{p.layout(r, i18n.N("Receitas"), "receitas", s), recipeBook()}
}

func (p *Panel) logout(r *http.Request) (string, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := p.Engine.LogOut(ctx); err != nil && p.Engine.Status().State != engine.StateLoggedOut {
		return "/conta", userError{i18n.T(logoutWarning)}
	}
	_ = p.State.SetSetting(r.Context(), setupSetting, "")
	return "/instalacao", nil
}

// nameNumber gives the Telegram account a short name, such as Pessoal or
// Trabalho; an empty one clears it.
func (p *Panel) nameNumber(r *http.Request) (string, error) {
	label := account.CleanLabel(r.FormValue("label"))
	if err := p.State.SetSetting(r.Context(), account.LabelSetting, label); err != nil {
		return "/conta", err
	}
	ok := i18n.T("Nome da conta salvo.")
	if label == "" {
		ok = i18n.T("Nome da conta removido.")
	}
	return "/conta?ok=" + url.QueryEscape(ok), nil
}

// pause is Disconnect (lifecycle.md §4): network and sync stop, the session
// and the local history stay. It is never LogOut.
func (p *Panel) pause(r *http.Request) (string, error) {
	if err := p.Engine.Disconnect(r.Context(), "user"); err != nil {
		return "/conta", userError{i18n.T("não foi possível desconectar agora")}
	}
	return "/conta?ok=" + url.QueryEscape(i18n.T("Desconectado. A sessão e o histórico continuam neste computador.")), nil
}

func (p *Panel) resume(r *http.Request) (string, error) {
	if err := p.Engine.Connect(r.Context()); err != nil {
		return "/conta", userError{i18n.T("não foi possível reconectar agora")}
	}
	return "/conta?ok=" + url.QueryEscape(i18n.T("Reconectando ao Telegram.")), nil
}

// renameConnection names another tool's connection as the person calls it;
// an empty name brings back the one the tool gave.
func (p *Panel) renameConnection(r *http.Request) (string, error) {
	name, ok := strings.CutPrefix(r.FormValue("client"), "client:")
	if !ok || name == "" {
		return "/", userError{i18n.T("só dá para renomear a conexão de outra ferramenta")}
	}
	label := strings.Join(strings.Fields(r.FormValue("name")), " ")
	if runes := []rune(label); len(runes) > 40 {
		label = string(runes[:40])
	}
	if label == toolName(name) {
		label = ""
	}
	return "/", p.State.SetSetting(r.Context(), clientLabel+name, label)
}

func (p *Panel) removeConnection(r *http.Request) (string, error) {
	key := r.FormValue("client")
	var err error
	switch {
	case key == "claude-desktop":
		err = p.removeClaudeDesktop()
		_ = p.State.ForgetClient(r.Context(), "claude-ai")
	case key == "claude-code":
		err = p.removeClaudeCode(r.Context())
		_ = p.State.ForgetClient(r.Context(), "claude-code")
	case key == "codex" || key == "cursor":
		prefix := codexClient
		if key == "codex" {
			err = p.removeCodex(r.Context())
		} else {
			err, prefix = p.removeCursor(), cursorClient
		}
		if seen, e := p.State.Clients(r.Context()); e == nil {
			for _, c := range seen {
				if strings.HasPrefix(c.Name, prefix) {
					_ = p.State.ForgetClient(r.Context(), c.Name)
				}
			}
		}
	case strings.HasPrefix(key, "client:"):
		name := strings.TrimPrefix(key, "client:")
		err = p.State.ForgetClient(r.Context(), name)
		_ = p.State.SetSetting(r.Context(), clientLabel+name, "")
	}
	if err != nil {
		return "/", err
	}
	return "/", nil
}

// ---- API ----

func (p *Panel) apiState(r *http.Request) (any, error) {
	s := p.snapshot(r.Context())
	s.Pairing = p.Engine.Login()
	s.QRSeq = p.observeQR(s.Pairing)
	set := p.cheapSetup(r.Context())
	live := 0
	for _, c := range p.connections(r.Context(), set) {
		if c.Live {
			live++
		}
	}
	clients, _ := p.State.Clients(r.Context())
	if clients == nil {
		clients = []state.Client{}
	}
	sync := s.Sync
	sync.Recent = nil
	codeText := ""
	if s.Pairing.State == engine.LoginWantCode {
		codeText = codeVia(s.Pairing.CodeType)
	}
	return map[string]any{
		"code_via": codeText, "read_only": s.ReadOnly,
		"arriving": s.Arriving, "arriving_count": s.ArrivingCount,
		"account": s.Account, "name": s.Name, "phone": s.Phone,
		"sync": sync, "pairing": s.Pairing, "health": s.Health,
		"history": s.History, "clients_live": live, "clients": clients, "endpoint": p.endpoint(), "version": mcp.Version,
		"pairing_error": pairingError(s.Pairing.Error), "qr_seq": s.QRSeq,
	}, nil
}

// apiPulse is what every page asks, over and over, to follow the connection
// and the Status tab's alert: far less than the whole state.
func (p *Panel) apiPulse(r *http.Request) (any, error) {
	sync := p.lightStatus()
	h, at, fresh := p.cache.health.peek()
	healthTone := ""
	if fresh {
		healthTone = tone(h.Health.Checks, sync)
	} else {
		p.refreshHealth()
	}
	return map[string]any{
		"key": syncKey(sync), "tone": healthTone, "fresh": fresh, "checked_at": at,
		"busy": settling(sync) || sync.State == "reconnecting",
	}, nil
}

func (p *Panel) apiPair(r *http.Request) (any, error) {
	var body struct {
		Method engine.LoginMethod `json:"method"`
		Phone  string             `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, userError{i18n.T("pedido inválido")}
	}
	a, err := p.Engine.Account(r.Context())
	if err != nil && !errors.Is(err, engine.ErrNotAuthorized) {
		return nil, err
	}
	if a.Authenticated {
		return nil, engine.ErrAccountMismatch
	}
	phone := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, body.Phone)
	if body.Method == "" {
		body.Method = engine.LoginQR
		if body.Phone != "" {
			body.Method = engine.LoginPhone
		}
	}
	if body.Method != engine.LoginQR && body.Method != engine.LoginPhone {
		return nil, engine.ErrInvalid
	}
	if body.Method == engine.LoginPhone {
		if len(phone) < 8 || len(phone) > 15 {
			return nil, userError{i18n.T("informe o número com DDI e DDD, por exemplo +55 11 91234-5678")}
		}
		phone = "+" + phone
	} else if body.Phone != "" {
		return nil, engine.ErrInvalid
	}
	// A requested phone login may replace a QR flow; an automatic QR poll
	// must not replace a phone login already waiting for human input.
	cur := p.Engine.Login()
	if body.Method == engine.LoginPhone && (cur.Method != body.Method || cur.Phone != phone) && loginActive(cur.State) {
		p.Engine.CancelLogin()
	}
	if err := p.Engine.StartLogin(r.Context(), engine.LoginRequest{Method: body.Method, Phone: phone}); err != nil && !errors.Is(err, engine.ErrLoginRunning) {
		return nil, err
	}
	login, _ := p.renewQR(r.Context())
	return login, nil
}

func (p *Panel) apiRefreshQR(r *http.Request) (any, error) {
	login, seq := p.renewQR(r.Context())
	return map[string]any{"state": login.State, "qr_seq": seq}, nil
}

func loginActive(state engine.LoginState) bool {
	switch state {
	case engine.LoginStarting, engine.LoginShowQR, engine.LoginWantCode, engine.LoginWantPass, engine.LoginSyncing:
		return true
	}
	return false
}

func (p *Panel) apiCancelPair(r *http.Request) (any, error) {
	p.Engine.CancelLogin()
	return p.Engine.Login(), nil
}

func (p *Panel) apiAddClient(r *http.Request) (any, error) {
	var body struct {
		Replace bool `json:"replace"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var err error
	switch r.PathValue("client") {
	case "claude-code":
		err = p.addClaudeCode(r.Context(), body.Replace)
	case "claude-desktop":
		err = p.addClaudeDesktop(body.Replace)
	case "codex":
		err = p.addCodex(r.Context(), body.Replace)
	case "cursor":
		err = p.addCursor(body.Replace)
	default:
		return nil, userError{i18n.T("cliente desconhecido")}
	}
	var conflict errConflict
	if errors.As(err, &conflict) {
		// Not a failure: a question for the person, which the page asks. The
		// existing entry can contain credentials, so expose only the fact that
		// there is a conflict and the client where it was found.
		return map[string]any{"conflict": true, "client": conflict.client}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func (p *Panel) qr(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !sameOrigin(r) && !p.fromAgent(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	pairing := p.Engine.Login()
	if pairing.State != engine.LoginShowQR || pairing.QRURL == "" {
		http.Error(w, "no QR code right now", http.StatusNotFound)
		return
	}
	png, err := qrcode.Encode(pairing.QRURL, qrcode.Medium, 520)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// ---- template helpers ----

func funcs() template.FuncMap {
	return template.FuncMap{
		"css":           func() template.CSS { return template.CSS(stylesheet) },
		"t":             translate,
		"tf":            translatef,
		"lang":          i18n.Lang,
		"languages":     func() []i18n.Language { return i18n.Languages },
		"flag":          flag,
		"languageHint":  languageHint,
		"icon":          icon,
		"toolmark":      toolmark,
		"logo":          panelMark,
		"productName":   func() string { return productName },
		"shortName":     func() string { return shortName },
		"codeVia":       codeVia,
		"eventLabel":    eventLabel,
		"peerLabel":     peerLabel,
		"categoryLabel": func(c string) string { return i18n.T(categoryLabels[c]) },
		"repositoryURL": func() string { return brand.RepositoryURL },
		"upstreamURL":   func() string { return upstreamURL },
		"version":       func() string { return strings.TrimPrefix(mcp.Version, "v") },
		"serverName":    func() string { return ServerName },
		"tray":          trayName,
		"mac":           func() bool { return runtime.GOOS == "darwin" },
		"accel": func(a string) string {
			switch a {
			case "metal":
				return i18n.T("na GPU do seu Mac")
			case "cuda":
				return i18n.T("na placa de vídeo NVIDIA")
			}
			return i18n.T("no processador")
		},
		"baseOf":        func(endpoint string) string { return strings.TrimSuffix(endpoint, "/mcp") },
		"relativeSince": relativeSinceAny,
		"moment":        momentAny,
		"count":         countFormat,
		"gapCauses":     gapCauses,
		"newMessages":   newMessages,
		"chatCount":     chatCount,
		"liveTools":     liveTools,
		"initial": func(s string) string {
			for _, r := range s {
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					return strings.ToUpper(string(r))
				}
			}
			return "T"
		},
		"hours": func(h float64) string {
			if h >= 48 {
				return i18n.Tf("%d dias", int(math.Round(h/24)))
			}
			return i18n.Tf("%d horas", int(math.Round(h)))
		},
	}
}

// translate is s in the person's language. The catalogues are the project's
// own text, so their markup is trusted, as the Portuguese in the templates is.
func translate(s string) template.HTML { return template.HTML(i18n.T(s)) }

// translatef is translate of a format, with the values filled in escaped.
func translatef(format string, args ...any) template.HTML {
	for i, a := range args {
		switch v := a.(type) {
		case template.HTML:
		case string:
			args[i] = template.HTMLEscapeString(v)
		case fmt.Stringer:
			args[i] = template.HTMLEscapeString(v.String())
		case int, int64, float64:
		default:
			// An error, or anything else that prints as text.
			args[i] = template.HTMLEscapeString(fmt.Sprint(v))
		}
	}
	return template.HTML(i18n.Tf(format, args...))
}

// translations serves the catalogue of the language in use to the panel's
// scripts, as window.I18N, with t(s, ...args) to look a text up.
func translations(w http.ResponseWriter, _ *http.Request) {
	body, _ := json.Marshal(i18n.Catalogue())
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "window.I18N={lang:%q,text:%s};\n%s", i18n.Lang(), body, translateScript)
}

// translateScript is t for the scripts: the same lookup as T, with %s and %d
// filled in order.
const translateScript = `window.t=function(s){var r=(window.I18N.text||{})[s]||s,a=arguments,i=1;return r.replace(/%[sd]/g,function(m){return i<a.length?String(a[i++]):m})};
`

// trayName is what each system calls the place the app's icon lives.
func trayName() string {
	switch runtime.GOOS {
	case "darwin":
		return i18n.T("barra de menus")
	case "windows":
		return i18n.T("área de notificação")
	}
	return i18n.T("bandeja do sistema")
}

func timeOf(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, !t.IsZero()
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, !t.IsZero()
	}
	return time.Time{}, false
}

func relativeSinceAny(v any) string {
	t, ok := timeOf(v)
	if !ok {
		return "—"
	}
	return relativeSince(t)
}

func relativeSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return i18n.T("agora há pouco")
	case d < time.Hour:
		return i18n.Tf("há %d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return i18n.Tf("há %d h", int(d.Hours()))
	}
	return i18n.Tf("há %d dias", int(d.Hours()/24))
}

func momentAny(v any) string {
	t, ok := timeOf(v)
	if !ok {
		return "—"
	}
	// Day first in Portuguese and Spanish; in English, the month by name,
	// which no reader takes for the day.
	if i18n.Lang() == "en" {
		return t.Local().Format("Jan 2, 2006, 3:04 PM")
	}
	return t.Local().Format("02/01/2006 15:04")
}

// newMessages is a count of new messages, in the singular for one.
func newMessages(v any) string {
	if n, _ := v.(int); n == 1 || v == int64(1) {
		return i18n.T("1 mensagem nova")
	}
	return i18n.Tf("%s mensagens novas", countFormat(v))
}

// chatCount is a count of chats, in the singular for one.
func chatCount(n int) string {
	if n == 1 {
		return i18n.T("1 conversa")
	}
	return i18n.Tf("%d conversas", n)
}

// liveTools names the AI tools already using Telegram, as a list in the
// person's language: "Claude Code", "Claude Code e Cursor", "A, B e C".
func liveTools(conns []Connection) template.HTML {
	var names []string
	for _, c := range conns {
		if c.Live {
			names = append(names, "<strong>"+template.HTMLEscapeString(c.Tool)+"</strong>")
		}
	}
	if len(names) < 2 {
		return template.HTML(strings.Join(names, ""))
	}
	return template.HTML(i18n.Tf("%s e %s", strings.Join(names[:len(names)-1], ", "), names[len(names)-1]))
}

func countFormat(v any) string {
	var n int64
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int64:
		n = x
	}
	s := strconv.FormatInt(n, 10)
	// English groups thousands with a comma; Portuguese and Spanish, a point.
	sep := byte('.')
	if i18n.Lang() == "en" {
		sep = ','
	}
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, sep)
		}
		out = append(out, s[i])
	}
	return string(out)
}
