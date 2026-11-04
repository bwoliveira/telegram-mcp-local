package panel

// The chat preview's background refresh, run under node: the whole of
// assets/app.js is evaluated unchanged against a minimal DOM and a fake
// panel API that orders and pages exactly like /api/chats and
// /api/chats/{jid}: newest first by (date, id), at most 100 rows, has_more,
// next_offset, and the before/since message cursors. Every 15s poll must
// read again everything already loaded and show only what the store still
// returns: deleted and expired rows go, edits and transcripts appear, also
// while a search is active; history imported behind the window brings the
// "older" button back; a list paged past 100 chats keeps its place; a
// failure of the list or of the chat is shown, backs off, and never stops
// the other; equal dates keep their order; one list never mixes filters;
// a failed or stale list shows no preview until its window is read again,
// and a failed filter never leaves another filter's rows under the controls.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const refreshHarness = `
"use strict";
const fs = require("fs"), vm = require("vm"), assert = require("assert");
const src = fs.readFileSync(process.argv[2], "utf8");

class Node {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase(); this.children = []; this.parentNode = null; this.attrs = {};
    this.listeners = {}; this._text = ""; this.className = ""; this.hidden = false; this.disabled = false;
    this.value = ""; this.checked = false; this.style = {}; this.scrollTop = 0; this.clientHeight = 100; this.title = "";
    const self = this;
    this.classList = {
      add(c) { if (!self.classList.contains(c)) self.className = (self.className + " " + c).trim(); },
      remove(c) { self.className = self.className.split(" ").filter((x) => x && x !== c).join(" "); },
      toggle(c, on) { if (on === undefined ? !self.classList.contains(c) : on) self.classList.add(c); else self.classList.remove(c); },
      contains(c) { return self.className.split(" ").indexOf(c) >= 0; },
    };
  }
  get scrollHeight() { return 20 * this.children.length; }
  appendChild(c) { c.parentNode = this; this.children.push(c); return c; }
  insertBefore(c, ref) { c.parentNode = this; const i = this.children.indexOf(ref); if (i < 0) this.children.push(c); else this.children.splice(i, 0, c); return c; }
  remove() { if (this.parentNode) { const s = this.parentNode.children; s.splice(s.indexOf(this), 1); this.parentNode = null; } }
  get firstChild() { return this.children[0] || null; }
  get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children = []; this._text = String(v); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  hasAttribute(k) { return k in this.attrs; }
  addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
  fire(type, extra) { const ev = Object.assign({ type, preventDefault() {}, currentTarget: this, target: this }, extra || {}); (this.listeners[type] || []).forEach((fn) => fn(ev)); }
  focus() {} select() {}
  querySelectorAll() { return []; }
}

// The Conversas card: every part the script asks for exists.
const preview = new Node("section");
const parts = {};
preview.querySelector = (sel) => parts[sel] || (parts[sel] = new Node("div"));
const document = new Node("document");
document.documentElement = { lang: "pt-BR", hasAttribute: () => false };
document.hidden = false;
document.createElement = (tag) => new Node(tag);
document.querySelector = (sel) => (sel === "[data-chats]" ? preview : null);
document.querySelectorAll = () => [];

// The fake store: chats newest first, messages oldest first.
const base = Date.parse("2026-11-03T12:00:00Z");
const iso = (minutes) => new Date(base + minutes * 60000).toISOString();
const store = { chats: [], messages: {} };
for (let i = 1; i <= 120; i++) store.chats.push({ jid: "user:" + i, name: "Pessoa " + i, at: iso(-i), last_at: iso(-i), last_text: "oi " + i, messages: 0 });
const msg = (id, minute, extra) => Object.assign({ id, at: iso(minute), text: "texto " + id, from_me: false }, extra || {});
store.messages["user:1"] = [];
for (let i = 1; i <= 70; i++) store.messages["user:1"].push(msg("m" + i, -1000 + i));
store.messages["user:2"] = [];
for (let i = 1; i <= 20; i++) store.messages["user:2"].push(msg("b" + i, -500 + i));

