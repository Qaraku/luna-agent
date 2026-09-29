// 行为断言：模块真的只通过 <link> 引样式表、卸载时把它摘掉，而且从不创建
// <style>（服务的 CSP 是 default-src 'self'，注入的 <style> 会被拒绝）。
// Go 侧的断言只能读源码，这一条把模块放进一个小 DOM 里真的跑一遍。
//
// 面板重组之后，这里同样断言它给出的界面：生效中与已撤回分开、每条带着来源与
// 时间、只有生效中的那条有可点的动作、空记忆是一个安静的初始态，而状态行既不
// 承载计数也不承载生命周期事件。
//
// 运行：node --test internal/plugins/memory/panel.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';

function element(tag, created) {
  if (created) created.push(tag);
  const node = {
    tagName: tag,
    children: [],
    attributes: new Map(),
    listeners: [],
    textContent: '',
    parentNode: null,
    removed: false,
  };
  Object.defineProperty(node, 'textContent', { get() { return (node._text || '') + node.children.map(child => child.textContent || '').join(''); }, set(value) { node._text = String(value); node.children = []; } });
  node.append = (...nodes) => {
    for (const child of nodes) {
      child.parentNode = node;
      node.children.push(child);
    }
  };
  node.setAttribute = (name, value) => node.attributes.set(name, String(value));
  node.getAttribute = name => node.attributes.get(name) ?? null;
  node.focus = () => { globalThis.document.activeElement = node; };
  node.addEventListener = (type, handler) => node.listeners.push([type, handler]);
  node.remove = () => {
    node.removed = true;
    const parent = node.parentNode;
    if (parent) parent.children = parent.children.filter((child) => child !== node);
    node.parentNode = null;
  };
  node.replaceChildren = (...nodes) => {
    node._text = '';
    for (const child of node.children) child.parentNode = null;
    node.children = [];
    node.append(...nodes);
  };
  return node;
}

function hasClass(node, name) {
  return String(node.className || '').split(/\s+/).includes(name);
}

function allByClass(root, name, found = []) {
  for (const child of root.children || []) {
    if (hasClass(child, name)) found.push(child);
    allByClass(child, name, found);
  }
  return found;
}

function textOf(root, name) {
  const nodes = allByClass(root, name);
  return nodes.length ? nodes[0].textContent : '';
}

function click(node) {
  for (const [type, handler] of node.listeners) if (type === 'click') handler();
}

const FACT = { text: '示例偏好：回答先给结论。', at: '2026-01-01T00:00:00Z', source_session: 'aaaaaaaa11112222' };
const GONE = { text: '用户住在杭州。', at: '2025-12-31T00:00:00Z', retracted_at: '2026-01-02T00:00:00Z' };

// settle drains the microtask queue twice: enough for the module's fetch chain
// (read the route, draw, then the route again after a retract).
async function settle() {
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));
}

// withDom installs the smallest DOM the module touches, plus a fetch answering the
// facts route, a timer queue the panel's self-clearing status is driven from, and
// restores the globals afterwards.
async function withDom(run, { respond } = {}) {
  const created = [];
  const document = {
    created,
    head: element('head', created),
    createElement: (tag) => element(tag, created),
    createTextNode: (text) => ({ nodeType: 3, textContent: text }),
  };
  const timers = {
    queued: [],
    network: [],
    flush() {
      const pending = timers.queued.splice(0, timers.queued.length);
      for (const timer of pending) timer.fn();
      return pending.length;
    },
  };
  const previousDocument = Object.getOwnPropertyDescriptor(globalThis, 'document');
  const previousFetch = globalThis.fetch;
  const previousSetTimeout = globalThis.setTimeout;
  const previousClearTimeout = globalThis.clearTimeout;
  globalThis.document = document;
  globalThis.fetch = async (url, options) => respond
    ? respond(url, options)
    : { ok: true, status: 200, json: async () => ({ facts: [], retracted: [] }) };
  globalThis.setTimeout = (fn, delay = 0) => {
    const timer = { fn };
    (delay >= 10000 ? timers.network : timers.queued).push(timer);
    return timer;
  };
  globalThis.clearTimeout = (timer) => {
    const index = timers.queued.indexOf(timer);
    if (index >= 0) timers.queued.splice(index, 1);
    const networkIndex = timers.network.indexOf(timer); if (networkIndex >= 0) timers.network.splice(networkIndex, 1);
  };
  try {
    return await run({ document, timers });
  } finally {
    globalThis.setTimeout = previousSetTimeout;
    globalThis.clearTimeout = previousClearTimeout;
    globalThis.fetch = previousFetch;
    if (previousDocument) Object.defineProperty(globalThis, 'document', previousDocument);
    else delete globalThis.document;
  }
}

