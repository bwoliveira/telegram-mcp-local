package panel

// The Conta page's chat preview in a real headless Chromium: the page the
// panel renders, its real scripts, and synthetic /api/chats, /api/chats/{jid}
// and /api/history answers. A driver script injected into the page clicks
// and types like a person and posts what it saw back to the test. Nothing
// here reaches Telegram or a real account. Skipped without Chromium.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

func TestContaRendersTheInheritedPreview(t *testing.T) {
	_, h := panelWithFake(t)
	page := pageBody(t, h, "/conta")
	mustContain(t, "/conta", page, `data-chats`, `data-chat-list`, `data-thread`, `data-history-chat`, `data-history-all`,
		`data-chat-filters`, `data-chat-q`, `data-chat-unread`, `data-chat-type`, `data-chats-more`,
		`data-thread-search`, `data-thread-older`, `data-thread-status`, `data-chats-status`, "Conversas recentes")
	for _, bad := range []string{"/conversas", "conversations.js", "<textarea", "celular"} {
		if strings.Contains(page[strings.Index(page, "<h1>"):strings.Index(page, "<footer")], bad) {
			t.Errorf("/conta contains %q", bad)
		}
	}
}

type previewFixture struct {
	mu       sync.Mutex
	chats    []map[string]any
	failOnce map[string]bool
	requests []string
	history  []string
	report   chan []byte
}

func newPreviewFixture() *previewFixture {
	f := &previewFixture{failOnce: map[string]bool{"falha": true}, report: make(chan []byte, 1)}
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	types := []string{"user", "chat", "channel"}
	for i := 1; i <= 25; i++ {
		typ := types[i%3]
		at := base.Add(-time.Duration(i) * time.Hour)
		unread := 0
		if i%4 == 0 {
			unread = i
		}
		f.chats = append(f.chats, map[string]any{
			"jid": fmt.Sprintf("%s:%d", typ, i), "name": fmt.Sprintf("Chat %02d", i), "kind": typ,
			"group": typ != "user", "unread": unread, "at": at, "last_at": at,
			"last_text": fmt.Sprintf("última de %d", i), "messages": 45,
		})
	}
	return f
}

func (f *previewFixture) chatsAPI(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := strings.ToLower(q.Get("q"))
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	fail := f.failOnce[name]
	delete(f.failOnce, name)
	f.mu.Unlock()
	if name == "lento" {
		time.Sleep(900 * time.Millisecond)
	}
	if fail {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "falhou de propósito"})
		return
	}
	var out []map[string]any
	for _, c := range f.chats {
		if name == "lento" {
			out = append(out, map[string]any{"jid": "user:999", "name": "Resposta velha", "at": time.Now(), "messages": 1})
			break
		}
		if name != "" && name != "falha" && !strings.Contains(strings.ToLower(c["name"].(string)), name) {
			continue
		}
		if q.Get("unread") == "true" && c["unread"].(int) == 0 {
			continue
		}
		if typ := q.Get("type"); typ != "" && c["kind"] != typ {
			continue
		}
		out = append(out, c)
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 {
		limit = 10
	}
	end := min(offset+limit, len(out))
	page := []map[string]any{}
	if offset < len(out) {
		page = out[offset:end]
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"chats": page, "has_more": end < len(out), "next_offset": end, "history": nil})
}

func (f *previewFixture) conversationAPI(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	f.mu.Unlock()
	jid := r.PathValue("jid")
	base := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	var all []map[string]any // newest first, as a store page would come
	for n := 45; n >= 1; n-- {
		text := fmt.Sprintf("mensagem %d de %s", n, jid)
		if n%10 == 0 {
			text += " alfa"
		}
		if n == 7 {
			text = `<img src=x onerror="window.pwned=1">`
		}
		// app.js sends literal text as an escaped FTS5 phrase. This scripted
		// fixture models only that phrase; real FTS is covered store-backed.
		s := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(q.Get("q"), `"`), `"`), `""`, `"`)
		if s != "" && !strings.Contains(text, s) {
			continue
		}
		at := base.Add(time.Duration(n) * time.Minute)
		// The store's keyset bounds: before is exclusive, since inclusive.
		key := [2]int64{at.Unix(), int64(n)}
		if c := q.Get("before"); c != "" {
			if b, err := store.ParsePanelCursor(c); err != nil || key[0] > b.Date || (key[0] == b.Date && key[1] >= b.ID) {
				continue
			}
		}
		if c := q.Get("since"); c != "" {
			if b, err := store.ParsePanelCursor(c); err != nil || key[0] < b.Date || (key[0] == b.Date && key[1] < b.ID) {
				continue
			}
		}
		all = append(all, map[string]any{"id": fmt.Sprintf("m%02d", n), "cursor": store.PanelCursor{Date: key[0], ID: key[1]}.String(), "at": at, "from_me": n%3 == 0, "sender": "Ana", "text": text})
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	end := min(offset+limit, len(all))
	page := []map[string]any{}
	if offset < len(all) {
		page = all[offset:end]
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"messages": page, "has_more": end < len(all), "next_offset": end, "oldest": base.Add(time.Minute)})
}

