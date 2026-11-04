package panel

// Configurações behind the real HTTP boundary: every mutation the page makes
// (webhooks, retention and cleanup, transcription install, language, one-click
// client setup) needs the browser session and its CSRF token, and is refused
// without them. The fake engine only; nothing here reaches Telegram.

import (
	"context"
	"encoding/json"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/localasr"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"github.com/bwoliveira/telegram-mcp-local/internal/webhook"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// newSettingsPanel is newBrowserPanel with the webhook and media routes the
// daemon registers, and a home of its own so client files are temporary.
func newSettingsPanel(t *testing.T) *browserPanel { return newSettingsPanelWith(t, nil) }

func newSettingsPanelWith(t *testing.T, host Host) *browserPanel {
	return newSettingsPanelConfigured(t, host, nil)
}

func newSettingsPanelConfigured(t *testing.T, host Host, configureASR func(string) *localasr.Engine) *browserPanel {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Windows resolves home and client configuration independently of HOME.
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("HERMES_HOME", filepath.Join(home, ".hermes"))
	t.Setenv("PATH", t.TempDir()) // no claude or codex to run
	eng, err := fake.New(engine.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	st, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var asr *localasr.Engine
	if configureASR != nil {
		asr = configureASR(dir)
	}
	server := mcp.New(mcp.Config{State: st, MediaDir: filepath.Join(dir, "media"), ExportDir: filepath.Join(dir, "exports"), Logger: logger, ASR: asr})
	hooks := webhook.New(st, logger, "test", nil)
	b := &browserPanel{t: t, f: eng.(*fake.Controller)}
	b.p = &Panel{Engine: eng, State: st, Server: server, Token: integrationToken, DataDir: dir, Host: host,
		Desktop: DesktopCommand{Command: "/usr/local/bin/telegram-mcp-bridge"}}
	register := func(mux *http.ServeMux) {
		b.p.Register(mux)
		b.p.RegisterWebhooks(mux, hooks, true)
		httpserver.RegisterMediaAPIs(mux, server, hooks)
	}
	ts := httptest.NewServer(httpserver.Handler(server, httpserver.Options{Token: integrationToken, PanelBootstrapSecret: integrationBootstrapSecret, Register: register, Logger: logger}))
	t.Cleanup(ts.Close)
	b.base = ts.URL
	b.p.Endpoint = func() string { return ts.URL + "/mcp" }
	jar, _ := cookiejar.New(nil)
	b.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return b
}

func TestReviewBearerBootstrapReachesHumanErase(t *testing.T) {
	host := &fakeHost{settings: HostSettings{CanHide: true, Version: "review"}}
	b := newSettingsPanelWith(t, host)
	code, _, body := b.do(http.MethodPost, "/api/session/bootstrap", "application/json", "{}", map[string]string{"Authorization": "Bearer " + integrationToken})
	if code == http.StatusForbidden {
		return
	}
	var link struct{ URL string }
	if code != http.StatusOK || json.Unmarshal([]byte(body), &link) != nil || link.URL == "" {
		b.t.Fatalf("bootstrap: %d %s", code, body)
	}
	u, _ := url.Parse(link.URL)
	if code, _, body = b.do(http.MethodGet, u.RequestURI(), "", "", map[string]string{"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "navigate"}); code != http.StatusSeeOther {
		b.t.Fatalf("exchange: %d %s", code, body)
	}
	code, _, body = b.do(http.MethodGet, "/api/session", "", "", nil)
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &session) != nil || session.CSRF == "" {
		b.t.Fatalf("session: %d %s", code, body)
	}
	b.csrf = session.CSRF
	code, _, body = b.page(http.MethodPost, "/configuracoes/apagar", "application/x-www-form-urlencoded", "confirm=apagar", true)
	_, _, erased := host.counts()
	t.Fatalf("bearer minted a human session: erase_status=%d erase_effects=%d body=%s", code, erased, body)
}

func TestIntegrationSettingsMutationsNeedSessionAndCSRF(t *testing.T) {
	b := newSettingsPanel(t)
	defer i18n.Set(i18n.Source)
	const js = "application/json"
	mutations := []struct{ method, path, body string }{
		{"POST", "/api/webhooks", `{"url":"http://127.0.0.1:9/telegram","events":["message"],"allow_private_network":true}`},
		{"POST", "/api/media/retention", `{"days":7}`},
		{"POST", "/api/media/purge", `{"what":"exports","older_than_days":0,"confirm":true}`},
		{"POST", "/api/idioma", `{"language":"es"}`},
		{"POST", "/api/clients/claude-desktop", `{}`},
		{"POST", "/api/asr/install", `{}`},
	}
	// Without any session: the bearer-less browser is turned away.
	for _, m := range mutations {
		if code, _, _ := b.do(m.method, m.path, js, m.body, map[string]string{"Origin": b.base, "Sec-Fetch-Site": "same-origin"}); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session: %d", m.method, m.path, code)
		}
	}
	b.login()
	// With a session but no token, or a wrong one, or cross-site: refused.
	for _, m := range mutations {
		if code, _, _ := b.page(m.method, m.path, js, m.body, false); code != http.StatusForbidden {
			t.Errorf("%s %s without CSRF: %d", m.method, m.path, code)
		}
		if code, _, _ := b.do(m.method, m.path, js, m.body, map[string]string{"Origin": b.base, "Sec-Fetch-Site": "same-origin", "X-CSRF-Token": b.csrf + "x"}); code != http.StatusForbidden {
			t.Errorf("%s %s with a wrong CSRF: %d", m.method, m.path, code)
		}
		if code, _, _ := b.do(m.method, m.path, js, m.body, map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site", "X-CSRF-Token": b.csrf}); code != http.StatusForbidden {
			t.Errorf("%s %s cross-site: %d", m.method, m.path, code)
		}
	}
	if _, err := os.Stat(DesktopConfigPath()); !os.IsNotExist(err) {
		t.Fatal("a refused request wrote the Claude Desktop configuration")
	}
	if code, _, body := b.page("GET", "/api/webhooks", "", "", false); code != 200 || !strings.Contains(body, `"webhooks":[]`) {
		t.Fatalf("a refused request created a webhook: %d %s", code, body)
	}

	// With the token: each works.
	code, _, body := b.page("POST", "/api/webhooks", js, mutations[0].body, true)
	var created struct {
		Webhook struct {
			ID string `json:"id"`
		} `json:"webhook"`
		Secret string `json:"secret"`
		Note   string `json:"note"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &created) != nil || created.Webhook.ID == "" || created.Secret == "" {
		t.Fatalf("create webhook: %d %s", code, body)
	}
	if !strings.Contains(created.Note, "X-Telegram-MCP-Signature") {
		t.Errorf("the webhook note names %q", created.Note)
	}
	id := created.Webhook.ID
	for _, m := range []struct{ method, path, body string }{
		{"PATCH", "/api/webhooks/" + id, `{"enabled":false}`},
		{"DELETE", "/api/webhooks/" + id, ``},
	} {
		if code, _, _ := b.page(m.method, m.path, js, m.body, false); code != http.StatusForbidden {
			t.Errorf("%s %s without CSRF: %d", m.method, m.path, code)
		}
		if code, _, body := b.page(m.method, m.path, js, m.body, true); code != 200 {
			t.Errorf("%s %s with CSRF: %d %s", m.method, m.path, code, body)
		}
	}
	if code, _, body := b.page("POST", "/api/media/retention", js, `{"days":7}`, true); code != 200 || !strings.Contains(body, `"retention_days":7`) {
		t.Errorf("retention: %d %s", code, body)
	}
	if code, _, body := b.page("POST", "/api/media/purge", js, `{"what":"exports","older_than_days":0,"confirm":true}`, true); code != 200 || !strings.Contains(body, `"deleted_files":0`) {
		t.Errorf("purge exports: %d %s", code, body)
	}
	if code, _, body := b.page("POST", "/api/idioma", js, `{"language":"es"}`, true); code != 200 {
		t.Errorf("language: %d %s", code, body)
	}
}

// One-click setup keeps a copy of the client's file before changing it, and
// writes this MCP under the name "telegram".
func TestIntegrationOneClickDesktopKeepsABackup(t *testing.T) {
	b := newSettingsPanel(t)
	b.login()
	path := DesktopConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := `{"mcpServers":{"other":{"command":"other-mcp"}},"theme":"dark"}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, body := b.page("POST", "/api/clients/claude-desktop", "application/json", `{}`, true); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("add Claude Desktop: %d %s", code, body)
	}
	backups, _ := filepath.Glob(path + ".bak-*")
	if len(backups) != 1 {
		t.Fatalf("backups: %v", backups)
	}
	if raw, _ := os.ReadFile(backups[0]); string(raw) != before {
		t.Fatalf("the backup is not the file as it was: %s", raw)
	}
	cfg, err := readJSONConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	servers := cfg["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok || cfg["theme"] != "dark" {
		t.Fatalf("the rest of the configuration was lost: %v", cfg)
	}
	if entry, ok := servers[ServerName].(map[string]any); !ok || entry["command"] != "/usr/local/bin/telegram-mcp-bridge" {
		t.Fatalf("telegram entry: %v", servers[ServerName])
	}
}

func TestBackupConfig(t *testing.T) {
	dir := t.TempDir()
	if got, err := backupConfig(filepath.Join(dir, "missing.json")); err != nil || got != "" {
		t.Fatalf("missing file: %q %v", got, err)
	}
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := backupConfig(path)
	if err != nil || !strings.HasPrefix(got, path+".bak-") {
		t.Fatalf("backup: %q %v", got, err)
	}
	if raw, _ := os.ReadFile(got); string(raw) != `{"a":1}` {
		t.Fatalf("backup content %q", raw)
	}
	if info, _ := os.Stat(got); service.CheckPrivateDataPath(got, false) != nil {
		t.Fatalf("backup mode %v", info.Mode())
	}
}

// Hermes is configured by text: the YAML names the server and reads the
// token from Hermes' own environment file, never inline.
func TestHermesSetupKeepsTheTokenOutOfTheYAML(t *testing.T) {
	p := &Panel{Token: "synthetic-token-for-hermes-test", Endpoint: func() string { return "http://127.0.0.1:47999/mcp" }}
	y := p.hermesYAML()
	for _, want := range []string{"mcp_servers:", "  telegram:", `url: "http://127.0.0.1:47999/mcp"`, `Authorization: "Bearer ${TELEGRAM_MCP_TOKEN}"`} {
		if !strings.Contains(y, want) {
			t.Errorf("Hermes YAML lacks %q:\n%s", want, y)
		}
	}
	if strings.Contains(y, p.Token) {
		t.Error("the token is inline in the YAML")
	}
	if p.hermesEnv() != "TELEGRAM_MCP_TOKEN="+p.Token {
		t.Errorf("env line %q", p.hermesEnv())
	}
	conns := []Connection{{Key: "client:hermes", Live: true}, {Key: "client:windsurf", Configured: true}}
	if s := toolState(guideByKey(t, "hermes"), conns); s != "live" {
		t.Errorf("hermes state %q", s)
	}
	if s := toolState(guideByKey(t, "outra"), conns); s != "configured" {
		t.Errorf("outra state %q", s)
	}
}

func guideByKey(t *testing.T, key string) guide {
	t.Helper()
	for _, g := range guides {
		if g.Key == key {
			return g
		}
	}
	t.Fatalf("no guide %q", key)
	return guide{}
}

func TestLocalWebhookExamplesRequirePrivateOptIn(t *testing.T) {
	api, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{"panel": webhookDocsSource, "API": string(api)} {
		t.Run(name, func(t *testing.T) {
			local := 0
			for _, match := range regexp.MustCompile(`-d '([^']+)'`).FindAllStringSubmatch(html.UnescapeString(source), -1) {
				var payload map[string]any
				if err := json.Unmarshal([]byte(match[1]), &payload); err != nil {
					t.Fatalf("invalid example JSON: %v", err)
				}
				if url, _ := payload["url"].(string); strings.HasPrefix(url, "http://127.0.0.1:") {
					local++
					if payload["allow_private_network"] != true {
						t.Errorf("local creation example lacks explicit opt-in: %s", match[1])
					}
				}
			}
			if local == 0 {
				t.Fatal("no local creation example exercised")
			}
			if !strings.Contains(source, `"enabled":true,"allow_private_network":true`) {
				t.Error("missing local re-enable example with explicit opt-in")
			}
		})
	}
}

// Exercise the production settings script, translations, session/CSRF and real
// webhook persistence in Chromium. All addresses are loopback and ephemeral;
// the fake engine never connects to Telegram or opens a user's data directory.
func TestSettingsPrivateNetworkOptInBrowser(t *testing.T) {
	browser := chromiumPath()
	node, err := exec.LookPath("node")
	if browser == "" || err != nil {
		t.Skip("Chromium and Node required")
	}
	if err := exec.Command(node, "-e", "if(typeof WebSocket !== 'function')process.exit(1)").Run(); err != nil {
		t.Skip("Node with native WebSocket required")
	}
	path := os.Getenv("PATH")
	defer i18n.Set(i18n.Source)
	for _, lang := range []string{"pt-BR", "en", "es"} {
		t.Run(lang, func(t *testing.T) {
			b := newSettingsPanel(t)
			if err := b.p.State.SetSetting(t.Context(), setupSetting, "done"); err != nil {
				t.Fatal(err)
			}
			i18n.Set(lang)
			code, _, body := b.do("POST", "/api/webhooks", "application/json",
				`{"url":"`+b.base+`/receiver","events":["message"],"enabled":false}`, map[string]string{"Authorization": "Bearer " + integrationToken})
			var seeded struct{ Webhook webhook.Status }
			if code != 200 || json.Unmarshal([]byte(body), &seeded) != nil || seeded.Webhook.ID == "" {
				t.Fatalf("seed disabled legacy hook: %d %s", code, body)
			}
			code, _, body = b.do("POST", "/api/session/bootstrap", "application/json", "{}", map[string]string{httpserver.PanelBootstrapHeader: integrationBootstrapSecret})
			var link struct{ URL string }
			if code != 200 || json.Unmarshal([]byte(body), &link) != nil || link.URL == "" {
				t.Fatal("bootstrap link unavailable")
			}
			dir := t.TempDir()
			driver := filepath.Join(dir, "private-network.cjs")
			if err := os.WriteFile(driver, []byte(settingsPrivateNetworkDriver), 0600); err != nil {
				t.Fatal(err)
			}
			label := i18n.T("Permitir destinos locais e de rede privada")
			warning := i18n.T("Isso permite enviar mensagens para serviços neste computador ou na rede privada (incluindo loopback e link-local). Marque apenas se você confia no destino.")
			if lang != i18n.Source && (label == i18n.In(i18n.Source, "Permitir destinos locais e de rede privada") || warning == i18n.In(i18n.Source, "Isso permite enviar mensagens para serviços neste computador ou na rede privada (incluindo loopback e link-local). Marque apenas se você confia no destino.")) {
				t.Error("private-network label or warning is not translated")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, driver, browser, link.URL, filepath.Join(dir, "profile"), label, warning)
			cmd.Env = append(os.Environ(), "PATH="+path)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("browser assertions: %v\n%s", err, out)
			}
			t.Logf("%s", out)
			code, _, body = b.do("GET", "/api/webhooks/"+seeded.Webhook.ID, "", "", map[string]string{"Authorization": "Bearer " + integrationToken})
			var got webhook.Status
			if code != 200 || json.Unmarshal([]byte(body), &got) != nil || !got.Enabled || !got.AllowPrivateNetwork {
				t.Fatalf("re-enabled hook did not persist opt-in: %d %s", code, body)
			}
		})
	}
}