async function loadModule() {
  return import(new URL('./panel.js', import.meta.url));
}

test('the module links its stylesheet instead of injecting a style element', async () => {
  await withDom(async ({ document }) => {
    const module = await loadModule();
    const target = document.createElement('div');
    module.mount(target, { log() {} });
    assert.equal(document.created.includes('style'), false, '模块不得创建 <style> 元素');
    const links = document.head.children.filter((node) => node.tagName === 'link');
    assert.equal(links.length, 1, '一次挂载只引用一份样式表');
    assert.equal(links[0].rel, 'stylesheet');
    assert.match(String(links[0].href), /\/panel\.css$/, 'href 由模块自己的 URL 推导');
    module.unmount(target);
  });
});

test('unmount takes the stylesheet link away with the panel', async () => {
  await withDom(async ({ document }) => {
    const module = await loadModule();
    const target = document.createElement('div');
    module.mount(target, { log() {} });
    const link = document.head.children.find((node) => node.tagName === 'link');
    assert.ok(link, '挂载后 head 里应有一份样式表引用');
    module.unmount(target);
    assert.equal(link.removed, true, '卸载必须摘掉 <link>');
    assert.equal(document.head.children.includes(link), false);
    assert.equal(document.head.children.length, 0);
  });
});

test('mounting and unmounting write no lifecycle log', async () => {
  const logs = [];
  await withDom(async ({ document }) => {
    const module = await loadModule();
    const target = document.createElement('div');
    module.mount(target, { log: (message) => logs.push(message) });
    await settle();
    module.unmount(target);
  });
  assert.deepEqual(logs, [], '面板不把挂载、卸载或请求次数写成日志');
});

test('the panel splits the facts in effect from the retracted ones', async () => {
  const logs = [];
  await withDom(async ({ document }) => {
    const module = await loadModule();
    const target = document.createElement('div');
    module.mount(target, { log: (message) => logs.push(message) });
    await settle();

    // 两组各有自己的标题：计数属于这里，不需要再占一行状态。
    assert.deepEqual(allByClass(target, 'memory-panel-heading').map((node) => node.textContent),
      ['生效中 · 1 条', '已撤回 · 1 条']);

    const active = allByClass(target, 'memory-panel-list')[0];
    const inEffect = allByClass(active, 'memory-panel-item');
    assert.equal(inEffect.length, 1);
    assert.equal(textOf(active, 'memory-panel-text'), FACT.text);
    const meta = textOf(active, 'memory-panel-meta');
    assert.match(meta, /来自会话 aaaaaaaa11112222/, '生效中的那条说出它来自哪个会话');
    assert.match(meta, /记录于 /, '并且给出记录时间');

    const gone = allByClass(target, 'memory-panel-retracted')[0];
    const removed = allByClass(gone, 'memory-panel-item');
    assert.equal(removed.length, 1, '已撤回的那条留在文件里，也在面板上');
    assert.equal(removed[0].className.includes('memory-panel-item-retracted'), true, '已撤回的条目有区别');
    assert.equal(textOf(gone, 'memory-panel-text'), GONE.text);
    const goneMeta = textOf(gone, 'memory-panel-meta');
    assert.match(goneMeta, /记录于/, '已撤回的那条也说出它什么时候被记下的');
    assert.match(goneMeta, /撤回于/, '并且说出它是被撤回的');
    assert.equal(allByClass(gone, 'memory-panel-retract').length, 0, '已撤回的条目没有可点的动作');

    // 能做什么就写在生效中那条上：撤回按钮属于它，而且只有它。
    assert.equal(allByClass(active, 'memory-panel-retract').length, 1);
    assert.equal(allByClass(target, 'memory-panel-retract').length, 1);

    // 状态行既不承载计数，也不承载挂载事件。
    assert.equal(textOf(target, 'memory-panel-status'), '');
    assert.deepEqual(logs, []);
    module.unmount(target);
  }, { respond: async () => ({ ok: true, status: 200, json: async () => ({ facts: [FACT], retracted: [GONE] }) }) });
});

