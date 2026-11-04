package panel

// Production SQLite readers, HTTP guards, bootstrap/session and app.js in an
// isolated Chromium. Mutations are applied by the Go test directly to synthetic
// Store data over a child-process pipe, never through a panel or Telegram API.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
)

func TestConversationSearchAndUpdatesInStoreBackedBrowser(t *testing.T) {
	browser := chromiumPath()
	if browser == "" {
		t.Skip("Chromium not installed")
	}
	node, err := exec.LookPath("node")
	if err != nil || exec.Command(node, "-e", "if(typeof WebSocket !== 'function')process.exit(1)").Run() != nil {
		t.Skip("Node with native WebSocket required")
	}
	p, _, eng := phaseFixture(t, "")
	original := eng.Controller
	eng.Controller = storeBrowserOffline{local: original, status: original.Status, diagnostics: original.Diagnostics}
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 999}
	base := time.Now().Add(time.Minute).Truncate(time.Second)
	texts := []string{
		"plain original nonlatest", "plain delete nonlatest", "panel-probe-20261103 original",
		"panel-probe-20261103 retained", `latest unrelated: café "quoted" OR sender:needle (test)*`,
	}
	messages := make([]engine.Message, len(texts))
	for i, text := range texts {
		messages[i] = engine.Message{Key: engine.MessageKey{Account: p.AccountID, Peer: peer, ID: i + 1}, Date: base.Add(time.Duration(i) * time.Second), Text: text}
	}
	apply := func(batch engine.Batch) (engine.Applied, error) {
		batch.Account, batch.Source = p.AccountID, engine.SourceUpdate
		return eng.store.Apply(t.Context(), batch)
	}
	if _, err := apply(engine.Batch{
		Peers:   []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Kind: engine.KindSupergroup, Title: "Synthetic search regression"}}},
		Dialogs: []engine.Dialog{{Peer: peer, UnreadCount: 7}}, Messages: messages,
	}); err != nil {
		t.Fatal(err)
	}
	h := httpserver.Handler(p.Server, httpserver.Options{Token: integrationToken, PanelBootstrapSecret: integrationBootstrapSecret, Register: p.Register, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
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
	var link struct{ URL string }
	err = json.NewDecoder(resp.Body).Decode(&link)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || link.URL == "" {
		t.Fatal("bootstrap unavailable")
	}
	dir := t.TempDir()
	evidence := os.Getenv("PANEL_UPDATES_BROWSER_EVIDENCE")
	if evidence == "" {
		evidence = dir
	}
	if err := os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(dir, "driver.cjs")
	if err := os.WriteFile(driver, []byte(conversationUpdatesDriver), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, driver, browser, link.URL, filepath.Join(dir, "profile"), evidence)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	mutations, reports := 0, 0
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var event struct {
			Mutation string `json:"mutation"`
			Report   bool   `json:"report"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Error("invalid browser protocol")
			break
		}
		if event.Report {
			reports++
			t.Logf("browser evidence: %s", scanner.Text())
			continue
		}
		var batch engine.Batch
		switch event.Mutation {
		case "plain-edit":
			messages[0].Text = "plain edited nonlatest"
			batch.Edits = []engine.Message{messages[0]}
		case "plain-delete":
			batch.Deletes = []engine.Deletion{{Peer: &peer, IDs: []int{2}}}
		case "search-edit":
			messages[2].Text = "panel-probe-20261103 edited search"
			batch.Edits = []engine.Message{messages[2]}
		case "search-unmatch":
			messages[2].Text = "no longer matching"
			batch.Edits = []engine.Message{messages[2]}
		case "search-delete":
			batch.Deletes = []engine.Deletion{{Peer: &peer, IDs: []int{4}}}
		default:
			t.Errorf("unknown mutation %q", event.Mutation)
		}
		for i := range batch.Edits {
			edited := base.Add(time.Duration(100+mutations) * time.Second)
			batch.Edits[i].EditDate = &edited
		}
		applied, err := apply(batch)
		if err != nil || len(applied.Edited)+len(applied.Deleted) != 1 {
			t.Errorf("synthetic mutation %s: %+v %v", event.Mutation, applied, err)
		}
		mutations++
		fmt.Fprintln(stdin, `{"applied":true}`)
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("browser assertions: %v\n%s", err, stderr.String())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if mutations != 5 || reports != 1 {
		t.Fatalf("incomplete browser exercise: mutations=%d reports=%d", mutations, reports)
	}
}

const conversationUpdatesDriver = `
const fs = require('node:fs');
const {spawn} = require('node:child_process');
const assert = require('node:assert/strict');
const readline = require('node:readline');
const [browser, link, profile, evidence] = process.argv.slice(2);
const delay = ms => new Promise(r=>setTimeout(r,ms));
const child = spawn(browser,['--headless=new','--no-sandbox','--disable-gpu','--no-first-run','--disable-extensions','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{stdio:'ignore'});
const input = readline.createInterface({input:process.stdin});
let ws;
const checks=[], http=[], exceptions=[], errors=[], updates=[];
(async()=>{
 let port;
 for(let i=0;i<200;i++){try{port=fs.readFileSync(profile+'/DevToolsActivePort','utf8').split('\n')[0];break;}catch{} await delay(25);}
 assert(port,'browser debugging endpoint ready');
 const tabs=await (await fetch('http://127.0.0.1:'+port+'/json/list')).json();
 ws=new WebSocket(tabs.find(t=>t.type==='page').webSocketDebuggerUrl);
 await new Promise((resolve,reject)=>{ws.onopen=resolve;ws.onerror=reject;});
 let seq=0;const pending=new Map();
 ws.onmessage=ev=>{const m=JSON.parse(ev.data);if(m.id){const p=pending.get(m.id);pending.delete(m.id);m.error?p.reject(Error(JSON.stringify(m.error))):p.resolve(m.result);}
  if(m.method==='Runtime.exceptionThrown')exceptions.push(m.params.exceptionDetails.text);
  if(m.method==='Runtime.consoleAPICalled' && m.params.type==='error')errors.push(m.params.args.map(a=>a.value||a.description).join(' '));
  if(m.method==='Network.responseReceived'){const r=m.params.response,u=new URL(r.url);if(u.pathname.startsWith('/api/chats'))http.push({path:u.pathname,query:u.searchParams.get('q'),status:r.status});}
 };
 const cdp=(method,params={})=>new Promise((resolve,reject)=>{const id=++seq;pending.set(id,{resolve,reject});ws.send(JSON.stringify({id,method,params}));});
 const ev=async expression=>{const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 const until=async(label,expression)=>{for(let i=0;i<900;i++){if(await ev(expression)){checks.push(label);return;}await delay(25);}throw Error('timeout: '+label+' thread='+await ev('document.querySelector("[data-thread]")?.innerText')+' status='+await ev('document.querySelector("[data-thread-status]")?.innerText'));};
 const screenshot=async name=>{await ev("document.querySelector('[data-chats]').scrollIntoView({block:'start'})");const r=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync(evidence+'/'+name+'.png',Buffer.from(r.data,'base64'));};
 const search=async query=>{await ev('document.querySelector("[data-thread-q]").value='+JSON.stringify(query)+';document.querySelector("[data-thread-search]").dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}))');};
 const bubble=id=>'document.querySelector(\'[data-thread] .bubble[data-message-id="'+id+'"]\')';
 const mutate=async(name,condition)=>{
  const ack=new Promise(resolve=>input.once('line',line=>{assert.equal(JSON.parse(line).applied,true);resolve();}));
  const start=Date.now();console.log(JSON.stringify({mutation:name}));await ack;
  await until(name+' in message-area DOM by natural 15s poll',condition);
  updates.push({mutation:name,observedMs:Date.now()-start,scope:'[data-thread] .bubble'});
  await screenshot(name);
 };
 await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Network.enable');
 await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:1100,deviceScaleFactor:1,mobile:false});
 await cdp('Page.navigate',{url:link});
 await until('authenticated bootstrap',"location.pathname!='/bootstrap' && !!document.querySelector('nav')");
 const base=new URL(link).origin;
 await cdp('Page.navigate',{url:base+'/conta'});
 await until('synthetic conversation open',"document.querySelector('[data-thread-title]')?.textContent==='Synthetic search regression' && document.querySelectorAll('[data-thread] .bubble').length===5");
 // The latest list preview never contains the edited/deleted messages below.
 assert.equal(await ev("document.querySelector('[data-chat-list] [data-chat-jid=\"channel:999\"] .chat__last').textContent.includes('latest unrelated')"),true);
 await search('panel-probe-20261103');
 await until('hyphenated text search succeeds through real guarded HTTP',"document.querySelectorAll('[data-thread] .bubble').length===2 && document.querySelector('[data-thread-status]').hidden && document.querySelector('[data-thread-meta]').textContent.includes('encontradas')");
 await screenshot('hyphen-search');
 for(const query of ['café "quoted"','OR sender:needle (test)*','"','---']){
  const mark=http.length;
  await search(query);
  await until('literal punctuation search '+query,"document.querySelector('[data-thread]').getAttribute('aria-busy')!=='true' && document.querySelector('[data-thread-status]').hidden");
  assert(http.slice(mark).some(r=>r.path==='/api/chats/channel%3A999' || r.path==='/api/chats/channel:999'));
  if(query==='"' || query==='---')assert.equal(await ev("document.querySelectorAll('[data-thread] .bubble').length"),0,'tokenless input must not list all content');
  else assert.equal(await ev("document.querySelectorAll('[data-thread] .bubble').length"),1,'literal text is not interpreted as operators');
 }
 await ev("document.querySelector('[data-thread-clear]').click()");
 await until('unfiltered thread restored',"document.querySelectorAll('[data-thread] .bubble').length===5 && document.querySelector('[data-thread-status]').hidden");
 await mutate('plain-edit',bubble(1)+"?.querySelector('.bubble__text')?.textContent==='plain edited nonlatest'");
 assert.equal(await ev("document.querySelector('[data-thread]').textContent.includes('plain original nonlatest')"),false);
 await mutate('plain-delete','!'+bubble(2)+" && document.querySelectorAll('[data-thread] .bubble').length===4");
 await search('panel-probe-20261103');
 await until('search reopened with two results',"document.querySelectorAll('[data-thread] .bubble').length===2 && document.querySelector('[data-thread-status]').hidden");
 await mutate('search-edit',bubble(3)+"?.querySelector('.bubble__text')?.textContent==='panel-probe-20261103 edited search'");
 await mutate('search-unmatch','!'+bubble(3)+" && document.querySelectorAll('[data-thread] .bubble').length===1");
 await mutate('search-delete','!'+bubble(4)+" && document.querySelectorAll('[data-thread] .bubble').length===0 && document.querySelector('[data-thread] .empty')?.textContent.includes('Nenhuma mensagem')");
 assert.equal(await ev("document.querySelector('[data-thread-q]').value"),'panel-probe-20261103','search remains open');
 assert.equal(await ev("document.querySelector('[data-thread-clear]').hidden"),false);
 assert.equal(await ev("document.querySelector('[data-chat-list] [data-chat-jid=\"channel:999\"] .chat__badge').textContent"),'7','no read receipt');
 assert.deepEqual(http.filter(r=>r.status!==200),[],'no API errors');
 assert.deepEqual(exceptions,[],'no JS exceptions');assert.deepEqual(errors,[],'no console errors');
 assert(http.filter(r=>r.query==='"panel-probe-20261103"').length>=5,'open and refresh use the same literal FTS contract');
 const report={report:true,checks,updates,http,exceptions,errors,syntheticOnly:true,productionScriptsUnmodified:true,naturalPollingMs:15000};
 fs.writeFileSync(evidence+'/conversation-updates.json',JSON.stringify(report,null,2));console.log(JSON.stringify(report));
})().catch(e=>{console.error(e.stack);process.exitCode=1;}).finally(()=>{input.close();if(ws)ws.close();child.kill('SIGTERM');});
`