const log = [], violations = [];
let hold = false, failList = false, failThread = false;
const pending = [];
function page(rows, limit, offset) {
  const more = offset + limit < rows.length;
  return { slice: rows.slice(offset, offset + limit), has_more: more, next_offset: more ? offset + limit : 0 };
}
// The fake orders messages exactly like the store, by (date, id), and
// pages them by offset or by the before (exclusive) / since (inclusive)
// cursors, which cannot be combined with an offset.
const secs = (at) => Math.floor(Date.parse(at) / 1000);
const num = (id) => Number(String(id).replace(/^\D+/, ""));
const keyOf = (m) => [secs(m.at), num(m.id)];
const cmp = (a, b) => a[0] - b[0] || a[1] - b[1];
const cursorOf = (m) => keyOf(m).join(":");
const parseCursor = (c) => { const k = /^(\d+):(\d+)$/.exec(c); return k ? [Number(k[1]), Number(k[2])] : null; };
function answer(path) {
  const url = new URL(path, "http://127.0.0.1");
  const sp = url.searchParams;
  const limit = Number(sp.get("limit")), offset = sp.has("offset") ? Number(sp.get("offset")) : 0;
  if (!(limit >= 1 && limit <= 100) || !(offset >= 0)) { violations.push(path); return { status: 400, body: { error: "invalid" } }; }
  const q = sp.get("q") || "";
  if (url.pathname === "/api/chats") {
    if (failList) return { status: 500, body: { error: "synthetic list failure" } };
    const p = page(store.chats.filter((c) => !q || c.name.includes(q)), limit, offset);
    return { status: 200, body: { chats: JSON.parse(JSON.stringify(p.slice)), has_more: p.has_more, next_offset: p.next_offset, history: null } };
  }
  const jid = decodeURIComponent(url.pathname.slice("/api/chats/".length));
  if (failThread) return { status: 500, body: { error: "synthetic thread failure" } };
  if (!store.messages[jid]) return { status: 404, body: { error: "Conversation unavailable" } };
  const before = sp.has("before") ? parseCursor(sp.get("before")) : null, since = sp.has("since") ? parseCursor(sp.get("since")) : null;
  if ((sp.has("before") && !before) || (sp.has("since") && !since) || ((before || since) && sp.has("offset"))) { violations.push(path); return { status: 400, body: { error: "invalid" } }; }
  const all = store.messages[jid].map((m) => Object.assign({}, m, { cursor: cursorOf(m) })).sort((a, b) => cmp(keyOf(b), keyOf(a)));
  // Model app.js's literal FTS phrase, not arbitrary FTS syntax. The real
  // SQLite/guarded HTTP regression lives in conversation_updates_browser_test.
  const text = q.startsWith('"') && q.endsWith('"') ? q.slice(1, -1).replace(/""/g, '"') : q;
  const newest = all.filter((m) => (!text || m.text.includes(text) || (m.transcript || "").includes(text)) &&
    (!before || cmp(keyOf(m), before) < 0) && (!since || cmp(keyOf(m), since) >= 0));
  const p = page(newest, limit, offset);
  return { status: 200, body: { messages: JSON.parse(JSON.stringify(p.slice.reverse())), has_more: p.has_more, next_offset: p.next_offset, oldest: all.length ? all[all.length - 1].at : null } };
}
function fakeFetch(path, options) {
  log.push(path);
  return new Promise((resolve, reject) => {
    const signal = options && options.signal;
    const deliver = () => {
      if (signal && signal.aborted) return;
      const a = answer(path);
      resolve({ ok: a.status === 200, status: a.status, json: () => Promise.resolve(a.body) });
    };
    if (signal) signal.addEventListener("abort", () => { const e = new Error("aborted"); e.name = "AbortError"; reject(e); });
    if (hold) pending.push(deliver); else setImmediate(deliver);
  });
}

const intervals = [];
const window = {
  panelCSRF: { fetch: fakeFetch },
  AbortController,
  setTimeout: () => 0, clearTimeout() {},
  setInterval: (fn, ms) => { intervals.push({ fn, ms }); return intervals.length; }, clearInterval() {},
  addEventListener() {}, location: { hash: "", pathname: "/conversas", search: "", href: "http://127.0.0.1/conversas", origin: "http://127.0.0.1" },
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  localStorage: { getItem: () => null, setItem() {} },
};
const t = (s, ...args) => { let i = 0; return String(s).replace(/%[sd]/g, () => String(args[i++])); };
vm.runInNewContext(src, { window, document, navigator: {}, t, console, URL, AbortController });

