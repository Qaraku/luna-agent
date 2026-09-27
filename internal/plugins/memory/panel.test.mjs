// 行为断言：模块真的只通过 <link> 引样式表、卸载时把它摘掉，而且从不创建
// <style>（服务的 CSP 是 default-src 'self'，注入的 <style> 会被拒绝）。
// Go 侧的断言只能读源码，这一条把模块放进一个小 DOM 里真的跑一遍。
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
  node.append = (...nodes) => {
    for (const child of nodes) {
      child.parentNode = node;
      node.children.push(child);
    }
  };
  node.setAttribute = (name, value) => node.attributes.set(name, String(value));
  node.addEventListener = (type, handler) => node.listeners.push([type, handler]);
  node.remove = () => {
    node.removed = true;
    const parent = node.parentNode;
    if (parent) parent.children = parent.children.filter((child) => child !== node);
    node.parentNode = null;
  };
  node.replaceChildren = (...nodes) => {
    for (const child of node.children) child.parentNode = null;
    node.children = [];
    node.append(...nodes);
  };
  return node;
}

// withDom installs the smallest DOM the module touches, plus a fetch that answers
// the facts route with an empty memory, and restores the globals afterwards.
async function withDom(run) {
  const created = [];
  const document = {
    created,
    head: element('head', created),
    createElement: (tag) => element(tag, created),
    createTextNode: (text) => ({ nodeType: 3, textContent: text }),
  };
  const previousDocument = Object.getOwnPropertyDescriptor(globalThis, 'document');
  const previousFetch = globalThis.fetch;
  globalThis.document = document;
  globalThis.fetch = async () => ({ ok: true, status: 200, json: async () => ({ facts: [], retracted: [] }) });
  try {
    return await run(document);
  } finally {
    globalThis.fetch = previousFetch;
    if (previousDocument) Object.defineProperty(globalThis, 'document', previousDocument);
    else delete globalThis.document;
  }
}

async function loadModule() {
  return import(new URL('./panel.js', import.meta.url));
}

test('the module links its stylesheet instead of injecting a style element', async () => {
  await withDom(async (document) => {
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
  await withDom(async (document) => {
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