test('an empty memory is a quiet initial state', async () => {
  await withDom(async ({ document }) => {
    const module = await loadModule();
    const target = document.createElement('div');
    module.mount(target, { log() {} });
    await settle();

    assert.equal(allByClass(target, 'memory-panel-item').length, 0);
    const empty = allByClass(target, 'memory-panel-empty');
    assert.equal(empty.length, 1);
    assert.equal(empty[0].hidden, false);
    assert.match(empty[0].textContent, /还没有记录任何事实/);
    // 没有任何撤回记录时，整组"已撤回"不出现：初始态不是一串事件。
    const goneGroup = allByClass(target, 'memory-panel-group-retracted');
    assert.equal(goneGroup.length, 1);
    assert.equal(goneGroup[0].hidden, true);
    assert.deepEqual(allByClass(target, 'memory-panel-heading').map((node) => node.textContent),
      ['生效中 · 0 条', '已撤回 · 0 条']);
    assert.equal(textOf(target, 'memory-panel-status'), '');
    module.unmount(target);
  });
});

test('a retract names the exact fact and leaves the status line empty when it lands', async () => {
  let release;
  let stored = { facts: [FACT], retracted: [] };
  const requests = [];
  await withDom(async ({ document, timers }) => {
    const module = await loadModule();
    const target = document.createElement('div');
    module.mount(target, { log() {} });
    await settle();

    click(allByClass(target, 'memory-panel-retract')[0]);
    assert.equal(textOf(target, 'memory-panel-status'), '正在撤回…', '在途时状态行说正在做什么');
    assert.equal(allByClass(target, 'memory-panel-retract')[0].disabled, true, '在途时没有第二次撤回');
    assert.equal(timers.queued.length, 1, '这条瞬时反馈自己会消失');

    release();
    await settle();
    assert.deepEqual(requests, [{ at: FACT.at, text: FACT.text }], '撤回请求按时间与原文点名这一条事实');

    // 成功不另写一条事件：证据是它真的移到了「已撤回」。
    const active = allByClass(target, 'memory-panel-list')[0];
    assert.equal(allByClass(active, 'memory-panel-item').length, 0, '生效集合里没有它了');
    assert.equal(allByClass(allByClass(target, 'memory-panel-retracted')[0], 'memory-panel-item').length, 1);
    assert.deepEqual(allByClass(target, 'memory-panel-heading').map((node) => node.textContent),
      ['生效中 · 0 条', '已撤回 · 1 条']);
    assert.equal(textOf(target, 'memory-panel-status'), '');
    assert.equal(timers.queued.length, 0, '被取代的定时器已经撤掉，不会稍后再清一次状态');
    module.unmount(target);
  }, { respond: async (url, options) => {
    if (url === '/api/memory/retract') {
      requests.push(JSON.parse(options.body));
      return new Promise((resolve) => {
        release = () => {
          stored = { facts: [], retracted: [GONE] };
          resolve({ ok: true, status: 200, json: async () => ({ retracted: GONE }) });
        };
      });
    }
    return { ok: true, status: 200, json: async () => stored };
  } });
});

test('a retract failure stays in the status line', async () => {
  await withDom(async ({ document, timers }) => {
    const module = await loadModule();
    const target = document.createElement('div');
    module.mount(target, { log() {} });
    await settle();

    click(allByClass(target, 'memory-panel-retract')[0]);
    await settle();
    assert.equal(textOf(target, 'memory-panel-status'), '这条记忆已经不在生效集合里');
    assert.equal(allByClass(target, 'memory-panel-status')[0].className, 'memory-panel-status failure');
    // 在途反馈的定时器在写入错误时就被撤掉了；没撤掉的话，flush 会把错误抹掉。
    assert.equal(timers.queued.length, 0, '错误不带瞬时定时器');
    timers.flush();
    assert.equal(textOf(target, 'memory-panel-status'), '这条记忆已经不在生效集合里');
    // 失败之后这条事实仍然在生效集合里，仍然可以再试一次。
    assert.equal(allByClass(allByClass(target, 'memory-panel-list')[0], 'memory-panel-retract').length, 1);
    module.unmount(target);
  }, { respond: async (url) => {
    if (url === '/api/memory/retract') {
      return { ok: false, status: 404, json: async () => ({ error: '这条记忆已经不在生效集合里' }) };
    }
    return { ok: true, status: 200, json: async () => ({ facts: [FACT], retracted: [] }) };
  } });
});


