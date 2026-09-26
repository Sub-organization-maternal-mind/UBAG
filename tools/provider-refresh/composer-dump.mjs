// One-off reconnaissance: dump composer DOM trees for selector rebasing.
import { readFileSync } from 'node:fs';
const base = process.env.UBAG_PROBE_CDP || 'http://127.0.0.1:15923';
const hosts = [
  ['deepseek', /deepseek/],
  ['gemini', /gemini\.google/],
  ['chatgpt', /chatgpt\.com/],
];
const list = await (await fetch(base + '/json/list')).json();
const expr = `(() => {
  const c = document.querySelector("textarea, rich-textarea, div[contenteditable='true']");
  if (!c) return 'no composer';
  let box = c.closest('form') || c.parentElement.parentElement.parentElement;
  const out = [];
  const walk = (el, depth) => {
    if (depth > 6 || out.length > 80) return;
    for (const el2 of el.children) {
      const attrs = [...el2.attributes].map(a => a.name + '=' + a.value.slice(0, 48)).join(' ');
      const txt = (el2.textContent || '').trim().slice(0, 30);
      out.push('  '.repeat(depth) + '<' + el2.tagName.toLowerCase() + (attrs ? ' ' + attrs : '') + '>' + (el2.children.length === 0 ? txt : ''));
      walk(el2, depth + 1);
    }
  };
  walk(box, 0);
  return out.join('\\n').slice(0, 3600);
})()`;
for (const [name, hostRe] of hosts) {
  const tab = list.find((t) => t.type === 'page' && hostRe.test(t.url));
  if (!tab) { console.log(name, ': no tab'); continue; }
  const ws = new WebSocket(tab.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let seq = 0; const pend = new Map();
  ws.onmessage = (ev) => { const m = JSON.parse(ev.data); if (m.id && pend.has(m.id)) { pend.get(m.id)(m.result); pend.delete(m.id); } };
  const send = (method, params) => new Promise((res) => { const id = ++seq; pend.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
  const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true });
  console.log('=====', name, '=====');
  if (r.exceptionDetails) console.log('EXCEPTION:', JSON.stringify(r.exceptionDetails).slice(0, 300));
  else console.log(r.result.value);
  ws.close();
}