const previewDriver = `
(async function () {
  const out = {};
  const $ = (s) => document.querySelector(s);
  const rows = () => Array.from(document.querySelectorAll("[data-chat-list] [data-chat-jid]"));
  const names = () => rows().map((r) => r.querySelector(".chat__name").textContent);
  const bubbles = () => Array.from(document.querySelectorAll("[data-thread] .bubble"));
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  async function until(what, cond, ms) {
    const end = Date.now() + (ms || 5000);
    while (Date.now() < end) { try { if (cond()) return true; } catch (e) {} await sleep(20); }
    out.timeouts = (out.timeouts || []).concat(what);
    return false;
  }
  function type(sel, value) { const i = $(sel); i.value = value; i.dispatchEvent(new Event("input", { bubbles: true })); }
  function change(sel, apply) { const i = $(sel); apply(i); i.dispatchEvent(new Event("change", { bubbles: true })); }
  try {
    await until("first page", () => rows().length === 10 && bubbles().length === 30);
    out.first = { rows: rows().length, pill: $("[data-chats-count]").textContent, current: rows()[0].getAttribute("aria-current"),
      title: $("[data-thread-title]").textContent, more: !$("[data-chats-more]").hidden, older: !$("[data-thread-older]").hidden };
    $("[data-chats-more]").click();
    await until("page 2", () => rows().length === 20);
    $("[data-chats-more]").click();
    await until("page 3", () => rows().length === 25);
    out.paged = { rows: rows().length, more: !$("[data-chats-more]").hidden, unique: new Set(names()).size };

    change("[data-chat-type]", (i) => { i.value = "channel"; });
    await until("type filter", () => rows().length === 8);
    out.type = names();
    change("[data-chat-unread]", (i) => { i.checked = true; });
    await until("unread filter", () => rows().length === 2);
    out.unread = names();
    change("[data-chat-unread]", (i) => { i.checked = false; });
    change("[data-chat-type]", (i) => { i.value = ""; });
    await until("filters cleared", () => rows().length === 10);

    type("[data-chat-q]", "lento");
    await until("slow loading", () => !$("[data-chats-status]").hidden && $("[data-chat-list]").getAttribute("aria-busy") === "true", 2000);
    out.loading = { status: $("[data-chats-status]").textContent, spinner: !!$("[data-chats-status] .spinner") };
    type("[data-chat-q]", "chat 03");
    await until("name filter", () => names().join() === "Chat 03");
    await sleep(1200);
    out.name = { names: names(), busy: $("[data-chat-list]").getAttribute("aria-busy"), status: $("[data-chats-status]").hidden };

    type("[data-chat-q]", "falha");
    await until("error", () => !$("[data-chats-status]").hidden && $("[data-chats-status]").getAttribute("role") === "alert");
    out.error = { text: $("[data-chats-status]").textContent, retry: !!$("[data-chats-status] button") };
    $("[data-chats-status] button").click();
    await until("retry", () => rows().length === 10 && $("[data-chats-status]").hidden);
    out.retried = rows().length;
    type("[data-chat-q]", "");
    await until("cleared", () => rows().length === 10 && names()[0] === "Chat 01");

    rows()[1].click();
    await until("open chat", () => $("[data-thread-title]").textContent === "Chat 02" && bubbles().length === 30 && bubbles()[0].textContent.includes("channel:2") && !$("[data-thread-older]").disabled);
    out.open = { title: $("[data-thread-title]").textContent, bubbles: bubbles().length, older: !$("[data-thread-older]").hidden,
      current: rows()[1].getAttribute("aria-current"), meta: $("[data-thread-meta]").textContent };
    $("[data-thread-older]").click();
    await until("older", () => bubbles().length === 45);
    const ids = bubbles().map((b) => b.getAttribute("data-message-id"));
    out.older = { bubbles: ids.length, sorted: ids.join() === ids.slice().sort().join(), older: !$("[data-thread-older]").hidden,
      xss: typeof window.pwned, literal: document.querySelector("[data-thread]").textContent.includes("<img src=x") };

    $("[data-thread-q]").value = "alfa";
    $("[data-thread-search]").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await until("search", () => bubbles().length === 4);
    out.search = { bubbles: bubbles().length, meta: $("[data-thread-meta]").textContent, clear: !$("[data-thread-clear]").hidden,
      all: bubbles().every((b) => b.textContent.includes("alfa")) };
    $("[data-thread-clear]").click();
    await until("search cleared", () => bubbles().length === 30);
    out.unsearched = bubbles().length;

    $("[data-history-chat]").click();
    await until("history", () => !$("[data-history-note]").hidden && $("[data-history-note]").textContent.length > 0);
    out.history = $("[data-history-note]").textContent;
  } catch (e) {
    out.exception = String(e && e.stack || e);
  }
  await fetch("/report", { method: "POST", body: JSON.stringify(out) });
})();
`

