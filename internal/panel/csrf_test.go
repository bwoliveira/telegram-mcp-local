package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// csrfHarness runs assets/csrf.js under node with a minimal stand-in for the
// browser: fetch, Headers and the document's submit listener. It checks what
// the panel sends, not how a real browser renders.
const csrfHarness = `
"use strict";
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const scenario = process.argv[3];
const calls = [];
let sessionStatus = scenario === "desktop" || scenario === "desktop-submit" ? 401 : 200;
class Headers {
  constructor(init) { this.m = {}; if (init instanceof Headers) { Object.assign(this.m, init.m); } else if (init) { for (const k of Object.keys(init)) this.m[k.toLowerCase()] = String(init[k]); } }
  set(k, v) { this.m[k.toLowerCase()] = String(v); }
  get(k) { const v = this.m[k.toLowerCase()]; return v === undefined ? null : v; }
  has(k) { return k.toLowerCase() in this.m; }
}
function fetch(path, options) {
  options = options || {};
  const h = options.headers instanceof Headers ? options.headers : new Headers(options.headers);
  calls.push({ path, method: (options.method || "GET").toUpperCase(), csrf: h.get("X-CSRF-Token"), ctype: h.get("Content-Type"), creds: options.credentials || "" });
  if (typeof options.body === "string" && path.startsWith("/conta/")) calls[calls.length - 1].body = options.body;
  if (path === "/api/session") {
    return Promise.resolve({ ok: sessionStatus === 200, status: sessionStatus, json: () => Promise.resolve(sessionStatus === 200 ? { csrf_token: "tok-123" } : { error: "panel session required" }) });
  }
  if (path.startsWith("/conta/") && scenario === "html") return Promise.resolve({ ok: true, status: 200, redirected: false, url: "", headers: new Headers({ "Content-Type": "text/html; charset=utf-8" }), text: () => Promise.resolve("<p>encerrado</p>") });
  if (path.startsWith("/conta/") && scenario === "refused") return Promise.resolve({ ok: false, status: 403, redirected: false, url: "", headers: new Headers({ "Content-Type": "text/html; charset=utf-8" }), text: () => Promise.resolve("<b>invalid CSRF token</b>") });
  if (path.startsWith("/conta/")) return Promise.resolve({ ok: true, status: 200, redirected: true, url: "http://127.0.0.1:1/conta?ok=1", headers: new Headers({}), text: () => Promise.resolve("") });
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) });
}
class FormData {
  constructor(form) { this.form = form; }
  *[Symbol.iterator]() { for (const c of this.form.children) yield [c.name, c.value]; }
}
const listeners = {};
function makeForm(method, fields) {
  const children = (fields || []).map(([name, value]) => ({ name, value }));
  return {
    tagName: "FORM", dataset: {}, submitted: 0, children,
    getAttribute: (k) => (k === "method" ? method : k === "action" ? "/conta/nome" : null),
    querySelector: (sel) => children.find((c) => sel === "input[name=csrf]" && c.name === "csrf") || null,
    appendChild: (c) => { children.push(c); return c; },
    submit() { this.submitted++; },
  };
}
const forms = [makeForm("post"), makeForm("POST")];
const written = [];
const document = {
  readyState: "loading",
  body: { textContent: "" },
  open: () => written.push("open"), write: (s) => written.push(s), close: () => written.push("close"),
  addEventListener: (type, fn, capture) => { (listeners[type] = listeners[type] || []).push(fn); },
  createElement: (tag) => ({ tagName: tag.toUpperCase(), type: "", name: "", value: "" }),
  querySelectorAll: (sel) => forms,
};
const assigned = [];
const window = { location: { pathname: "/conta", assign: (u) => assigned.push(u) } };
// The source is the panel's own assets/csrf.js from this repository, never
// outside input; wrapping it gives it the stand-ins above as its globals.
new Function("window", "document", "fetch", "Headers", "FormData", "URLSearchParams", src)(window, document, fetch, Headers, FormData, URLSearchParams);
const out = { calls, forms: [], assigned, written };
const tick = () => new Promise((r) => setTimeout(r, 0));
(async () => {
  const api = window.panelCSRF;
  if (!api || typeof api.fetch !== "function" || typeof api.token !== "function") { console.log(JSON.stringify({ error: "no panelCSRF" })); return; }
  if (scenario === "submit" || scenario === "desktop-submit" || scenario === "html" || scenario === "refused") {
    const form = makeForm("post", [["label", "Trabalho & co"]]);
    let prevented = false;
    for (const fn of listeners.submit || []) fn({ target: form, preventDefault: () => { prevented = true; } });
    await tick(); await tick(); await tick();
    out.forms.push({ prevented, submitted: form.submitted, csrf: (form.children.find((c) => c.name === "csrf") || {}).value || "", type: (form.children[0] || {}).type || "" });
    const getForm = makeForm("get");
    let getPrevented = false;
    for (const fn of listeners.submit || []) fn({ target: getForm, preventDefault: () => { getPrevented = true; } });
    out.getForm = { prevented: getPrevented, children: getForm.children.length };
    out.body = document.body.textContent;
  } else if (scenario === "prefetch") {
    for (const fn of listeners.DOMContentLoaded || []) fn({});
    await tick(); await tick(); await tick();
    out.forms = forms.map((f) => ({ csrf: (f.children.find((c) => c.name === "csrf") || {}).value || "", ready: f.dataset.csrf || "" }));
  } else {
    await api.fetch("/api/state", { credentials: "same-origin" });
    await api.fetch("/api/pair", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
    await api.fetch("/api/webhooks/1", { method: "delete", headers: { Accept: "application/json" } });
    await api.fetch("/api/webhooks/1", { method: "PATCH", headers: { "Content-Type": "application/json" }, body: "{}" });
  }
  console.log(JSON.stringify(out));
})();
`

