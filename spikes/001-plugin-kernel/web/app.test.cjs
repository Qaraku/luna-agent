const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

class Element {
  constructor() { this.children = []; this.value = ''; this.textContent = ''; this.dataset = {}; this.handlers = {}; this.disabled = false; this.hidden = false; }
  addEventListener(name, fn) { this.handlers[name] = fn; }
  append(...nodes) { this.children.push(...nodes); }
  prepend(...nodes) { this.children.unshift(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
  setAttribute(name, value) { this[name] = value; }
  get lastElementChild() { return this.children.at(-1); }
  get childElementCount() { return this.children.length; }
  remove() {}
}
const state = { host_pid: 100, active: { generation: 1, version: 'v1', plugin_pid: 101, candidate: 'v1' }, plugins: [], events: [], demo: true };
test('HTML declares every JS target and only local assets', () => {
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  const source = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
  const ids = [...html.matchAll(/\bid="([^"]+)"/g)].map(match => match[1]);
  assert.equal(ids.length, new Set(ids).size, 'IDs must be unique');
  for (const match of source.matchAll(/\$\('([^']+)'\)/g)) assert.ok(ids.includes(match[1]), `missing #${match[1]}`);
  assert.doesNotMatch(html, /(?:src|href)="https?:/);
  assert.doesNotMatch(source, /innerHTML/);
});
const tick = () => new Promise(resolve => setImmediate(resolve));
function boot(fetch) {
  const elements = new Map();
  const el = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
  el('tool-input').value = '保留草稿'; el('delay-ms').value = '3000'; el('candidate').value = 'v2';
  const document = { getElementById: el, createElement: () => new Element() };
  const timers = [];
  const source = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
  vm.runInNewContext(source, { document, fetch, AbortController, setTimeout: fn => timers.push(fn), clearTimeout() {}, Date, console });
  return { el, timers };
}
test('state polling renders backend PIDs without replacing input or selection', async () => {
  assert.ok(fs.existsSync(path.join(__dirname, 'app.js')), 'UI implementation is missing');
  const { el } = boot(async () => ({ ok: true, json: async () => state }));
  await tick();
  assert.equal(el('host-pid').textContent, '100');
  assert.equal(el('plugin-pid').textContent, '101');
  assert.equal(el('tool-input').value, '保留草稿');
  assert.equal(el('candidate').value, 'v2');
});
test('in-flight invocation allows reload, retains returned generation and reports rollback', async () => {
  let finishInvoke;
  const requests = [];
  const { el } = boot(async (url, options) => {
    requests.push({ url, body: options.body && JSON.parse(options.body) });
    if (url === '/api/invoke') return new Promise(resolve => { finishInvoke = () => resolve({ ok: true, json: async () => ({ result: '<b>真实结果</b>', generation: 1, version: 'v1', plugin_pid: 101 }) }); });
    if (url === '/api/reload') return { ok: false, json: async () => ({ error: 'broken handshake' }) };
    return { ok: true, json: async () => state };
  });
  await tick();
  assert.equal(typeof el('invoke-form').handlers.submit, 'function', 'invoke form needs a working handler');
  const pending = el('invoke-form').handlers.submit({ preventDefault() {} });
  assert.equal(el('invoke-btn').disabled, true);
  assert.equal(el('reload-btn').disabled, false);
  await el('reload-form').handlers.submit({ preventDefault() {} });
  assert.match(el('error').textContent, /broken handshake/);
  assert.equal(el('reload-btn').disabled, false);
  finishInvoke(); await pending;
  assert.equal(el('invoke-btn').disabled, false);
  const entry = el('output').children[0];
  assert.match(entry.children[0].textContent, /g1.*v1.*101/);
  assert.equal(entry.children[1].textContent, '<b>真实结果</b>');
  assert.deepEqual(requests.find(item => item.url === '/api/invoke').body, { text: '保留草稿', delay_ms: 3000 });
  assert.deepEqual(requests.find(item => item.url === '/api/reload').body, { candidate: 'v2' });
});
