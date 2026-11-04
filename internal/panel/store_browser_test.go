package panel

// Real SQLite -> panel APIs -> production HTTP/session boundary -> real browser.
// Only latency is injected, before forwarding to the actual handlers. No API
// response or production script is replaced, and no real account is opened.
import (
	"context"
	"encoding/json"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
)

// Any unexpected network operation must fail rather than silently succeeding
// against the fake engine. Only local lifecycle/account snapshots and event
// subscription are delegated; all Telegram reads and mutations remain nil.
type storeBrowserOffline struct {
	engine.Controller
	local       engine.Controller
	status      func() engine.Status
	diagnostics func(context.Context) (engine.Diagnostics, error)
}

func (e storeBrowserOffline) Account(ctx context.Context) (engine.Account, error) {
	return e.local.Account(ctx)
}
func (e storeBrowserOffline) Login() engine.Login { return e.local.Login() }
func (e storeBrowserOffline) Subscribe(ctx context.Context, opts engine.SubscribeOptions) <-chan engine.Event {
	return e.local.Subscribe(ctx, opts)
}
func (e storeBrowserOffline) Status() engine.Status { return e.status() }
func (e storeBrowserOffline) Diagnostics(ctx context.Context) (engine.Diagnostics, error) {
	return e.diagnostics(ctx)
}

func TestStoreBackedPanelInHeadlessBrowser(t *testing.T) {
	browser := chromiumPath()
	if browser == "" {
		t.Skip("Chromium not installed")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node with native WebSocket required")
	}
	if err := exec.Command(node, "-e", "if(typeof WebSocket !== 'function')process.exit(1)").Run(); err != nil {
		t.Skip("Node has no native WebSocket")
	}
	p, _, eng := phaseFixture(t, "") // always 25,000 synthetic messages / 110 dialogs
	original := eng.Controller
	eng.Controller = storeBrowserOffline{local: original, status: original.Status, diagnostics: original.Diagnostics}
	var mu sync.Mutex
	var requests []string
	inner := http.NewServeMux()
	p.Register(inner)
	h := httpserver.Handler(p.Server, httpserver.Options{
		Token: integrationToken, PanelBootstrapSecret: integrationBootstrapSecret, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Register: func(mux *http.ServeMux) {
			mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, r.Method+" "+r.URL.RequestURI())
				mu.Unlock()
				if r.URL.Path == "/api/chats" || strings.HasPrefix(r.URL.Path, "/api/chats/") || strings.HasPrefix(r.URL.Path, "/api/panel/") {
					select {
					case <-time.After(450 * time.Millisecond):
					case <-r.Context().Done():
						return
					}
				}
				inner.ServeHTTP(w, r)
			}))
		},
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	p.Endpoint = func() string { return srv.URL + "/mcp" }
	req, _ := http.NewRequest("POST", srv.URL+"/api/session/bootstrap", strings.NewReader("{}"))
	req.Header.Set(httpserver.PanelBootstrapHeader, integrationBootstrapSecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var link struct {
		URL string `json:"url"`
	}
	err = json.NewDecoder(resp.Body).Decode(&link)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || link.URL == "" {
		t.Fatal("bootstrap link unavailable")
	}
	dir := t.TempDir()
	evidence := os.Getenv("PANEL_STORE_BROWSER_EVIDENCE")
	if evidence == "" {
		evidence = dir
	}
	if err := os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(dir, "driver.cjs")
	if err := os.WriteFile(driver, []byte(storeBrowserDriver), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, driver, browser, link.URL, filepath.Join(dir, "profile"), evidence)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser assertions: %v\n%s", err, out)
	}
	t.Logf("browser DOM/runtime/console evidence:\n%s", out)
	mu.Lock()
	observed := append([]string(nil), requests...)
	mu.Unlock()
	var pages, searches int
	for _, request := range observed {
		if !strings.HasPrefix(request, "GET ") {
			t.Errorf("browse mutated server: %s", request)
		}
		if strings.HasPrefix(request, "GET /api/chats?") {
			pages++
			if !strings.Contains(request, "limit=10") {
				t.Errorf("unbounded chat request: %s", request)
			}
		}
		if u, err := url.Parse(strings.TrimPrefix(request, "GET ")); err == nil && u.Query().Get("q") == `"Synthetic"` {
			searches++
		}
		if strings.Contains(request, "/api/history") {
			t.Errorf("unexpected Telegram history request: %s", request)
		}
	}
	if pages < 11 || searches == 0 {
		t.Errorf("missing real pagination/search: pages=%d searches=%d", pages, searches)
	}
	if eng.store.messageReads.Load() != 0 {
		t.Error("browse materialized all messages")
	}
	history, _, err := eng.store.ReadStatus(ctx, eng.account)
	if err != nil || history.Messages != 25000 || history.Conversations != 110 {
		t.Fatal("browse changed fixture totals")
	}
	for _, shot := range []struct {
		name string
		w, h int
	}{{"conversations-desktop.png", 1440, 1100}, {"conversations-mobile.png", 390, 844}, {"status-loaded.png", 1440, 1100}} {
		f, err := os.Open(filepath.Join(evidence, shot.name))
		if err != nil {
			t.Fatal(err)
		}
		size, err := png.DecodeConfig(f)
		f.Close()
		if err != nil || size.Width != shot.w || size.Height != shot.h {
			t.Fatalf("invalid screenshot %s: %v %v", shot.name, size, err)
		}
	}
	t.Logf("real API reads=%d paginated_lists=%d FTS_requests=%d; full_message_reads=0; browse_writes=0; Telegram controller unavailable", len(observed), pages, searches)
}

