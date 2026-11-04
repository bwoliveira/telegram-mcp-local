package panel

// Real SQLite with the real-account gap shape -> production HTTP/session
// boundary -> headless Chromium running the production app.js. Offline: the
// Telegram controller is the fake behind storeBrowserOffline.
import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
)

func TestStatusTerminalGapsInHeadlessBrowser(t *testing.T) {
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
	p, _, eng := phaseFixture(t, "")
	seedTerminalGaps(t, p, eng)
	original := eng.Controller
	eng.Controller = storeBrowserOffline{local: original, status: original.Status, diagnostics: original.Diagnostics}
	inner := http.NewServeMux()
	p.Register(inner)
	var mu sync.Mutex
	var mutations []string
	h := httpserver.Handler(p.Server, httpserver.Options{
		Token: integrationToken, PanelBootstrapSecret: integrationBootstrapSecret, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Register: func(mux *http.ServeMux) {
			mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mu.Lock()
					mutations = append(mutations, r.Method+" "+r.URL.Path)
					mu.Unlock()
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
	evidence := os.Getenv("PANEL_STATUS_BROWSER_EVIDENCE")
	if evidence == "" {
		evidence = dir
	}
	if err := os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(dir, "driver.cjs")
	if err := os.WriteFile(driver, []byte(statusReadinessDriver), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, driver, browser, link.URL, filepath.Join(dir, "profile"), evidence).CombinedOutput()
	if err != nil {
		t.Fatalf("browser assertions: %v\n%s", err, out)
	}
	t.Logf("browser evidence:\n%s", out)
	mu.Lock()
	defer mu.Unlock()
	for _, m := range mutations {
		// The session bootstrap redemption is the only expected non-GET.
		if !strings.HasPrefix(m, "POST /api/session/") && m != "POST /bootstrap" {
			t.Errorf("Status browsing mutated the server: %s", m)
		}
	}
}

const statusReadinessDriver = `
const fs = require('node:fs');
const {spawn} = require('node:child_process');
const assert = require('node:assert/strict');
const [browser, link, profile, evidence] = process.argv.slice(2);
const delay = ms => new Promise(r=>setTimeout(r,ms));
const child = spawn(browser,['--headless=new','--no-sandbox','--disable-gpu','--no-first-run','--disable-extensions','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{stdio:'ignore'});
let ws;
const checks = [], consoleEvents = [], exceptions = [], failures = [];
let pulses = 0;
(async()=>{
 let port;
 for(let i=0;i<200;i++){try{port=fs.readFileSync(profile+'/DevToolsActivePort','utf8').split('\n')[0];break;}catch{} await delay(25);}
 assert(port,'browser debugging endpoint ready');
 const tabs=await (await fetch('http://127.0.0.1:'+port+'/json/list')).json();
 ws=new WebSocket(tabs.find(t=>t.type==='page').webSocketDebuggerUrl);
 await new Promise((resolve,reject)=>{ws.onopen=resolve;ws.onerror=reject;});
 let seq=0;const pending=new Map();
 ws.onmessage = ev=>{const m=JSON.parse(ev.data);if(m.id){const p=pending.get(m.id);pending.delete(m.id);m.error?p.reject(Error(JSON.stringify(m.error))):p.resolve(m.result);}
  if(m.method==='Runtime.exceptionThrown')exceptions.push(m.params.exceptionDetails.text);
  if(m.method==='Runtime.consoleAPICalled')consoleEvents.push({type:m.params.type,text:m.params.args.map(a=>a.value||a.description).join(' ')});
  if(m.method==='Network.responseReceived'){const u=new URL(m.params.response.url);if(u.pathname==='/api/pulse')pulses++;if(m.params.response.status>=400)failures.push({status:m.params.response.status,url:u.pathname});}
 };
 const cdp=(method,params={})=>new Promise((resolve,reject)=>{const id=++seq;pending.set(id,{resolve,reject});ws.send(JSON.stringify({id,method,params}));});
 const ev=async expression=>{const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 const until=async(label,expression,tries=1200)=>{for(let i=0;i<tries;i++){if(await ev(expression)){checks.push(label);return;}await delay(25);}throw Error('timeout: '+label+' DOM='+await ev('document.body.innerText.slice(-2500)'));};
 const quietNav=async where=>{assert.equal(await ev("!!document.querySelector('nav .nav__alert')"),false,where+': Status tab warning mark');assert.notEqual(await ev("document.querySelector('nav').getAttribute('data-live-tone')"),'warn',where+': live tone');checks.push(where+': nav without warning mark');};
 await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Network.enable');
 await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:1100,deviceScaleFactor:1,mobile:false});
 await cdp('Page.navigate',{url:link});
 await until('bootstrap redeemed into authenticated panel',"location.pathname!='/bootstrap' && !!document.querySelector('nav')");
 const base=new URL(link).origin;
 await cdp('Page.navigate',{url:base+'/status'});
 await until('Status hydrated',"document.querySelector('[data-status-snapshot]')?.getAttribute('aria-busy')==='false' && !!document.querySelector('[data-coverage]')");
 assert.equal(await ev("document.querySelector('[data-coverage]').dataset.coverage"),'info');
 assert.match(await ev("document.querySelector('[data-coverage]').textContent"),/Pronto: as atualizações estão em dia\..*42 lacunas.*não comprova histórico completo/s);
 checks.push('coverage is ready-with-partial-history information');
 assert.equal(await ev("document.querySelector('[data-open-gaps]').textContent"),'0');
 assert.equal(await ev("document.querySelector('[data-terminal-gaps]').textContent"),'42');
 assert.equal(await ev("[...document.querySelectorAll('[data-gap-cause]')].map(e=>e.dataset.gapCause).join(',')"),'access_lost,channel_too_long,unknown_baseline');
 assert.equal(await ev("!!document.querySelector('[data-terminal-note]')"),true);
 checks.push('open=0, terminal=42 with causes');
 assert.equal(await ev("document.querySelector('[data-check=coverage]').dataset.checkStatus"),'info');
 assert.equal(await ev("document.querySelectorAll('.problems__warn').length"),0);
 assert.equal(await ev("!!document.querySelector('[data-status-connection] .alert--ok')"),true);
 checks.push('no problems listed; coverage check is information');
 await quietNav('/status hydrated');
 for(let i=0;i<1200 && pulses===0;i++)await delay(25);
 assert(pulses>0,'at least one /api/pulse followed');
 await delay(200);
 await quietNav('/status after pulse');
 await cdp('Page.captureScreenshot',{format:'png'}).then(r=>fs.writeFileSync(evidence+'/status-terminal-gaps.png',Buffer.from(r.data,'base64')));
 for(const path of ['/conta','/funcoes','/receitas']){
  await cdp('Page.navigate',{url:base+path});
  await until(path+' loaded',"location.pathname==="+JSON.stringify(path)+" && !!document.querySelector('nav [data-nav-status]')");
  await quietNav(path);
 }
 assert.deepEqual(exceptions,[],'no runtime exceptions');
 assert.deepEqual(consoleEvents.filter(e=>e.type==='error'),[],'no console errors');
 assert.deepEqual(failures,[],'no HTTP errors');
 const report={checks,pulses,exceptions,console:consoleEvents,httpFailures:failures};
 fs.writeFileSync(evidence+'/status-readiness-assertions.json',JSON.stringify(report,null,2));
 console.log(JSON.stringify(report,null,2));
})().catch(e=>{console.error(e.stack);process.exitCode=1;}).finally(()=>{if(ws)ws.close();child.kill('SIGTERM');});
`
