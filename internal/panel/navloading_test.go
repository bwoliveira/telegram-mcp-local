package panel

// The navigation feedback at the end of assets/app.js, run under node with a
// minimal DOM: a click on a page link shows the bar at once, a note when the
// page is slow and an alert with a retry when it does not come, and leaves
// alone what is no navigation of the panel.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const navMarker = "// Feedback for a click on a link to another page of the panel."

const navHarness = `
"use strict";
const fs = require("fs");
const all = fs.readFileSync(process.argv[2], "utf8");
const src = all.slice(all.indexOf(process.argv[3]));
class Node {
  constructor(tag) { this.tagName = tag.toUpperCase(); this.children = []; this.parentNode = null; this.attrs = {}; this.listeners = {}; this._text = ""; this.className = ""; this.classList = { toggle: (c, on) => { this["is_" + c] = !!on; } }; }
  appendChild(c) { c.parentNode = this; this.children.push(c); return c; }
  remove() { if (this.parentNode) { const s = this.parentNode.children; s.splice(s.indexOf(this), 1); this.parentNode = null; } }
  get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children = []; this._text = String(v); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  hasAttribute(k) { return k in this.attrs; }
  addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
  closest(sel) { let n = this; while (n) { if (n.tagName === "A" && n.hasAttribute("href")) return n; n = n.parentNode; } return null; }
}
const document = new Node("document");
document.body = document.appendChild(new Node("body"));
document.createElement = (tag) => new Node(tag);
const timers = [];
const assigned = [];
const window = {
  location: { href: "http://127.0.0.1:47831/conta", origin: "http://127.0.0.1:47831", pathname: "/conta", search: "", assign: (u) => assigned.push(u) },
  setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
  clearTimeout: (id) => { if (timers[id - 1]) timers[id - 1].fn = null; },
  addEventListener: (type, fn) => { window["on_" + type] = fn; },
  stop: () => {},
};
const t = (s) => s;
// src is the tail of this repository's own assets/app.js; nothing from
// outside is evaluated.
new Function("window", "document", "t", "URL", src)(window, document, t, URL);
function fire(ms) { for (const tm of timers) if (tm.fn && (ms === undefined || tm.ms === ms)) { const f = tm.fn; tm.fn = null; f(); } }
function link(href, extra) { const a = new Node("a"); a.setAttribute("href", href); Object.assign(a, extra || {}); document.body.appendChild(a); return a; }
function click(a, extra) { const ev = Object.assign({ button: 0, target: a, defaultPrevented: false }, extra || {}); for (const fn of document.listeners.click) fn(ev); fire(0); return ev; }
const bars = () => document.body.children.filter((n) => n.className === "navload").length;
const note = () => document.body.children.find((n) => n.className === "navnote");
const out = {};
click(link("/conta#desconectar")); out.hash = bars();
click(link("https://example.com/x")); out.external = bars();
click(link("/api/pair/qr.png")); out.file = bars();
click(link("/status"), { ctrlKey: true }); out.modified = bars();
click(link("/status", { target: "_blank" })); out.blank = bars();
click(link("/status"), { defaultPrevented: true }); out.prevented = bars();
const a = link("/status");
click(a);
out.bar = bars();
out.busy = a.getAttribute("aria-busy");
fire(4000);
out.slow = note() && note().getAttribute("role") + ":" + note().textContent;
fire(25000);
out.stuck = note() && note().getAttribute("role") + ":" + note().children[0].textContent;
out.barAfterStuck = bars();
const buttons = note().children.filter((n) => n.tagName === "BUTTON");
for (const fn of buttons[0].listeners.click) fn();
out.retried = assigned;
for (const fn of buttons[1].listeners.click) fn();
out.cleared = !note() && bars() === 0 && a.getAttribute("aria-busy") === null;
click(link("/funcoes"));
window.on_pageshow();
out.pageshow = bars() === 0 && !note();
console.log(JSON.stringify(out));
`

func TestNavigationFeedback(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the navigation feedback is not exercised")
	}
	src, err := filepath.Abs(filepath.Join("assets", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(src)
	if !strings.Contains(string(raw), navMarker) {
		t.Fatal("app.js lost the navigation feedback block")
	}
	harness := filepath.Join(t.TempDir(), "harness.js")
	if err := os.WriteFile(harness, []byte(navHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command(node, harness, src, navMarker).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, b)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	for _, k := range []string{"hash", "external", "file", "modified", "blank", "prevented"} {
		if out[k] != float64(0) {
			t.Errorf("%s: a bar for what is no panel navigation", k)
		}
	}
	if out["bar"] != float64(1) || out["busy"] != "true" {
		t.Errorf("click: bar %v busy %v", out["bar"], out["busy"])
	}
	if out["slow"] != "status:Carregando a página…" {
		t.Errorf("slow: %v", out["slow"])
	}
	if s, _ := out["stuck"].(string); !strings.HasPrefix(s, "alert:A página está demorando") || out["barAfterStuck"] != float64(0) {
		t.Errorf("stuck: %v bar %v", out["stuck"], out["barAfterStuck"])
	}
	if r, _ := out["retried"].([]any); len(r) != 1 || r[0] != "http://127.0.0.1:47831/status" {
		t.Errorf("retry: %v", out["retried"])
	}
	if out["cleared"] != true || out["pageshow"] != true {
		t.Errorf("cleared %v pageshow %v", out["cleared"], out["pageshow"])
	}
}