const settle = async () => { for (let i = 0; i < 40; i++) await new Promise((r) => setImmediate(r)); };
const list = parts["[data-chat-list]"], thread = parts["[data-thread]"];
const rows = () => list.children.map((li) => li.children[0]).filter((b) => b && b.hasAttribute("data-chat-jid"));
const bubbles = () => thread.children.filter((n) => n.classList.contains("bubble"));
const ids = () => bubbles().map((b) => b.getAttribute("data-message-id"));
const bubble = (id) => bubbles().find((b) => b.getAttribute("data-message-id") === id);
const poll = intervals.find((x) => x.ms === 15000);
const since = (n) => log.slice(n);
const remove = (jid, id) => { const all = store.messages[jid]; all.splice(all.findIndex((m) => m.id === id), 1); };
const find = (jid, id) => store.messages[jid].find((m) => m.id === id);
const checks = [];
const cur = (jid, id) => encodeURIComponent(cursorOf(find(jid, id)));
const threadPath = (jid, extra) => "/api/chats/" + encodeURIComponent(jid) + "?" + extra;
const threadReqs = (n) => since(n).filter((p) => p.startsWith("/api/chats/"));
const listReqs = (n) => since(n).filter((p) => p.startsWith("/api/chats?"));
const flush = async () => { for (let i = 0; i < 6 && pending.length; i++) { pending.splice(0).forEach((f) => f()); await settle(); } };
const scenario = process.argv[3];