func runCSRFScript(t *testing.T, scenario string) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; csrf.js behaviour is not exercised")
	}
	dir := t.TempDir()
	harness := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(harness, []byte(csrfHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := filepath.Abs(filepath.Join("assets", "csrf.js"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, harness, src, scenario).CombinedOutput()
	if err != nil {
		t.Fatalf("node %s: %v\n%s", scenario, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Every unsafe fetch carries the session's token in X-CSRF-Token, fetched
// once from GET /api/session; a GET carries none and fetches no token.
func TestCSRFScriptAddsTokenToUnsafeFetches(t *testing.T) {
	got := runCSRFScript(t, "session")
	for _, want := range []string{
		`{"path":"/api/state","method":"GET","csrf":null`,
		`{"path":"/api/session","method":"GET","csrf":null`,
		`{"path":"/api/pair","method":"POST","csrf":"tok-123","ctype":"application/json","creds":"same-origin"}`,
		`{"path":"/api/webhooks/1","method":"DELETE","csrf":"tok-123"`,
		`{"path":"/api/webhooks/1","method":"PATCH","csrf":"tok-123"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if n := strings.Count(got, `"path":"/api/session"`); n != 1 {
		t.Errorf("token fetched %d times: %s", n, got)
	}
}

// In the desktop window there is no browser session (the window is trusted
// in memory): the calls still go out, without a token.
func TestCSRFScriptWithoutSession(t *testing.T) {
	got := runCSRFScript(t, "desktop")
	if !strings.Contains(got, `{"path":"/api/pair","method":"POST","csrf":null`) {
		t.Fatalf("desktop call: %s", got)
	}
}

// A post form goes as a same-origin fetch with the token (header and csrf
// field) and its fields, and the page then follows the redirect: a plain form
// post would carry "Origin: null" under the guard's no-referrer policy. A GET
// form is left alone.
func TestCSRFScriptSendsPostFormsWithToken(t *testing.T) {
	got := runCSRFScript(t, "submit")
	for _, want := range []string{
		`{"path":"/conta/nome","method":"POST","csrf":"tok-123","ctype":"application/x-www-form-urlencoded","creds":"same-origin","body":"label=Trabalho+%26+co&csrf=tok-123"}`,
		`"forms":[{"prevented":true,"submitted":0,"csrf":"tok-123"`,
		`"assigned":["http://127.0.0.1:1/conta?ok=1"]`,
		`"getForm":{"prevented":false,"children":0}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

// A page in answer replaces the current one; a refusal is shown as text,
// never as markup.
func TestCSRFScriptFormAnswers(t *testing.T) {
	if got := runCSRFScript(t, "html"); !strings.Contains(got, `"written":["open","<p>encerrado</p>","close"]`) {
		t.Errorf("html answer: %s", got)
	}
	if got := runCSRFScript(t, "refused"); !strings.Contains(got, `"written":[]`) || !strings.Contains(got, `"body":"<b>invalid CSRF token</b>"`) {
		t.Errorf("refusal: %s", got)
	}
}

// Without a session (the desktop window) the form is posted as it is.
func TestCSRFScriptDesktopFormsPostNatively(t *testing.T) {
	got := runCSRFScript(t, "desktop-submit")
	if !strings.Contains(got, `"forms":[{"prevented":true,"submitted":1,"csrf":""`) || strings.Contains(got, `"/conta/nome"`) {
		t.Fatalf("desktop form: %s", got)
	}
}

// Once the page has loaded, the post forms already carry the token, so a
// submit does not have to wait for it.
func TestCSRFScriptPrefetchesForForms(t *testing.T) {
	got := runCSRFScript(t, "prefetch")
	if !strings.Contains(got, `"forms":[{"csrf":"tok-123","ready":"ready"},{"csrf":"tok-123","ready":"ready"}]`) {
		t.Fatalf("prefetch: %s", got)
	}
}

// Every page loads csrf.js before the scripts that act, and no script sends
// an unsafe request around it.
func TestEveryMutationGoesThroughCSRF(t *testing.T) {
	head := pageSource[strings.Index(pageSource, `{{define "head"}}`):]
	head = head[:strings.Index(head, "</head>")]
	if !strings.Contains(head, `<script src="/assets/csrf.js"></script>`) || strings.Index(head, "csrf.js") > strings.Index(pageSource, "app.js") {
		t.Fatal(`the "head" template does not load csrf.js`)
	}
	if strings.Count(pageSource, "<html") != 1 || strings.Count(webhookDocsSource, "<html") != 0 {
		t.Fatal("a page has its own <html> head, which may skip csrf.js")
	}
	entries, err := os.ReadDir("assets")
	if err != nil {
		t.Fatal(err)
	}
	// A bare fetch is allowed only as a literal read: a path and
	// { credentials: "same-origin" }, nothing that could carry a method.
	raw := regexp.MustCompile(`(^|[^.\w])fetch\(`)
	read := regexp.MustCompile(`(^|[^.\w])fetch\("[^"]+", \{ credentials: "same-origin" \}\)`)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".js" || e.Name() == "csrf.js" {
			continue
		}
		src, _ := os.ReadFile(filepath.Join("assets", e.Name()))
		for i, line := range strings.Split(string(src), "\n") {
			if len(raw.FindAllString(line, -1)) != len(read.FindAllString(line, -1)) {
				t.Errorf("assets/%s:%d sends an unsafe request without panelCSRF.fetch: %s", e.Name(), i+1, strings.TrimSpace(line))
			}
			if strings.Contains(line, "XMLHttpRequest") || strings.Contains(line, "sendBeacon") {
				t.Errorf("assets/%s:%d bypasses fetch: %s", e.Name(), i+1, strings.TrimSpace(line))
			}
		}
	}
}