test('Memory 范围选择请求对应项目，并能纠正条目而不伪造来源',async()=>{
 const writes=[];const calls=[];let current={...FACT,ref:'id:a',lineage:'id:a',scope:'global',origin:'model'};
 await withDom(async({document})=>{const module=await loadModule();const target=document.createElement('div');module.mount(target,{});await settle();
  const correct=allByClass(target,'memory-panel-correct')[0];assert.ok(correct);click(correct);const editor=allByClass(target,'memory-edit-text')[0];editor.value='更新的偏好';const form=allByClass(target,'memory-edit-form')[0];for(const [type,fn]of form.listeners)if(type==='submit')fn({preventDefault(){}});await settle();await settle();
  assert.equal(writes.length,1);assert.deepEqual(writes[0],{ref:'id:a',text:'更新的偏好',reason:'用户纠错'});
  const select=allByClass(target,'memory-scope-select')[0];select.value='project:p';for(const [type,fn]of select.listeners)if(type==='change')fn();await settle();assert.ok(calls.includes('/api/memory?scope=project&workspace=p'));module.unmount(target);
 },{respond:async(url,options)=>{calls.push(url);if(url==='/api/workspaces')return {ok:true,json:async()=>({workspaces:[{id:'p',name:'项目 P'}]})};if(url==='/api/memory/correct'){writes.push(JSON.parse(options.body));current={...current,text:'更新的偏好',ref:'id:b',origin:'user'};return {ok:true,json:async()=>({fact:current})}};return {ok:true,json:async()=>({facts:[current],retracted:[],changes:[],scopes:[{scope:'global',count:1},{scope:'project',workspace_id:'p',count:0}]})}}});
});

test('Memory 恢复使用历史引用及当前引用，先确认再提交',async()=>{
 const before={...FACT,text:'旧内容',ref:'id:a',lineage:'id:a',scope:'global'};const after={...before,text:'新内容',ref:'id:b'};const writes=[];
 await withDom(async({document})=>{const module=await loadModule();const target=document.createElement('div');module.mount(target,{});await settle();click(allByClass(target,'memory-history-restore')[0]);assert.equal(writes.length,0);click(allByClass(target,'memory-restore-confirm')[0]);await settle();assert.equal(writes.length,1);assert.equal(writes[0].ref,'id:a');assert.equal(writes[0].expected_ref,'id:b');module.unmount(target);},{respond:async(url,options)=>{if(url==='/api/workspaces')return {ok:true,json:async()=>({workspaces:[]})};if(url==='/api/memory/restore'){writes.push(JSON.parse(options.body));return {ok:true,json:async()=>({})}}return {ok:true,json:async()=>({facts:[after],retracted:[],changes:[{id:'change-one',action:'correct',before,after,origin:{kind:'user'}}]})}}});
});


test('Memory 纠错失败保留编辑内容和旧引用',async()=>{
 const item={...FACT,ref:'id:a',lineage:'id:a',scope:'global'};
 await withDom(async({document})=>{const module=await loadModule();const target=document.createElement('div');module.mount(target,{});await settle();click(allByClass(target,'memory-panel-correct')[0]);const text=allByClass(target,'memory-edit-text')[0];text.value='未保存纠错';const form=allByClass(target,'memory-edit-form')[0];for(const[type,fn]of form.listeners)if(type==='submit')fn({preventDefault(){}});await settle();assert.equal(form.hidden,false);assert.equal(text.value,'未保存纠错');assert.match(textOf(target,'memory-panel-status'),/stale reference/);module.unmount(target);},{respond:async url=>url==='/api/memory/correct'?{ok:false,status:409,json:async()=>({error:'stale reference'})}:url==='/api/workspaces'?{ok:true,json:async()=>({workspaces:[]})}:{ok:true,json:async()=>({facts:[item],retracted:[],changes:[]})}});
});

test('Memory 卸载取消数据请求，迟到结果不复活内容或定时器',async()=>{
 let release,signal;await withDom(async({document,timers})=>{const module=await loadModule();const target=document.createElement('div');module.mount(target,{});await settle();module.unmount(target);assert.equal(signal.aborted,true);release();await settle();assert.equal(target.children.length,0);assert.equal(document.head.children.length,0);assert.equal(timers.network.length,0);},{respond:async(url,options)=>{if(url==='/api/workspaces')return {ok:true,json:async()=>({workspaces:[]})};signal=options.signal;await new Promise(resolve=>release=resolve);return {ok:true,json:async()=>({facts:[FACT],retracted:[]})}}});
});