`

// refreshScenarios are independent runs of the harness, one per behaviour.
var refreshScenarios = []struct{ name, body string }{
	{"reconcile", `
  assert(poll, "the 15s refresh is scheduled");
  await settle();
  assert.equal(rows().length, 10);
  assert.deepEqual([ids().length, ids()[0], ids()[29]], [30, "m41", "m70"]);
  let mark = log.length;
  parts["[data-thread-older]"].fire("click");
  await settle();
  assert.deepEqual(since(mark), [threadPath("user:1", "limit=30&before=" + cur("user:1", "m41"))], "older pages by cursor, not offset");
  assert.deepEqual([ids().length, ids()[0]], [60, "m11"]);
  for (let i = 0; i < 10; i++) { parts["[data-chats-more]"].fire("click"); await settle(); }
  assert.equal(rows().length, 110);
  assert.equal(parts["[data-chats-more]"].hidden, false);
  checks.push("loaded 110 chats and 60 messages");

  // Deleted (m50) and expired by TTL (m11, the oldest loaded) rows leave the
  // store; m60 is edited and m65 gets its transcript, keeping their IDs.
  const c11 = cur("user:1", "m11"), c12 = cur("user:1", "m12");
  remove("user:1", "m50");
  remove("user:1", "m11");
  find("user:1", "m60").text = "texto editado";
  Object.assign(find("user:1", "m65"), { media: "audio", transcript: "transcrição nova" });
  mark = log.length;
  poll.fn();
  await settle();
  assert.deepEqual(listReqs(mark), ["/api/chats?limit=100&offset=0", "/api/chats?limit=10&offset=100"]);
  // Everything from the oldest shown on, then the two that fill the window.
  assert.deepEqual(threadReqs(mark), [threadPath("user:1", "limit=100&since=" + c11), threadPath("user:1", "limit=2&before=" + c12)]);
  assert.equal(rows().length, 110, "120 chats paged to 110 survive the refresh");
  assert.equal(rows()[109].getAttribute("data-chat-jid"), "user:110");
  assert.equal(parts["[data-chats-more]"].hidden, false);
  assert.equal(parts["[data-chats-count]"].textContent, "110 conversas");
  assert.equal(ids().indexOf("m50"), -1, "deleted message is gone");
  assert.equal(ids().indexOf("m11"), -1, "expired message is gone");
  assert.equal(thread.textContent.indexOf("texto m50"), -1);
  assert.equal(thread.textContent.indexOf("texto m11"), -1);
  assert.deepEqual([ids().length, ids()[0], ids()[59]], [60, "m9", "m70"]);
  assert(bubble("m60").textContent.includes("texto editado"), "edit is drawn");
  assert(bubble("m65").textContent.includes("transcrição nova"), "transcript is drawn");
  checks.push("deletion/expiry removed, edit and transcript drawn, 110 of 120 chats kept");

  // Same IDs, new content only: an edit and a corrected transcript.
  find("user:1", "m61").text = "só editado";
  Object.assign(find("user:1", "m65"), { transcript: "transcrição corrigida", transcript_corrected: true });
  poll.fn();
  await settle();
  assert.deepEqual([ids().length, ids()[0], ids()[59]], [60, "m9", "m70"]);
  assert(bubble("m61").textContent.includes("só editado"), "edit alone is drawn");
  assert(bubble("m65").textContent.includes("transcrição corrigida"), "transcript alone is drawn");
  assert.equal(thread.textContent.indexOf("transcrição nova"), -1);
  checks.push("edit-only and transcript-only refresh drawn");

  // Arrivals do not push loaded history out of the window; a one-row probe
  // keeps the "older" button honest.
  for (let i = 71; i <= 75; i++) store.messages["user:1"].push(msg("m" + i, -1000 + i));
  mark = log.length;
  poll.fn();
  await settle();
  assert.deepEqual(threadReqs(mark), [threadPath("user:1", "limit=100&since=" + cur("user:1", "m9")), threadPath("user:1", "limit=1&before=" + cur("user:1", "m9"))]);
  assert.deepEqual([ids().length, ids()[0], ids()[64]], [65, "m9", "m75"]);
  assert.equal(parts["[data-thread-older]"].hidden, false);
  checks.push("arrivals extend the window instead of dropping loaded history");

  // A refresh still in flight for user:1 is abandoned when user:2 opens.
  hold = true;
  mark = log.length;
  poll.fn();
  await settle();
  assert.deepEqual(threadReqs(mark), [threadPath("user:1", "limit=100&since=" + cur("user:1", "m9"))]);
  rows()[1].fire("click");
  await settle();
  hold = false;
  await flush();
  assert.equal(parts["[data-thread-title]"].textContent, "Pessoa 2");
  assert.deepEqual([ids().length, ids()[0], ids()[19]], [20, "b1", "b20"]);
  assert.equal(rows()[1].getAttribute("aria-current"), "true");
  assert.equal(parts["[data-thread-older]"].hidden, true);
  checks.push("stale refresh for the previous chat never draws");

  // History imported behind the loaded window brings the button back.
  const older = [];
  for (let i = 1; i <= 40; i++) older.push(msg("a" + i, -900 + i));
  store.messages["user:2"] = older.concat(store.messages["user:2"]);
  poll.fn();
  await settle();
  assert.equal(parts["[data-thread-older]"].hidden, false, "imported history shows the older button");
  // A chat that had fewer than a page fills the page, as opening it would.
  assert.deepEqual([ids().length, ids()[0], ids()[29]], [30, "a31", "b20"]);
  mark = log.length;
  parts["[data-thread-older]"].fire("click");
  await settle();
  assert.deepEqual(since(mark), [threadPath("user:2", "limit=30&before=" + cur("user:2", "a31"))]);
  assert.deepEqual([ids().length, ids()[0], ids()[59]], [60, "a1", "b20"]);
  assert.equal(parts["[data-thread-older]"].hidden, true);
  checks.push("imported history reachable after refresh");

  // Losing the complete conversation must not leave unverifiable text visible.
  delete store.messages["user:2"];
  poll.fn();
  await settle();
  assert.equal(bubbles().length, 0, "unavailable conversation clears retained text");
  assert(parts["[data-thread-status]"].textContent.includes("Conversation unavailable"));
  checks.push("unavailable conversation fails closed with visible retry");
`},
	{"search", `
  // F1: an active text/transcript search keeps being reconciled: deleted,
  // expired and no-longer-matching results leave, transcripts update.
  await settle();
  parts["[data-thread-q]"].value = "texto";
  parts["[data-thread-search]"].fire("submit");
  await settle();
  assert.deepEqual([ids().length, ids()[0], ids()[29]], [30, "m41", "m70"]);
  remove("user:1", "m50");                       // deleted
  remove("user:1", "m69");                       // expired by TTL
  find("user:1", "m60").text = "outra coisa";    // edited, no longer matches
  Object.assign(find("user:1", "m65"), { text: "", media: "audio", transcript: "texto transcrito novo" });
  const mark = log.length;
  poll.fn();
  await settle();
  const reqs = threadReqs(mark);
  assert(reqs.length > 0, "the open search is refreshed");
  assert(reqs.every((p) => new URL(p, 'http://127.0.0.1').searchParams.get('q') === '"texto"'), "the refresh keeps the literal search");
  for (const gone of ["m50", "m69", "m60"]) assert.equal(ids().indexOf(gone), -1, gone + " left the results");
  assert.equal(thread.textContent.indexOf("texto m50"), -1);
  assert.equal(thread.textContent.indexOf("texto m69"), -1);
  assert(bubble("m65").textContent.includes("texto transcrito novo"), "changed transcript is drawn");
  assert.deepEqual([ids().length, ids()[0], ids()[29]], [30, "m38", "m70"]);
  assert(parts["[data-thread-meta]"].textContent.includes("30"));
  // Several polls in a row keep refreshing, not only the first one.
  remove("user:1", "m70");
  poll.fn(); await settle();
  assert.equal(ids().indexOf("m70"), -1);
  console.log(JSON.stringify({ scenario: "search", threadPollRequests: reqs.length, deletedVisible: ids().includes("m50"), expiredVisible: ids().includes("m69"), unmatchedVisible: ids().includes("m60") }));