const storeBrowserDriver = `
const fs = require('node:fs');
const {spawn} = require('node:child_process');
const assert = require('node:assert/strict');
const [browser, link, profile, evidence] = process.argv.slice(2);
const delay = ms => new Promise(r=>setTimeout(r,ms));
const child = spawn(browser,['--headless=new','--no-sandbox','--disable-gpu','--no-first-run','--disable-extensions','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{stdio:'ignore'});
let ws;
const checks = [], consoleEvents = [], exceptions = [], failures = [];
(async()=>{
 let port;
 for(let i=0;i<200;i++){try{port=fs.readFileSync(profile+'/DevToolsActivePort','utf8').split('\n')[0];break;}catch{} await delay(25);}
 assert(port,'browser debugging endpoint ready');
 const tabs=await (await fetch('http://127.0.0.1:'+port+'/json/list')).json();
 ws=new WebSocket(tabs.find(t=>t.type==='page').webSocketDebuggerUrl);
 await new Promise((resolve,reject)=>{ws.onopen=resolve;ws.onerror=reject;});
 let seq=0;const pending=new Map();
 ws.onmessage = ev=>{const m=JSON.parse(ev.data);if(m.id){const p=pending.get(m.id);pending.delete(m.id);m.error?p.reject(Error(JSON.stringify(m.error))):p.resolve(m.result);}
  if(m.method==='Runtime.exceptionThrown')exceptions.push(m.params.exceptionDetails.text+': '+JSON.stringify(m.params.exceptionDetails.exception));
  if(m.method==='Runtime.consoleAPICalled')consoleEvents.push({type:m.params.type,text:m.params.args.map(a=>a.value||a.description).join(' ')});
  if(m.method==='Network.responseReceived' && m.params.response.status>=400)failures.push({status:m.params.response.status,url:new URL(m.params.response.url).pathname});
 };
 const cdp=(method,params={})=>new Promise((resolve,reject)=>{const id=++seq;pending.set(id,{resolve,reject});ws.send(JSON.stringify({id,method,params}));});
 const ev=async expression=>{const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 const until=async(label,expression)=>{for(let i=0;i<1200;i++){if(await ev(expression)){checks.push(label);return;}await delay(25);}throw Error('timeout: '+label+' DOM='+await ev('document.body.innerText.slice(-2500)'));};
 const click=selector=>ev('document.querySelector('+JSON.stringify(selector)+').click()');
 const viewport=async(width,height)=>{await cdp('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:1,mobile:width<500});assert.deepEqual(await ev('[innerWidth,innerHeight]'),[width,height]);checks.push('viewport '+width+'x'+height);};
 const screenshot=async name=>{const r=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync(evidence+'/'+name,Buffer.from(r.data,'base64'));};
 await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Network.enable');
 await viewport(1440,1100);
 await cdp('Page.navigate',{url:link});
 await until('bootstrap redeemed into authenticated panel',"location.pathname!='/bootstrap' && !!document.querySelector('nav')");
 const base=new URL(link).origin;
 await cdp('Page.navigate',{url:base+'/conta'});
 await until('chat loading feedback',"!!document.querySelector('[data-chats-status] .spinner') && document.querySelector('[data-chat-list]').getAttribute('aria-busy')==='true'");
 await until('first real store page and thread',"document.querySelectorAll('[data-chat-list] [data-chat-jid]').length===10 && document.querySelectorAll('[data-thread] .bubble').length===30");
 await until('account hydrated: 25000 messages / 110 chats',"document.querySelector('[data-account-messages]').textContent.replace(/[^0-9]/g,'')==='25000' && document.querySelector('[data-account-conversations]').textContent==='110'");
 assert.equal(await ev("['/assets/app.js','/assets/cache.js'].every(path=>[...document.scripts].some(s=>new URL(s.src,location.href).pathname===path))"),true);
 checks.push('production app.js and cache.js loaded under real CSP');
 const cookies = await cdp('Network.getCookies',{urls:[base]});
 assert(cookies.cookies.some(c=>c.httpOnly && c.sameSite==='Strict'),'bootstrap HttpOnly strict session cookie');
 checks.push('real HttpOnly SameSite=Strict browser session');
 for(let n=20;n<=110;n+=10){await click('[data-chats-more]');await until('real chat page '+n,"document.querySelectorAll('[data-chat-list] [data-chat-jid]').length==="+n);}
 assert.equal(await ev("new Set([...document.querySelectorAll('[data-chat-jid]')].map(e=>e.dataset.chatJid)).size"),110);
 assert.equal(await ev("document.querySelector('[data-chats-more]').hidden"),true);
 checks.push('110 unique conversations; no more pages');
 await ev("document.querySelectorAll('[data-chat-list] [data-chat-jid]')[1].click()");
 await until('second real thread selected',"document.querySelectorAll('[data-thread] .bubble').length===30 && document.querySelectorAll('[data-chat-jid]')[1].getAttribute('aria-current')==='true' && document.querySelector('[data-thread-title]').textContent.includes('Synthetic') && !document.querySelector('[data-thread-older]').disabled");
 await click('[data-thread-older]');
 await until('older real messages appended',"document.querySelectorAll('[data-thread] .bubble').length===60");
 await ev("document.querySelector('[data-thread-q]').value='Synthetic';document.querySelector('[data-thread-search]').dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}))");
 await until('real FTS Synthetic results',"document.querySelectorAll('[data-thread] .bubble').length===30 && !document.querySelector('[data-thread-clear]').hidden && [...document.querySelectorAll('[data-thread] .bubble')].every(e=>e.textContent.includes('Synthetic performance fixture')) && document.querySelector('[data-thread-meta]').textContent.includes('encontradas')");
 await ev("document.querySelector('[data-chats]').scrollIntoView()");
 await screenshot('conversations-desktop.png');
 await viewport(390,844);
 assert.equal(await ev('document.documentElement.scrollWidth <= innerWidth'),true,'mobile no horizontal document overflow');
 checks.push('mobile no horizontal document overflow');
 await ev("document.querySelector('[data-chats]').scrollIntoView()");
 await screenshot('conversations-mobile.png');
 await viewport(1440,1100);
 await cdp('Page.navigate',{url:base+'/status'});
 await until('Status initial loading state',"!!document.querySelector('[data-status-snapshot][aria-busy=true] [data-snapshot-note] .spinner') && document.querySelectorAll('[data-conversation-coverage] li').length===0");
 await until('Status hydrated all 110 coverage rows',"document.querySelectorAll('[data-conversation-coverage] li').length===110 && document.querySelector('[data-status-snapshot]').getAttribute('aria-busy')==='false'");
 assert.equal(await ev("[...document.querySelectorAll('[data-conversation-coverage] li')].filter(e=>e.textContent.includes('Histórico completo')).length"),110);
 assert.equal(await ev("document.querySelector('[data-account-messages]').textContent.replace(/[^0-9]/g,'')"),'25000');
 assert.equal(await ev("document.querySelector('[data-account-conversations]').textContent"),'110');
 await screenshot('status-loaded.png');
 await cdp('Page.navigate',{url:base+'/conectar/claude-code'});
 await until('client discovery loading feedback',"!!document.querySelector('[data-add-client=claude-code]') && document.querySelector('[data-add-client=claude-code]').previousElementSibling?.textContent.includes('Verificando') && !document.querySelector('[data-add-client=claude-code]').previousElementSibling.hidden");
 await until('client discovery real API completed',"document.querySelector('[data-add-client=claude-code]').previousElementSibling.hidden");
 assert.deepEqual(exceptions,[],'no runtime exceptions');
 assert.deepEqual(consoleEvents.filter(e=>e.type==='error'),[],'no app console errors');
 assert.deepEqual(failures,[],'no HTTP errors');
 const report={checks,exceptions,console:consoleEvents,httpFailures:failures};
 fs.writeFileSync(evidence+'/browser-assertions.json',JSON.stringify(report,null,2));
 console.log(JSON.stringify(report,null,2));
})().catch(e=>{console.error(e.stack);process.exitCode=1;}).finally(()=>{if(ws)ws.close();child.kill('SIGTERM');});
`
