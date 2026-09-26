const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const root = __dirname;
const source = (name) => fs.readFileSync(path.join(root, name), 'utf8');

// 无外部依赖的 DOM 适配器：执行完整 app.js 与真实页面结构，不模拟被测导航逻辑。
// 几何、CSS 与浏览器原生 Tab 顺序仍由隔离浏览器验收负责。
function navigationHarness({ narrow = false, hash = '', respond, dark = false, storage = new Map(), storageError = '' } = {}) {
  const vm = require('node:vm');
  const listeners = () => ({
    handlers: new Map(),
    addEventListener(type, handler) {
      if (!this.handlers.has(type)) this.handlers.set(type, []);
      this.handlers.get(type).push(handler);
    },
    emit(type, extra = {}) {
      const event = { type, target: this, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, ...extra };
      for (const handler of this.handlers.get(type) || []) handler(event);
      return event;
    }
  });
  const document = listeners();
  class Element {
    constructor(tag) {
      Object.assign(this, listeners());
      this.tagName = tag.toUpperCase();
      this.childNodes = [];
      this.parentElement = null;
      this.attributes = new Map();
      this.dataset = {};
      // 真实 DOM 的 style 支持 setProperty；应用用 inline 变量驱动侧栏宽度。
      this.style = {
        setProperty(name, value) { this[name] = String(value); },
        removeProperty(name) { delete this[name]; }
      };
      this.hidden = false;
      this.inert = false;
      this.disabled = false;
      this.value = '';
      this.className = '';
      this.scrollHeight = 100;
      this.scrollTop = 0;
      this.clientHeight = 100;
      this.classList = {
        contains: (name) => this.className.split(/\s+/).includes(name),
        toggle: (name, force) => {
          const names = new Set(this.className.split(/\s+/).filter(Boolean));
          const add = force === undefined ? !names.has(name) : force;
          if (add) names.add(name); else names.delete(name);
          this.className = [...names].join(' ');
          return add;
        },
        add: (name) => this.classList.toggle(name, true),
        remove: (name) => this.classList.toggle(name, false)
      };
    }
    get children() { return this.childNodes.filter((child) => child.tagName !== '#TEXT'); }
    get childElementCount() { return this.children.length; }
    get firstElementChild() { return this.children[0]; }
    get lastChild() { return this.childNodes.at(-1); }
    get isConnected() { return this === document.body || Boolean(this.parentElement?.isConnected); }
    get tabIndex() {
      return this.attributes.has('tabindex') ? Number(this.getAttribute('tabindex'))
        : ['BUTTON', 'INPUT', 'SELECT', 'TEXTAREA', 'SUMMARY', 'A'].includes(this.tagName) ? 0 : -1;
    }
    get textContent() { return this._text || this.childNodes.map((child) => child.textContent).join(''); }
    set textContent(text) { this.replaceChildren(); this._text = String(text); }
    setAttribute(name, value) {
      this.attributes.set(name, String(value));
      if (name === 'class') this.className = value;
      if (name === 'id') this.id = value;
      // 真实 DOM 把 data-* 映射到 dataset；分类导航靠 dataset.pane 找面板。
      if (name.startsWith('data-')) {
        this.dataset[name.slice(5).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = String(value);
      }
      if (name === 'hidden' || name === 'inert' || name === 'disabled') this[name] = true;
    }
    getAttribute(name) { return this.attributes.get(name) ?? null; }
    removeAttribute(name) {
      this.attributes.delete(name);
      if (name === 'hidden' || name === 'inert' || name === 'disabled') this[name] = false;
    }
    append(...nodes) { for (const node of nodes) this.insertBefore(node, null); }
    insertBefore(node, before) {
      node.remove();
      const index = before ? this.childNodes.indexOf(before) : this.childNodes.length;
      this.childNodes.splice(index, 0, node);
      node.parentElement = this;
      this._text = '';
    }
    remove() {
      if (!this.parentElement) return;
      if (this.contains(document.activeElement)) document.activeElement = document.body;
      const siblings = this.parentElement.childNodes;
      siblings.splice(siblings.indexOf(this), 1);
      this.parentElement = null;
    }
    replaceChildren(...nodes) { for (const node of [...this.childNodes]) node.remove(); this._text = ''; this.append(...nodes); }
    contains(node) { return node === this || this.childNodes.some((child) => child.contains(node)); }
    matches(selector) {
      return selector.split(',').some((part) => {
        const token = part.trim();
        if (token.startsWith('.')) return this.classList.contains(token.slice(1));
        if (token.startsWith('#')) return this.id === token.slice(1);
        if (token.startsWith('[')) {
          const body = token.slice(1, -1);
          const equals = body.indexOf('=');
          if (equals >= 0) {
            const value = body.slice(equals + 1).replace(/^"|"$/g, '');
            return this.attributes.get(body.slice(0, equals)) === value;
          }
          return ['hidden', 'inert', 'disabled'].includes(body) ? this[body] : this.attributes.has(body);
        }
        return this.tagName === token.toUpperCase();
      });
    }
    closest(selector) { return this.matches(selector) ? this : this.parentElement?.closest(selector) || null; }
    querySelectorAll(selector) {
      return this.children.flatMap((child) => [...(child.matches(selector) ? [child] : []), ...child.querySelectorAll(selector)]);
    }
    querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
    getClientRects() { return this.isConnected && !this.closest('[hidden]') ? [{}] : []; }
    focus() {
      if (!this.disabled && this.getClientRects().length && !this.closest('[inert]')) {
        document.activeElement = this;
        document.emit('focusin', { target: this });
      }
    }
    click() { if (!this.disabled) this.emit('click'); }
    scrollTo({ top }) { this.scrollTop = top; }
    requestSubmit() { this.emit('submit'); }
  }
  document.body = new Element('body');
  document.documentElement = new Element('html');
  document.documentElement.append(document.body);
  document.activeElement = document.body;
  document.createElement = (tag) => new Element(tag);
  document.createTextNode = (text) => { const node = new Element('#text'); node.textContent = text; return node; };
  document.querySelector = (selector) => document.body.querySelector(selector);
  document.getElementById = (id) => document.querySelector(`#${id}`);
  const stack = [document.body];
  const body = source('index.html').split('<body>')[1].split('</body>')[0];
  for (const token of body.match(/<[^>]+>|[^<]+/g)) {
    if (token.startsWith('</')) { stack.pop(); continue; }
    if (!token.startsWith('<')) { stack.at(-1).append(document.createTextNode(token)); continue; }
    const tag = token.match(/^<(\w+)/)?.[1];
    if (!tag) continue;
    const node = new Element(tag);
    for (const attr of token.slice(tag.length + 1, -1).matchAll(/([\w-]+)(?:="([^"]*)")?/g)) node.setAttribute(attr[1], attr[2] ?? '');
    stack.at(-1).append(node);
    if (!['input', 'meta', 'link', 'br', 'hr'].includes(tag)) stack.push(node);
  }
  const window = listeners();
  const media = Object.assign(listeners(), { matches: narrow });
  const colorMedia = Object.assign(listeners(), { matches: dark });
  window.matchMedia = (query) => query.includes('prefers-color-scheme') ? colorMedia : media;
  const storageCalls = [];
  Object.defineProperty(window, 'localStorage', { get() {
    if (storageError === 'access') throw new Error('storage inaccessible');
    return {
      getItem(key) {
        if (storageError === 'read') throw new Error('storage unreadable');
        return storage.get(key) ?? null;
      },
      setItem(key, value) {
        if (storageError === 'write') throw new Error('storage full');
        storageCalls.push([key, value]);
        storage.set(key, value);
      }
    };
  } });
  const calls = [];
  const frames = [];
  const intervals = [];
  const location = { pathname: '/', search: '', _hash: hash };
  Object.defineProperty(location, 'hash', {
    get() { return this._hash; },
    set(value) { this._hash = value; queueMicrotask(() => window.emit('hashchange')); }
  });
  const data = {
    sessions: { sessions: [{ id: 'aaaaaaaa', title: '会话 A', run_count: 1 }, { id: 'bbbbbbbb', title: '会话 B', run_count: 2 }] },
    memory: { facts: [{ text: '测试事实', at: '2026-09-25T10:00:00Z', source_session: 'aaaaaaaa' }] }
  };
  const context = vm.createContext({
    document, window, location, URLSearchParams, TextDecoder, console,
    history: { replaceState(_state, _title, url) { location._hash = url.includes('#') ? `#${url.split('#')[1]}` : ''; } },
    requestAnimationFrame: (fn) => frames.push(fn),
    setTimeout: (fn) => frames.push(fn), clearTimeout() {}, setInterval: (fn) => intervals.push(fn),
    fetch: async (url, options) => {
      calls.push({ url, options });
      const custom = respond && await respond(url, options, data);
      if (custom) return custom;
      const payload = url === '/api/sessions' ? data.sessions : url === '/api/memory' ? data.memory
        : url.startsWith('/api/sessions/') ? { records: [{ type: 'message', role: 'user', text: '已保存的消息' }] } : {};
      return { ok: true, json: async () => payload };
    }
  });
  const bootstrap = source('index.html').match(/<script id="theme-bootstrap">([\s\S]*?)<\/script>/)?.[1];
  if (bootstrap) vm.runInContext(bootstrap, context, { filename: 'theme-bootstrap' });
  const bootstrapTheme = document.documentElement.dataset.theme;
  vm.runInContext(source('app.js'), context, { filename: 'app.js' });
  const settle = async () => {
    await new Promise((resolve) => setImmediate(resolve));
    while (frames.length) frames.shift()();
    await new Promise((resolve) => setImmediate(resolve));
  };
  return {
    document, window, location, data, calls, settle, storage, storageCalls, bootstrapTheme,
    $: document.getElementById,
    async click(id) { const node = document.getElementById(id); node.focus(); node.click(); await settle(); },
    async poll() { for (const fn of intervals) fn(); await settle(); },
    async resize(matches) { media.matches = matches; media.emit('change'); await settle(); },
    async systemTheme(matches) { colorMedia.matches = matches; colorMedia.emit('change'); await settle(); },
    async theme(value, id = 'theme-select') { const control = document.getElementById(id); control.value = value; control.emit('change'); await settle(); },
    key(key, shiftKey = false) { return document.emit('keydown', { key, shiftKey }); }
  };
}

test('theme follows the OS until explicitly selected and restores only its preference on reload', async () => {
  const h = navigationHarness({ dark: true });
  await h.settle();
  assert.equal(h.bootstrapTheme, 'dark', '首帧主题在样式载入前确定');
  assert.equal(h.document.documentElement.dataset.theme, 'dark');
  assert.equal(h.$('theme-select').value, 'system');
  await h.systemTheme(false);
  assert.equal(h.document.documentElement.dataset.theme, 'light');
  h.$('message').value = '主题切换不丢草稿';
  h.$('message').focus();
  const sessionButton = h.$('session-list').querySelector('button');
  const requests = h.calls.length;
  await h.theme('dark');
  assert.equal(h.document.documentElement.dataset.theme, 'dark');
  assert.equal(h.$('theme-select').value, 'dark', '外观只有一个控件，设置面板里那一个');
  assert.equal(h.$('message').value, '主题切换不丢草稿');
  assert.equal(h.document.activeElement, h.$('message'));
  assert.equal(h.$('session-list').querySelector('button'), sessionButton);
  assert.equal(h.calls.length, requests, '切主题不请求或重新渲染产品数据');
  assert.deepEqual(h.storageCalls, [['luna.theme', 'dark']]);
  await h.systemTheme(true);
  await h.systemTheme(false);
  assert.equal(h.document.documentElement.dataset.theme, 'dark', '显式主题不受 OS 改变影响');
  const reloaded = navigationHarness({ storage: h.storage });
  await reloaded.settle();
  assert.equal(reloaded.bootstrapTheme, 'dark');
  assert.equal(reloaded.$('theme-select').value, 'dark');
  await h.theme('light');
  await h.systemTheme(true);
  assert.equal(h.document.documentElement.dataset.theme, 'light');
  await h.theme('system');
  assert.equal(h.document.documentElement.dataset.theme, 'dark');
  assert.equal(h.storage.get('luna.theme'), 'system');
});

test('theme rejects invalid preferences and safely follows system when storage fails', async () => {
  for (const value of ['sepia', '', 'Dark', '<style>', null]) {
    const h = navigationHarness({ dark: true, storage: new Map([['luna.theme', value]]) });
    await h.settle();
    assert.equal(h.bootstrapTheme, 'dark');
    assert.equal(h.$('theme-select').value, 'system');
    await h.theme('invalid');
    assert.equal(h.$('theme-select').value, 'system');
    assert.equal(h.storage.get('luna.theme'), 'system');
  }
  for (const storageError of ['access', 'read', 'write']) {
    const h = navigationHarness({ dark: true, storageError });
    await h.settle();
    assert.equal(h.bootstrapTheme, 'dark');
    assert.equal(h.$('theme-select').value, 'system');
    await h.systemTheme(false);
    assert.equal(h.document.documentElement.dataset.theme, 'light');
    await h.theme('dark');
    assert.equal(h.$('theme-select').value, 'dark', '写入失败不忽略本页用户的显式选择');
    assert.equal(h.document.documentElement.dataset.theme, 'dark');
    const reloaded = navigationHarness({ storage: h.storage, storageError });
    await reloaded.settle();
    assert.equal(reloaded.$('theme-select').value, 'system');
  }
});

test('extensions open independently of diagnostics and preserve their subtree across theme and panel changes', async () => {
  const h = navigationHarness({ respond: async (url) => {
    if (url === '/api/ui-plugins') return { ok: true, json: async () => ({ plugins: [
      { name: 'counter', title: '计数器', description: '计数与计时', entry: 'plugin.js' }
    ] }) };
  } });
  await h.settle();
  assert.ok(h.$('extensions-toggle'), '扩展有独立可见入口');
  assert.equal(h.$('runtime-drawer').contains(h.$('ui-plugin-list')), false);
  assert.equal(h.$('extensions-panel').contains(h.$('ui-plugin-list')), true);
  await h.click('runtime-toggle');
  assert.equal(h.calls.some(({ url }) => url === '/api/ui-plugins'), false, '诊断不请求界面插件');
  h.$('extensions-toggle').click();
  await h.settle();
  assert.equal(h.$('runtime-drawer').hidden, true);
  assert.equal(h.$('extensions-panel').hidden, false);
  assert.equal(h.document.activeElement, h.$('extensions-close'));
  const list = h.$('ui-plugin-list');
  const row = list.firstElementChild;
  const stage = row.querySelector('.ui-plugin-stage');
  const content = h.document.createElement('button');
  content.textContent = '插件自己的状态';
  // 挂载容器默认是隐藏的；这里按已启用插件的状态取焦点。
  stage.hidden = false;
  stage.append(content);
  // 主题更新与面板开关不得触碰插件自行维护的 DOM，也不应重新读清单。
  content.focus();
  await h.theme('dark');
  assert.equal(h.document.activeElement, content, '切主题不移动插件内的焦点');
  assert.equal(stage.firstElementChild, content);
  assert.equal(list.firstElementChild, row);
  assert.equal(h.$('theme-select').value, 'dark');
  // 焦点陷阱覆盖插件自己的控件：从最后一个可聚焦元素回绕到关闭按钮，再回绕回来。
  h.key('Tab');
  assert.equal(h.document.activeElement, h.$('extensions-close'));
  h.key('Tab', true);
  assert.equal(h.document.activeElement, content);
  await h.resize(true);
  assert.equal(h.$('extensions-panel').hidden, false);
  h.key('Escape');
  assert.equal(h.document.activeElement, h.$('extensions-toggle'));
  assert.equal(h.document.querySelector('.app-shell').inert, false);
  await h.click('extensions-toggle');
  assert.equal(stage.firstElementChild, content);
  assert.equal(h.calls.filter(({ url }) => url === '/api/ui-plugins').length, 1);
  await h.click('runtime-backdrop');
  assert.equal(h.$('extensions-panel').hidden, true);
  assert.equal(h.document.activeElement, h.$('extensions-toggle'));
});

test('navigation lifecycle routes independent panels and restores focus', async () => {
  const h = navigationHarness({ narrow: true });
  await h.settle();
  assert.equal(h.$('session-sidebar').hidden, true, '窄屏会话侧栏初始关闭');
  h.$('message').value = '保留草稿';
  await h.click('memory-toggle');
  assert.equal(h.$('memory-panel').hidden, false);
  assert.equal(h.$('runtime-drawer').hidden, true);
  assert.equal(h.$('memory-toggle').getAttribute('aria-expanded'), 'true');
  assert.equal(h.document.querySelector('.app-shell').inert, true);
  assert.equal(h.document.activeElement, h.$('memory-close'));
  assert.equal(h.calls.filter(({ url }) => url === '/api/memory').length, 1);
  const retract = h.$('memory-list').querySelector('button');
  h.key('Tab', true);
  assert.equal(h.document.activeElement, retract, 'Shift+Tab 留在面板内');
  h.key('Tab');
  assert.equal(h.document.activeElement, h.$('memory-close'));
  assert.equal(h.key('Escape').defaultPrevented, true);
  assert.equal(h.$('memory-panel').hidden, true);
  assert.equal(h.document.activeElement, h.$('memory-toggle'));
  assert.equal(h.document.querySelector('.app-shell').inert, false);
  await h.click('runtime-toggle');
  assert.equal(h.$('runtime-drawer').hidden, false);
  await h.poll();
  assert.equal(h.calls.filter(({ url }) => url === '/api/memory').length, 1, '运行详情不读取记忆');
  // 即使入口被程序触发，也不能堆叠模态层。
  h.$('memory-toggle').click();
  await h.settle();
  assert.equal(h.$('runtime-drawer').hidden, true);
  assert.equal(h.$('memory-panel').hidden, false);
  await h.click('runtime-backdrop');
  assert.equal(h.$('memory-panel').hidden, true);
  await h.click('session-toggle');
  assert.equal(h.$('session-sidebar').hidden, false);
  assert.equal(h.$('session-sidebar').getAttribute('aria-modal'), 'true');
  h.key('Escape');
  assert.equal(h.document.activeElement, h.$('session-toggle'));
  assert.equal(h.$('message').value, '保留草稿');
});

test('session navigation closes after selection and survives breakpoint changes', async () => {
  const h = navigationHarness({ narrow: true, hash: '#session=aaaaaaaa' });
  await h.settle();
  assert.match(h.$('conversation').textContent, /已保存的消息/);
  h.$('message').value = '保留草稿';
  await h.click('session-toggle');
  h.$('session-list').querySelectorAll('button')[1].click();
  await h.settle();
  assert.equal(h.location.hash, '#session=bbbbbbbb');
  assert.equal(h.$('session-sidebar').hidden, true, '选中会话后回到对话');
  assert.equal(h.$('runtime-backdrop').hidden, true);
  await h.click('session-toggle');
  await h.resize(false);
  assert.equal(h.$('session-sidebar').hidden, false);
  assert.equal(h.$('session-sidebar').getAttribute('aria-modal'), null);
  assert.equal(h.$('runtime-backdrop').hidden, true);
  assert.equal(h.document.querySelector('.app-shell').inert, false);
  assert.equal(h.document.activeElement, h.$('session-new'));
  await h.click('memory-toggle');
  assert.equal(h.$('session-sidebar').inert, true);
  await h.resize(true);
  assert.equal(h.$('memory-panel').hidden, false, '切换断点不关闭另一模态面板');
  h.key('Escape');
  assert.equal(h.$('session-sidebar').hidden, true);
  await h.click('session-toggle');
  await h.click('session-new');
  assert.equal(h.$('session-sidebar').hidden, true);
  assert.equal(h.location.hash, '');
  assert.equal(h.$('message').value, '保留草稿');
});

test('the shell reserves desktop space for the sidebar and contains it when narrow', () => {
  const css = source('style.css');
  // 侧栏宽度只有一个来源，页头和正文都让开同一个值。
  assert.match(css, /--luna-sidebar-w:\s*244px/);
  assert.match(css, /\.chat-pane\s*\{[^}]*margin-left:\s*var\(--luna-sidebar-w\)/s);
  assert.match(css, /\.session-sidebar\s*\{[^}]*width:\s*var\(--luna-sidebar-w\)/s);
  assert.match(css, /@media\s*\(max-width:\s*800px\)/);
  const narrow = css.slice(css.indexOf('@media (max-width: 800px)'), css.indexOf('@media (max-width: 600px)'));
  assert.match(narrow, /\.chat-pane\s*\{[^}]*margin-left:\s*0/s);
  assert.match(narrow, /\.utility-header\s*\{[^}]*margin-left:\s*0/s, '窄屏页头必须让出侧栏宽度');
  assert.match(narrow, /\.session-sidebar\s*\{[^}]*width:\s*min\(320px, calc\(100vw - 36px\)\)/s);
});

test('the sidebar collapses, expands and remembers its width in the browser', async () => {
  const h = navigationHarness();
  await h.settle();
  assert.equal(h.document.documentElement.dataset.sidebar, 'expanded');
  assert.equal(h.document.documentElement.style['--luna-sidebar-w'], '244px');
  assert.equal(h.$('session-toggle').hidden, true, '展开时页头不显示展开入口');
  await h.click('sidebar-collapse');
  assert.equal(h.document.documentElement.dataset.sidebar, 'collapsed');
  assert.equal(h.document.documentElement.style['--luna-sidebar-w'], '0px', '折叠后主内容接管整块宽度');
  assert.equal(h.$('session-toggle').hidden, false, '折叠后页头出现展开入口');
  assert.equal(h.storage.get('luna.sidebar'), 'collapsed');
  await h.click('session-toggle');
  assert.equal(h.document.documentElement.dataset.sidebar, 'expanded');
  assert.equal(h.document.documentElement.style['--luna-sidebar-w'], '244px');

  // 拖拽把指针的 x 当作宽度，并且夹在 min/max 之间。
  const resizer = h.$('sidebar-resizer');
  resizer.emit('pointerdown', { button: 0, clientX: 244, preventDefault() {} });
  resizer.emit('pointermove', { clientX: 320 });
  assert.equal(h.document.documentElement.style['--luna-sidebar-w'], '320px');
  resizer.emit('pointermove', { clientX: 5000 });
  assert.equal(h.document.documentElement.style['--luna-sidebar-w'], '420px', '不允许无限拉宽');
  resizer.emit('pointermove', { clientX: 10 });
  assert.equal(h.document.documentElement.style['--luna-sidebar-w'], '200px', '不允许压到不可用');
  resizer.emit('pointerup', {});
  assert.equal(h.storage.get('luna.sidebarWidth'), '200');

  // 键盘同样可用：方向键 8px，Shift 32px，Home/End 到两端。
  resizer.emit('keydown', { key: 'ArrowRight', preventDefault() {} });
  assert.equal(h.document.documentElement.style['--luna-sidebar-w'], '208px');
  resizer.emit('keydown', { key: 'ArrowRight', shiftKey: true, preventDefault() {} });
  assert.equal(h.document.documentElement.style['--luna-sidebar-w'], '240px');
  resizer.emit('keydown', { key: 'End', preventDefault() {} });
  assert.equal(h.document.documentElement.style['--luna-sidebar-w'], '420px');
  resizer.emit('keydown', { key: 'Home', preventDefault() {} });
  assert.equal(h.document.documentElement.style['--luna-sidebar-w'], '200px');

  // 重新打开页面沿用上一次的状态。
  const reloaded = navigationHarness({ storage: h.storage });
  await reloaded.settle();
  assert.equal(reloaded.document.documentElement.style['--luna-sidebar-w'], '200px');

  // 窄屏不套用桌面折叠：侧栏由 drawer 控制，页头入口常驻。
  reloaded.$('session-sidebar').hidden = false;
  await reloaded.resize(true);
  assert.equal(reloaded.$('session-toggle').hidden, false);
  assert.equal(reloaded.$('session-sidebar').hidden, true);
});

test('settings is one modal with its own category navigation and panes', async () => {
  const h = navigationHarness();
  await h.settle();
  await h.click('settings-toggle');
  assert.equal(h.$('settings-panel').hidden, false);
  assert.equal(h.$('settings-panel').getAttribute('role'), 'dialog');
  assert.equal(h.$('settings-panel').getAttribute('aria-modal'), 'true');
  assert.equal(h.$('settings-pane-appearance').hidden, false);
  assert.equal(h.$('settings-pane-model').hidden, true);
  // 背景内容让位给模态，关闭后回到原来的会话上下文。
  assert.equal(h.document.querySelector('.app-shell').inert, true);
  await h.click('settings-tab-model');
  assert.equal(h.$('settings-tab-model').getAttribute('aria-selected'), 'true');
  assert.equal(h.$('settings-tab-appearance').getAttribute('aria-selected'), 'false');
  assert.equal(h.$('settings-pane-model').hidden, false);
  assert.equal(h.$('settings-pane-appearance').hidden, true);
  assert.equal(h.$('settings-tab-appearance').tabIndex, -1, '未选中的分类不参与 Tab');
  h.$('settings-tab-model').focus();
  h.$('settings-panel').emit('keydown', { key: 'ArrowDown', preventDefault() {} });
  assert.equal(h.document.activeElement, h.$('settings-tab-appearance'), '方向键在分类之间循环');
  // 模态自身占满视口，点对话框外的空白关闭。
  h.$('settings-panel').emit('click', { target: h.$('settings-panel') });
  assert.equal(h.$('settings-panel').hidden, true);
  assert.equal(h.document.querySelector('.app-shell').inert, false);
  assert.equal(h.document.activeElement, h.$('settings-toggle'));
});

test('visible sessions refresh without diagnostics or replacing the focused action', async () => {
  const h = navigationHarness();
  await h.settle();
  assert.equal(h.$('session-sidebar').hidden, false);
  assert.equal(h.$('runtime-drawer').hidden, true);
  const buttons = h.$('session-list').querySelectorAll('button');
  buttons[1].focus();
  const before = h.calls.filter(({ url }) => url === '/api/sessions').length;
  h.data.sessions.sessions[1].title = '标题更新';
  h.data.sessions.sessions.reverse();
  await h.poll();
  assert.equal(h.calls.filter(({ url }) => url === '/api/sessions').length, before + 1);
  assert.equal(h.document.activeElement, buttons[1], '列表更新仍保留原来的会话按钮');
  assert.match(buttons[1].textContent, /标题更新/);
  assert.equal(h.$('session-list').querySelectorAll('button')[0], buttons[1]);
  await h.poll();
  assert.equal(h.document.activeElement, buttons[1], '无变更的轮询不重建按钮');
  await h.resize(true);
  assert.equal(h.document.activeElement, h.$('session-toggle'));
  const hiddenCount = h.calls.length;
  await h.poll();
  assert.deepEqual(h.calls.slice(hiddenCount).map(({ url }) => url), ['/api/state']);
});

test('memory polling preserves the retract action and posts its exact fact', async () => {
  let finishRetraction;
  let posted;
  const h = navigationHarness({ respond: async (url, options, data) => {
    if (url !== '/api/memory/retract') return;
    posted = JSON.parse(options.body);
    await new Promise((resolve) => { finishRetraction = resolve; });
    data.memory = { facts: [], retracted: [posted] };
    return { ok: true, json: async () => ({}) };
  } });
  await h.settle();
  await h.click('memory-toggle');
  const button = h.$('memory-list').querySelector('button');
  button.focus();
  await h.poll();
  assert.ok(h.document.activeElement === button, '记忆轮询保留原撤回按钮');
  button.click();
  await h.settle();
  assert.equal(button.disabled, true);
  await h.poll();
  assert.ok(h.$('memory-list').querySelector('button') === button, '撤回进行中不替换按钮');
  assert.equal(button.disabled, true, '轮询不能重新启用正在撤回的按钮');
  assert.deepEqual(posted, { text: '测试事实', at: '2026-09-25T10:00:00Z' });
  finishRetraction();
  await h.settle();
  assert.equal(h.$('memory-list').childElementCount, 0);
  assert.equal(h.$('memory-empty').hidden, false);
  assert.match(h.$('memory-status').textContent, /这一条已撤回/);
  assert.ok(h.document.activeElement === h.$('memory-close'), '撤回后焦点留在面板内');
});

test('navigation preserves running and switching guards without discarding drafts', async () => {
  let finishRead;
  let finishRun;
  const h = navigationHarness({ respond: async (url) => {
    if (url === '/api/sessions/bbbbbbbb') {
      await new Promise((resolve) => { finishRead = resolve; });
      return { ok: true, json: async () => ({ records: [] }) };
    }
    if (url === '/api/runs') {
      await new Promise((resolve) => { finishRun = resolve; });
      return { ok: false, status: 409, json: async () => ({ error: '测试准入拒绝' }) };
    }
  } });
  await h.settle();
  h.$('session-list').querySelectorAll('button')[1].click();
  await h.settle();
  assert.equal(h.$('send').disabled, true);
  assert.equal(h.$('session-new').disabled, true);
  assert.ok(h.$('session-list').querySelectorAll('button').every((button) => button.disabled));
  await h.click('session-new');
  assert.equal(h.location.hash, '#session=bbbbbbbb');
  finishRead();
  await h.settle();
  assert.equal(h.$('send').disabled, false);
  assert.equal(h.$('session-new').disabled, false);
  h.$('message').value = '发送内容';
  h.$('chat-form').emit('submit');
  await h.settle();
  assert.equal(h.$('session-new').disabled, true);
  assert.ok(h.$('session-list').querySelectorAll('button').every((button) => button.disabled));
  h.location.hash = '#session=aaaaaaaa';
  await h.settle();
  assert.equal(h.location.hash, '#session=bbbbbbbb', '运行中更改 hash 会恢复原会话');
  await h.click('memory-toggle');
  finishRun();
  await h.settle();
  assert.ok(h.document.activeElement === h.$('memory-close'), '运行结束不夺走面板焦点');
  assert.equal(h.$('session-new').disabled, false);
  assert.equal(h.$('message').value, '发送内容', '准入失败保留原有草稿恢复行为');
});

test('memory waits out an older read before refreshing a completed retraction', async () => {
  let finishRead;
  let reads = 0;
  const h = navigationHarness({ respond: async (url, options, data) => {
    if (url === '/api/memory' && ++reads === 2) {
      const older = data.memory;
      await new Promise((resolve) => { finishRead = resolve; });
      return { ok: true, json: async () => older };
    }
    if (url === '/api/memory/retract') {
      data.memory = { facts: [], retracted: [JSON.parse(options.body)] };
      return { ok: true, json: async () => ({}) };
    }
  } });
  await h.settle();
  await h.click('memory-toggle');
  await h.poll();
  const button = h.$('memory-list').querySelector('button');
  button.focus();
  button.click();
  await h.settle();
  assert.equal(button.disabled, true);
  finishRead();
  await h.settle();
  assert.equal(reads, 3, '旧读取结束后确实重新读取，而不是用旧事实宣布撤回完成');
  assert.equal(h.$('memory-list').childElementCount, 0);
  assert.match(h.$('memory-status').textContent, /这一条已撤回/);
});

test('closing a panel before its frame or request completes cannot reopen it', async () => {
  let finishRead;
  const h = navigationHarness({ narrow: true, respond: async (url) => {
    if (url === '/api/memory') {
      await new Promise((resolve) => { finishRead = resolve; });
      return { ok: false, status: 500, json: async () => ({ error: '测试读取失败' }) };
    }
  } });
  await h.settle();
  h.$('memory-toggle').click();
  h.key('Escape');
  finishRead();
  await h.settle();
  assert.equal(h.$('memory-panel').hidden, true);
  assert.equal(h.$('memory-panel').classList.contains('is-open'), false);
  assert.equal(h.$('runtime-backdrop').hidden, true);
  assert.ok(h.document.activeElement === h.$('memory-toggle'));
  assert.match(h.$('memory-status').textContent, /无法读取记忆/);
  await h.click('session-toggle');
  await h.click('runtime-backdrop');
  assert.ok(h.document.activeElement === h.$('session-toggle'));
});

test('SSE parser preserves app-owned type and JSON payload', () => {
  const { parseEventBlock } = require('./app.js');
  assert.deepEqual(parseEventBlock('event: tool.finished\ndata: {"generation":2,"version":"v2","plugin_pid":44}\n'), {
    type: 'tool.finished', data: { generation: 2, version: 'v2', plugin_pid: 44 }
  });
});

test('assistant markdown becomes safe structured blocks and inline runs', () => {
  const { parseMarkdownBlocks, parseInline } = require('./app.js');
  assert.deepEqual(parseMarkdownBlocks('结果如下：\n\n- **输入**：` moon `\n- **输出**：`moon`\n\n```text\nraw <tag>\n```'), [
    { type: 'paragraph', text: '结果如下：' },
    { type: 'list', ordered: false, items: ['**输入**：` moon `', '**输出**：`moon`'] },
    { type: 'code', language: 'text', text: 'raw <tag>' }
  ]);
  assert.deepEqual(parseInline('**输入**：` moon `'), [
    { type: 'strong', text: '输入' },
    { type: 'text', text: '：' },
    { type: 'code', text: ' moon ' }
  ]);
});

test('tool summary preserves immutable execution identity', () => {
  const { toolSummary } = require('./app.js');
  assert.equal(toolSummary({ generation: 3, version: 'v1', plugin_pid: 91 }), 'generation 3 · v1 · PID 91');
  assert.equal(toolSummary({}), 'generation — · — · PID —');
});

test('tool copy names each tool and falls back without inventing one', () => {
  const { toolLabel } = require('./app.js');
  assert.deepEqual(toolLabel('luna_text_transform'), {
    noun: '文本转换', running: '正在转换文本…', finished: '文本转换完成', failed: '文本转换失败'
  });
  assert.deepEqual(toolLabel('luna_read_file'), {
    noun: '读取文件', running: '正在读取文件…', finished: '读取文件完成', failed: '读取文件失败'
  });
  assert.deepEqual(toolLabel(undefined), {
    noun: '工具调用', running: '正在调用工具…', finished: '工具调用完成', failed: '工具调用失败'
  });
  assert.deepEqual(toolLabel('luna_unknown'), toolLabel(undefined), 'an unknown tool must not borrow a known tool name');
});

test('tool activity copy is per tool while values remain exact', () => {
  const { toolActivityLabel, candidateLabel, reloadCopy } = require('./app.js');
  assert.equal(toolActivityLabel('luna_text_transform', 'running'), '正在转换文本…');
  assert.equal(toolActivityLabel('luna_text_transform', 'finished'), '文本转换完成');
  assert.equal(toolActivityLabel('luna_text_transform', 'failed'), '文本转换失败');
  assert.equal(toolActivityLabel('luna_read_file', 'running'), '正在读取文件…');
  assert.equal(toolActivityLabel('luna_read_file', 'finished'), '读取文件完成');
  assert.equal(toolActivityLabel('luna_read_file', 'failed'), '读取文件失败');
  assert.equal(toolActivityLabel('luna_read_file', 'unknown-state'), '读取文件');
  assert.equal(toolActivityLabel(undefined, 'running'), '正在调用工具…');
  assert.equal(candidateLabel('v1'), '稳定版本 v1');
  assert.equal(candidateLabel('v2'), '候选版本 v2');
  assert.equal(candidateLabel('broken'), '故障演练 broken');
  assert.deepEqual(reloadCopy('pending', 'v2'), { summary: '正在验证 v2…', technical: '' });
  assert.deepEqual(reloadCopy('success', 'v2'), { summary: 'v2 已启用。', technical: '' });
  assert.deepEqual(reloadCopy('failure', 'broken', 'handshake timeout'), {
    summary: '无法启用 broken，当前版本保持不变。', technical: 'handshake timeout'
  });
});

test('valueOrDash keeps exact values and marks missing ones', () => {
  const { valueOrDash } = require('./app.js');
  assert.equal(valueOrDash(0), '0');
  assert.equal(valueOrDash('v1'), 'v1');
  assert.equal(valueOrDash(12), '12');
  assert.equal(valueOrDash(undefined), '—');
  assert.equal(valueOrDash(null), '—');
  assert.equal(valueOrDash(''), '—');
});

test('plugin status copy distinguishes active, draining and failed records', () => {
  const { pluginStatusLabel } = require('./app.js');
  assert.equal(pluginStatusLabel('active'), '启用中');
  assert.equal(pluginStatusLabel('retiring'), '退役中');
  assert.equal(pluginStatusLabel('failed'), '不可用');
  assert.equal(pluginStatusLabel(undefined), '状态未知');
  assert.equal(pluginStatusLabel('something-else'), '状态未知');
});

test('plugin rows cover every tool and stay honest for empty and partial states', () => {
  const { pluginRows } = require('./app.js');
  assert.deepEqual(pluginRows([
    { tool: 'luna_text_transform', generation: 2, version: 'v2', plugin_pid: 91, candidate: 'v2', status: 'active', inflight: 0 },
    { tool: 'luna_read_file', generation: 2, version: 'v2', plugin_pid: 92, candidate: 'v2', status: 'active', inflight: 1 }
  ]), [
    { tool: 'luna_text_transform', label: '文本转换', status: '启用中', identity: 'generation 2 · v2 · PID 91' },
    { tool: 'luna_read_file', label: '读取文件', status: '启用中', identity: 'generation 2 · v2 · PID 92' }
  ]);

  // A tool mid-replacement reports both generations; both rows are shown.
  assert.deepEqual(pluginRows([
    { tool: 'luna_read_file', generation: 3, version: 'v2', plugin_pid: 93, candidate: 'v2', status: 'active', inflight: 0 },
    { tool: 'luna_read_file', generation: 2, version: 'v1', plugin_pid: 41, candidate: 'v1', status: 'retiring', inflight: 1 }
  ]), [
    { tool: 'luna_read_file', label: '读取文件', status: '启用中', identity: 'generation 3 · v2 · PID 93' },
    { tool: 'luna_read_file', label: '读取文件', status: '退役中', identity: 'generation 2 · v1 · PID 41' }
  ]);

  // No records, no payload, or a record without fields: never an invented tool.
  assert.deepEqual(pluginRows([]), []);
  assert.deepEqual(pluginRows(undefined), []);
  assert.deepEqual(pluginRows(null), []);
  assert.deepEqual(pluginRows('luna_read_file'), []);
  assert.deepEqual(pluginRows([{}]), [
    { tool: '—', label: '工具调用', status: '状态未知', identity: 'generation — · — · PID —' }
  ]);
  assert.deepEqual(pluginRows([null, { tool: 'luna_read_file', version: 'v1', status: 'failed' }]), [
    { tool: '—', label: '工具调用', status: '状态未知', identity: 'generation — · — · PID —' },
    { tool: 'luna_read_file', label: '读取文件', status: '不可用', identity: 'generation — · v1 · PID —' }
  ]);
});

test('chat markup is conversation-first with an accessible hidden runtime drawer', () => {
  const html = source('index.html');
  assert.match(html, /<title>Luna<\/title>/);
  assert.match(html, />Luna<\/span>/);
  assert.match(html, /会话记录和记忆事实都保存在本机/);
  assert.match(html, /有什么想一起看看？/);
  assert.match(html, /给 Luna 发消息…/);
  assert.match(html, /id="send"[^>]*aria-label="发送"/);
  assert.match(html, /id="conversation-title"/);
  assert.match(html, /id="runtime-toggle"[^>]*aria-expanded="false"[^>]*aria-controls="runtime-drawer"/);
  assert.match(html, /id="runtime-drawer"[^>]*hidden/);
  assert.match(html, /id="runtime-backdrop"[^>]*hidden/);
  assert.match(html, /id="runtime-close"[^>]*aria-label="关闭运行详情"/);
  assert.match(html, /id="transcript"[^>]*tabindex="0"/);
  assert.equal(/id="transcript"[^>]*aria-live/.test(html), false, 'streaming transcript must not announce every token');
  assert.match(html, /<summary>生命周期<\/summary>/);
  assert.match(html, /<summary>技术详情<\/summary>/);
  assert.match(html, /<section class="drawer-section" aria-labelledby="plugins-title">/);
  assert.match(html, /<h3 id="plugins-title">工具插件<\/h3>/);

  for (const id of ['model', 'provider', 'host-pid', 'busy', 'plugins', 'plugins-empty', 'candidate', 'reload', 'reload-status', 'events']) {
    assert.match(html, new RegExp(`id="${id}"`));
  }
  for (const value of ['v1', 'v2', 'broken']) {
    assert.match(html, new RegExp(`<option value="${value}"`));
  }
});

test('the plugin section is an empty list in markup and never a fixed tool', () => {
  const html = source('index.html');
  const js = source('app.js');
  assert.match(html, /<dl id="plugins" class="fact-list"><\/dl>/, 'the drawer list is filled from state, not from markup');
  assert.match(html, /id="plugins-empty"[^>]*hidden[^>]*>暂未读到工具插件。</);
  for (const id of ['plugin-version', 'plugin-generation', 'plugin-pid']) {
    assert.equal(html.includes(id), false, `stale single-plugin field ${id} must be gone`);
  }
  assert.equal(html.includes('文本转换器'), false, 'the drawer must not be titled after one tool');
  assert.equal(js.includes('state.active'), false, 'the removed single-active field must not be read');
  assert.equal(js.includes('plugin-version'), false, 'the removed single-plugin field must not be written');
  assert.match(js, /pluginRows\(state\.plugins\)/, 'the drawer renders the plugins array');
});

test('the memory section is an empty list in markup and says what it can do', () => {
  const html = source('index.html');
  assert.match(html, /id="memory-panel"[^>]*aria-labelledby="memory-title"/);
  assert.match(html, /<h2 id="memory-title">记忆<\/h2>/);
  assert.match(html, /<ul id="memory-list" class="memory-list"><\/ul>/, 'the list is filled from the API, not from markup');
  assert.match(html, /id="memory-empty"[^>]*hidden[^>]*>还没有记录任何事实。/);
  assert.match(html, /id="memory-status"[^>]*role="status"/);
  assert.match(html, /只能撤回/, 'the section must say that retracting is all it can do');
});

test('memory rows show every fact newest first with where it came from', () => {
  const { memoryRows } = require('./app.js');
  const view = memoryRows({
    facts: [
      { text: 'prefers Go', at: '2026-09-25T10:00:00Z', source_session: 'aaaaaaaa11112222' },
      { text: 'uses voice input', at: '2026-09-25T11:00:00Z', source_session: 'bbbbbbbb33334444' }
    ],
    retracted: [{ text: 'a stale fact', at: '2026-09-24T09:00:00Z', retracted_at: '2026-09-25T12:00:00Z' }]
  });
  assert.equal(view.facts.length, 2);
  assert.equal(view.facts[0].text, 'uses voice input', 'the newest fact comes first');
  assert.equal(view.facts[1].text, 'prefers Go');
  assert.equal(view.facts[0].at, '2026-09-25T11:00:00Z', 'the exact timestamp is what a retraction posts back');
  assert.equal(view.facts[0].session, '#bbbbbbbb');
  assert.equal(view.retracted.length, 1);
  assert.equal(view.retracted[0].text, 'a stale fact');
});

test('memory rows stay honest for an empty or malformed payload', () => {
  const { memoryRows } = require('./app.js');
  assert.deepEqual(memoryRows(undefined), { facts: [], retracted: [] });
  assert.deepEqual(memoryRows({}), { facts: [], retracted: [] });
  assert.deepEqual(
    memoryRows({ facts: [null, {}, { text: 'no timestamp' }, { at: '2026-09-25T10:00:00Z' }] }),
    { facts: [], retracted: [] },
    'a fact without both a text and a timestamp is never rendered as a fact'
  );
});

test('a retraction names its target exactly and never invents one', () => {
  const { retractPayload } = require('./app.js');
  assert.deepEqual(retractPayload({ at: '2026-09-25T11:00:00Z', text: 'uses voice input' }), {
    at: '2026-09-25T11:00:00Z', text: 'uses voice input'
  });
  assert.equal(retractPayload({ at: '', text: 'x' }), null);
  assert.equal(retractPayload({ at: '2026-09-25T11:00:00Z', text: '' }), null);
  assert.equal(retractPayload({ text: 'x' }), null);
  assert.equal(retractPayload(null), null);
});

test('the memory section reads the API and posts the retraction to it', () => {
  const js = source('app.js');
  assert.match(js, /fetch\('\/api\/memory'/);
  assert.match(js, /fetch\('\/api\/memory\/retract'/);
  assert.match(js, /memoryRows\(payload\)/);
  assert.match(js, /retractPayload\(/);
  assert.equal(/innerHTML/.test(js), false, 'fact text reaches the DOM as text, never as markup');
});

test('the primary surface does not claim that nothing is stored', () => {
  const html = source('index.html');
  assert.equal(html.includes('不保存记录'), false, 'sessions are written to disk since the session slice');
  assert.equal(html.includes('刷新后不会保留'), false, 'a reload resumes the same session through the fragment');
  assert.match(html, /会话记录和记忆事实都保存在本机/);
});

test('the primary surface omits console-era and fabricated content', () => {
  const html = source('index.html');
  for (const text of ['Core preview', 'LOCAL KERNEL', '真实模型', 'Run agent', 'Idle', 'Finished', 'luna_text_transform', 'Moonline']) {
    assert.equal(html.includes(text), false, `unexpected primary copy: ${text}`);
  }
  assert.equal(source('app.js').includes('Moonline'), false, 'internal codename must not reach the frontend script');
  assert.equal(source('style.css').includes('Moonline'), false, 'internal codename must not reach the stylesheet');
  assert.equal(/<article[^>]*class="[^"]*assistant/.test(html), false, 'empty state must not fabricate an assistant turn');
  assert.equal(/\b(?:src|href)=["']https?:\/\//.test(html), false, 'remote assets are not allowed');
});

test('styles expose semantic light and dark tokens with opt-in controls and readable chat', () => {
  const css = source('style.css');
  const light = css.match(/:root\s*\{([^}]+)\}/)?.[1] || '';
  const dark = css.match(/:root\[data-theme="dark"\]\s*\{([^}]+)\}/)?.[1] || '';
  for (const token of ['bg', 'surface', 'raised', 'code', 'text', 'text-muted', 'text-subtle', 'border-weak', 'border',
                       'accent', 'accent-contrast', 'success', 'warning', 'danger', 'hover', 'active']) {
    assert.match(light, new RegExp(`--luna-${token}:`), `light token ${token}`);
    assert.match(dark, new RegExp(`--luna-${token}:`), `dark token ${token}`);
  }
  // 尺度只能来自这一套有限的值，组件不得自己决定。
  for (const scale of ['--luna-radius-xs: 4px', '--luna-radius-lg: 12px', '--luna-control-md: 32px',
                       '--luna-row-h: 32px', '--luna-font-label: 11px', '--luna-font-body: 15px',
                       '--luna-content-max: 820px', '--luna-sidebar-w: 244px', '--luna-gutter:', '--luna-topbar-h:']) {
    assert.ok(light.includes(scale), `missing scale token ${scale}`);
  }
  for (const control of ['button', 'input', 'list', 'list-item', 'status']) {
    assert.match(css, new RegExp(`\\.luna-${control}[\\s:{,.]`), `opt-in control ${control}`);
  }
  // topbar、正文和输入区共用同一个 content container，才有同一条左基线。
  assert.match(css, /\.content-container\s*\{[^}]*max-width:\s*var\(--luna-content-max\)/s);
  assert.match(css, /\.topbar-inner\s*\{/);
  assert.match(css, /\.composer-inner\s*\{/);
  assert.match(css, /\.utility-header\s*\{[^}]*margin-left:\s*var\(--luna-sidebar-w\)/s, '页头让开同一个侧栏宽度');
  assert.match(css, /\.transcript-content\s*\{[^}]*padding-block/s);
  assert.match(css, /height:\s*100dvh/);
  assert.match(css, /Noto Sans SC/);
  assert.doesNotMatch(css, /Noto Serif SC/);
  assert.match(css, /Iosevka Fixed/);
  assert.match(css, /\.assistant-body\s*>\s*p/);
  assert.match(css, /\.assistant-body\s+code/);
  assert.match(css, /\.assistant-body\s*>\s*pre/);
  assert.match(css, /@media\s*\(max-width:\s*600px\)/);
  assert.equal(/\.section-kicker|\.empty-kicker/.test(css), false, 'unused kicker styles must not remain');
  assert.equal(/\.turn\.assistant\s*\{[^}]*padding-right/.test(css), false, 'assistant turns must share one right edge with user turns');
  assert.match(css, /\.composer-hint[^}]*font-size:\s*var\(--luna-font-label\)/s);
  assert.match(css, /max-height:\s*92dvh/);
  assert.match(css, /prefers-reduced-motion:\s*reduce/);
  const reducedMotion = css.slice(css.indexOf('prefers-reduced-motion'));
  assert.equal(/transform:\s*none/.test(reducedMotion), false, 'reduced motion must not cancel the drawer transform');
  assert.match(css, /\.luna-mark[^}]*width:\s*24px/s);
  assert.equal(/gradient\s*\(/i.test(css), false, 'gradients are not allowed');
  assert.match(css, /\.composer\s*\{[^}]*border-radius:\s*var\(--luna-radius-lg\)/s, '输入和发送在同一个有边界的容器中');
  assert.match(css, /\.composer textarea\s*\{[^}]*border:\s*0/s);
  assert.match(css, /\.session-row\s*\{[^}]*border:\s*0/s, '历史记录是列表行而不是卡片');
  assert.match(css, /\.session-row\s*\{[^}]*height:\s*var\(--luna-row-h\)/s, '会话项是紧凑单行');
  assert.match(css, /\.session-title\s*\{[^}]*text-overflow:\s*ellipsis/s, '过长标题省略而不是撑宽');
  assert.match(css, /\.empty-moon\s*\{[^}]*width:\s*56px/s, '空状态图标是 illustration 尺寸，不是页面主体');
  assert.doesNotMatch(css, /border-radius:\s*(?:1[0-9]|[2-9][0-9])px/, '圆角只来自尺度 token');
  assert.doesNotMatch(css, /(?:^|\n)button\s*[{,:]/, '不为插件的任意原生按钮强加全局样式');
});

test('conversation header reflects the selected title while session rows hide technical metadata', async () => {
  const h = navigationHarness({ hash: '#session=aaaaaaaa' });
  await h.settle();
  assert.ok(h.$('conversation-title'), '主标题显示当前会话而非品牌或运行状态');
  assert.equal(h.$('conversation-title').textContent, '会话 A');
  const button = h.$('session-list').querySelector('button');
  assert.doesNotMatch(button.textContent, /aaaaaaaa|次运行|当前/);
  assert.match(button.getAttribute('title'), /aaaaaaaa.*1 次运行/);
  h.data.sessions.sessions[0].title = '新的标题';
  button.focus();
  await h.poll();
  assert.equal(h.$('conversation-title').textContent, '新的标题');
  assert.equal(h.document.activeElement, button);
  await h.click('session-new');
  assert.equal(h.$('conversation-title').textContent, '新会话');
});

test('frontend uses safe DOM APIs and includes interaction contracts', () => {
  const js = source('app.js');
  assert.equal(js.includes('innerHTML'), false);
  assert.equal(/\beval\s*\(/.test(js), false);
  assert.equal(js.includes('new Function'), false);
  assert.match(js, /currentTurn\.terminal\s*=\s*true/);
  assert.match(js, /if \(!currentTurn \|\| !currentTurn\.terminal\) showRunFailure\(error\.message\)/);
  assert.match(js, /function resolveOpenTools\(\)/);
  assert.match(js, /工具在完成前中断。/);
  assert.match(js, /终止事件/);
  assert.match(js, /if \(currentTurn && !currentTurn\.terminal && !currentTurn\.failed\)/);
  assert.match(js, /'回应在完成前中断了。'/);
  assert.match(js, /isComposing/);
  assert.match(js, /event\.key === 'Escape'/);
  assert.match(js, /\.inert\s*=/);
  assert.match(js, /document\.querySelector\('\.app-shell'\)/);
  assert.match(js, /appShell\.inert = value/);
  assert.match(js, /scrollHeight\s*-\s*scrollTop/);
  assert.match(source('index.html'), /回到最新/);
  assert.match(js, /Luna 正在回应…/);
  assert.match(js, /Luna 没能完成这次回应。/);
  assert.match(js, /正在连接…/);
  assert.match(js, /运行详情暂不可用/);
  assert.match(js, /const retryMessage = currentTurn\.message/);
  assert.match(js, /submitMessage\(retryMessage\)/);
  assert.match(js, /replaceChildren/);
  assert.match(js, /document\.createElement/);
  assert.match(js, /function renderMarkdown/);
  assert.match(js, /currentTurn\.answer \+= text/);
  assert.match(js, /renderMarkdown\(currentTurn\.body, currentTurn\.answer\)/);
  assert.match(js, /'工具'/);
  assert.match(js, /\$\('plugins-empty'\)\.hidden = rows\.length > 0/);
});

test('the current session travels in the URL hash and only a well formed id is used', () => {
  const { parseSessionHash, sessionHash, isSessionID } = require('./app.js');
  assert.equal(parseSessionHash('#session=8f2a1c4d9e0b'), '8f2a1c4d9e0b');
  assert.equal(parseSessionHash('session=8f2a1c4d9e0b'), '8f2a1c4d9e0b');
  assert.equal(parseSessionHash('#session=8f2a1c4d9e0b&other=1'), '8f2a1c4d9e0b');
  assert.equal(parseSessionHash('#session='), '');
  assert.equal(parseSessionHash('#session=../../etc/passwd'), '');
  assert.equal(parseSessionHash('#session=8F2A1C4D'), '', 'uppercase is outside the store charset');
  assert.equal(parseSessionHash('#session=short'), '', 'below the length bound');
  assert.equal(parseSessionHash('#other=1'), '');
  assert.equal(parseSessionHash(''), '');
  assert.equal(parseSessionHash(undefined), '');

  assert.equal(sessionHash('8f2a1c4d9e0b'), '#session=8f2a1c4d9e0b');
  assert.equal(sessionHash('bad id'), '', 'a fragment is only written for a usable id');
  assert.equal(isSessionID('0123456789abcdef'), true);
  assert.equal(isSessionID('a'.repeat(64)), true);
  assert.equal(isSessionID('a'.repeat(65)), false);
  assert.equal(isSessionID('01234567'), true);
  assert.equal(isSessionID('0123456'), false);
  assert.equal(isSessionID(undefined), false);
});

test('session rows carry title, relative time and run count while the current one is marked', () => {
  const { sessionRows, relativeTime } = require('./app.js');
  const now = Date.parse('2026-09-25T17:14:16+08:00');
  assert.deepEqual(sessionRows({
    sessions: [
      { id: '8f2a1c4d9e0b', title: '把这段文字改短', updated_at: '2026-09-23T17:14:16.123456789+08:00', run_count: 3 },
      { id: 'a1b2c3d4e5f6', title: '', updated_at: '', run_count: 0 }
    ]
  }, '8f2a1c4d9e0b', now), [
    { id: '8f2a1c4d9e0b', shortId: '8f2a1c4d', title: '把这段文字改短', time: '2 天前', runs: '3 次运行', current: true },
    { id: 'a1b2c3d4e5f6', shortId: 'a1b2c3d4', title: '未命名会话', time: '—', runs: '尚无运行', current: false }
  ]);

  // 相对时间每一档都有界；无法解析的值和未来时间都不编造。
  assert.equal(relativeTime('2026-09-25T17:13:46+08:00', now), '刚刚');
  assert.equal(relativeTime('2026-09-25T17:09:16+08:00', now), '5 分钟前');
  assert.equal(relativeTime('2026-09-25T14:14:16+08:00', now), '3 小时前');
  assert.equal(relativeTime('2026-08-16T17:14:16+08:00', now), '1 个月前');
  assert.equal(relativeTime('2025-07-25T17:14:16+08:00', now), '1 年前');
  assert.equal(relativeTime('not-a-time', now), '—');
  assert.equal(relativeTime('', now), '—');
  assert.equal(relativeTime('2027-01-01T00:00:00+08:00', now), '刚刚', '未来时间不倒负');

  // No list, an unreadable list or entries without an id: nothing is invented.
  assert.deepEqual(sessionRows({ sessions: [] }, ''), []);
  assert.deepEqual(sessionRows({ sessions: [] }), []);
  assert.deepEqual(sessionRows({}), []);
  assert.deepEqual(sessionRows(undefined), []);
  assert.deepEqual(sessionRows({ sessions: 'nope' }), []);
  assert.deepEqual(sessionRows({ sessions: [null, 'x', { id: '../../etc' }, { id: '8f2a1c4d9e0b' }] }, ''), [
    { id: '8f2a1c4d9e0b', shortId: '8f2a1c4d', title: '未命名会话', time: '—', runs: '运行次数未知', current: false }
  ], 'a row without a usable id cannot be switched to');

  // The server order (newest first) is kept; the browser does not re-sort it.
  const rows = sessionRows({
    sessions: [
      { id: 'aaaaaaaaaaaa', title: '先', updated_at: '2026-09-23T09:00:00+08:00', run_count: 2 },
      { id: 'bbbbbbbbbbbb', title: '后', updated_at: '2026-09-23T10:00:00+08:00', run_count: 1 }
    ]
  }, 'aaaaaaaaaaaa');
  assert.deepEqual(rows.map((row) => row.id), ['aaaaaaaaaaaa', 'bbbbbbbbbbbb']);
  assert.deepEqual(rows.map((row) => row.current), [true, false]);
});

test('a session replays in record order and tool calls attach to their answer', () => {
  const { replaySession } = require('./app.js');
  const replay = replaySession({
    id: '8f2a1c4d9e0b',
    title: '把这段文字改短',
    truncated: false,
    records: [
      { type: 'session', id: '8f2a1c4d9e0b', created_at: '2026-09-23T17:00:00+08:00', title: '把这段文字改短' },
      { type: 'message', run_id: 'r1', role: 'user', text: '把  moon  改短', at: '2026-09-23T17:00:01+08:00' },
      { type: 'tool_call', run_id: 'r1', name: 'luna_text_transform', arguments: '{"text":"  moon  "}', result: 'moon', error: '', at: '2026-09-23T17:00:02+08:00' },
      { type: 'message', run_id: 'r1', role: 'assistant', text: '结果如下：\n\n- `moon`', at: '2026-09-23T17:00:03+08:00' },
      { type: 'run', run_id: 'r1', started_at: '2026-09-23T17:00:00+08:00', ended_at: '2026-09-23T17:00:03+08:00', status: 'ok' }
    ]
  });
  assert.equal(replay.title, '把这段文字改短');
  assert.deepEqual(replay.notices, []);
  assert.deepEqual(replay.turns, [
    { role: 'user', text: '把  moon  改短' },
    {
      role: 'assistant',
      answer: '结果如下：\n\n- `moon`',
      tools: [{ name: 'luna_text_transform', arguments: '{"text":"  moon  "}', result: 'moon', error: '', failed: false }],
      status: 'ok',
      failed: false
    }
  ], 'the session header record carries no conversation');
});

test('replay stays honest for empty, partial, unknown and truncated sessions', () => {
  const { replaySession } = require('./app.js');
  assert.deepEqual(replaySession({ records: [] }), { title: '未命名会话', truncated: false, turns: [], notices: [] });
  assert.deepEqual(replaySession({}), { title: '未命名会话', truncated: false, turns: [], notices: [] });
  assert.deepEqual(replaySession(undefined), { title: '未命名会话', truncated: false, turns: [], notices: [] });
  assert.deepEqual(replaySession({ records: 'nope', title: 'x' }), { title: 'x', truncated: false, turns: [], notices: [] });

  // A torn tail is reported as such, never silently repaired.
  const truncated = replaySession({ records: [{ type: 'message', role: 'user', text: '好' }], truncated: true });
  assert.equal(truncated.truncated, true);
  assert.deepEqual(truncated.notices, ['这个会话的最后一行没有写完，已按可读的部分回放。']);
  assert.equal(truncated.turns.length, 1);
  assert.equal(replaySession({ records: [{ type: 'message', role: 'user', text: '好' }], truncated: false }).notices.length, 0);

  // An unknown type, an unknown role and an unreadable entry are counted, never guessed at.
  const unknown = replaySession({
    records: [
      { type: 'message', role: 'user', text: '好' },
      { type: 'message', role: 'system', text: 'x' },
      { type: 'tool_result' },
      null,
      'nope',
      {}
    ]
  });
  assert.deepEqual(unknown.notices, ['有 5 条记录无法识别，未回放。']);
  assert.deepEqual(unknown.turns, [{ role: 'user', text: '好' }]);

  // Missing fields are replayed as they are, not filled in.
  const partial = replaySession({
    records: [
      { type: 'message', role: 'user' },
      { type: 'tool_call' },
      { type: 'run', status: 'error' }
    ]
  });
  assert.deepEqual(partial.turns, [
    { role: 'user', text: '' },
    {
      role: 'assistant',
      answer: '',
      tools: [{ name: undefined, arguments: undefined, result: undefined, error: '', failed: false }],
      status: 'error',
      failed: true
    }
  ]);

  // A run that ended without leaving a turn still says so; an ok one stays metadata.
  assert.deepEqual(replaySession({
    records: [
      { type: 'message', role: 'user', text: '好' },
      { type: 'run', status: 'cancelled' },
      { type: 'run', status: 'ok' }
    ]
  }).turns, [
    { role: 'user', text: '好' },
    { role: 'note', text: '这次运行被取消' }
  ]);
});

test('a replayed tool call shows the frozen facts and no invented identity', () => {
  const { toolCallFacts, argumentsText, runStatusLabel, sessionTitle, sessionTime, runCountLabel } = require('./app.js');

  // The model's argument text is JSON; it is pretty printed for reading and
  // shown verbatim when it is not JSON at all.
  assert.equal(argumentsText('{"text":"  moon  "}'), '{\n  "text": "  moon  "\n}');
  assert.equal(argumentsText('not json'), 'not json');
  assert.equal(argumentsText(''), '—');
  assert.equal(argumentsText(undefined), '—');
  assert.equal(argumentsText({ text: 'moon' }), '—');

  assert.deepEqual(toolCallFacts({ name: 'luna_read_file', arguments: '{"path":"a.txt"}', result: 'raw <tag>', error: '' }), [
    { label: '工具', value: 'luna_read_file' },
    { label: '参数', value: '{\n  "path": "a.txt"\n}' },
    { label: '结果', value: 'raw <tag>' }
  ]);
  assert.deepEqual(toolCallFacts({ name: 'luna_text_transform', arguments: '{}', result: 'ignored', error: '工具执行失败' }), [
    { label: '工具', value: 'luna_text_transform' },
    { label: '参数', value: '{}' },
    { label: '错误', value: '工具执行失败' }
  ], 'a failed call reports the error instead of a result');
  assert.deepEqual(toolCallFacts({}), [
    { label: '工具', value: '—' },
    { label: '参数', value: '—' },
    { label: '结果', value: '—' }
  ]);
  assert.deepEqual(toolCallFacts(undefined), [
    { label: '工具', value: '—' },
    { label: '参数', value: '—' },
    { label: '结果', value: '—' }
  ]);

  // A record has no plugin identity field, so replay cannot show one.
  const labels = toolCallFacts({ name: 'luna_read_file', arguments: '{}', result: 'ok' }).map((fact) => fact.label);
  assert.equal(labels.includes('执行身份'), false);
  assert.equal(JSON.stringify(toolCallFacts({ name: 'luna_read_file', result: 'ok' })).includes('generation'), false);

  assert.equal(runStatusLabel('ok'), '这次运行已完成');
  assert.equal(runStatusLabel('error'), '这次运行失败了');
  assert.equal(runStatusLabel('cancelled'), '这次运行被取消');
  assert.equal(runStatusLabel('interrupted'), '这次运行中断了');
  assert.equal(runStatusLabel('something-else'), '这次运行的结果未知');
  assert.equal(runStatusLabel(undefined), '这次运行的结果未知');

  assert.equal(sessionTitle('  把文字改短  '), '把文字改短');
  assert.equal(sessionTitle(''), '未命名会话');
  assert.equal(sessionTitle(7), '未命名会话');

  assert.equal(sessionTime('2026-09-23T17:14:16Z'), '2026-09-23 17:14');
  assert.equal(sessionTime('2026-09-23 17:14'), '—');
  assert.equal(sessionTime(undefined), '—');

  assert.equal(runCountLabel(0), '尚无运行');
  assert.equal(runCountLabel(3), '3 次运行');
  assert.equal(runCountLabel('3'), '运行次数未知');
  assert.equal(runCountLabel(-1), '运行次数未知');
  assert.equal(runCountLabel(undefined), '运行次数未知');
});

test('a run carries the current session only when there is one', () => {
  const { runPayload } = require('./app.js');
  assert.deepEqual(runPayload('你好', ''), { message: '你好' });
  assert.equal('session_id' in runPayload('你好', ''), false, 'a new session sends no session_id at all');
  assert.deepEqual(runPayload('你好', '8f2a1c4d9e0b'), { message: '你好', session_id: '8f2a1c4d9e0b' });
  assert.equal('session_id' in runPayload('你好', 'not a session id'), false, 'a malformed id never reaches the server');
  assert.equal(runPayload('你好', undefined).message, '你好');
});

test('sessions and memory have independent navigation outside diagnostics', () => {
  const html = source('index.html');
  const runtime = html.match(/<aside\b[^>]*id="runtime-drawer"[\s\S]*?<\/aside>/)?.[0];
  const sessions = html.match(/<aside\b[^>]*id="session-sidebar"[\s\S]*?<\/aside>/)?.[0];
  const memory = html.match(/<aside\b[^>]*id="memory-panel"[\s\S]*?<\/aside>/)?.[0];
  assert.ok(sessions, '会话需要独立的侧栏，不能藏在运行详情内');
  assert.ok(memory, '记忆需要独立面板');
  assert.match(sessions, /id="session-new"/);
  assert.match(sessions, /id="session-list"/);
  assert.match(memory, /id="memory-list"/);
  assert.doesNotMatch(runtime, /id="(?:session-new|session-list|memory-list)"/);
  for (const [button, panel] of [['session-toggle', 'session-sidebar'], ['memory-toggle', 'memory-panel']]) {
    assert.match(html, new RegExp(`id="${button}"[^>]*aria-controls="${panel}"`));
  }
  for (const id of ['session-new', 'session-list', 'memory-list', 'session-status', 'memory-status']) {
    assert.equal([...html.matchAll(new RegExp(`id="${id}"`, 'g'))].length, 1, `${id} 只能有一个实例`);
  }
});

test('the session surface keeps the replay area without widening the transcript contract', () => {
  const html = source('index.html');
  assert.match(html, /id="session-sidebar"[^>]*aria-labelledby="sessions-title"/);
  assert.match(html, /<h2 id="sessions-title"[^>]*>会话<\/h2>/);
  assert.match(html, /<button id="session-new"[\s\S]*?<svg class="ui-icon"[^>]*aria-hidden="true"[\s\S]*?<span>新建会话<\/span>\s*<\/button>/, '新建会话是带图标的动作控件');
  assert.match(html, /<ul id="session-list" class="session-list"><\/ul>/, 'the list is filled from the server, not from markup');
  assert.match(html, /id="sessions-empty"[^>]*hidden[^>]*>还没有历史会话。/);
  assert.match(html, /id="session-notices" class="session-notices" hidden/);
  assert.match(html, /id="current-session"/);
  assert.match(html, /id="session-status"[^>]*role="status"/);

  // The transcript keeps its own contract: labelled, keyboard scrollable and
  // never aria-live, because a replay must not be announced record by record.
  assert.match(html, /id="transcript"[^>]*tabindex="0"[^>]*aria-label="对话记录"/);
  assert.equal(/id="transcript"[^>]*aria-live/.test(html), false, 'the replay area must not announce every record');
  assert.match(html, /id="runtime-drawer"[^>]*role="dialog"[^>]*aria-modal="true"[^>]*hidden/);
  assert.match(html, /id="runtime-toggle"[^>]*aria-expanded="false"[^>]*aria-controls="runtime-drawer"/);

  assert.equal(/data-session-id/.test(html), false, 'no session is fabricated in markup');
  assert.equal(/\b(?:src|href)=["']https?:\/\//.test(html), false);
});

test('the front end keeps the session in the hash and reaches the DOM only through safe APIs', () => {
  const js = source('app.js');
  // 浏览器本地只放界面偏好：主题、侧栏折叠、侧栏宽度。会话仍然只走 hash。
  const storedKeys = new Set([...js.matchAll(/localStorage\.(?:getItem|setItem)\('([^']+)'/g)].map((match) => match[1]));
  assert.deepEqual([...storedKeys].sort(), ['luna.sidebar', 'luna.sidebarWidth', 'luna.theme'], 'storage 只保存界面偏好，会话仍用 hash');
  assert.match(js, /location\.hash/);
  assert.match(js, /addEventListener\('hashchange'/);
  assert.match(js, /history\.replaceState/);
  assert.match(js, /runPayload\(message, currentSessionID\)/, 'a message carries the current session id');
  assert.match(js, /adoptSession\(data\.session_id\)/, 'a new session id arrives on run.started');
  assert.match(js, /\/api\/sessions\/\$\{id\}/);
  assert.match(js, /fetch\('\/api\/sessions'/);
  assert.match(js, /replaySession\(detail\)/);
  assert.match(js, /emptyState\.hidden = sessionNotices\.childElementCount > 0 \|\| replay\.turns\.length > 0/);
  assert.match(js, /sessionRowNode/);
  assert.match(js, /assistantReplayNode/);
  assert.match(js, /valueOrDash\(state\.current_session_id\)/);
  assert.equal(js.includes('innerHTML'), false);
  assert.equal(/\beval\s*\(/.test(js), false);
  assert.equal(js.includes('new Function'), false);
});

test('every session style the script builds a class for exists in the stylesheet', () => {
  const css = source('style.css');
  const selectors = ['.session-new', '.session-list', '.session-row', '.session-row.is-current', '.session-title', '.session-meta', '.session-empty', '.session-status', '.session-notice', '.turn-note', '.assistant-body.failed', '.memory-list', '.memory-item', '.memory-text', '.memory-meta', '.memory-retract'];
  for (const selector of selectors) {
    assert.ok(
      [`${selector} {`, `${selector}:`, `${selector},`, `${selector}.`].some((form) => css.includes(form)),
      `missing style ${selector}`
    );
  }
  // 会话与记忆沿用语义主题，不用另一套装饰色。
  const added = css.slice(css.indexOf('/* --- Sidebar'));
  assert.equal(/gradient\s*\(/i.test(added), false);
});

test('the UI plugin listing shows every plugin and never hides a skipped one', () => {
  const { uiPluginRows, UI_PLUGIN_ENTRY_REASON } = require('./app.js');
  assert.deepEqual(uiPluginRows({
    plugins: [
      { name: 'counter', title: 'Counter', description: 'A counter whose timer unmount clears.', entry: 'plugin.js' },
      { name: 'hello', title: 'Hello', description: '', entry: 'plugin.js' }
    ],
    skipped: [
      { name: 'bare', reason: 'plugin.json is not valid JSON' },
      { name: 'mismatched', reason: '' }
    ]
  }), [
    { name: 'counter', title: 'Counter', description: 'A counter whose timer unmount clears.', entry: 'plugin.js', url: '/api/ui-plugins/counter/plugin.js', skipped: false, reason: '' },
    { name: 'hello', title: 'Hello', description: '', entry: 'plugin.js', url: '/api/ui-plugins/hello/plugin.js', skipped: false, reason: '' },
    { name: 'bare', title: 'bare', description: '', entry: '', url: '', skipped: true, reason: 'plugin.json is not valid JSON' },
    { name: 'mismatched', title: 'mismatched', description: '', entry: '', url: '', skipped: true, reason: '未说明原因' }
  ]);

  // A listed plugin this front end cannot import stays visible as a skip with a
  // reason of its own, rather than disappearing from the section.
  assert.deepEqual(uiPluginRows({ plugins: [{ name: 'escape', title: 'Escape', description: '', entry: '../../secret.js' }] }), [
    { name: 'escape', title: 'escape', description: '', entry: '', url: '', skipped: true, reason: UI_PLUGIN_ENTRY_REASON }
  ]);

  // A title-less but usable plugin is named by its directory.
  assert.equal(uiPluginRows({ plugins: [{ name: 'hello', entry: 'plugin.js' }] })[0].title, 'hello');

  // Nothing read, nothing invented.
  assert.deepEqual(uiPluginRows(undefined), []);
  assert.deepEqual(uiPluginRows({}), []);
  assert.deepEqual(uiPluginRows({ plugins: 'nope', skipped: null }), []);
  assert.deepEqual(uiPluginRows({ plugins: [null, 'nope', {}] }).map((row) => row.skipped), [true, true, true]);
  assert.deepEqual(uiPluginRows({ skipped: ['nope'] }), [
    { name: '—', title: '—', description: '', entry: '', url: '', skipped: true, reason: '未说明原因' }
  ]);
});

test('a plugin entry becomes an import path only while it stays inside the plugin', () => {
  const { uiPluginEntryURL, uiPluginEntrySafe, uiPluginNameValid } = require('./app.js');
  assert.equal(uiPluginEntryURL('hello', 'plugin.js'), '/api/ui-plugins/hello/plugin.js');
  assert.equal(uiPluginEntryURL('hello', ' dist/main.js '), '/api/ui-plugins/hello/dist/main.js');
  assert.equal(uiPluginEntryURL('hello', 'sub/dir/plugin.js'), '/api/ui-plugins/hello/sub/dir/plugin.js');

  const refused = ['', '   ', './plugin.js', '../plugin.js', 'sub/../../plugin.js', 'sub//plugin.js', 'a/b/', '/etc/passwd', 'C:\\plugin.js', 'https://evil.test/p.js', 'plugin.js?x=1', 'plugin.js#x', 'a%2fb.js', 'a\\b.js', 'plugin\u0000.js'];
  for (const entry of refused) {
    assert.equal(uiPluginEntrySafe(entry), false, `entry ${JSON.stringify(entry)} must not be accepted`);
    assert.equal(uiPluginEntryURL('hello', entry), '', `entry ${JSON.stringify(entry)} must not become an import path`);
  }
  assert.equal(uiPluginEntrySafe(undefined), false);
  assert.equal(uiPluginEntryURL('Hello', 'plugin.js'), '', 'a plugin name is one lowercase directory name');
  assert.equal(uiPluginEntryURL('../hello', 'plugin.js'), '');
  assert.equal(uiPluginEntryURL('', 'plugin.js'), '');
  assert.equal(uiPluginNameValid('hello-world'), true);
  assert.equal(uiPluginNameValid('hello.world'), false);
  assert.equal(uiPluginNameValid('a'.repeat(32)), true);
  assert.equal(uiPluginNameValid('a'.repeat(33)), false);
});

test('a plugin module must export both mount and unmount to be usable', () => {
  const { uiPluginMissingExports, UI_PLUGIN_REQUIRED_EXPORTS } = require('./app.js');
  assert.deepEqual(UI_PLUGIN_REQUIRED_EXPORTS, ['mount', 'unmount']);
  assert.deepEqual(uiPluginMissingExports({ mount() {}, unmount() {} }), []);
  assert.deepEqual(uiPluginMissingExports({ mount() {} }), ['unmount']);
  assert.deepEqual(uiPluginMissingExports({ unmount() {} }), ['mount']);
  assert.deepEqual(uiPluginMissingExports({}), ['mount', 'unmount']);
  assert.deepEqual(uiPluginMissingExports({ mount: 'not a function', unmount: null }), ['mount', 'unmount']);
  assert.deepEqual(uiPluginMissingExports(undefined), ['mount', 'unmount']);
  assert.deepEqual(uiPluginMissingExports(null), ['mount', 'unmount']);
  assert.deepEqual(uiPluginMissingExports('nope'), ['mount', 'unmount']);
});

test('every UI plugin failure carries its own message', () => {
  const { uiPluginErrorDetail, uiPluginImportError, uiPluginMissingExportError, uiPluginMountError, uiPluginUnmountError } = require('./app.js');
  assert.equal(uiPluginImportError('hello', 'Failed to fetch dynamically imported module'), '无法加载界面插件 hello 的入口文件：Failed to fetch dynamically imported module');
  assert.equal(uiPluginMissingExportError('hello', ['mount']), '界面插件 hello 缺少必需的导出 mount。');
  assert.equal(uiPluginMissingExportError('hello', ['mount', 'unmount']), '界面插件 hello 缺少必需的导出 mount、unmount。');
  assert.equal(uiPluginMountError('counter', 'boom'), '界面插件 counter 挂载失败，容器已移除：boom');
  assert.equal(uiPluginUnmountError('counter', 'boom'), '界面插件 counter 停用时清理失败，容器已移除：boom');

  assert.equal(uiPluginErrorDetail(new Error('boom')), 'boom');
  assert.equal(uiPluginErrorDetail({ message: 'boom' }), 'boom');
  assert.equal(uiPluginErrorDetail('boom'), 'boom');
  assert.equal(uiPluginErrorDetail(404), '404');
  assert.equal(uiPluginErrorDetail(undefined), '未知错误');
  assert.equal(uiPluginErrorDetail(null), '未知错误');
  assert.equal(uiPluginErrorDetail(''), '未知错误');
  assert.equal(uiPluginErrorDetail('   '), '未知错误');
  // A thrown value that cannot even be stringified still produces one line of text.
  assert.equal(uiPluginErrorDetail({ toString() { throw new Error('no'); } }), '未知错误');
});

test('the UI plugin enable and disable transitions are one pure machine', () => {
  const { uiPluginInitialState, uiPluginTransition, uiPluginToggleAction, uiPluginToggleLabel, uiPluginStatusText, uiPluginEnableFailureEvent, uiPluginDisableEvent } = require('./app.js');
  const initial = uiPluginInitialState();
  assert.deepEqual(initial, { status: 'disabled', error: '' }, 'a page load starts disabled, with nothing persisted');
  assert.equal(uiPluginToggleAction(initial.status), 'enable');
  assert.equal(uiPluginToggleLabel('disabled'), '启用');
  assert.equal(uiPluginStatusText(initial), '');

  const loading = uiPluginTransition(initial, 'enable');
  assert.deepEqual(loading, { status: 'loading', error: '' });
  assert.equal(uiPluginToggleAction('loading'), '', 'there is no second transition while one is in flight');
  assert.equal(uiPluginToggleLabel('loading'), '处理中…');
  assert.equal(uiPluginTransition(loading, 'enable'), loading, 'a second enable does not restart the load');

  const enabled = uiPluginTransition(loading, 'enabled');
  assert.deepEqual(enabled, { status: 'enabled', error: '' });
  assert.equal(uiPluginToggleAction('enabled'), 'disable');
  assert.equal(uiPluginToggleLabel('enabled'), '停用');
  assert.equal(uiPluginStatusText(enabled), '已启用 · 停用时会调用 unmount');

  const failed = uiPluginTransition(loading, { type: 'enable-failed', error: 'boom' });
  assert.deepEqual(failed, { status: 'failed', error: 'boom' });
  assert.equal(uiPluginStatusText(failed), 'boom', 'the row says what failed');
  assert.equal(uiPluginToggleAction('failed'), 'enable', 'a failed plugin can be tried again');
  assert.deepEqual(uiPluginTransition(failed, 'enable'), { status: 'loading', error: '' }, 'the last failure is cleared on the next try');

  const disabling = uiPluginTransition(enabled, 'disable');
  assert.deepEqual(disabling, { status: 'loading', error: '' });
  assert.deepEqual(uiPluginTransition(disabling, 'disabled'), { status: 'disabled', error: '' });

  // A throwing unmount still leaves the plugin off — the host removed the
  // container — and the error is carried on the disabled state.
  const unmountFailed = uiPluginTransition(disabling, { type: 'disable-failed', error: '清理失败' });
  assert.deepEqual(unmountFailed, { status: 'disabled', error: '清理失败' });
  assert.equal(uiPluginStatusText(unmountFailed), '清理失败');
  assert.equal(uiPluginToggleAction(unmountFailed.status), 'enable');

  assert.deepEqual(uiPluginTransition(enabled, 'nonsense'), enabled, 'an unknown event changes nothing');
  assert.deepEqual(uiPluginTransition(undefined, 'enable'), { status: 'loading', error: '' });
  assert.deepEqual(uiPluginTransition('nope', 'nonsense'), { status: 'disabled', error: '' });
  assert.deepEqual(uiPluginStatusText(undefined), '');

  // The two events the loader itself builds are decided here, not in the DOM
  // glue: a failed enable is a failure, and a throwing unmount is still a
  // disabled plugin with its error kept.
  assert.deepEqual(uiPluginEnableFailureEvent('boom'), { type: 'enable-failed', error: 'boom' });
  assert.deepEqual(uiPluginTransition(loading, uiPluginEnableFailureEvent('boom')), { status: 'failed', error: 'boom' });
  assert.deepEqual(uiPluginDisableEvent('counter', ''), { type: 'disabled' });
  assert.deepEqual(uiPluginDisableEvent('counter', '   '), { type: 'disabled' });
  assert.deepEqual(uiPluginDisableEvent('counter', 'boom'), { type: 'disable-failed', error: '界面插件 counter 停用时清理失败，容器已移除：boom' });
  assert.deepEqual(uiPluginTransition(disabling, uiPluginDisableEvent('counter', 'boom')), {
    status: 'disabled', error: '界面插件 counter 停用时清理失败，容器已移除：boom'
  }, 'the container is gone either way, so the plugin is off and the error is shown');
});

test('the host removes the container on every failed enable and on every disable', () => {
  const { uiPluginTeardown, uiPluginAbandonMount } = require('./app.js');
  let removed = 0;
  const target = { remove() { removed += 1; } };

  assert.deepEqual(uiPluginTeardown({ unmount() {} }, target, 'hello'), { type: 'disabled' });
  assert.equal(removed, 1, 'a clean unmount still leaves the host removing the container');

  assert.deepEqual(uiPluginTeardown({ unmount() { throw new Error('boom'); } }, target, 'counter'), {
    type: 'disable-failed',
    error: '界面插件 counter 停用时清理失败，容器已移除：boom'
  }, 'a throwing unmount is reported, and the plugin is still off');
  assert.equal(removed, 2, 'the container is removed even when unmount threw');

  // A plugin throwing something that is not an Error is reported the same way.
  assert.deepEqual(uiPluginTeardown({ unmount() { throw 'plain string'; } }, target, 'hello'), {
    type: 'disable-failed',
    error: '界面插件 hello 停用时清理失败，容器已移除：plain string'
  });
  assert.equal(removed, 3);
  assert.deepEqual(uiPluginTeardown({ unmount() { throw undefined; } }, target, 'hello'), {
    type: 'disable-failed',
    error: '界面插件 hello 停用时清理失败，容器已移除：未知错误'
  });
  assert.equal(removed, 4);

  // An enable that never finished is cleaned up here: the container created for
  // the attempt is removed and the stage is hidden again, so no half-mounted
  // plugin stays on screen after a failed import, a throwing mount or a missing
  // export.
  const stage = { hidden: false };
  uiPluginAbandonMount(stage, target);
  assert.equal(removed, 5);
  assert.equal(stage.hidden, true);
});

test('the host interface handed to a plugin is narrow and frozen', () => {
  const { uiPluginHostAPI, UI_PLUGIN_API_VERSION } = require('./app.js');
  const lines = [];
  const api = uiPluginHostAPI(UI_PLUGIN_API_VERSION, (message) => lines.push(message));
  assert.deepEqual(Object.keys(api), ['version', 'log'], 'a plugin gets the contract version and a log line, nothing else');
  assert.equal(api.version, '1');
  assert.equal(Object.isFrozen(api), true, 'a plugin cannot widen the interface it was handed');
  api.log('hello');
  api.log(7);
  api.log(undefined);
  assert.deepEqual(lines, ['hello', '7', ''], 'the log takes text, whatever the plugin passes');
  for (const absent of ['fetch', 'state', 'document', 'session', 'plugins', 'goto']) {
    assert.equal(Object.getOwnPropertyNames(api).includes(absent), false, `${absent} must not be handed out`);
  }
  assert.doesNotThrow(() => uiPluginHostAPI('1').log('no sink'), 'a missing sink must not throw inside the plugin call');
  assert.equal(uiPluginHostAPI(undefined).version, '');
});

test('the extensions panel carries a 界面插件 section and imports a plugin only on a click', () => {
  const html = source('index.html');
  const js = source('app.js');
  assert.match(html, /<section class="drawer-section" aria-labelledby="ui-plugins-title">/);
  assert.match(html, /<h3 id="ui-plugins-title">界面插件<\/h3>/);
  assert.match(html, /<ul id="ui-plugin-list" class="ui-plugin-list"><\/ul>/, 'the list is filled from the server, not from markup');
  assert.match(html, /id="ui-plugins-empty"[^>]*hidden[^>]*>暂未读到界面插件。/);
  assert.match(html, /id="ui-plugins-status"[^>]*role="status"/);
  assert.match(html, /刷新后回到停用/);
  assert.match(html, /id="ui-plugins-retry"[^>]*hidden[^>]*>重新读取插件列表<\/button>/);

  // The dynamic import is a runtime call inside the enable path and never a
  // top-level statement: this file is loaded by Node under node --test.
  assert.equal(/^\s*import\s/m.test(js), false, 'no static import may sit in this file');
  assert.match(js, /pluginModule = await import\(node\.row\.url\)/);
  assert.match(js, /uiPluginRows\(payload\)/);
  assert.match(js, /uiPluginHostAPI\(UI_PLUGIN_API_VERSION/);
  assert.match(js, /uiPluginTeardown\(mounted\.pluginModule, mounted\.target, node\.row\.title\)/, 'the unmount-and-remove guarantee is the tested helper');
  assert.match(js, /uiPluginAbandonMount\(node\.stage, target\)/, 'a failed enable cleans the container up through the tested helper');
  assert.match(js, /uiPluginEnableFailureEvent\(message\)/, 'a failed enable is decided by the tested helper');
  assert.match(js, /failUIPlugin\(node, target, uiPluginImportError/, 'a failed import names itself');
  assert.match(js, /failUIPlugin\(node, target, uiPluginMissingExportError/, 'a missing export names itself');
  assert.match(js, /failUIPlugin\(node, target, uiPluginMountError/, 'a throwing mount names itself');
  assert.match(js, /fetch\('\/api\/ui-plugins'/);
  assert.doesNotMatch(js, /localStorage\.(?:getItem|setItem)\('(?!luna\.(?:theme|sidebar|sidebarWidth)')/,
    '启用状态不持久化，只有界面偏好在浏览器本地');
  assert.equal(js.includes('sessionStorage'), false);
  assert.equal(js.includes('innerHTML'), false);
  assert.equal(js.includes('new Function'), false);
});

test('every UI plugin style the script builds a class for exists in the stylesheet', () => {
  const css = source('style.css');
  const selectors = ['.ui-plugin-hint', '.ui-plugin-list', '.ui-plugin-row', '.ui-plugin-row.skipped', '.ui-plugin-head', '.ui-plugin-title', '.ui-plugin-toggle', '.ui-plugin-toggle.is-on', '.ui-plugin-description', '.ui-plugin-reason', '.ui-plugin-reason-label', '.ui-plugin-stage', '.ui-plugin-target', '.ui-plugin-status', '.ui-plugin-status.failure', '.ui-plugin-log', '.ui-plugin-retry'];
  for (const selector of selectors) {
    assert.ok(
      [`${selector} {`, `${selector}:`, `${selector},`, `${selector}.`].some((form) => css.includes(form)),
      `missing style ${selector}`
    );
  }
  // 插件容器沿用语义主题，控件须显式使用 luna-* 类。
  const added = css.slice(css.indexOf('/* --- 界面插件'), css.indexOf('/* --- 记忆与回放提示'));
  assert.ok(added.length > 0, 'the UI plugin block must be present');
  assert.equal(/gradient\s*\(/i.test(added), false);
});