`},
	{"list_failure", `
  // F2: a failing list refresh is visible, hides unverifiable previews and
  // backs off, while the open chat keeps refreshing on its own.
  await settle();
  assert(list.textContent.includes("oi 1"));
  remove("user:1", "m50");
  failList = true;
  let mark = log.length;
  for (let i = 0; i < 4; i++) { poll.fn(); await settle(); }
  assert.equal(ids().indexOf("m50"), -1, "the chat refreshed although the list failed");
  assert.equal(threadReqs(mark).filter((p) => p.includes("&since=")).length, 4, "the chat refresh does not back off for a list failure");
  assert.equal(listReqs(mark).length, 2, "the list backs off: ticks 1 and 3 of 4");
  assert.equal(parts["[data-chats-status]"].hidden, false, "the failure is shown");
  assert(parts["[data-chats-status]"].textContent.includes("synthetic list failure"));
  assert.equal(parts["[data-chats-status]"].getAttribute("role"), "alert");
  assert.equal(rows().length, 10, "chats stay navigable");
  assert.equal(list.textContent.indexOf("oi 1"), -1, "unverifiable previews are hidden");
  assert(parts["[data-thread-status]"].hidden);
  const listFailures = listReqs(mark).length;
  failList = false;
  // Waits 3 ticks after the second failure, then recovers by itself.
  for (let i = 0; i < 4; i++) { poll.fn(); await settle(); }
  assert.equal(listReqs(mark).length, 4, "two ticks skipped, then refreshed");
  assert(parts["[data-chats-status]"].hidden, "recovered list clears the error");
  assert(list.textContent.includes("oi 1"), "previews come back");

  // The chat failing does not stop the list either; it fails closed.
  failThread = true; mark = log.length;
  for (let i = 0; i < 4; i++) { poll.fn(); await settle(); }
  assert.equal(bubbles().length, 0, "a failed chat refresh clears its text");
  assert.equal(parts["[data-thread-status]"].hidden, false);
  assert.equal(listReqs(mark).length, 4, "the list keeps refreshing");
  assert.equal(threadReqs(mark).length, 2, "the chat backs off");
  failThread = false;
  for (let i = 0; i < 4; i++) { poll.fn(); await settle(); }
  assert.equal(ids().length, 30, "the chat recovers by itself");
  assert(parts["[data-thread-status]"].hidden);
  console.log(JSON.stringify({ scenario: "list_failure", deletedVisible: false, errorShown: true, listRequestsIn4Failing: listFailures }));
`},
	{"same_second", `
  // F3: arrivals in the same second as the newest message shown extend the
  // window; they never push out older history already loaded.
  await settle();
  parts["[data-thread-older]"].fire("click"); await settle();
  assert.deepEqual([ids().length, ids()[0]], [60, "m11"]);
  for (const id of ["m71", "m72", "m73"]) store.messages["user:1"].push(msg(id, -1000 + 70));
  poll.fn(); await settle();
  assert.deepEqual([ids().length, ids()[0]], [63, "m11"], "loaded history kept");
  assert.deepEqual(ids().slice(-4), ["m70", "m71", "m72", "m73"]);
  // An arrival older-dated than the newest still counts as an arrival.
  store.messages["user:1"].push(msg("m74", -1000 + 69));
  poll.fn(); await settle();
  assert.deepEqual([ids().length, ids()[0]], [64, "m11"]);
  console.log(JSON.stringify({ scenario: "same_second", shown: ids().length, oldest: ids()[0] }));