func chromiumPath() string {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func TestContaPreviewInHeadlessBrowser(t *testing.T) {
	browser := chromiumPath()
	if browser == "" {
		t.Skip("no Chromium on this machine")
	}
	_, h := panelWithFake(t)
	page := pageBody(t, h, "/conta")
	page = strings.Replace(page, "</body>", `<script src="/driver.js" defer></script></body>`, 1)

	f := newPreviewFixture()
	sub, _ := fs.Sub(assets, "assets")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /conta", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})
	plain := pageBody(t, h, "/conta")
	mux.HandleFunc("GET /conta-plain", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, plain)
	})
	mux.HandleFunc("GET /driver.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = io.WriteString(w, previewDriver)
	})
	mux.HandleFunc("GET /assets/i18n.js", translations)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(sub))))
	mux.HandleFunc("GET /api/chats", f.chatsAPI)
	mux.HandleFunc("GET /api/chats/{jid}", f.conversationAPI)
	mux.HandleFunc("POST /api/history", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.history = append(f.history, string(body))
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"started": true, "job": map[string]any{"chats": []string{"chat:2"}}})
	})
	mux.HandleFunc("POST /report", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case f.report <- body:
		default:
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, browser, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run",
		"--disable-extensions", "--user-data-dir="+t.TempDir(), "--remote-debugging-port=0", srv.URL+"/conta")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	var raw []byte
	select {
	case raw = <-f.report:
	case <-ctx.Done():
		t.Fatal("the browser never reported")
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("report %s: %v", raw, err)
	}
	pretty, _ := json.MarshalIndent(got, "", "  ")
	if got["exception"] != nil || got["timeouts"] != nil {
		f.mu.Lock()
		defer f.mu.Unlock()
		t.Fatalf("driver: %s\nrequests:\n%s", pretty, strings.Join(f.requests, "\n"))
	}
	check := func(path string, want any) {
		t.Helper()
		var v any = got
		for _, k := range strings.Split(path, ".") {
			v = v.(map[string]any)[k]
		}
		if fmt.Sprint(v) != fmt.Sprint(want) {
			t.Errorf("%s = %v, want %v\n%s", path, v, want, pretty)
		}
	}
	check("first.rows", 10)
	check("first.pill", "10 conversas")
	check("first.current", "true")
	check("first.title", "Chat 01")
	check("first.more", true)
	check("first.older", true)
	check("paged.rows", 25)
	check("paged.more", false)
	check("paged.unique", 25)
	check("type", "[Chat 02 Chat 05 Chat 08 Chat 11 Chat 14 Chat 17 Chat 20 Chat 23]")
	check("unread", "[Chat 08 Chat 20]")
	check("loading.status", "Carregando conversas…")
	check("loading.spinner", true)
	check("name.names", "[Chat 03]")
	check("name.busy", "<nil>")
	check("name.status", true)
	check("error.text", "falhou de propósitoTentar de novo")
	check("error.retry", true)
	check("retried", 10)
	check("open.title", "Chat 02")
	check("open.bubbles", 30)
	check("open.older", true)
	check("open.current", "true")
	check("older.bubbles", 45)
	check("older.sorted", true)
	check("older.older", false)
	check("older.xss", "undefined")
	check("older.literal", true)
	check("search.bubbles", 4)
	check("search.meta", "4 mensagens encontradas.")
	check("search.clear", true)
	check("search.all", true)
	check("unsearched", 30)
	check("history", "Pedido enviado ao Telegram. As mensagens mais antigas chegam aos poucos; a tela se atualiza sozinha.")

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.history) != 1 || f.history[0] != `{"chat_jid":"channel:2"}` {
		t.Errorf("history requests: %q", f.history)
	}
	var writes []string
	for _, r := range f.requests {
		if !strings.HasPrefix(r, "GET ") {
			writes = append(writes, r)
		}
	}
	sort.Strings(writes)
	if len(writes) != 0 {
		t.Errorf("the preview wrote: %q", writes)
	}
	for _, r := range f.requests {
		if strings.HasPrefix(r, "GET /api/chats?") && !strings.Contains(r, "limit=") {
			t.Errorf("unbounded list request %q", r)
		}
	}
	// PREVIEW_UI_SCREENSHOT=/path.png keeps a picture of the page, without
	// the driver, for a person to look at.
	if shot := os.Getenv("PREVIEW_UI_SCREENSHOT"); shot != "" {
		f.mu.Unlock()
		out, err := exec.CommandContext(ctx, browser, "--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
			"--user-data-dir="+t.TempDir(), "--window-size=1280,1700", "--virtual-time-budget=4000",
			"--screenshot="+shot, srv.URL+"/conta-plain").CombinedOutput()
		f.mu.Lock()
		if err != nil {
			t.Errorf("screenshot: %v %s", err, out)
		}
	}
	if os.Getenv("PREVIEW_UI_VERBOSE") != "" {
		t.Logf("%s\n%s", pretty, strings.Join(f.requests, "\n"))
	}
}
