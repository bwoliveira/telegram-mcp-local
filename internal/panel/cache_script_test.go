package panel

import (
	"os/exec"
	"testing"
)

func TestSnapshotScriptRestoresBFCacheAndRetainsOnError(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs=require('fs'), vm=require('vm'), assert=require('assert');
const events={}, listeners={};
let calls=0, replacements=0, failing=false;
const content={replaceChildren(){replacements++;}};
const note={textContent:''};
const retry={hidden:true,addEventListener(name,fn){listeners[name]=fn;}};
const region={querySelector(selector){return selector==='[data-status-content]'?content:note;},setAttribute(){}};
const document={hidden:false,documentElement:{lang:'en'},querySelector(selector){if(selector==='[data-status-snapshot]')return region;if(selector==='[data-snapshot-retry]')return retry;return null;},querySelectorAll(){return [];},createElement(tag){assert.equal(tag,'template');return {content:{},innerHTML:''};}};
const window={addEventListener(name,fn){events[name]=fn;},clearTimeout(){},setTimeout(){return 1;},panelCSRF:{fetch(){calls++;return failing?Promise.reject(new Error('unavailable')):Promise.resolve({ok:true,json(){return Promise.resolve({fresh:true,html:'<section>escaped server content</section>',checked_at:'2026-11-02T12:00:00Z'});}});}}};
vm.runInNewContext(fs.readFileSync('assets/cache.js','utf8'),{window,document,AbortController,Date,Number});
const settle=()=>new Promise(resolve=>setImmediate(resolve));
(async()=>{
 await settle();assert.equal(calls,1);assert.equal(replacements,1);
 events.pagehide({persisted:true});events.pageshow({persisted:true});
 await settle();assert.equal(calls,2);assert.equal(replacements,2);
 failing=true;listeners.click();await settle();assert.equal(calls,3);assert.equal(replacements,2);assert.equal(retry.hidden,false);assert(note.textContent.includes('desatualizados'));
 failing=false;listeners.click();await settle();assert.equal(calls,4);assert.equal(replacements,3);assert.equal(retry.hidden,true);
 console.log('snapshot hydration, BFCache restore, error retention and retry passed');
})().catch(error=>{console.error(error);process.exitCode=1;});
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("snapshot script: %v\n%s", err, out)
	}
}