`},
	{"ties", `
  // F4: equal dates keep the store's (date, id) order across refresh pages.
  store.messages["user:1"] = Array.from({ length: 140 }, (_, i) => msg("m" + (i + 1), -1000 + i + 1));
  for (let i = 35; i <= 45; i++) find("user:1", "m" + i).at = find("user:1", "m40").at;
  await settle();
  for (let i = 0; i < 3; i++) { parts["[data-thread-older]"].fire("click"); await settle(); }
  const expected = Array.from({ length: 120 }, (_, i) => "m" + (i + 21));
  assert.deepEqual(ids(), expected, "paging older by cursor keeps the order and loses nothing");
  for (let i = 0; i < 3; i++) { poll.fn(); await settle(); assert.deepEqual(ids(), expected, "refresh keeps tied order"); }
  console.log(JSON.stringify({ scenario: "ties", around: ids().slice(18, 22), shown: ids().length }));
`},
	{"filters", `
  // F5: one list refresh never mixes filters; changing a control abandons
  // the refresh in flight and the next requests use the new filter only.
  await settle();
  for (let i = 0; i < 10; i++) { parts["[data-chats-more]"].fire("click"); await settle(); }
  assert.equal(rows().length, 110);
  hold = true;
  let mark = log.length;
  poll.fn(); await settle();
  assert.deepEqual(listReqs(mark), ["/api/chats?limit=100&offset=0"]);
  parts["[data-chat-q]"].value = "Pessoa 12";
  parts["[data-chat-q]"].fire("input");
  hold = false;
  await flush();
  assert.deepEqual(listReqs(mark), ["/api/chats?limit=100&offset=0"], "the obsolete refresh issues no further page");
  assert.equal(rows().length, 110, "the shown list stays as it was read");
  // A tick during the debounce refreshes the shown list with its own filter.
  poll.fn(); await settle();
  assert(listReqs(mark).slice(1).every((p) => !p.includes("q=")), "refresh uses the shown list's filter");
  assert.equal(rows().length, 110);
  // The debounced reset applies the new filter; every later page uses it.
  mark = log.length;
  parts["[data-chat-filters]"].fire("submit"); await settle();
  assert.deepEqual(rows().map((r) => r.getAttribute("data-chat-jid")), ["user:12", "user:120"]);
  poll.fn(); await settle();
  assert(listReqs(mark).every((p) => p.includes("q=Pessoa%2012")), "one filter per list");
  assert.equal(parts["[data-chats-more]"].hidden, true);
  console.log(JSON.stringify({ scenario: "filters", requests: listReqs(mark).length }));
