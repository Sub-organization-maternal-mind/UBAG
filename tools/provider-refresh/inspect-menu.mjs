// Bounded, menu-only DOM inspection. Pass an observed CSS menu trigger.
// Emits controls and attributes only; excludes messages, cookies and storage.
const [host, trigger, action = 'inspect'] = process.argv.slice(2);
if (!host || !trigger) throw Error('usage: inspect-menu.mjs HOST OBSERVED_TRIGGER [inspect|open]');
const tabs = await fetch(process.env.UBAG_PROBE_CDP + '/json/list', {signal: AbortSignal.timeout(8000)}).then(r => r.json());
const target = tabs.find(t => new URL(t.url).hostname === host && t.type === 'page');
if (!target) throw Error('provider tab missing');
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((ok, no) => { ws.onopen = ok; ws.onerror = no; });
let sequence = 0;
const pending = new Map();
ws.onmessage = ev => { const m = JSON.parse(ev.data); const p = pending.get(m.id); if (p) {pending.delete(m.id); m.error ? p.no(Error(m.error.message)) : p.ok(m.result);} };
async function evaluate(expression) {
  const id = ++sequence;
  const result = await new Promise((ok,no) => {pending.set(id,{ok,no}); ws.send(JSON.stringify({id,method:'Runtime.evaluate',params:{expression,returnByValue:true}})); setTimeout(()=>no(Error('CDP timeout')),8000).unref();});
  if (result.exceptionDetails) throw Error('page evaluation failed');
  return result.result.value;
}
try {
  if (action === 'open') {
    console.log('opened',await evaluate(`(()=>{ const e=document.querySelector(${JSON.stringify(trigger)}); if(!e||!e.getClientRects().length)return false;e.click();return true;})()`));
    await new Promise(r=>setTimeout(r,500));
  }
  console.log(JSON.stringify(await evaluate(`(()=>[...document.querySelectorAll(${JSON.stringify(action === 'open' ? "[role='menuitemradio'],[role='menuitem'],[role='option'],gem-menu-item,[role='slider']" : trigger)})].filter(e=>e.getClientRects().length).slice(0,30).map(e=>({tag:e.tagName,label:(e.getAttribute('aria-label')||e.textContent||'').trim().slice(0,120),attributes:Object.fromEntries([...e.attributes].filter(a=>/^(aria-|data-test|role|class)/.test(a.name)).map(a=>[a.name,a.value]))})))()`),null,2));
} finally {ws.close();}