const settingsPrivateNetworkDriver = `
const fs = require('node:fs');
const {spawn} = require('node:child_process');
const assert = require('node:assert/strict');
const [browser, link, profile, label, warning] = process.argv.slice(2);
const delay = ms => new Promise(r => setTimeout(r, ms));
const child = spawn(browser, ['--headless=new','--no-sandbox','--disable-gpu','--no-first-run','--disable-extensions','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'], {stdio:'ignore'});
let ws;
(async () => {
  let port;
  for (let i=0; i<200; i++) { try { port=fs.readFileSync(profile+'/DevToolsActivePort','utf8').split('\n')[0]; break; } catch {} await delay(25); }
  assert(port, 'Chromium debugging endpoint ready');
  const tabs = await (await fetch('http://127.0.0.1:'+port+'/json/list')).json();
  ws = new WebSocket(tabs.find(t => t.type==='page').webSocketDebuggerUrl);
  await new Promise((resolve,reject) => { ws.onopen=resolve; ws.onerror=reject; });
  let seq=0; const pending=new Map();
  ws.onmessage = e => { const m=JSON.parse(e.data); if(m.id) { const p=pending.get(m.id); pending.delete(m.id); m.error?p.reject(Error(JSON.stringify(m.error))):p.resolve(m.result); } };
  const cdp=(method,params={}) => new Promise((resolve,reject) => { const id=++seq; pending.set(id,{resolve,reject}); ws.send(JSON.stringify({id,method,params})); });
  const ev=async expression => { const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true}); if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails)); return r.result.value; };
  const until=async (name,expression) => { for(let i=0;i<200;i++) { if(await ev(expression))return; await delay(25); } throw Error('timeout: '+name+' '+await ev('location.href+" "+document.body.innerText.slice(0,1500)')); };
  const click=selector => ev('document.querySelector('+JSON.stringify(selector)+').click()');
  await cdp('Page.enable');
  await cdp('Page.navigate',{url:link});
  await until('session bootstrap',"location.pathname!='/bootstrap' && !!document.querySelector('nav')");
  const base=new URL(link).origin;
  await cdp('Page.navigate',{url:base+'/configuracoes'});
  await until('settings loaded',"!!document.querySelector('[data-webhooks-list] li') && !!window.panelCSRF");
  // Observe actual payloads without replacing fetch, CSRF or the API handlers.
  await ev("window.sent=[]; const send=window.panelCSRF.fetch.bind(window.panelCSRF); window.panelCSRF.fetch=(url,options)=>{if(options.method!=='GET')sent.push({url,body:JSON.parse(options.body)});return send(url,options);}");
  const box='[data-webhook-form] [data-webhook-private]';
  assert.equal(await ev('!!document.querySelector('+JSON.stringify(box)+')'),true,'creation needs an explicit private-network checkbox');
  assert.equal(await ev('document.querySelector('+JSON.stringify(box)+').checked'),false,'creation must default to no opt-in');
  const text=await ev('document.querySelector('+JSON.stringify(box)+').closest("label").textContent');
  assert(text.includes(label) && text.includes(warning),'localized creation label and warning');
  await ev("document.querySelector('[data-webhook-url]').value="+JSON.stringify(base+'/created'));
  await ev("document.querySelector('[data-webhook-form]').requestSubmit()");
  await until('unchecked local creation rejected',"!document.querySelector('[data-webhook-error]').hidden && !document.querySelector('button[type=submit]').disabled");
  assert.equal(await ev('sent.at(-1).body.allow_private_network'),false,'unchecked checkbox must submit false');
  assert.equal(await ev("document.querySelectorAll('[data-webhooks-list] li').length"),1,'rejected creation must not add a hook');
  await click(box);
  await ev("document.querySelector('[data-webhook-form]').requestSubmit()");
  await until('opted-in creation persisted',"document.querySelectorAll('[data-webhooks-list] li').length===2 && !document.querySelector('[data-webhook-secret]').hidden");
  assert.equal(await ev('sent.at(-1).body.allow_private_network'),true,'checked checkbox must submit true');
  assert.equal(await ev('document.querySelector('+JSON.stringify(box)+').checked'),false,'successful creation resets consent');
  const hooks=await ev("panelCSRF.fetch('/api/webhooks',{method:'GET'}).then(r=>r.json()).then(d=>d.webhooks)");
  const created=hooks.find(h=>h.url===base+'/created');
  assert(created && created.enabled && created.allow_private_network,'creation permission read back from real API');
  await ev('window.legacyRow=Array.from(document.querySelectorAll("[data-webhooks-list] li")).find(li=>li.textContent.includes('+JSON.stringify(base+'/receiver')+'));');
  const rowBox='legacyRow.querySelector("[data-webhook-private]")';
  assert.equal(await ev('!!'+rowBox),true,'disabled hook needs private-network consent control');
  assert.equal(await ev(rowBox+'.checked'),false,'legacy local URL must not automatically grant consent');
  const rowText=await ev(rowBox+'.closest("label").textContent');
  assert(rowText.includes(label) && rowText.includes(warning),'localized re-enable label and warning');
  await ev('legacyRow.querySelectorAll("button")[1].click()');
  await until('unchecked re-enable rejected','sent.at(-1).body.enabled===true && !legacyRow.querySelectorAll("button")[1].disabled');
  assert.equal(await ev('sent.at(-1).body.allow_private_network'),false,'re-enable submits false without consent');
  await ev(rowBox+'.click(); legacyRow.querySelectorAll("button")[1].click()');
  await until('legacy hook re-enabled',"document.querySelectorAll('[data-webhooks-list] [data-webhook-private]').length===0");
  assert.deepEqual(await ev('sent.at(-1).body'),{enabled:true,allow_private_network:true});
  // Disabling must preserve the stored permission; re-enabling shows that
  // existing opt-in, not a new grant inferred from the destination URL.
  await ev('Array.from(document.querySelectorAll("[data-webhooks-list] li")).find(li=>li.textContent.includes('+JSON.stringify(base+'/receiver')+')).querySelectorAll("button")[1].click()');
  await until('disabled opted-in row rendered',"!!document.querySelector('[data-webhooks-list] [data-webhook-private]')");
  assert.deepEqual(await ev('sent.at(-1).body'),{enabled:false});
  assert.equal(await ev("document.querySelector('[data-webhooks-list] [data-webhook-private]').checked"),true,'stored permission is reflected');
  await ev("document.querySelector('[data-webhooks-list] [data-webhook-private]').closest('li').querySelectorAll('button')[1].click()");
  await until('re-enabled with stored permission',"document.querySelectorAll('[data-webhooks-list] [data-webhook-private]').length===0");
  await cdp('Page.navigate',{url:base+'/webhooks/documentacao'});
  await until('documentation loaded',"!!document.querySelector('#exemplo-programa')");
  assert((await ev('document.body.textContent')).includes('allow_private_network'),'local receiver documentation explains the opt-in');
  console.log('PASS: localized unchecked creation/re-enable controls; false/true submissions; rejected unconsented writes; persisted consent; reset; disable preserves permission; local docs');
})().catch(e=>{console.error(e.stack);process.exitCode=1;}).finally(()=>{if(ws)ws.close();child.kill('SIGKILL');});
`