`},
	{"failure_crossings", `
  // F6: the list shows one generation, the rows read with one filter. While
  // it is failed or stale no preview shows, on old or new pages, until one
  // read of that whole window succeeds; a failed new filter never leaves the
  // old filter's rows under the new controls, and every retry, manual or
  // automatic, reads the generation the controls show.
  await settle();
  const status = parts["[data-chats-status]"], more = parts["[data-chats-more]"], q = parts["[data-chat-q]"];
  const jids = () => rows().map((r) => r.getAttribute("data-chat-jid"));
  const lastOf = (jid) => rows().find((r) => r.getAttribute("data-chat-jid") === jid).children[1].children[1].children[0].textContent;
  const previews = () => rows().map((r) => r.children[1].children[1].children[0].textContent).filter(Boolean);
  const retry = () => status.children.find((n) => n.tagName === "BUTTON");
  assert.equal(lastOf("user:1"), "oi 1");

  // A failed refresh hides every preview; opening a chat meanwhile does not
  // bring them back.
  failList = true;
  poll.fn(); await settle();
  assert.deepEqual(previews(), [], "failed refresh hides previews");
  rows()[1].fire("click"); await settle();
  assert.deepEqual(previews(), [], "opening a chat during the failure shows no preview");
  // More chats while still failing adds nothing and keeps the error.
  more.fire("click"); await settle();
  assert.equal(rows().length, 10);
  assert.deepEqual(previews(), []);
  assert.equal(status.hidden, false);
  // The store drops a preview and recovers. More chats reads the whole
  // window again, from the top, before any preview shows.
  store.chats[0].last_text = "";
  failList = false;
  let mark = log.length;
  more.fire("click"); await settle();
  assert.deepEqual(listReqs(mark), ["/api/chats?limit=20&offset=0"], "stale rows are read again, not only the next page");
  assert.equal(rows().length, 20);
  assert.equal(lastOf("user:1"), "", "a deleted preview does not come back");
  assert.equal(lastOf("user:2"), "oi 2");
  assert.equal(lastOf("user:15"), "oi 15");
  assert(status.hidden, "the window read clears the error");
  checks.push("stale list: no preview until the whole window is read again");

  // A new filter that fails drops the old filter's rows: nothing shown can
  // disagree with the controls, and the error stays while it keeps failing.
  failList = true;
  q.value = "Pessoa 12";
  parts["[data-chat-filters]"].fire("submit"); await settle();
  assert.deepEqual(jids(), [], "no rows of another filter under the new controls");
  assert.equal(more.hidden, true, "no next page of another filter");
  assert(status.textContent.includes("synthetic list failure"));
  mark = log.length;
  for (let i = 0; i < 4; i++) { poll.fn(); await settle(); }
  assert(listReqs(mark).length > 0, "the failed filter is retried automatically");
  assert(listReqs(mark).every((p) => p.includes("q=Pessoa%2012")), "automatic retries read the requested filter");
  assert.deepEqual(jids(), []);
  assert.equal(status.hidden, false, "a failing generation keeps its error");
  mark = log.length;
  retry().fire("click"); await settle();
  assert.deepEqual(listReqs(mark), ["/api/chats?limit=10&offset=0&q=Pessoa%2012"], "manual retry reads the requested filter");
  more.fire("click"); await settle();
  assert.deepEqual(listReqs(mark).length, 1, "no next page while nothing is shown");
  failList = false;
  for (let i = 0; i < 8; i++) { poll.fn(); await settle(); }
  assert.deepEqual(jids(), ["user:12", "user:120"], "the requested filter loads by itself");
  assert.equal(q.value, "Pessoa 12");
  assert(status.hidden);
  assert.equal(lastOf("user:12"), "oi 12");
  mark = log.length;
  poll.fn(); await settle();
  assert(listReqs(mark).every((p) => p.includes("q=Pessoa%2012")));
  checks.push("failed filter: no divergent rows, retries read the requested filter");

  // A change of control while the list is stale abandons the refresh in
  // flight but neither the error nor the hidden previews.
  failList = true;
  poll.fn(); await settle();
  assert.deepEqual(previews(), []);
  hold = true;
  mark = log.length;
  for (let i = 0; i < 2; i++) { poll.fn(); await settle(); }
  assert.equal(listReqs(mark).length, 1, "a refresh is in flight");
  q.value = "Pessoa 1";
  q.fire("input");
  hold = false; await flush();
  assert.equal(status.hidden, false, "the stale list keeps its error");
  assert(status.textContent.includes("synthetic list failure"));
  assert.deepEqual(previews(), []);
  // The same filter failing again keeps the rows, stale and without previews.
  q.value = "Pessoa 12";
  parts["[data-chat-filters]"].fire("submit"); await settle();
  assert.deepEqual(jids(), ["user:12", "user:120"]);
  assert.deepEqual(previews(), []);
  assert.equal(status.hidden, false);
  failList = false;
  parts["[data-chat-filters]"].fire("submit"); await settle();
  assert.deepEqual(previews(), ["oi 12", "oi 120"]);
  assert(status.hidden);
  checks.push("control change and same-filter failure keep the list stale");
  console.log(JSON.stringify({ scenario: "failure_crossings", checks: checks.length }));
`},
}

func TestPreviewRefreshReconcilesLoadedWindows(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the preview refresh is not exercised")
	}
	src, err := filepath.Abs(filepath.Join("assets", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range refreshScenarios {
		t.Run(scenario.name, func(t *testing.T) {
			harness := filepath.Join(t.TempDir(), "refresh.js")
			script := refreshHarness + "(async () => {" + scenario.body + `
  assert.deepEqual(violations, [], "every request stays within the API contract");
  if (checks.length) console.log(checks.join("\n"));
})().catch((error) => { console.error(error); process.exitCode = 1; });
`
			if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(node, harness, src).CombinedOutput()
			if err != nil {
				t.Fatalf("preview refresh %s: %v\n%s", scenario.name, err, out)
			}
			t.Logf("%s", out)
		})
	}
}
