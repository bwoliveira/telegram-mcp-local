package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// Status is the gateway in one line, for the tray: a colour, what it is
// doing, and a detail.
type Status struct {
	// Tone is ok (connected and receiving), warn (reconnecting, paused,
	// receiving history) or fail (Telegram disconnected or sync stopped).
	Tone   string `json:"tone"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	// Sync is the engine's connection state, for notifications to act on.
	Sync string `json:"sync"`
	// MCPDown is set when the MCP is not answering because its port is
	// taken.
	MCPDown bool `json:"mcp_down,omitempty"`
}

// Status reports the gateway now.
func (d *Daemon) Status() Status { return d.status(context.Background()) }

func (d *Daemon) status(ctx context.Context) Status {
	sync := d.primary.eng.Status()
	pairing := d.primary.eng.Login()
	st := Status{Sync: string(sync.State)}
	switch sync.State {
	case engine.StateConnected:
		st.Tone, st.Title = "ok", i18n.T("Conectado")
	case engine.StatePaused:
		st.Tone, st.Title = "warn", i18n.T("Pausado por instantes")
		if sync.PausedFor == "pairing" {
			st.Title = i18n.T("Conectando o Telegram")
		}
	case engine.StateStarting, engine.StateConnecting:
		st.Tone, st.Title = "warn", i18n.T("Carregando")
	case engine.StateResyncing:
		st.Tone, st.Title = "warn", i18n.T("Recebendo o histórico")
	case engine.StateReconnecting:
		st.Tone, st.Title = "warn", i18n.T("Reconectando")
	case engine.StateNotAuthorized:
		st.Tone, st.Title = "fail", i18n.T("Telegram não conectado")
	case engine.StateLoggedOut, engine.StateRevoked:
		st.Tone, st.Title = "fail", i18n.T("Sessão do Telegram encerrada")
	default:
		st.Tone, st.Title = "fail", i18n.T("Sincronização parada")
	}
	arriving := pairing.State == engine.LoginSyncing || sync.State == engine.StateResyncing || (!sync.History.LastAt.IsZero() && time.Since(sync.History.LastAt) < 90*time.Second)
	switch {
	case arriving && st.Tone == "ok":
		st.Tone, st.Detail = "warn", i18n.T("Recebendo o histórico")
	}
	if d.PortProblem() != nil {
		st.MCPDown = true
		if st.Tone == "ok" {
			st.Tone = "warn"
		}
		st.Detail = i18n.Tf("MCP fora do ar: a porta %d está ocupada", d.Port())
	}
	return st
}

func messagesLastHour(n int64) string {
	switch n {
	case 0:
		return i18n.T("Nenhuma mensagem na última hora")
	case 1:
		return i18n.T("1 mensagem na última hora")
	}
	return i18n.Tf("%d mensagens na última hora", n)
}

// Watch delivers the status every time it changes, starting with the current
// one. Call stop when done.
func (d *Daemon) Watch() (updates <-chan Status, stop func()) {
	c := make(chan Status, 1)
	c <- d.Status()
	id := d.watch.add(c)
	return c, func() { d.watch.remove(id) }
}

// watchLoop recomputes the status when sync changes, and every half minute
// for the message count.
func (d *Daemon) watchLoop(ctx context.Context) {
	defer close(d.watchDone)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	// Downloaded media past its retention goes a minute after start, then
	// every six hours.
	sweep := time.NewTimer(time.Minute)
	defer sweep.Stop()
	changes := d.primary.eng.Changes()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			for _, a := range d.Accounts() {
				_, _, _ = a.server.SweepMedia(ctx)
			}
			sweep.Reset(6 * time.Hour)
			continue
		case _, ok := <-changes:
			if !ok {
				changes = nil
			}
		case <-tick.C:
		}
		d.watch.publish(d.status(ctx))
	}
}

type watchers struct {
	mu   sync.Mutex
	next int
	subs map[int]chan Status
	last Status
}

func (w *watchers) add(c chan Status) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.subs == nil {
		w.subs = map[int]chan Status{}
	}
	w.next++
	w.subs[w.next] = c
	return w.next
}

func (w *watchers) remove(id int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.subs, id)
}

// publish hands the newest status to every watcher, replacing one it has
// not read yet: a watcher only ever needs the latest.
func (w *watchers) publish(s Status) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s == w.last {
		return
	}
	w.last = s
	for _, c := range w.subs {
		select {
		case <-c:
		default:
		}
		c <- s
	}
}
