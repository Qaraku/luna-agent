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
    // 真实 DOM 的 class 既是特性也是属性：className 赋值后 getAttribute('class')
    // 也要读得到；没有 class 的元素不该凭空多出一个 class 特性（真 DOM 里是 null）。
    get className() { return this.attributes.get('class') ?? ''; }
    set className(value) {
      const text = value === undefined || value === null ? '' : String(value);
      if (text === '') this.attributes.delete('class');
      else this.attributes.set('class', text);
    }
    // dataset 是 data-* 特性上的视图：两个方向都要通，删除也要落到特性上——
    // 产品代码两种写法都用（`dataset.state = …` 与 `delete dataset.resizing`）。
    get dataset() {
      const element = this;
      const attribute = (name) => `data-${name.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}`;
      return new Proxy({}, {
        get: (_target, name) => (typeof name === 'string' ? element.attributes.get(attribute(name)) : undefined),
        set: (_target, name, value) => { element.setAttribute(attribute(name), String(value)); return true; },
        has: (_target, name) => typeof name === 'string' && element.attributes.has(attribute(name)),
        deleteProperty: (_target, name) => { element.removeAttribute(attribute(name)); return true; }
      });
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
      // data-* 不再额外存一份：dataset 只是 attributes 上的视图（见 dataset getter）。
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
  // 贡献面板的图标是真 SVG：产品代码用 createElementNS 建它，适配器同样照建，
  // 只是不模拟命名空间（Element 本身不区分）。
  document.createElementNS = (_namespace, tag) => new Element(tag);
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
    sessions: { sessions: [{ id: 'aaaaaaaa', title: '会话 A', run_count: 1 }, { id: 'bbbbbbbb', title: '会话 B', run_count: 2 }] }
  };
  const context = vm.createContext({
    document, window, location, URLSearchParams, TextDecoder, console,
    history: { replaceState(_state, _title, url) { location._hash = url.includes('#') ? `#${url.split('#')[1]}` : ''; } },
    requestAnimationFrame: (fn) => { const frame = { run: fn }; frames.push(frame); return frame; },
    // 间隔定时器一并登记回调本身，所以 clearInterval 能把它从 poll() 的名单里摘掉；
    // 应用只在一次运行期间开一个秒表，结束时必须能停掉它。
    // setTimeout 与 rAF 共用一个待执行队列，settle() 按登记顺序把它们跑完。定时器
    // 必须有身份：clearTimeout 要真的把回调从队列里摘掉，否则一个已经被取消的瞬时
    // 提示仍然会照常触发，测试看到的状态变化顺序就和浏览器不一致。
    setTimeout: (fn) => { const timer = { run: fn }; frames.push(timer); return timer; },
    clearTimeout: (timer) => {
      const index = frames.indexOf(timer);
      if (index >= 0) frames.splice(index, 1);
    },
    setInterval: (fn) => { intervals.push(fn); return fn; },
    clearInterval: (fn) => {
      const index = intervals.indexOf(fn);
      if (index >= 0) intervals.splice(index, 1);
    },
    fetch: async (url, options) => {
      calls.push({ url, options });
      const custom = respond && await respond(url, options, data);
      if (custom) return custom;
      const payload = url === '/api/sessions' ? data.sessions
        : url.startsWith('/api/sessions/') ? { records: [{ type: 'message', role: 'user', text: '已保存的消息' }] } : {};
      return { ok: true, json: async () => payload };
    }
  });
  // 首帧主题现在是一个外置脚本（内联脚本会被服务的 CSP 拒绝），所以从 theme.js 取源码，
  // 而 index.html 只负责在样式表之前引用它——这条断言同时守住"必须是同步引用"。
  const linked = source('index.html').match(/<script src="\/theme\.js"><\/script>/);
  assert.ok(linked, 'index.html 必须在样式表之前同步引用 /theme.js');
  // 没有声明图标时浏览器会自己去要 /favicon.ico，服务没有这个路由，于是每次加载都
  // 留下一条控制台 404；一条无害的噪声会训练人和测试忽略控制台错误。
  assert.match(source('index.html'), /<link rel="icon" href="data:,">/, 'index.html 必须声明一个图标');
  const bootstrap = linked ? source('theme.js') : undefined;
  if (bootstrap) vm.runInContext(bootstrap, context, { filename: 'theme.js' });
  const bootstrapTheme = document.documentElement.dataset.theme;
  vm.runInContext(source('app.js'), context, { filename: 'app.js' });
  const settle = async () => {
    await new Promise((resolve) => setImmediate(resolve));
    while (frames.length) frames.shift().run();
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

// 一个启用中的能力贡献了一块面板：宿主从 /api/state 只拿到"在用"和"模块入口"，
// 面板内容由模块自己带来。这个夹具把那份状态钉住，并给出定位入口的两种方式。
function capabilityHarness(options = {}) {
  const state = { capabilities: [{
    id: 'memory', title: 'Memory', deployment: 'builtin', state: 'enabled',
    contributions: [{ kind: 'panel', id: 'memory' }],
    panels: [{ id: 'memory', title: '记忆', entry: '/api/memory/panel.js' }]
  }] };
  const h = navigationHarness({
    ...options,
    respond: async (url, requestOptions, data) => {
      if (url === '/api/state') return { ok: true, json: async () => state };
      return options.respond ? options.respond(url, requestOptions, data) : undefined;
    }
  });
  h.capabilityState = state;
  h.capabilityToggle = () => h.document.querySelector('.capability-panel-toggle');
  h.capabilityToggles = () => [...h.document.querySelectorAll('.capability-panel-toggle')];
  return h;
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

test('a contributed panel routes independently of diagnostics and restores focus', async () => {
  const h = capabilityHarness({ narrow: true });
  await h.settle();
  assert.equal(h.$('session-sidebar').hidden, true, '窄屏会话侧栏初始关闭');
  h.$('message').value = '保留草稿';
  const entry = h.capabilityToggle();
  entry.focus();
  entry.click();
  await h.settle();
  const drawer = h.$('capability-panel-memory');
  assert.equal(drawer.hidden, false);
  assert.equal(h.$('runtime-drawer').hidden, true);
  assert.equal(entry.getAttribute('aria-expanded'), 'true');
  assert.equal(h.document.querySelector('.app-shell').inert, true);
  assert.equal(h.document.activeElement, drawer.querySelector('button'));
  assert.equal(h.calls.filter(({ url }) => url.startsWith('/api/memory')).length, 0, '宿主自己从不请求能力自己的接口');
  // 焦点约束覆盖面板里的每个可聚焦元素：从最后一个回绕到关闭按钮，再回绕回来。
  const content = h.document.createElement('button');
  content.textContent = '模块自己的控件';
  drawer.querySelector('.drawer-body').append(content);
  content.focus();
  assert.equal(h.document.activeElement, content);
  h.key('Tab');
  assert.equal(h.document.activeElement, drawer.querySelector('button'), 'Tab 从最后一个回绕到关闭按钮');
  h.key('Tab', true);
  assert.equal(h.document.activeElement, content, 'Shift+Tab 留在面板内');
  assert.equal(h.key('Escape').defaultPrevented, true);
  assert.equal(drawer.hidden, true);
  assert.equal(h.document.activeElement, entry);
  assert.equal(h.document.querySelector('.app-shell').inert, false);
  await h.click('runtime-toggle');
  assert.equal(h.$('runtime-drawer').hidden, false);
  await h.poll();
  assert.equal(h.calls.filter(({ url }) => url.startsWith('/api/memory')).length, 0, '运行详情不读取能力自己的接口');
  // 即使入口被程序触发，也不能堆叠模态层。
  entry.click();
  await h.settle();
  assert.equal(h.$('runtime-drawer').hidden, true);
  assert.equal(drawer.hidden, false);
  await h.click('runtime-backdrop');
  assert.equal(drawer.hidden, true);
  await h.click('session-toggle');
  assert.equal(h.$('session-sidebar').hidden, false);
  assert.equal(h.$('session-sidebar').getAttribute('aria-modal'), 'true');
  h.key('Escape');
  assert.equal(h.document.activeElement, h.$('session-toggle'));
  assert.equal(h.$('message').value, '保留草稿');
});

test('session navigation closes after selection and survives breakpoint changes', async () => {
  const h = capabilityHarness({ narrow: true, hash: '#session=aaaaaaaa' });
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
  const entry = h.capabilityToggle();
  entry.click();
  await h.settle();
  assert.equal(h.$('session-sidebar').inert, true);
  await h.resize(true);
  assert.equal(h.$('capability-panel-memory').hidden, false, '切换断点不关闭另一模态面板');
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

test('capability panels come only from enabled capabilities, in the kernel order', () => {
  const { capabilityPanels } = require('./app.js');
  assert.deepEqual(capabilityPanels([
    { id: 'memory', title: 'Memory', state: 'enabled', panels: [{ id: 'memory', title: '记忆', entry: '/api/memory/panel.js' }] },
    { id: 'quiet', title: 'Quiet', state: 'disabled', panels: [{ id: 'quiet', title: '安静的', entry: '/api/quiet/panel.js' }] },
    { id: 'notes', title: 'Notes', state: 'enabled', panels: [
      { id: 'notes', title: '笔记', entry: '/api/notes/panel.js' },
      { id: 'notes-index', title: '', entry: '/api/notes/index.js' }
    ] }
  ]), [
    { id: 'memory', title: '记忆', entry: '/api/memory/panel.js' },
    { id: 'notes', title: '笔记', entry: '/api/notes/panel.js' },
    { id: 'notes-index', title: 'notes-index', entry: '/api/notes/index.js' }
  ], '只有 enabled 的能力贡献面板，顺序按内核注册顺序，缺 title 时用 id');

  // 一条停用中、失败或状态未知的能力什么都不贡献。
  for (const state of ['disabled', 'failed', 'retiring', '']) {
    assert.deepEqual(capabilityPanels([
      { id: 'memory', title: 'Memory', state, panels: [{ id: 'memory', title: '记忆', entry: '/api/memory/panel.js' }] }
    ]), [], `状态 ${JSON.stringify(state)} 不贡献面板`);
  }
  assert.deepEqual(capabilityPanels([{ id: 'memory', title: 'Memory', panels: [{ id: 'memory', title: '记忆', entry: '/api/memory/panel.js' }] }]), [],
    '没有状态就当作不在用');

  // 载荷缺失或形状不对：一块面板都不编出来。
  assert.deepEqual(capabilityPanels(undefined), []);
  assert.deepEqual(capabilityPanels(null), []);
  assert.deepEqual(capabilityPanels('nope'), []);
  assert.deepEqual(capabilityPanels([]), []);
  assert.deepEqual(capabilityPanels([null, 'x', {}]), []);
  assert.deepEqual(capabilityPanels([{ id: 'memory', state: 'enabled' }]), [], '没有 panels 数组就没有面板');
  assert.deepEqual(capabilityPanels([{ id: 'memory', state: 'enabled', panels: 'nope' }]), []);
  assert.deepEqual(capabilityPanels([{ id: 'memory', state: 'enabled', panels: [null, 'x', {}] }]), []);
  assert.deepEqual(capabilityPanels([{ id: 'memory', state: 'enabled', panels: [
    { id: 'memory', title: '记忆' },
    { id: '  ', title: '记忆', entry: '/api/memory/panel.js' },
    { title: '记忆', entry: '/api/memory/panel.js' }
  ] }]), [], '缺 id 或缺 entry 的面板不会变成一个按不动的入口');
  assert.deepEqual(capabilityPanels([{ id: 'memory', state: 'enabled', panels: [
    { id: 7, title: '记忆', entry: '/api/memory/panel.js' },
    { id: 'memory', title: '记忆', entry: 7 }
  ] }]), [], '字段不是字符串时不猜一个出来');
});

test('a panel entry becomes an import path only while it stays on this origin', () => {
  const { capabilityPanelEntryURL, capabilityPanelElementID, capabilityPanelText, capabilityPanelEntryError } = require('./app.js');
  assert.equal(capabilityPanelEntryURL('/api/memory/panel.js'), '/api/memory/panel.js');
  assert.equal(capabilityPanelEntryURL('  /api/memory/panel.js  '), '/api/memory/panel.js');

  const refused = ['', '   ', 'panel.js', './panel.js', '../panel.js', 'sub/panel.js', '//evil.test/p.js',
    'https://evil.test/p.js', 'http://evil.test/p.js', 'data:text/javascript,0', '/api/memory/panel.js?x=1',
    '/api/memory/panel.js#x', 'a%2fb.js', 'a\\b.js', '/api/panel\u0000.js', `/${'a'.repeat(200)}`];
  for (const entry of refused) {
    assert.equal(capabilityPanelEntryURL(entry), '', `entry ${JSON.stringify(entry)} must not become an import path`);
  }
  assert.equal(capabilityPanelEntryURL(undefined), '');
  assert.equal(capabilityPanelEntryURL(7), '');

  // 元素 id 是固定派生格式，夹具靠它定位。
  assert.equal(capabilityPanelElementID('memory'), 'capability-panel-memory');
  assert.equal(capabilityPanelElementID('notes-index'), 'capability-panel-notes-index');

  assert.equal(capabilityPanelText('  记忆 '), '记忆');
  assert.equal(capabilityPanelText(undefined), '');
  assert.equal(capabilityPanelEntryError('记忆'), '能力面板 记忆 的入口地址无法识别，未加载。');
});

test('navigation preserves running and switching guards without discarding drafts', async () => {
  let finishRead;
  let finishRun;
  const h = capabilityHarness({ respond: async (url) => {
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
  const entry = h.capabilityToggle();
  entry.click();
  await h.settle();
  finishRun();
  await h.settle();
  assert.ok(h.document.activeElement === h.$('capability-panel-memory').querySelector('button'), '运行结束不夺走面板焦点');
  assert.equal(h.$('session-new').disabled, false);
  assert.equal(h.$('message').value, '发送内容', '准入失败保留原有草稿恢复行为');
});

test('the navigation column carries no run state; a new session says so where the user reads', async () => {
  const h = capabilityHarness();
  await h.settle();
  const sidebar = () => h.$('session-status').textContent;
  const main = () => h.$('conversation-status');

  // 侧栏的主体是会话导航。新建会话这件事不在那里写一行状态，而是在用户正在看的
  // 主区短暂停留后自己消失。
  h.$('session-new').click();
  assert.equal(sidebar(), '', '侧栏不承载操作反馈');
  assert.equal(main().textContent, '新会话：发送第一条消息后开始记录。', '瞬时提示出现在主区');
  assert.equal(main().hidden, false);
  await h.settle();
  assert.equal(main().textContent, '', '瞬时提示自行消失');
  assert.equal(main().hidden, true, '空的时候不占位置');

  // 恢复成功不写确认：会话已经显示出来了，列表、标题和消息就是证据。
  h.location.hash = '#session=aaaaaaaa';
  await h.settle();
  assert.equal(sidebar(), '', '切换会话不在侧栏写"正在恢复/已恢复"');
  assert.equal(main().textContent, '', '成功也不在主区写确认');
  assert.equal(h.$('conversation').textContent.includes('已保存的消息'), true, '会话真的被恢复了');
});

test('a failed session read states itself in the area the user is looking at', async () => {
  const h = capabilityHarness({ respond: async (url) => {
    if (url === '/api/sessions/aaaaaaaa') {
      return { ok: false, status: 500, json: async () => ({ error: '会话文件读不出来' }) };
    }
  } });
  await h.settle();
  h.location.hash = '#session=aaaaaaaa';
  await h.settle();
  const main = () => h.$('conversation-status');
  assert.equal(main().textContent, '无法读取这个会话：会话文件读不出来', '错误出现在主区');
  assert.equal(main().className, 'conversation-status failure');
  assert.equal(main().hidden, false);
  assert.equal(h.$('session-status').textContent, '', '侧栏不承载会话内容的错误');
  // 错误保留到下一次操作：这里不因定时器自己消失。
  await h.settle();
  assert.equal(main().textContent, '无法读取这个会话：会话文件读不出来');
  assert.equal(h.$('session-status').textContent, '');
  // 下一次操作（发一条消息）让这一行回到空。
  h.$('message').value = '换一条会话';
  h.$('chat-form').emit('submit');
  await h.settle();
  assert.equal(main().textContent, '', '下一次操作清掉上一条错误');
});

test('the sidebar keeps its own read failure and nothing else', async () => {
  const failing = [true];
  const h = capabilityHarness({ respond: async (url) => {
    if (url === '/api/sessions' && failing[0]) {
      return { ok: false, status: 500, json: async () => ({ error: '列表读不出来' }) };
    }
  } });
  await h.settle();
  assert.equal(h.$('session-status').textContent, '无法读取会话列表：列表读不出来',
    '导航列表自己读不到时，错误留在这一列（主体就是它）');
  assert.equal(h.$('session-status').className, 'session-status failure');
  assert.equal(h.$('conversation-status').textContent, '', '主区不为侧栏的列表故障写一行');
  failing[0] = false;
  await h.poll();
  assert.equal(h.$('session-status').textContent, '', '列表重新读到之后错误不再挂着');
});

test('the loading state and the session notices live in the conversation area, never in the navigation column', () => {
  const html = source('index.html');
  const css = source('style.css');
  const js = source('app.js');
  // 会话切换的占位在主区（转录区里），并且默认不占位置。
  assert.match(html, /id="transcript-skeleton"[^>]*hidden/);
  const sidebarMarkup = html.slice(html.indexOf('id="session-sidebar"'), html.indexOf('</aside>'));
  assert.doesNotMatch(sidebarMarkup, /transcript-skeleton/, '加载占位不在侧栏');
  assert.match(css, /\.transcript-skeleton\s*\{/);
  assert.match(js, /transcript\.setAttribute\('aria-busy'/, '主区声明自己正在加载');

  // 会话区域的状态行在对话区与输入区之间：和用户正在看的内容同一个区域。
  assert.match(html, /id="conversation-status"[^>]*role="status"[^>]*hidden/);
  assert.match(css, /\.conversation-status\s*\{[^}]*max-width:\s*var\(--luna-content-max\)/s);
  assert.match(css, /\.conversation-status\.failure\s*\{[^}]*color:\s*var\(--luna-danger\)/s);

  // 侧栏状态行只剩这一个用途：它自己的列表读不到。运行状态与确认类文案一个都不写。
  // 这里断言的是**写入集合**（下面的 deepEqual），不是文件里出现过哪些字：注释里提到
  // “不再写正在恢复会话”是文档，不是写给用户看的文案，不该被禁。
  assert.doesNotMatch(js, /\bSESSION_STATUS_LINGER\b/, '侧栏状态行不再有自己消失的瞬时文案');
  // 只看调用点：定义行 `function setSessionStatus(text, className = '')` 也会被这个正则
  // 匹配到，但它是声明不是写入。
  const writes = (js.match(/setSessionStatus\([^)]*\)/g) || [])
    .filter((write) => !write.includes('className'));
  // 断言"只允许这两种写入"，不是"恰好出现两条"：同一处清空可以在多个路径上被调用，
  // 那不是契约的一部分。契约是侧栏永远只承载"列表读到/读不到"，其余一概不写。
  const allowed = new Set([
    'setSessionStatus(\'\')',
    'setSessionStatus(`无法读取会话列表：${error.message}`, \'failure\')'
  ]);
  for (const write of writes) {
    assert.ok(allowed.has(write), `侧栏只允许“列表读到/读不到”两种写入，出现了：${write}`);
  }
  assert.ok(writes.includes('setSessionStatus(\'\')')
    && writes.some((write) => write.includes('无法读取会话列表')),
    '两种写入都应当存在：读到列表时清空，读不到时报错');
  assert.match(css, /\.session-status:empty\s*\{\s*display:\s*none/s);
});

test('switching a session shows the loading state in the conversation area, not in the sidebar', async () => {
  let release = null;
  const held = new Promise((resolve) => { release = resolve; });
  const h = capabilityHarness({ respond: async (url) => {
    if (url === '/api/sessions/aaaaaaaa') {
      await held;
      return { ok: true, json: async () => ({ records: [{ type: 'message', role: 'user', text: '已保存的消息' }] }) };
    }
  } });
  await h.settle();
  const skeleton = () => h.$('transcript-skeleton');

  // 占位属于主区，默认不占位置。
  assert.equal(skeleton().hidden, true);
  assert.equal(h.$('transcript').contains(skeleton()), true, '加载占位在对话记录里');
  assert.equal(h.$('session-sidebar').contains(skeleton()), false, '加载占位不在侧栏');

  h.location.hash = '#session=aaaaaaaa';
  await h.settle();
  assert.equal(h.$('transcript').getAttribute('aria-busy'), 'true', '主区声明自己正在加载');
  assert.equal(skeleton().hidden, false, '切换期间主区给出真实的加载表现');
  assert.equal(h.$('conversation').hidden, true, '加载期间不显示上一份内容');
  assert.equal(h.$('empty-state').hidden, true, '加载期间不显示空状态');
  assert.equal(h.$('session-status').textContent, '', '侧栏整个切换过程都没有运行状态');

  release();
  await h.settle();
  assert.equal(skeleton().hidden, true, '内容到达后占位让位');
  assert.equal(h.$('transcript').getAttribute('aria-busy'), 'false');
  assert.equal(h.$('conversation').hidden, false);
  assert.equal(h.$('conversation').textContent.includes('已保存的消息'), true);
  assert.equal(h.$('session-status').textContent, '', '加载结束也不在侧栏补一句"已恢复"');
});

test('an enabled capability contributes a header entry and a panel container', async () => {
  const h = capabilityHarness();
  await h.settle();
  const entry = h.capabilityToggle();
  assert.ok(entry, '页头出现贡献面板的入口');
  assert.ok(entry.classList.contains('icon-button'), '入口沿用宿主的图标按钮');
  assert.equal(entry.getAttribute('aria-controls'), 'capability-panel-memory');
  assert.equal(entry.getAttribute('aria-label'), '记忆');
  assert.equal(entry.getAttribute('title'), '记忆');
  assert.equal(entry.getAttribute('aria-expanded'), 'false');
  assert.equal(entry.getAttribute('id'), null, '入口不带面板专属的固定 id');

  const drawer = h.$('capability-panel-memory');
  assert.ok(drawer, '面板容器按能力贡献创建');
  assert.equal(drawer.getAttribute('role'), 'dialog');
  assert.equal(drawer.getAttribute('aria-modal'), 'true');
  assert.equal(drawer.getAttribute('aria-labelledby'), 'capability-panel-memory-title');
  assert.equal(drawer.hidden, true);
  assert.equal(drawer.querySelector('h2').textContent, '记忆');
  assert.ok(drawer.querySelector('.drawer-header'), '面板复用宿主的抽屉头部');
  assert.ok(drawer.querySelector('.drawer-body'), '面板复用宿主的抽屉主体');

  // 入口插在扩展之前：页头顺序就是能力被声明的顺序。
  const siblings = entry.parentElement.children;
  assert.equal(siblings.indexOf(entry) + 1, siblings.indexOf(h.$('extensions-toggle')));

  // 状态轮询是幂等的：入口与容器不重建，打开中的面板不被重置。
  await h.poll();
  assert.equal(h.capabilityToggle(), entry);
  assert.equal(h.$('capability-panel-memory'), drawer);
  assert.ok(h.calls.some(({ url }) => url === '/api/state'));
  assert.equal(h.calls.filter(({ url }) => url.startsWith('/api/memory')).length, 0, '宿主从不直接请求能力自己的接口');

  // 打开时才挂载：模块没加载起来时，面板里说的是这一条失败，整页不报错。
  entry.click();
  await h.settle();
  assert.equal(drawer.hidden, false);
  assert.equal(entry.getAttribute('aria-expanded'), 'true');
  assert.equal(drawer.querySelector('.capability-panel-target'), null, '挂载没走通时容器由宿主移除');
  assert.match(drawer.querySelector('.capability-panel-error').textContent, /无法加载能力面板 记忆 的模块/);
  h.key('Escape');
  assert.equal(drawer.hidden, true);
  assert.equal(entry.getAttribute('aria-expanded'), 'false');
  assert.equal(h.document.activeElement, entry);
});

test('a capability that stops being enabled loses its entry and its panel together', async () => {
  const h = capabilityHarness();
  await h.settle();
  const entry = h.capabilityToggle();
  entry.click();
  await h.settle();
  assert.equal(h.$('capability-panel-memory').hidden, false);

  // 停用：开着的面板先关掉，然后入口与容器一起消失。
  h.capabilityState.capabilities = [{
    id: 'memory', title: 'Memory', deployment: 'builtin', state: 'disabled', contributions: [],
    panels: [{ id: 'memory', title: '记忆', entry: '/api/memory/panel.js' }]
  }];
  await h.poll();
  assert.equal(h.capabilityToggle(), null, '停用后页头不再有这个入口');
  assert.equal(h.$('capability-panel-memory'), null, '面板容器也一起消失');
  assert.equal(h.$('runtime-backdrop').hidden, true, '面板先被关闭，没有留下遮罩');
  assert.equal(h.document.activeElement, h.document.body, '入口被移除后焦点不留在已经删掉的节点上');

  // 再启用：入口与面板都回来，下次打开是一次全新的挂载。
  h.capabilityState.capabilities[0].state = 'enabled';
  await h.poll();
  const restored = h.capabilityToggle();
  assert.ok(restored, '重新启用后入口回来');
  assert.notEqual(restored, entry, '回来的入口是新建的，不是复用已经移除的节点');
  restored.click();
  await h.settle();
  assert.equal(h.$('capability-panel-memory').hidden, false);
});

// 设置里的能力清单：一份状态载荷 + 一个记录请求的假服务，就能把「看得见」与
// 「点得动」都钉住。夹具自己翻转状态，页面必须重读状态，而不是本地改一个变量
// 就宣称成功。
function capabilitySettingsHarness(options = {}) {
  const state = {
    model: 'fixture-model', provider_host: 'example.invalid', reasoning_effort: 'high',
    capabilities: [
      { id: 'memory', title: 'Memory', deployment: 'builtin', state: 'enabled',
        contributions: [
          { kind: 'tool', id: 'luna_remember' },
          { kind: 'context', id: 'facts' },
          { kind: 'route', id: '/api/memory' },
          { kind: 'panel', id: 'memory' }
        ],
        claims: [{ kind: 'route-prefix', id: '/api/memory' }, { kind: 'state-namespace', id: '.runtime' }],
        permissions: [{ kind: 'state.write' }],
        panels: [{ id: 'memory', title: '记忆', entry: '/api/memory/panel.js' }] },
      // 只贡献一条上下文块、没有 claim 也没有 permission 的能力同样要在清单里。
      { id: 'workspace', title: 'Workspace', deployment: 'builtin', state: 'enabled',
        contributions: [{ kind: 'context', id: 'project' }], claims: [], permissions: [], panels: [] }
    ]
  };
  const h = navigationHarness({
    ...options,
    respond: async (url, requestOptions) => {
      if (url === '/api/state') return { ok: true, json: async () => state };
      if (url.startsWith('/api/plugins/')) {
        if (options.reject) return { ok: false, status: 409, json: async () => ({ error: '这个能力现在不能切换' }) };
        const [, , , id, action] = url.split('/');
        state.capabilities = state.capabilities.map((capability) => capability.id === id
          ? { ...capability, state: action === 'disable' ? 'disabled' : 'enabled' }
          : capability);
        return { ok: true, json: async () => ({}) };
      }
      return options.respond ? options.respond(url, requestOptions) : undefined;
    }
  });
  h.capabilityState = state;
  // 页头入口与能力面板由同一份状态派生：停用后入口必须和面板一起消失，所以这一处
  // 查询和 capabilityHarness 用的是同一个选择器。
  h.capabilityToggle = () => h.document.querySelector('.capability-panel-toggle');
  h.capabilityToggles = () => [...h.document.querySelectorAll('.capability-panel-toggle')];
  h.posts = () => h.calls
    .filter(({ url, options: call }) => url.startsWith('/api/plugins/') && call && call.method === 'POST')
    .map(({ url }) => url);
  h.capabilityRow = (id) => [...h.$('capability-list').children].find((row) => row.dataset.capability === id);
  h.openCapabilities = async () => {
    await h.click('settings-toggle');
    await h.click('settings-tab-capabilities');
  };
  return h;
}

test('the settings list reads every capability into a row with its contributions', () => {
  const { capabilityRows, capabilityStateLabel, capabilityDeploymentLabel, capabilityKindLabel } = require('./app.js');
  const memory = {
    id: 'memory', title: 'Memory', deployment: 'builtin', state: 'enabled',
    contributions: [
      { kind: 'tool', id: 'luna_remember' },
      { kind: 'context', id: 'facts' },
      { kind: 'route', id: '/api/memory' },
      { kind: 'panel', id: 'memory' }
    ],
    claims: [{ kind: 'route-prefix', id: '/api/memory' }, { kind: 'state-namespace', id: '.runtime' }],
    permissions: [{ kind: 'state.write' }],
    panels: [{ id: 'memory', title: '记忆', entry: '/api/memory/panel.js' }]
  };
  const rows = capabilityRows([memory, {
    id: 'workspace', title: 'Workspace', deployment: 'builtin', state: 'enabled',
    contributions: [{ kind: 'context', id: 'project' }], claims: [], permissions: [], panels: []
  }]);
  assert.deepEqual(rows.map((row) => [row.id, row.title, row.deploymentLabel, row.stateLabel, row.action, row.actionLabel]), [
    ['memory', 'Memory', '内置', '已启用', 'disable', '停用'],
    ['workspace', 'Workspace', '内置', '已启用', 'disable', '停用']
  ], '两个能力都在，顺序就是注册顺序');

  assert.deepEqual(rows[0].groups, [
    { kind: 'tool', label: '工具', items: ['luna_remember'], status: '' },
    { kind: 'context', label: '上下文', items: ['facts'], status: '' },
    { kind: 'route', label: '路由', items: ['/api/memory'], status: '' },
    { kind: 'panel', label: '面板', items: ['记忆'], status: '' }
  ], '贡献按种类分组、按固定顺序，面板给的是用户看得到的名字');
  assert.deepEqual(rows[0].claims, ['route-prefix · /api/memory', 'state-namespace · .runtime']);
  assert.deepEqual(rows[0].permissions, ['state.write']);
  assert.deepEqual(rows[1].groups, [{ kind: 'context', label: '上下文', items: ['project'], status: '' }],
    '只贡献一条上下文同样完整列出来');
  assert.deepEqual(rows[1].claims, []);
  assert.deepEqual(rows[1].permissions, []);

  // 停用不删掉清单：每一组都还在，只是标成“未在服务”。
  const off = capabilityRows([{ ...memory, state: 'disabled' }])[0];
  assert.equal(off.enabled, false);
  assert.equal(off.stateLabel, '已停用');
  assert.equal(off.action, 'enable');
  assert.equal(off.actionLabel, '启用');
  assert.deepEqual(off.groups.map((group) => group.kind), ['tool', 'context', 'route', 'panel'], '停用不丢分组');
  for (const group of off.groups) assert.equal(group.status, '未在服务');
  for (const group of rows[0].groups) assert.equal(group.status, '', '在服务时不打标记');

  // 标签只查闭合的表；表里没有的值原样带出来，缺字段有缺字段的说法。
  assert.equal(capabilityStateLabel('registered'), '已注册');
  assert.equal(capabilityStateLabel('failed'), '启动失败');
  assert.equal(capabilityStateLabel(''), '状态未知');
  assert.equal(capabilityDeploymentLabel('process'), '子进程');
  assert.equal(capabilityDeploymentLabel('browser'), '浏览器模块');
  assert.equal(capabilityDeploymentLabel(undefined), '未说明');
  assert.equal(capabilityKindLabel('context'), '上下文');
  assert.equal(capabilityKindLabel('wat'), 'wat（未识别）');
});

test('the settings list survives a payload the kernel did not send', () => {
  const { capabilityRows } = require('./app.js');
  for (const payload of [undefined, null, 'nope', 7, {}]) {
    assert.deepEqual(capabilityRows(payload), [], `${JSON.stringify(payload)} 不是能力数组`);
  }
  assert.deepEqual(capabilityRows([null, 'x', 7, {}, { title: '没有 id' }]), [], '没有 id 的行无法寻址，不编出来');

  // 未知种类、未知状态、未知形态、空字段：都留着，按自己的名字显示。
  const unknown = capabilityRows([{
    id: 'probe', title: '', deployment: 'orbital', state: 'weird',
    contributions: [{ kind: 'wasm', id: 'module.a' }, { kind: 'tool', id: '' }, {}, 'nope'],
    claims: [{ kind: 'route-prefix' }, null, 'x'],
    permissions: [{ kind: '' }, 'x']
  }])[0];
  assert.equal(unknown.title, 'probe', '没有 title 时用 id');
  assert.equal(unknown.deploymentLabel, 'orbital（未识别）');
  assert.equal(unknown.stateLabel, 'weird（未识别）');
  assert.deepEqual(unknown.groups, [
    { kind: 'tool', label: '工具', items: ['未声明'], status: '' },
    { kind: 'wasm', label: 'wasm（未识别）', items: ['module.a'], status: '' },
    { kind: 'unknown', label: '未说明', items: ['未声明'], status: '' }
  ], '未知种类排在后排，按它自己的名字');
  assert.deepEqual(unknown.claims, ['route-prefix'], '缺 id 的 claim 仍按它的 kind 显示');
  assert.deepEqual(unknown.permissions, []);

  // 没有 contributions 数组的能力就是“没有声明任何贡献”，不是一个空行都没有。
  const bare = capabilityRows([{ id: 'bare', title: 'Bare', state: 'enabled' }])[0];
  assert.deepEqual(bare.groups, []);
  assert.deepEqual(bare.claims, []);
  assert.deepEqual(bare.permissions, []);
});

test('the capability state path refuses anything the kernel could not have registered', () => {
  const { capabilityStatePath, capabilityRows } = require('./app.js');
  assert.equal(capabilityStatePath('memory', 'disable'), '/api/plugins/memory/disable');
  assert.equal(capabilityStatePath('workspace', 'enable'), '/api/plugins/workspace/enable');
  const refused = [
    ['', 'disable'], ['Memory', 'disable'], ['../memory', 'disable'], ['a/b', 'enable'],
    ['memory ', 'enable'], ['memory?x=1', 'enable'], ['m'.repeat(33), 'enable'], [7, 'enable'],
    ['memory', 'reload'], ['memory', ''], ['memory', undefined]
  ];
  for (const [id, action] of refused) {
    assert.equal(capabilityStatePath(id, action), '', `${JSON.stringify(id)} / ${action} 不得变成路径`);
  }
  // 行上也不给控件：按一个不可能存在的路径发请求只会失败。
  assert.equal(capabilityRows([{ id: '../memory', title: 'Memory', state: 'enabled' }])[0].action, '');
});

test('the reasoning tier is read as a startup parameter and never guessed', () => {
  const { reasoningEffortView, REASONING_EFFORT_LEVELS } = require('./app.js');
  assert.deepEqual(REASONING_EFFORT_LEVELS, ['minimal', 'low', 'medium', 'high', 'none']);
  const high = reasoningEffortView('high');
  assert.equal(high.present, true);
  assert.equal(high.value, 'high');
  assert.equal(high.label, '高（high）');
  assert.match(high.note, /LUNA_REASONING_EFFORT/);
  assert.match(high.note, /推理过程是否展示无关/, '档位与推理展示是两件事，文案分开说');
  assert.equal(reasoningEffortView('none').label, '不思考（none）');
  assert.equal(reasoningEffortView(' medium ').value, 'medium', '两侧空白不算另一个档位');

  // 没有发送档位：说清楚是进程没发、provider 默认生效，绝不当成 medium。
  for (const absent of ['', '   ', undefined, null, 0, [], {}]) {
    const view = reasoningEffortView(absent);
    assert.equal(view.present, false, `${JSON.stringify(absent)} 就是“没有发送档位”`);
    assert.equal(view.value, '');
    assert.equal(view.label, '进程未发送该档位');
    assert.match(view.note, /默认档位/);
    assert.match(view.note, /LUNA_REASONING_EFFORT/);
    assert.doesNotMatch(view.label + view.note, /medium/, '没有发送档位不是 medium，也不是“默认 medium”');
  }
  // 只有 provider 认的那几个档位是已知的：大小写不同就是另一个值。
  assert.equal(reasoningEffortView('highish').label, 'highish（未识别）');
  assert.equal(reasoningEffortView('Medium').label, 'Medium（未识别）');
});

test('the settings modal carries a capability area and a read-only model service block', () => {
  const html = source('index.html');
  const js = source('app.js');
  assert.match(html, /<button id="settings-tab-capabilities" class="settings-tab" role="tab"[^>]*data-pane="settings-pane-capabilities"/);
  assert.match(html, /<section id="settings-pane-capabilities" class="settings-pane" role="tabpanel"[^>]*hidden>/);
  assert.match(html, /<ul id="capability-list" class="luna-list capability-list"><\/ul>/, '清单由脚本填充，标记里不预置任何能力');
  assert.match(html, /id="capability-empty"[^>]*hidden>暂未读到能力。</);
  assert.match(html, /id="capability-status"[^>]*role="status"/);
  assert.match(html, /id="capability-retry"[^>]*>重新读取能力<\/button>/);
  // 能力名不写死在标记里：与能力面板入口同一条规则。
  for (const text of ['Memory', 'Workspace', 'luna_remember', 'state.write']) {
    assert.equal(html.includes(text), false, `宿主不预置 ${text}`);
  }
  // 模型服务：模型、提供方与档位都在同一份状态里，档位是只读项而不是控件。
  assert.match(html, /<dd id="settings-model">/);
  assert.match(html, /<dd id="settings-provider">/);
  assert.match(html, /<dd id="settings-effort">/);
  assert.match(html, /id="settings-effort-note"/);
  assert.doesNotMatch(html, /<(?:select|input|button)[^>]*id="settings-effort/);

  assert.match(js, /renderCapabilities\(state\.capabilities\)/);
  assert.match(js, /renderModelFacts\(state\)/);
  assert.match(js, /fetch\(path, \{ method: 'POST' \}\)/, '启停只有这一条请求路径');
  assert.match(js, /capabilityStatePath\(row\.id, row\.action\)/);
  assert.equal(js.includes('innerHTML'), false);
  assert.equal(js.includes('sessionStorage'), false);
});

test('every capability style the settings script builds a class for exists in the stylesheet', () => {
  const css = source('style.css');
  const selectors = ['.capability-list', '.capability-row', '.capability-head', '.capability-title', '.capability-name',
    '.capability-id', '.capability-meta', '.capability-badge', '.capability-state', '.capability-state.is-on',
    '.capability-state.is-off', '.capability-contributions', '.capability-group', '.capability-items', '.capability-item',
    '.capability-off', '.capability-none', '.capability-error', '.capability-developer', '.capability-developer-list',
    '.capability-developer-row', '.capability-status', '.capability-status.failure', '.settings-facts'];
  for (const selector of selectors) {
    assert.ok(
      [`${selector} {`, `${selector}:`, `${selector},`, `${selector}.`].some((form) => css.includes(form)),
      `missing style ${selector}`
    );
  }
  // 这一块沿用语义主题与尺度 token：不引入装饰色、渐变或自定的圆角。
  const added = css.slice(css.indexOf('/* --- 设置：能力清单'), css.indexOf('/* --- 界面插件'));
  assert.ok(added.length > 0, '能力清单的样式块必须在');
  assert.equal(/gradient\s*\(/i.test(added), false);
  assert.doesNotMatch(added, /border-radius:\s*(?:1[0-9]|[2-9][0-9])px/);
  assert.doesNotMatch(added, /#[0-9a-f]{3,8}\b/i, '颜色只来自 --luna-* token');
});

test('settings lists every capability with its contributions and toggles it through the kernel', async () => {
  const h = capabilitySettingsHarness();
  await h.settle();
  await h.openCapabilities();
  assert.equal(h.$('capability-list').children.length, 2, 'Memory 与 Workspace 都要在清单里');
  assert.equal(h.$('capability-empty').hidden, true);

  const memory = h.capabilityRow('memory');
  assert.equal(memory.querySelector('.capability-name').textContent, 'Memory');
  assert.equal(memory.querySelector('.capability-id').textContent, 'memory');
  assert.equal(memory.querySelector('.capability-badge').textContent, '内置');
  assert.equal(memory.querySelector('.capability-state').textContent, '已启用');
  const groups = [...memory.querySelectorAll('.capability-group')];
  assert.deepEqual(groups.map((group) => group.querySelector('dt').textContent), ['工具', '上下文', '路由', '面板']);
  assert.deepEqual(groups.map((group) => group.querySelector('.capability-item').textContent),
    ['luna_remember', 'facts', '/api/memory', '记忆'], '贡献逐项列出来');
  assert.equal(memory.querySelectorAll('.capability-off').length, 0, '在服务时不打标记');

  // 只贡献一条上下文、没有 claim 与 permission 的能力完整出现，而不是被藏起来。
  const workspace = h.capabilityRow('workspace');
  assert.deepEqual([...workspace.querySelectorAll('.capability-group')].map((group) => group.querySelector('dt').textContent), ['上下文']);
  assert.equal(workspace.querySelector('.capability-item').textContent, 'project');
  assert.equal(workspace.querySelector('.capability-developer').hidden, true, '没有声明与权限就没有次级块');
  assert.equal(workspace.querySelector('.capability-state').textContent, '已启用');

  // 声明与权限是次级层级：默认收起。
  const developer = memory.querySelector('.capability-developer');
  assert.equal(developer.hidden, false);
  assert.equal(developer.open, false, '声明与权限默认收起');
  assert.deepEqual([...developer.querySelectorAll('dd')].map((dd) => dd.textContent),
    ['route-prefix · /api/memory、state-namespace · .runtime', 'state.write']);

  // 轮询复用同一行：焦点留在停用按钮上。
  const toggle = memory.querySelector('.capability-toggle');
  toggle.focus();
  await h.poll();
  assert.equal(h.capabilityRow('memory').querySelector('.capability-toggle'), toggle, '轮询不重建行');
  assert.equal(h.document.activeElement, toggle);

  // 停用：请求真的发出去，然后按内核答复的状态重绘。
  toggle.click();
  await h.settle();
  assert.deepEqual(h.posts(), ['/api/plugins/memory/disable'], '停用请求真的发出去了');
  const after = h.capabilityRow('memory');
  assert.equal(after.querySelector('.capability-state').textContent, '已停用');
  assert.equal(after.querySelector('.capability-toggle').textContent, '启用');
  const off = [...after.querySelectorAll('.capability-group')];
  assert.deepEqual(off.map((group) => group.querySelector('dt').textContent),
    ['工具未在服务', '上下文未在服务', '路由未在服务', '面板未在服务']);
  assert.deepEqual(off.map((group) => group.querySelector('.capability-item').textContent),
    ['luna_remember', 'facts', '/api/memory', '记忆'], '停用后仍列全它贡献了什么');
  assert.equal(h.capabilityToggle(), null, '停用后页头入口与面板一起消失');
  assert.equal(h.capabilityRow('workspace').querySelector('.capability-state').textContent, '已启用', '别的能力不受影响');
  assert.match(h.$('capability-status').textContent, /Memory 已停用/);

  // 再启用：同一条路径回来。
  h.capabilityRow('memory').querySelector('.capability-toggle').click();
  await h.settle();
  assert.deepEqual(h.posts(), ['/api/plugins/memory/disable', '/api/plugins/memory/enable']);
  assert.equal(h.capabilityRow('memory').querySelector('.capability-state').textContent, '已启用');
  assert.ok(h.capabilityToggle(), '重新启用后页头入口回来');
  assert.equal(h.capabilityRow('memory').querySelectorAll('.capability-off').length, 0);

  // 载荷形状不对时清单说实话：清空、给出重读入口，不编行。
  h.capabilityState.capabilities = 'nope';
  await h.poll();
  assert.equal(h.$('capability-list').children.length, 0);
  assert.equal(h.$('capability-empty').hidden, false);
});

test('a rejected state change is reported as a failure and never as the new state', async () => {
  const h = capabilitySettingsHarness({ reject: true });
  await h.settle();
  await h.openCapabilities();
  h.capabilityRow('memory').querySelector('.capability-toggle').click();
  await h.settle();
  assert.deepEqual(h.posts(), ['/api/plugins/memory/disable'], '请求发出去了，即便内核拒绝');
  assert.equal(h.capabilityRow('memory').querySelector('.capability-state').textContent, '已启用',
    '被拒绝的请求不许写成已改变');
  assert.equal(h.capabilityRow('memory').querySelectorAll('.capability-off').length, 0, '没有“未在服务”的假标记');
  assert.equal(h.capabilityRow('memory').querySelector('.capability-toggle').textContent, '停用', '按钮回到可再试的状态');
  assert.equal(h.capabilityRow('memory').querySelector('.capability-toggle').disabled, false);
  assert.match(h.$('capability-status').textContent, /停用 Memory 失败：这个能力现在不能切换/);
  assert.equal(h.$('capability-status').className.includes('failure'), true);
  assert.ok(h.capabilityToggle(), '页头入口保持原样');
});

test('the skills list reads the discovery payload into one row per skill', () => {
  const { skillRows, skillActionPath, skillDescriptionText, skillScopeLabel, SKILLS_PATH,
    SKILL_DESCRIPTION_MAX_CHARS } = require('./app.js');
  assert.equal(SKILLS_PATH, '/api/skills', '只有一条读取路径');

  const rows = skillRows({ skills: [
    { name: 'demo', description: '一段演示用的说明。', scope: 'user', enabled: true, disabled_reason: '' },
    { name: 'notes_writer', description: '把会话整理成笔记。', scope: 'project', enabled: false,
      disabled_reason: '这一台机器没有配置入口。' },
    { name: 'mystery' }
  ] });
  assert.deepEqual(rows.map((row) => [row.name, row.scopeLabel, row.stateLabel, row.action, row.actionLabel]), [
    ['demo', '用户级', '已启用', 'disable', '停用'],
    ['notes_writer', '项目级', '已停用', 'enable', '启用'],
    ['mystery', '来源未知', '状态未报', '', '']
  ], '顺序就是服务端给的顺序；没有报启用状态的技能不写状态、也没有可点的控件');
  assert.equal(rows[0].disabledReason, '');
  assert.equal(rows[1].disabledReason, '这一台机器没有配置入口。', '停用原因照原样带出来');

  // 来源表里没有的取值原样带出来，不就近映射成 user。
  assert.equal(skillScopeLabel('bundle'), 'bundle（未识别）');
  assert.equal(skillScopeLabel(undefined), '来源未知');

  // 描述可能很长（上限 1024 字符）：行里截断，末尾必须是明确的省略号。
  const long = '很长的描述。'.repeat(80);
  const clipped = skillDescriptionText(long);
  assert.equal(clipped.length, SKILL_DESCRIPTION_MAX_CHARS);
  assert.equal(clipped.endsWith('…'), true, '截断要看得见');
  assert.equal(long.startsWith(clipped.slice(0, -1)), true, '截断的是原文的开头');
  assert.equal(skillDescriptionText('  一\n\n  段  '), '一 段', '空白折叠成单空格');
  assert.equal(skillDescriptionText(undefined), '');
  const bare = skillRows({ skills: [{ name: 'demo' }] })[0];
  assert.equal(bare.descriptionText, '没有写描述。');
  assert.equal(bare.descriptionMissing, true);

  // 载荷形状不对时没有行：没有名字的技能无法寻址，也不会被编一个名字。
  for (const payload of [undefined, {}, { skills: 'nope' }, { skills: [null, 'x', { description: '无名字' }] }]) {
    assert.deepEqual(skillRows(payload), []);
  }

  // 路径只在一个地方拼：名字是一个目录名，带分隔符或过长的名字直接拒。
  assert.equal(skillActionPath('demo', 'disable'), '/api/skills/demo/disable');
  assert.equal(skillActionPath('notes_writer', 'enable'), '/api/skills/notes_writer/enable');
  for (const name of ['', '../etc', 'a/b', '.hidden', 'x'.repeat(65), 'demo ', 42, null]) {
    assert.equal(skillActionPath(name, 'disable'), '', `${JSON.stringify(name)} 不得变成路径`);
  }
  assert.equal(skillActionPath('demo', 'toggle'), '', '只有启停两个动作');
});

test('the settings modal carries a skills area fed only by the server', () => {
  const html = source('index.html');
  const js = source('app.js');
  const css = source('style.css');
  assert.match(html, /<button id="settings-tab-skills" class="settings-tab" role="tab"[^>]*data-pane="settings-pane-skills"/);
  assert.match(html, /<section id="settings-pane-skills" class="settings-pane" role="tabpanel"[^>]*hidden>/);
  assert.match(html, /<ul id="skill-list" class="luna-list skill-list"><\/ul>/, '清单由脚本填充，标记里不预置任何技能');
  assert.match(html, /id="skill-status"[^>]*role="status"/);
  assert.match(html, /id="skill-refresh"[^>]*>重新读取技能<\/button>/);
  // 空态必须说清技能放在哪里才会被发现，而不是一句“没有数据”。
  assert.match(html, /id="skill-empty"[^>]*hidden>[^<]*XDG_DATA_HOME\/luna\/skills/);
  assert.match(html, /id="skill-empty"[^>]*hidden>[^<]*-skills-dir/);
  for (const text of ['demo', 'notes_writer', 'SKILL.md 正文']) {
    assert.equal(html.includes(text), false, `宿主不预置 ${text}`);
  }
  assert.match(js, /fetch\(SKILLS_PATH, \{ cache: 'no-store' \}\)/, '技能清单只有这一条读取路径');
  assert.match(js, /skillActionPath\(row\.name, row\.action\)/);
  assert.match(js, /renderSkills\(await response\.json\(\)\)/);

  const selectors = ['.skill-list', '.skill-row', '.skill-head', '.skill-title', '.skill-name', '.skill-scope',
    '.skill-description', '.skill-description.is-missing', '.skill-meta', '.skill-state', '.skill-state.is-on',
    '.skill-state.is-off', '.skill-reason', '.skill-error', '.skill-status', '.skill-status.failure'];
  for (const selector of selectors) {
    assert.ok(
      [`${selector} {`, `${selector}:`, `${selector},`, `${selector}.`].some((form) => css.includes(form)),
      `missing style ${selector}`
    );
  }
});

// 技能页：一份发现结果 + 一个记录请求的假服务。POST 只翻服务端自己那份状态，
// 页面必须重读 /api/skills 才可能看到变化——本地改一个变量是过不了这些断言的。
function skillSettingsHarness(options = {}) {
  const state = { skills: [
    { name: 'demo', description: '一段演示用的说明。', scope: 'user', enabled: true, disabled_reason: '' },
    { name: 'notes_writer', description: '把会话整理成笔记。', scope: 'project', enabled: false,
      disabled_reason: '这一台机器没有配置入口。' }
  ] };
  const h = navigationHarness({
    ...options,
    respond: async (url, requestOptions) => {
      // 夹具可以直接接管某一次读取（例如让它失败），否则走下面这份技能目录。
      const custom = options.respond ? await options.respond(url, requestOptions) : undefined;
      if (custom) return custom;
      // 服务端每次回答都是一份新的 JSON：读到的清单是那一刻的快照，之后目录变了
      // 也不会回头改写页面已经读到的那一份。
      if (url === '/api/skills') return { ok: true, json: async () => JSON.parse(JSON.stringify(state)) };
      if (url.startsWith('/api/skills/')) {
        if (options.status) {
          return { ok: false, status: options.status, json: async () => ({ error: options.message || '服务端拒绝' }) };
        }
        const [, , , name, action] = url.split('/');
        // 服务端只认它自己发现过的名字：读清单之后消失的技能在这里就是未知名字。
        if (!state.skills.some((skill) => skill.name === name)) {
          return { ok: false, status: 404, json: async () => ({ error: 'unknown skill' }) };
        }
        state.skills = state.skills.map((skill) => skill.name === name
          ? { ...skill, enabled: action === 'enable' } : skill);
        return { ok: true, json: async () => ({ name, enabled: action === 'enable' }) };
      }
      return options.respond ? options.respond(url, requestOptions) : undefined;
    }
  });
  h.skillState = state;
  h.skillRow = (name) => [...h.$('skill-list').children].find((row) => row.dataset.skill === name);
  h.skillPosts = () => h.calls
    .filter(({ url, options: call }) => url.startsWith('/api/skills/') && call && call.method === 'POST')
    .map(({ url }) => url);
  h.skillReads = () => h.calls.filter(({ url }) => url === '/api/skills').length;
  h.openSkills = async () => {
    await h.click('settings-toggle');
    await h.click('settings-tab-skills');
  };
  return h;
}

test('settings lists every discovered skill and switches it through the server', async () => {
  const h = skillSettingsHarness();
  await h.settle();
  assert.equal(h.skillReads(), 0, '没打开技能页就不读技能清单');

  await h.openSkills();
  assert.equal(h.skillReads(), 1, '切到技能页读一次');
  assert.equal(h.$('skill-list').children.length, 2, '发现几个就列几行');
  assert.equal(h.$('skill-empty').hidden, true);
  assert.match(h.$('skill-status').textContent, /已发现 2 个技能/);

  const demo = h.skillRow('demo');
  assert.equal(demo.querySelector('.skill-name').textContent, 'demo');
  assert.equal(demo.querySelector('.skill-scope').textContent, '用户级');
  assert.equal(demo.querySelector('.skill-state').textContent, '已启用');
  assert.equal(demo.querySelector('.skill-description').textContent, '一段演示用的说明。');
  assert.equal(demo.querySelector('.skill-toggle').textContent, '停用');
  assert.equal(demo.querySelector('.skill-reason').hidden, true, '启用中的技能不显示停用原因');

  const off = h.skillRow('notes_writer');
  assert.equal(off.querySelector('.skill-state').textContent, '已停用');
  assert.equal(off.querySelector('.skill-reason').hidden, false);
  assert.equal(off.querySelector('.skill-reason').textContent, '这一台机器没有配置入口。');
  assert.equal(off.querySelector('.skill-toggle').textContent, '启用');

  // 停用：真的发 POST，然后按服务端的答复重绘。
  demo.querySelector('.skill-toggle').click();
  await h.settle();
  assert.deepEqual(h.skillPosts(), ['/api/skills/demo/disable'], '停用请求真的发出去了');
  assert.equal(h.skillReads(), 2, '改完必须重读服务端的清单');
  assert.equal(h.skillRow('demo').querySelector('.skill-state').textContent, '已停用');
  assert.equal(h.skillRow('demo').querySelector('.skill-toggle').textContent, '启用');
  assert.match(h.$('skill-status').textContent, /demo 已停用/);
  assert.equal(h.skillRow('notes_writer').querySelector('.skill-state').textContent, '已停用', '别的技能不受影响');

  // 反向启用：同一条路径回来。
  h.skillRow('demo').querySelector('.skill-toggle').click();
  await h.settle();
  assert.deepEqual(h.skillPosts(), ['/api/skills/demo/disable', '/api/skills/demo/enable']);
  assert.equal(h.skillRow('demo').querySelector('.skill-state').textContent, '已启用');
  assert.equal(h.skillRow('demo').querySelector('.skill-toggle').textContent, '停用');

  // 一个都没发现：说清把技能放在哪里才会被发现，而不是留一片空白。
  h.skillState.skills = [];
  await h.click('skill-refresh');
  assert.equal(h.$('skill-list').children.length, 0);
  assert.equal(h.$('skill-empty').hidden, false);
  assert.match(h.$('skill-empty').textContent, /-skills-dir/);
});

test('a skill the server refuses or does not know keeps its reported state', async () => {
  const refused = skillSettingsHarness({ status: 409, message: '这个技能现在不能切换' });
  await refused.settle();
  await refused.openSkills();
  refused.skillRow('demo').querySelector('.skill-toggle').click();
  await refused.settle();
  assert.deepEqual(refused.skillPosts(), ['/api/skills/demo/disable'], '被拒绝的请求也是真发出去的');
  assert.equal(refused.skillRow('demo').querySelector('.skill-state').textContent, '已启用',
    '被拒绝的请求不许写成已改变');
  assert.equal(refused.skillRow('demo').querySelector('.skill-toggle').textContent, '停用');
  assert.match(refused.$('skill-status').textContent, /停用 demo 失败：这个技能现在不能切换/);
  assert.equal(refused.$('skill-status').className.includes('failure'), true);

  // 读清单之后从服务端消失的技能：请求打到的是一个未知名字，界面如实报 404，
  // 不把它说成停用。
  const gone = skillSettingsHarness();
  await gone.settle();
  await gone.openSkills();
  gone.skillState.skills = gone.skillState.skills.filter((skill) => skill.name !== 'demo');
  gone.skillRow('demo').querySelector('.skill-toggle').click();
  await gone.settle();
  assert.deepEqual(gone.skillPosts(), ['/api/skills/demo/disable']);
  assert.match(gone.$('skill-status').textContent, /停用 demo 失败：unknown skill/);
  assert.equal(gone.skillRow('demo').querySelector('.skill-state').textContent, '已启用');
  assert.equal(gone.skillRow('demo').querySelectorAll('.skill-state.is-off').length, 0, '没有假的停用标记');
});

test('a skill list that cannot be read keeps the last rows and says so', async () => {
  let failing = false;
  const h = skillSettingsHarness({
    respond: async (url) => (url === '/api/skills' && failing
      ? { ok: false, status: 503, json: async () => ({ error: '技能目录暂时读不到' }) }
      : undefined)
  });
  await h.settle();
  await h.openSkills();
  assert.equal(h.$('skill-list').children.length, 2);
  failing = true;
  await h.click('skill-refresh');
  assert.equal(h.$('skill-list').children.length, 2, '读失败不清空上一次读到的行');
  assert.equal(h.$('skill-empty').hidden, true, '读失败不是“一个技能都没有”');
  assert.match(h.$('skill-status').textContent, /读取技能清单失败：技能目录暂时读不到/);
  assert.equal(h.$('skill-status').className.includes('failure'), true);
});

test('the model service block shows the running model and the tier the process sent', async () => {
  const h = capabilitySettingsHarness();
  await h.settle();
  await h.click('settings-toggle');
  await h.click('settings-tab-model');
  assert.equal(h.$('settings-model').textContent, 'fixture-model');
  assert.equal(h.$('settings-provider').textContent, 'example.invalid');
  assert.equal(h.$('settings-effort').textContent, '高（high）');
  assert.match(h.$('settings-effort-note').textContent, /LUNA_REASONING_EFFORT/);

  // 同一个进程没有发送档位：界面上说清是“进程没发、provider 默认”，不是 medium。
  h.capabilityState.reasoning_effort = undefined;
  await h.poll();
  assert.equal(h.$('settings-effort').textContent, '进程未发送该档位');
  assert.doesNotMatch(h.$('settings-effort').textContent + h.$('settings-effort-note').textContent, /medium/);
});

test('closing a panel before its module resolves leaves nothing mounted', async () => {
  const h = capabilityHarness({ narrow: true });
  await h.settle();
  const entry = h.capabilityToggle();
  const drawer = h.$('capability-panel-memory');
  entry.click();
  // 加载还没回来就关闭：这一次挂载作废，面板里不留容器也不留错误。
  h.key('Escape');
  await h.settle();
  assert.equal(drawer.hidden, true);
  assert.equal(drawer.classList.contains('is-open'), false);
  assert.equal(h.$('runtime-backdrop').hidden, true);
  assert.equal(h.document.activeElement, entry);
  assert.equal(drawer.querySelector('.capability-panel-target'), null);
  assert.equal(drawer.querySelector('.capability-panel-error'), null);
  await h.click('session-toggle');
  await h.click('runtime-backdrop');
  assert.equal(h.document.activeElement, h.$('session-toggle'));
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
  assert.match(html, /会话记录与各能力保存的数据都在本机/);
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

test('the host names no specific capability in its markup, script or styles', () => {
  const html = source('index.html');
  const js = source('app.js');
  const css = source('style.css');
  for (const text of ['memory', 'Memory', '记忆']) {
    assert.equal(html.includes(text), false, `标记里不再出现 ${text}`);
    assert.equal(js.includes(text), false, `脚本里不再出现 ${text}`);
    assert.equal(css.includes(text), false, `样式里不再出现 ${text}`);
  }
  for (const id of ['memory-toggle', 'memory-panel', 'memory-list', 'memory-status', 'memory-retract']) {
    assert.equal(html.includes(id), false, `宿主不再写死 ${id}`);
  }
  assert.equal(js.includes('/api/memory'), false, '宿主不再直接请求某个能力自己的接口');
});

test('the host renders contributed panels only from the state payload', () => {
  const js = source('app.js');
  assert.match(js, /function capabilityPanels\(capabilities\)/, '页头入口来自 /api/state 的 capabilities');
  assert.match(js, /syncCapabilityPanels\(capabilityPanels\(state\.capabilities\)\)/, '轮询里按同一份状态核对入口与面板');
  assert.match(js, /capabilityPanelElementID/, '面板元素 id 是固定派生格式，夹具按它定位');
  assert.match(js, /capabilityModule = await import\(url\)/, '模块在打开面板时才动态 import');
  assert.match(js, /uiPluginHostAPI\(UI_PLUGIN_API_VERSION/, '挂载沿用同一个宿主 API 与日志路径');
  assert.match(js, /capabilityModule\.unmount\(target\)/, '关闭时调模块自己的 unmount');
  assert.match(js, /delete panels\[record\.key\]/, '能力消失时入口与面板一起从登记里移除');
  assert.equal(js.includes('innerHTML'), false, '面板内容只能作为文本与节点进入 DOM');
});

test('the primary surface does not claim that nothing is stored', () => {
  const html = source('index.html');
  assert.equal(html.includes('不保存记录'), false, 'sessions are written to disk since the session slice');
  assert.equal(html.includes('刷新后不会保留'), false, 'a reload resumes the same session through the fragment');
  assert.match(html, /会话记录与各能力保存的数据都在本机/);
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

test('sessions keep their own navigation and contributed panels are not part of diagnostics', () => {
  const html = source('index.html');
  const runtime = html.match(/<aside\b[^>]*id="runtime-drawer"[\s\S]*?<\/aside>/)?.[0];
  const sessions = html.match(/<aside\b[^>]*id="session-sidebar"[\s\S]*?<\/aside>/)?.[0];
  assert.ok(sessions, '会话需要独立的侧栏，不能藏在运行详情内');
  assert.match(sessions, /id="session-new"/);
  assert.match(sessions, /id="session-list"/);
  assert.doesNotMatch(runtime, /id="(?:session-new|session-list)"/);
  assert.match(html, /id="session-toggle"[^>]*aria-controls="session-sidebar"/);
  // 贡献面板的容器不写在标记里：它由 /api/state 决定，能力停用时一起消失。
  assert.equal(/id="memory-panel"/.test(html), false);
  assert.equal(/capability-panel-/.test(html), false, '宿主不预置任何贡献面板的元素 id');
  for (const id of ['session-new', 'session-list', 'session-status']) {
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
  // 状态行不是内部日志：确认类文案（列表与消息已经说清的事实）不再写进去。
  for (const text of ['已恢复会话。', '会话已开始记录。']) {
    assert.equal(js.includes(text), false, `状态行不写确认类文案：${text}`);
  }
  assert.equal(js.includes('innerHTML'), false);
  assert.equal(/\beval\s*\(/.test(js), false);
  assert.equal(js.includes('new Function'), false);
});

test('every session style the script builds a class for exists in the stylesheet', () => {
  const css = source('style.css');
  // `.capability-panel-toggle` 不在这里：它是给夹具用的定位钩子，外观全部来自
  // 宿主的 `.icon-button`，不给它单写一条只为占位的规则。
  const selectors = ['.session-new', '.session-list', '.session-row', '.session-row.is-current', '.session-title', '.session-meta', '.session-empty', '.session-status', '.conversation-status', '.conversation-status.failure', '.transcript-skeleton', '.session-notice', '.turn-note', '.assistant-body.failed', '.capability-panel-target', '.capability-panel-error'];
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

// --- Run observability: 一次运行在浏览器里可观、可理解、可控 ------------------
// 这些用例驱动的是真实的 SSE 消费路径：事件逐块喂入，所以在流还没结束时就能
// 检查界面，而不是只看终态。

// 可逐块喂入的 SSE 响应；push 会在应用正等待下一块时立刻交付。
function eventStream() {
  const encoder = new TextEncoder();
  const queue = [];
  const waiting = [];
  let ended = false;
  return {
    body: {
      getReader: () => ({
        read() {
          if (queue.length) return Promise.resolve({ value: encoder.encode(queue.shift()), done: false });
          if (ended) return Promise.resolve({ value: undefined, done: true });
          return new Promise((resolve) => waiting.push(resolve));
        }
      })
    },
    push(text) {
      if (waiting.length) waiting.shift()({ value: encoder.encode(text), done: false });
      else queue.push(text);
    },
    end() {
      ended = true;
      while (waiting.length) waiting.shift()({ value: undefined, done: true });
    }
  };
}

function sse(type, data) {
  return `event: ${type}\ndata: ${JSON.stringify(data)}\n\n`;
}

// 一次运行的夹具：每次 POST /api/runs 都给一条新的可喂入流；取消入口按用例给结果。
function runHarness(options = {}) {
  const streams = [];
  const h = navigationHarness({
    ...options,
    respond: async (url, requestOptions, data) => {
      if (url === '/api/runs') {
        const stream = eventStream();
        streams.push(stream);
        return { ok: true, body: stream.body };
      }
      if (url.endsWith('/cancel')) return options.cancel ? options.cancel(url) : { ok: true, status: 202, json: async () => ({}) };
      return options.respond ? options.respond(url, requestOptions, data) : undefined;
    }
  });
  h.streams = streams;
  h.stream = () => streams[streams.length - 1];
  h.cancelCalls = () => h.calls.filter(({ url }) => url === '/api/runs/run-1/cancel' || url === '/api/runs/run-7/cancel');
  h.startRun = async (message = '你好') => {
    h.$('message').value = message;
    h.$('chat-form').emit('submit');
    await h.settle();
  };
  return h;
}

// 当前这一次运行留在对话区的节点。选择器只用单个类名：适配器只认简单选择器。
function runView(h) {
  const turn = h.$('conversation').children.at(-1);
  const timeline = turn.querySelector('.run-timeline');
  const body = () => turn.querySelector('.assistant-body');
  return {
    turn,
    timeline,
    timelineBody: body,
    meta: turn.querySelector('.run-meta'),
    body: body(),
    // 时间线里除回答之外的条目，按它们在 DOM 里的顺序。
    order: () => [...timeline.children].map((node) => node.getAttribute('class')),
    steps: () => [...timeline.children].filter((node) => node !== body()),
    cards: () => [...turn.querySelectorAll('.tool-row')],
    notes: () => [...turn.querySelectorAll('.run-note')],
    phases: () => [...turn.querySelectorAll('.run-phase')],
    reasoning: () => [...turn.querySelectorAll('.run-reasoning')],
    reasoningText: () => [...turn.querySelectorAll('.run-reasoning-body')],
    status: h.$('run-status'),
    statusLabel: h.$('run-status').querySelector('.run-status-label'),
    statusElapsed: h.$('run-status').querySelector('.run-status-elapsed'),
    send: h.$('send')
  };
}

test('assistant text streams into the answer as it arrives', async () => {
  const h = runHarness();
  await h.settle();
  await h.startRun('你好');
  const view = runView(h);
  assert.equal(view.status.hidden, false, '发出消息后状态条就在会话区');
  assert.equal(view.statusLabel.textContent, '正在连接…');
  assert.equal(view.statusElapsed.getAttribute('aria-hidden'), 'true', '秒表不该被逐秒播报');
  assert.equal(view.send.dataset.action, 'cancel');
  assert.equal(view.send.getAttribute('aria-label'), '停止');
  assert.equal(view.send.disabled, false, '运行期间这个按钮必须可点：它就是 Stop');

  h.stream().push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  await h.settle();
  assert.equal(view.statusLabel.textContent, '已开始，等待模型回应…');
  assert.equal(view.statusElapsed.textContent, '0s');

  h.stream().push(sse('assistant.delta', { text: '第一' }));
  await h.settle();
  assert.equal(view.body.textContent, '第一', '第一块到达就渲染，不等整轮结束');
  assert.equal(view.statusLabel.textContent, 'Luna 正在回应…');

  h.stream().push(sse('assistant.delta', { text: '段' }));
  await h.settle();
  assert.equal(view.body.textContent, '第一段', '增量累加，不是每块重画整轮');

  h.stream().push(sse('run.finished', { run_id: 'run-1', answer: '第一段' }));
  await h.settle();
  assert.equal(view.body.textContent, '第一段');
  assert.equal(view.send.getAttribute('aria-label'), '发送', '终止事件一到就退出运行状态');
  assert.equal(view.send.dataset.action, 'send');
  assert.equal(view.status.hidden, true);
  assert.equal(h.$('session-new').disabled, false);
  h.stream().end();
  await h.settle();
});

test('words spoken before a tool call become a run note, not part of the answer', async () => {
  const h = runHarness();
  await h.settle();
  await h.startRun('读一下 notes.md');
  const view = runView(h);
  const stream = h.stream();
  stream.push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  stream.push(sse('assistant.delta', { text: '我先读一下这个文件。' }));
  await h.settle();
  assert.equal(view.body.textContent, '我先读一下这个文件。');
  assert.equal(view.phases().length, 0, '还没有过程可看时不出现过程条目');
  assert.equal(view.meta.hidden, true, '没有过程可概括时标题那一行不显示概况');

  stream.push(sse('tool.started', { run_id: 'run-1', name: 'luna_read_file', arguments: { path: 'notes.md' } }));
  await h.settle();
  assert.equal(view.meta.hidden, false, '有工具调用时这次运行的概况出现');
  assert.match(view.meta.textContent, /1 次工具调用/);
  assert.match(view.meta.textContent, /进行中/);
  assert.equal(view.body.textContent, 'Luna 正在回应…', '运行说明已经离开消息主体');
  const notes = view.notes();
  assert.equal(notes.length, 1);
  assert.match(notes[0].textContent, /运行说明：我先读一下这个文件。/);
  // 第一段不给阶段行：一次运行的第一段没有上一段可对照，那一行对短运行只是噪声。
  assert.equal(view.phases().length, 0, '第一段不出现阶段行');
  assert.deepEqual(view.order(), ['run-note', 'tool-row running', 'assistant-body placeholder'],
    '时间线按真实顺序排列：运行说明 → 工具卡片 → 回答（这一轮还没写出回答）');
  assert.equal(view.statusLabel.textContent, '正在使用工具：读取文件');
  const running = view.cards()[0];
  assert.equal(running.getAttribute('class'), 'tool-row running');
  assert.match(running.querySelector('.tool-detail').textContent, /notes\.md/);

  stream.push(sse('tool.finished', {
    run_id: 'run-1', name: 'luna_read_file', result: '文件内容', duration_ms: 420, generation: 3, version: 'v2', plugin_pid: 91
  }));
  stream.push(sse('assistant.delta', { text: '读完了。' }));
  stream.push(sse('run.finished', { run_id: 'run-1', answer: '读完了。' }));
  await h.settle();
  assert.equal(view.body.textContent, '读完了。');
  assert.equal(view.notes().length, 1, '运行说明只有一条，也没有被复制进回答');
  assert.deepEqual(view.phases().map((node) => node.textContent), ['第 2 段回应'],
    '工具之后模型又接着说，只有这一次续写带阶段行');
  assert.equal(view.cards().length, 1);
  assert.equal(view.cards()[0].querySelector('.tool-name').textContent, '读取文件完成');
  assert.equal(view.cards()[0].querySelector('.tool-meta').textContent, '420ms');
  assert.equal(view.notes()[0].parentElement, view.timeline, '说明与卡片都在同一条时间线里，按时间顺序');
  assert.equal(view.order().at(-1), 'assistant-body', '回答永远是时间线的最后一条');
  assert.match(view.meta.textContent, /已完成/);
  stream.end();
  await h.settle();
});

test('assistant.reasoning streams into its own entry and never into the answer', async () => {
  const { reasoningPreview, REASONING_PREVIEW_CHARS } = require('./app.js');
  const h = runHarness();
  await h.settle();
  await h.startRun('讲一下这个函数');
  const view = runView(h);
  const stream = h.stream();
  stream.push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  stream.push(sse('assistant.reasoning', { text: '先看' }));
  await h.settle();
  assert.equal(view.reasoning().length, 1, 'provider 给了推理才有这一条');
  assert.equal(view.reasoningText()[0].textContent, '先看');
  assert.equal(view.body.textContent, 'Luna 正在回应…', '推理不写进回答主体');
  assert.equal(view.statusLabel.textContent, 'Luna 正在回应…', '推理到了就说明模型已经在产生输出');
  assert.equal(view.reasoning()[0].open, false, '推理默认折叠：回答才是这个回合的主体');
  const preview = () => view.reasoning()[0].querySelector('.run-reasoning-preview').textContent;
  assert.equal(preview(), '先看', '折叠时那一行给出实时预览');

  stream.push(sse('assistant.reasoning', { text: '它的入参' }));
  await h.settle();
  assert.equal(view.reasoningText()[0].textContent, '先看它的入参', '推理条目随事件逐步增长');
  assert.equal(preview(), '先看它的入参', '预览跟着同一份文本一起长，折起来也看得到它在长');

  stream.push(sse('assistant.delta', { text: '它在做两件事。' }));
  await h.settle();
  assert.equal(view.body.textContent, '它在做两件事。');
  assert.equal(view.reasoningText()[0].textContent, '先看它的入参', '回答与推理互不覆盖');
  assert.equal(preview(), '先看它的入参');

  stream.push(sse('run.finished', { run_id: 'run-1', answer: '它在做两件事。' }));
  await h.settle();
  assert.equal(view.body.textContent, '它在做两件事。');
  assert.equal(view.body.textContent.includes('先看'), false, '答案对齐不会把推理带进来');
  assert.deepEqual(view.order(), ['run-reasoning', 'assistant-body'], '第一段推理直接跟在回合里，回答收尾');

  // 预览是纯函数：换行压成空格，方向按这一段是否还在产生取。进行中给最新到达的
  // 一段（前面省略），已经结束的条目给开头（后面省略）——不再都是前省略。
  assert.equal(reasoningPreview(undefined), '');
  assert.equal(reasoningPreview(''), '');
  assert.equal(reasoningPreview(7), '');
  assert.equal(reasoningPreview('  短的  '), '短的');
  assert.equal(reasoningPreview('第一行\n第二行'), '第一行 第二行');
  const wide = 'x'.repeat(REASONING_PREVIEW_CHARS + 10);
  assert.equal(reasoningPreview(wide, true), `…${'x'.repeat(REASONING_PREVIEW_CHARS)}`,
    '流式进行中：预览有界，只留最新到达的一段');
  assert.equal(reasoningPreview(wide), `${'x'.repeat(REASONING_PREVIEW_CHARS)}…`,
    '已经结束的条目：预览有界，给开头而不是一段残片');
  stream.end();
  await h.settle();
});

test('a run without reasoning shows no reasoning entry and no placeholder for it', async () => {
  const { RUN_REASONING_LABEL } = require('./app.js');
  const h = runHarness();
  await h.settle();
  await h.startRun('不需要推理的一轮');
  const view = runView(h);
  const stream = h.stream();
  stream.push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  stream.push(sse('assistant.delta', { text: '好的。' }));
  stream.push(sse('run.finished', { run_id: 'run-1', answer: '好的。' }));
  await h.settle();
  assert.equal(view.reasoning().length, 0, '没有 assistant.reasoning 就没有推理条目');
  assert.equal(view.timeline.textContent.includes(RUN_REASONING_LABEL), false, '也不留占位或编一句话');
  assert.deepEqual(view.order(), ['assistant-body'], '这一轮只有回答');
  assert.equal(view.meta.hidden, true);
  stream.end();
  await h.settle();
});

test('each model leg gets its own reasoning entry behind its continuation phase row', async () => {
  const h = runHarness();
  await h.settle();
  await h.startRun('读文件再总结');
  const view = runView(h);
  const stream = h.stream();
  stream.push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  stream.push(sse('assistant.reasoning', { text: '得先看文件' }));
  stream.push(sse('assistant.delta', { text: '我读一下。' }));
  stream.push(sse('tool.started', { run_id: 'run-1', name: 'luna_read_file', arguments: { path: 'notes.md' } }));
  stream.push(sse('tool.finished', { run_id: 'run-1', name: 'luna_read_file', result: 'ok', duration_ms: 120 }));
  await h.settle();
  assert.equal(view.reasoning()[0].open, false, '推理默认折叠：回答才是这个回合的主体');
  // 用户自己展开一条：后面的推理条目沿用同一个选择（只在会话内记住，不落盘）。
  view.reasoning()[0].open = true;
  view.reasoning()[0].emit('toggle');
  stream.push(sse('assistant.reasoning', { text: '文件里写着 ok，' }));
  stream.push(sse('assistant.reasoning', { text: '直接回答就行' }));
  stream.push(sse('assistant.delta', { text: '文件里写着 ok。' }));
  stream.push(sse('run.finished', { run_id: 'run-1', answer: '文件里写着 ok。' }));
  await h.settle();

  assert.deepEqual(view.order(), [
    'run-reasoning', 'run-note', 'tool-row ok', 'run-phase', 'run-reasoning', 'assistant-body'
  ], '时间线按真实顺序：推理 → 说明 → 工具 → 续写 → 第二段推理 → 回答');
  assert.deepEqual(view.phases().map((node) => node.textContent), ['第 2 段回应'],
    '第一段没有阶段行，续写的那一段才有');
  assert.equal(view.reasoningText().length, 2, '每一段模型输出有自己的推理条目');
  assert.equal(view.reasoning()[1].open, true, '展开过一次后，后面的推理条目沿用同一个选择');
  assert.equal(view.reasoningText()[0].textContent, '得先看文件');
  assert.equal(view.reasoningText()[1].textContent, '文件里写着 ok，直接回答就行', '第二段的推理不会并进第一条');
  assert.equal(view.body.textContent, '文件里写着 ok。');
  assert.equal(view.notes()[0].textContent.includes('得先看文件'), false, '推理不混进运行说明');
  stream.end();
  await h.settle();
});

test('a running reasoning entry previews its latest text, a finished one previews its opening', async () => {
  const { REASONING_PREVIEW_CHARS } = require('./app.js');
  const h = runHarness();
  await h.settle();
  await h.startRun('讲一段很长的推理');
  const view = runView(h);
  const stream = h.stream();
  const reasoning = `${'甲'.repeat(REASONING_PREVIEW_CHARS + 20)}尾`;
  stream.push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  stream.push(sse('assistant.reasoning', { text: reasoning }));
  await h.settle();
  const preview = () => view.reasoning()[0].querySelector('.run-reasoning-preview').textContent;
  assert.equal(preview(), `…${reasoning.slice(reasoning.length - REASONING_PREVIEW_CHARS)}`,
    '还在流式：预览给最新到达的一段（前面省略），折起来也看得出它在长');

  stream.push(sse('assistant.delta', { text: '好。' }));
  stream.push(sse('run.finished', { run_id: 'run-1', answer: '好。' }));
  await h.settle();
  assert.equal(preview(), `${reasoning.slice(0, REASONING_PREVIEW_CHARS)}…`,
    '已经结束：预览给开头（后面省略），不再是一段看起来被切掉开头的残片');

  // 回答不在自己的正文里带标注：答案文本就是渲染出来的那些节点。
  assert.equal(view.body.textContent, '好。', '答案文本里一个字都不多');
  stream.end();
  await h.settle();
});

test('reasoning text reaches the timeline as text, never as markup', async () => {
  const h = runHarness();
  await h.settle();
  await h.startRun('带标签的推理');
  const view = runView(h);
  const stream = h.stream();
  const payload = '<img src=x onerror="boom"> & <script>bad()</script>';
  stream.push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  stream.push(sse('assistant.reasoning', { text: 7 }));
  await h.settle();
  assert.equal(view.reasoning().length, 0, '不是文本的推理不会编出一条来');
  stream.push(sse('assistant.reasoning', { text: payload }));
  await h.settle();
  const entry = view.reasoning()[0];
  assert.equal(entry.querySelector('.run-reasoning-body').textContent, payload);
  assert.equal(entry.querySelector('.run-reasoning-body').childElementCount, 0, '推理正文只有一个文本节点');
  assert.equal(entry.querySelector('img'), null);
  assert.equal(entry.querySelector('script'), null);
  stream.end();
  await h.settle();
});

test('the terminal answer replaces the streamed text instead of being appended twice', async () => {
  const h = runHarness();
  await h.settle();
  await h.startRun('给我一个结论');
  const view = runView(h);
  const stream = h.stream();
  stream.push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  stream.push(sse('assistant.delta', { text: '草稿' }));
  stream.push(sse('assistant.delta', { text: '：一半' }));
  await h.settle();
  assert.equal(view.body.textContent, '草稿：一半');

  stream.push(sse('run.finished', { run_id: 'run-1', answer: '最终回答。' }));
  await h.settle();
  assert.equal(view.body.textContent, '最终回答。', '终止时用权威答案对齐，替换而不是追加');
  assert.equal(view.body.textContent.includes('草稿'), false);
  stream.end();
  await h.settle();

  // 流式文本与权威答案相同时也不能出现两遍。
  await h.startRun('再来一次');
  const second = runView(h);
  const again = h.stream();
  again.push(sse('run.started', { run_id: 'run-2', session_id: 'aaaaaaaa' }));
  again.push(sse('assistant.delta', { text: '同一句话' }));
  again.push(sse('run.finished', { run_id: 'run-2', answer: '同一句话' }));
  await h.settle();
  assert.equal(second.body.textContent, '同一句话');
  again.end();
  await h.settle();
});

test('Stop cancels once, keeps the partial text and leaves the running state', async () => {
  const h = runHarness();
  await h.settle();
  await h.startRun('写一段长文');
  const view = runView(h);
  const stream = h.stream();
  stream.push(sse('run.started', { run_id: 'run-7', session_id: 'aaaaaaaa' }));
  stream.push(sse('assistant.delta', { text: '写到一半' }));
  await h.settle();

  view.send.focus();
  view.send.click();
  await h.settle();
  const cancels = h.calls.filter(({ url }) => url === '/api/runs/run-7/cancel');
  assert.equal(cancels.length, 1, '点击 Stop 就是调用取消入口');
  assert.equal(cancels[0].options.method, 'POST');
  assert.equal(view.statusLabel.textContent, '正在取消…');

  view.send.click();
  view.send.click();
  await h.settle();
  assert.equal(h.calls.filter(({ url }) => url === '/api/runs/run-7/cancel').length, 1, '重复点击不再发请求，也不报错刷屏');

  stream.push(sse('run.cancelled', { run_id: 'run-7', reason: 'user' }));
  await h.settle();
  assert.equal(view.send.getAttribute('aria-label'), '发送');
  assert.equal(view.send.disabled, false);
  assert.equal(h.$('session-new').disabled, false, '收到 run.cancelled 立刻退出运行状态');
  assert.equal(view.status.hidden, true);
  assert.equal(view.body.textContent, '写到一半', '已渲染的部分保留');
  assert.match(view.turn.querySelector('.run-incomplete').textContent, /这次运行被取消了/);
  assert.match(view.meta.textContent, /已取消/);
  stream.end();
  await h.settle();
});

test('a cancel is answered by the server, not invented by the page', async () => {
  // 404：这次运行已经结束——按"已经结束"处理，不报错也不谎称取消成功。
  const missing = runHarness({ cancel: async () => ({ ok: false, status: 404, json: async () => ({ error: 'not found' }) }) });
  await missing.settle();
  await missing.startRun('取消一个已经结束的运行');
  const gone = runView(missing);
  const goneStream = missing.stream();
  goneStream.push(sse('run.started', { run_id: 'run-7', session_id: 'aaaaaaaa' }));
  await missing.settle();
  gone.send.click();
  await missing.settle();
  assert.equal(gone.statusLabel.textContent, '正在取消…', '404 不当成取消失败');
  goneStream.push(sse('run.cancelled', { run_id: 'run-7', reason: 'user' }));
  await missing.settle();
  assert.equal(missing.$('send').getAttribute('aria-label'), '发送');
  goneStream.end();
  await missing.settle();

  // 取消请求本身失败时只说一次，按钮回到可点状态，等下一个真实事件覆盖这句话。
  const broken = runHarness({ cancel: async () => ({ ok: false, status: 500, json: async () => ({ error: '内部错误' }) }) });
  await broken.settle();
  await broken.startRun('取消失败的情形');
  const failed = runView(broken);
  const brokenStream = broken.stream();
  brokenStream.push(sse('run.started', { run_id: 'run-7', session_id: 'aaaaaaaa' }));
  await broken.settle();
  failed.send.click();
  await broken.settle();
  assert.match(failed.statusLabel.textContent, /取消失败：内部错误/);
  assert.equal(failed.send.disabled, false, '取消失败后仍然可以再停一次');
  brokenStream.push(sse('assistant.delta', { text: '还在继续' }));
  await broken.settle();
  assert.equal(failed.statusLabel.textContent, 'Luna 正在回应…', '下一个真实事件覆盖那句话');
  brokenStream.end();
  await broken.settle();
});

test('a run that fails before any answer leaves no empty answer container', async () => {
  const h = runHarness();
  await h.settle();
  await h.startRun('会失败的一轮');
  const view = runView(h);
  const stream = h.stream();
  stream.push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  stream.push(sse('run.failed', { run_id: 'run-1', error: '模型不可用' }));
  await h.settle();
  // 没有答案时"回答"这一格不应该留下：标注只标真的答案。
  assert.equal(view.timelineBody(), null, '失败且没有答案时不留下空的回答容器');
  assert.match(view.turn.querySelector('.error-copy').textContent, /没能完成这次回应/);
  assert.match(view.turn.querySelector('.run-error-detail').textContent, /模型不可用/);
  assert.equal(view.status.hidden, true, '终止事件一到就退出运行状态');
  stream.end();
  await h.settle();
});

test('a tool card bounds long arguments and results and keeps identity secondary', async () => {
  const h = runHarness();
  await h.settle();
  await h.startRun('读一个大文件');
  const view = runView(h);
  const stream = h.stream();
  const args = { path: `notes/${'a'.repeat(2000)}.md` };
  const fullArguments = JSON.stringify(args, null, 2);
  const fullResult = `${'一行内容\n'.repeat(40)}最后一行`;
  stream.push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  stream.push(sse('tool.started', { run_id: 'run-1', name: 'luna_read_file', arguments: args }));
  stream.push(sse('tool.finished', {
    run_id: 'run-1', name: 'luna_read_file', result: fullResult, duration_ms: 1200, generation: 4, version: 'v1', plugin_pid: 55
  }));
  await h.settle();

  const card = view.cards()[0];
  const summary = card.querySelector('summary');
  assert.equal(card.getAttribute('class'), 'tool-row ok');
  assert.equal(card.querySelector('.tool-name').textContent, '读取文件完成');
  assert.equal(card.querySelector('.tool-meta').textContent, '1.2s');
  assert.equal(summary.textContent.includes('generation'), false, '执行身份不做主视觉');
  assert.equal(summary.textContent.includes('PID'), false);
  assert.ok(!summary.textContent.includes('最后一行'), '结果不塞进那一行摘要');

  const rows = card.querySelector('.tool-detail').querySelectorAll('dd');
  assert.equal(rows.length, 3, '工具、参数、结果各一行');
  const params = rows[1];
  // 有界性按"默认展示的那一段预览"来量：闭合的 `.value-more` 里的全文也属于这一行
  // 的 textContent（真实 DOM 与适配器都不排除折叠内容），拿整行去量量到的不是预览。
  const paramPreview = params.querySelector('.value-preview');
  assert.ok(paramPreview.textContent.length <= 1201, `参数默认有界，实际 ${paramPreview.textContent.length}`);
  assert.ok(paramPreview.textContent.endsWith('…'), '被截断的预览要说明还有更多');
  const paramMore = params.querySelector('.value-more');
  assert.ok(!paramMore.open, '全文默认折叠');
  assert.match(paramMore.querySelector('summary').textContent, /展开全文/);
  assert.equal(paramMore.querySelector('pre').textContent, fullArguments, '展开看到的是全文');
  paramMore.open = true;
  assert.equal(paramMore.querySelector('pre').textContent.includes('…'), false);

  const result = rows[2];
  // 标签与值同属一行（`dl > div > dt + dd`），dt 是 dd 的兄弟而不是它的后代。
  assert.equal(result.parentElement.querySelector('dt').textContent, '结果');
  const resultPreview = result.querySelector('.value-preview');
  assert.ok(resultPreview.textContent.length < fullResult.length, '长结果默认只给一段');
  const resultMore = result.querySelector('.value-more');
  assert.equal(resultMore.querySelector('pre').textContent, fullResult);

  // 执行身份只在次级层里，而且是权威的同一份读法。
  const detail = card.querySelector('.tool-more');
  assert.ok(!detail.open, '详情默认折叠');
  assert.equal(detail.querySelector('summary').textContent, '详情');
  assert.match(detail.querySelector('dl').textContent, /generation 4 · v1 · PID 55/);
  stream.push(sse('run.finished', { run_id: 'run-1', answer: '读完了。' }));
  await h.settle();
  stream.end();
  await h.settle();
});

test('a refused call is shown as a refusal while a failure stays a failure', async () => {
  const h = runHarness();
  await h.settle();
  await h.startRun('读一个越界的路径');
  const view = runView(h);
  const stream = h.stream();
  const raw = 'the tool refused this call: 路径越出可读范围';
  stream.push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  stream.push(sse('tool.started', { run_id: 'run-1', name: 'luna_read_file', arguments: '{"path":"/etc/passwd"}' }));
  stream.push(sse('tool.failed', { run_id: 'run-1', name: 'luna_read_file', error: raw, duration_ms: 3 }));
  stream.push(sse('tool.started', { run_id: 'run-1', name: 'luna_text_transform', arguments: '{}' }));
  stream.push(sse('tool.failed', { run_id: 'run-1', name: 'luna_text_transform', error: '插件已经不在', duration_ms: 8 }));
  await h.settle();

  const refused = view.cards()[0];
  assert.equal(refused.getAttribute('class'), 'tool-row refused', '拒绝不是一次崩溃');
  assert.equal(refused.querySelector('.tool-state').getAttribute('data-state'), 'refused');
  assert.equal(refused.querySelector('.tool-name').textContent, '读取文件被拒绝');
  const refusalDetail = refused.querySelector('.tool-detail').textContent;
  assert.match(refusalDetail, /拒绝原因/);
  assert.match(refusalDetail, /路径越出可读范围/);
  assert.equal(refused.querySelector('.tool-more').querySelector('pre').textContent, raw, '协议前缀的原文留在次级层里');
  assert.equal(refused.querySelector('summary').textContent.includes(raw), false);

  const failed = view.cards()[1];
  assert.equal(failed.getAttribute('class'), 'tool-row failed');
  assert.equal(failed.querySelector('.tool-name').textContent, '文本转换失败');
  assert.match(failed.querySelector('.tool-detail').textContent, /错误/);
  assert.match(failed.querySelector('.tool-detail').textContent, /插件已经不在/);
  stream.push(sse('run.finished', { run_id: 'run-1', answer: '这两次都没成。' }));
  await h.settle();
  stream.end();
  await h.settle();
});

test('the run process stays inline in one timeline while usage is secondary info', async () => {
  const h = runHarness();
  await h.settle();
  await h.startRun('直接回答就好');
  const first = runView(h);
  const stream = h.stream();
  stream.push(sse('run.started', { run_id: 'run-1', session_id: 'aaaaaaaa' }));
  stream.push(sse('assistant.delta', { text: '直接回答' }));
  await h.settle();
  assert.equal(first.steps().length, 0, '一次直接回答的运行没有过程条目');
  stream.push(sse('run.finished', { run_id: 'run-1', answer: '直接回答' }));
  await h.settle();
  assert.equal(first.steps().length, 0);
  assert.equal(first.meta.hidden, true, '没有过程可概括、也没有用量时，标题那一行不显示概况');
  assert.equal(first.body.textContent, '直接回答');
  stream.end();
  await h.settle();

  await h.startRun('先读文件再回答');
  const view = runView(h);
  const second = h.stream();
  second.push(sse('run.started', { run_id: 'run-2', session_id: 'aaaaaaaa' }));
  second.push(sse('tool.started', { run_id: 'run-2', name: 'luna_read_file', arguments: '{}' }));
  second.push(sse('tool.finished', { run_id: 'run-2', name: 'luna_read_file', result: 'ok', duration_ms: 300 }));
  second.push(sse('usage.updated', { run_id: 'run-2', input_tokens: 1234, output_tokens: 320 }));
  await h.settle();

  assert.equal(view.timeline.tagName, 'DIV', '过程内联在回合里，不再是一个独立折叠块');
  assert.equal(view.meta.hidden, false);
  assert.match(view.meta.textContent, /1 次工具调用/);
  assert.match(view.meta.textContent, /进行中/);
  // 用量属于整轮，落在标题那一行的元信息里：它不再夹在推理与工具卡片之间，也不再
  // 是一条会随事件插进时间线中间的行。
  assert.match(view.meta.textContent, /tokens：输入 1.2k · 输出 320/);
  assert.equal(view.meta.textContent.includes('缓存'), false, '没给的字段不显示');
  assert.equal(view.timeline.querySelector('.run-usage'), null, '时间线里没有用量这一条');
  // 条目按发生顺序，回答留在最后（这一轮还没有写出回答，所以它还是占位状态）。
  // 这一轮只有一次调用，所以没有"第 N 段回应"那种标号，只有一张工具卡片。
  assert.deepEqual(view.order(), ['tool-row ok', 'assistant-body placeholder']);
  assert.equal(view.phases().length, 0, '只有一次调用时不给阶段行');

  second.push(sse('usage.updated', { run_id: 'run-2', input_tokens: 1234, output_tokens: 320, cached_tokens: 64 }));
  second.push(sse('run.finished', { run_id: 'run-2', answer: '文件内容是 ok' }));
  await h.settle();
  assert.match(view.meta.textContent, /tokens：输入 1.2k · 输出 320 · 缓存 64/, '后来的用量更新覆盖同一行');
  assert.equal(view.meta.textContent.split('tokens：').length - 1, 1, '用量在同一行上只出现一次');
  assert.match(view.meta.textContent, /已完成/);
  assert.equal(view.meta.textContent.includes('进行中'), false);
  assert.equal(view.body.textContent, '文件内容是 ok');
  second.end();
  await h.settle();

  // 一次直接回答的运行也如实报出它真的收到的用量：元信息行只因为有量而出现，
  // 但它仍然不是时间线里的条目。
  await h.startRun('直接回答也要报用量');
  const third = runView(h);
  const again = h.stream();
  again.push(sse('run.started', { run_id: 'run-3', session_id: 'aaaaaaaa' }));
  again.push(sse('assistant.delta', { text: '好。' }));
  again.push(sse('usage.updated', { run_id: 'run-3', input_tokens: 40, output_tokens: 8 }));
  await h.settle();
  assert.equal(third.steps().length, 0, '用量不是时间线里的条目');
  assert.equal(third.meta.hidden, false, '有用量时标题那一行出现');
  assert.match(third.meta.textContent, /tokens：输入 40 · 输出 8/);
  again.push(sse('run.finished', { run_id: 'run-3', answer: '好。' }));
  await h.settle();
  assert.match(third.meta.textContent, /tokens：输入 40 · 输出 8/);
  again.end();
  await h.settle();
});

test('run phase and usage copy is read from events only', () => {
  const {
    runPhaseText, runPhaseEntryText, runPhaseVisible, runOutcomeLabel, runTraceMeta, replayRunState, usageText, formatTokenCount
  } = require('./app.js');
  assert.equal(runPhaseText('connecting'), '正在连接…');
  assert.equal(runPhaseText('waiting'), '已开始，等待模型回应…');
  assert.equal(runPhaseText('streaming'), 'Luna 正在回应…');
  assert.equal(runPhaseText('tool', 'luna_read_file'), '正在使用工具：读取文件');
  assert.equal(runPhaseText('tool', 'luna_unknown'), '正在使用工具：工具调用');
  assert.equal(runPhaseText('cancelling'), '正在取消…');
  assert.equal(runPhaseText('nonsense'), '');
  assert.equal(runPhaseText(undefined), '');
  for (const text of [runPhaseText('waiting'), runPhaseText('streaming'), runPhaseText('cancelling')]) {
    assert.equal(/思考|分析|意图|thinking/.test(text), false, '不写模型在想什么');
  }

  assert.equal(runPhaseEntryText(0), '第 1 段回应', '阶段行给出段号，不写开始/继续这类冗余措辞');
  assert.equal(runPhaseEntryText(1), '第 2 段回应');
  assert.equal(runPhaseEntryText(2), '第 3 段回应');
  assert.equal(runPhaseEntryText(undefined), '第 1 段回应', '拿不到段号时按第一段，不编一个别的数字');
  // 阶段行只在真的多段时出现：第一段没有上一段可对照，标号只是噪声。
  assert.equal(runPhaseVisible(0), false, '第一段不给阶段行');
  assert.equal(runPhaseVisible(1), true, '续写的那一段才给');
  assert.equal(runPhaseVisible(5), true);
  assert.equal(runPhaseVisible(undefined), false, '拿不到段号时不凭空加一行');
  assert.equal(runPhaseVisible('1'), false);
  assert.equal(runOutcomeLabel('ok'), '已完成');
  assert.equal(runOutcomeLabel('cancelled'), '已取消');
  assert.equal(runOutcomeLabel('failed'), '失败');
  assert.equal(runOutcomeLabel('running'), '进行中');
  assert.equal(runOutcomeLabel(''), '结果未知');

  assert.equal(runTraceMeta(0, null, 'running'), '进行中');
  assert.equal(runTraceMeta(2, 6100, 'ok'), '2 次工具调用 · 6s · 已完成');
  assert.equal(runTraceMeta(1, null, 'cancelled'), '1 次工具调用 · 已取消', '没有耗时就不写一个数字');
  assert.equal(runTraceMeta(0, null, ''), '结果未知');

  assert.equal(replayRunState('ok'), 'ok');
  assert.equal(replayRunState('cancelled'), 'cancelled');
  assert.equal(replayRunState('error'), 'failed');
  assert.equal(replayRunState(''), '');
  assert.equal(replayRunState(undefined), '');

  assert.equal(usageText({ input_tokens: 1234, output_tokens: 320 }), 'tokens：输入 1.2k · 输出 320');
  assert.equal(usageText({ input_tokens: 0 }), 'tokens：输入 0', '真实拿到的 0 照写');
  assert.equal(usageText({ run_id: 'r1' }), '', '拿不到就不显示，也不留占位');
  assert.equal(usageText(undefined), '');
  assert.equal(usageText({ input_tokens: '1200' }), '', '不是数字就不猜');
  assert.equal(formatTokenCount(999), '999');
  assert.equal(formatTokenCount(1000), '1k');
  assert.equal(formatTokenCount(1234), '1.2k');
  assert.equal(formatTokenCount(123456), '123k');
  assert.equal(formatTokenCount(-1), '');
  assert.equal(formatTokenCount(undefined), '');
});

test('long tool text is clipped to a bounded preview and the full text is kept', () => {
  const { clipText, TOOL_TEXT_MAX_CHARS, TOOL_TEXT_MAX_LINES } = require('./app.js');
  assert.deepEqual(clipText('短文本'), { text: '短文本', clipped: false });
  assert.deepEqual(clipText(''), { text: '', clipped: false });

  const manyLines = Array.from({ length: TOOL_TEXT_MAX_LINES + 4 }, (_, index) => `第 ${index} 行`).join('\n');
  const byLines = clipText(manyLines);
  assert.equal(byLines.clipped, true);
  assert.equal(byLines.text.endsWith('…'), true);
  assert.equal(byLines.text.split('\n').length, TOOL_TEXT_MAX_LINES, '默认展示有行数上限');

  const wide = 'x'.repeat(TOOL_TEXT_MAX_CHARS * 3);
  const byChars = clipText(wide);
  assert.equal(byChars.clipped, true);
  assert.equal(byChars.text.length, TOOL_TEXT_MAX_CHARS + 1);
  assert.equal(clipText(wide, 10).text, 'xxxxxxxxxx…');
  assert.equal(clipText('a\n\n\n\nb').clipped, false, '少量换行不触发截断');
  assert.equal(clipText(undefined).clipped, false);
  assert.equal(clipText(7).text, '7');
});

test('tool arguments and results are read in both shapes the contract allows', () => {
  const { toolArgumentsText, toolResultText } = require('./app.js');
  assert.equal(toolArgumentsText('{"text":"moon"}'), '{\n  "text": "moon"\n}', '原始字符串按同一读法美化');
  assert.equal(toolArgumentsText({ text: 'moon' }), '{\n  "text": "moon"\n}', 'JSON 对象直接给出时同样可读');
  assert.equal(toolArgumentsText('not json'), 'not json');
  assert.equal(toolArgumentsText(''), '—');
  assert.equal(toolArgumentsText(undefined), '—');
  assert.equal(toolArgumentsText(7), '—', '不是字符串也不是对象时不猜');

  assert.equal(toolResultText('文件内容'), '文件内容');
  assert.equal(toolResultText({ ok: true }), '{\n  "ok": true\n}');
  assert.equal(toolResultText(12), '12');
  assert.equal(toolResultText(''), '—');
  assert.equal(toolResultText(null), '—');
  assert.equal(toolResultText(undefined), '—');
  assert.equal(toolResultText(undefined, 1), '—');
});

test('a refusal is told apart from a failure and neither is written as a crash', () => {
  const { toolFailureKind, toolRefusalReason, toolRefusedLabel, toolStateLabel, TOOL_REFUSAL_PREFIX } = require('./app.js');
  assert.equal(TOOL_REFUSAL_PREFIX, 'the tool refused this call: ');
  const refused = 'the tool refused this call: 路径越出可读范围';
  assert.equal(toolFailureKind(refused), 'refused');
  assert.equal(toolFailureKind('插件已经不在'), 'failed');
  assert.equal(toolFailureKind('the tool refused to do it'), 'failed', '前缀不同就不是拒绝');
  assert.equal(toolFailureKind(''), 'failed');
  assert.equal(toolFailureKind(undefined), 'failed');
  assert.equal(toolRefusalReason(refused), '路径越出可读范围');
  assert.equal(toolRefusalReason('插件已经不在'), '');
  assert.equal(toolRefusedLabel('luna_read_file'), '读取文件被拒绝');
  assert.equal(toolRefusedLabel('luna_unknown'), '工具调用被拒绝');
  assert.equal(toolStateLabel('luna_read_file', 'refused'), '读取文件被拒绝');
  assert.equal(toolStateLabel('luna_read_file', 'running'), '正在读取文件…');
  assert.equal(toolStateLabel('luna_read_file', 'ok'), '读取文件完成');
  assert.equal(toolStateLabel('luna_read_file', 'failed'), '读取文件失败');
});

test('elapsed and duration are read from what the runtime measured', () => {
  const { formatElapsed, formatDuration } = require('./app.js');
  assert.equal(formatElapsed(0), '0s');
  assert.equal(formatElapsed(999), '0s');
  assert.equal(formatElapsed(12300), '12s');
  assert.equal(formatElapsed(59000), '59s');
  assert.equal(formatElapsed(60000), '1:00');
  assert.equal(formatElapsed(125000), '2:05');
  assert.equal(formatElapsed(-5), '0s');
  assert.equal(formatElapsed(undefined), '0s');

  assert.equal(formatDuration(0), '0ms');
  assert.equal(formatDuration(420), '420ms');
  assert.equal(formatDuration(999.4), '999ms');
  assert.equal(formatDuration(1200), '1.2s');
  assert.equal(formatDuration(60500), '1:00');
  assert.equal(formatDuration(undefined), '', 'Runtime 没给耗时就不写一个');
  assert.equal(formatDuration(-1), '');
  assert.equal(formatDuration('1200'), '');
});

test('every run observability style the script builds a class for exists in the stylesheet', () => {
  const css = source('style.css');
  const selectors = ['.run-status-dot', '.run-status-label', '.run-status-elapsed', '.assistant-head', '.run-meta',
    '.run-timeline', '.run-reasoning', '.run-reasoning-label', '.run-reasoning-preview', '.run-reasoning-body',
    '.run-phase', '.run-note',
    '.run-note-label', '.run-incomplete', '.tool-state', '.tool-name', '.tool-meta', '.tool-more',
    '.value-more', '.stop-icon', '.tool-row.ok', '.tool-row.refused'];
  for (const selector of selectors) {
    const escaped = selector.replace(/\./g, '\\.');
    assert.match(css, new RegExp(`${escaped}(?=[\\s,{:.])`), `missing style ${selector}`);
  }
  for (const state of ['ok', 'running', 'refused', 'failed']) {
    assert.ok(css.includes(`.tool-state[data-state="${state}"]`), `missing state colour ${state}`);
  }
  // 全文展开后必须自己滚动，否则一次长读取会把对话拉成一条看不到底的 log。
  assert.match(css, /\.value-more\s*>\s*pre\s*\{[^}]*max-height/s);
  // 推理正文同样有界：一次很长的推理不能把回答主体顶出视野。
  assert.match(css, /\.run-reasoning-body\s*\{[^}]*max-height/s);
  assert.match(css, /\.run-reasoning\s*>\s*summary\s*\{[^}]*min-height:\s*var\(--luna-control-sm\)/s, '推理那一行沿用控件高度');
  // 回答不再是"文档阅读器"式的重卡片：没有抬升背景、没有整圈边框、没有卡片圆角、
  // 没有行首标注，字号与用户消息同档（--luna-font-body）。它仍然是主体，靠的是
  // 位置（时间线收尾）、整宽、与过程条目之间一档更大的留白，以及一条极轻的左侧线。
  assert.doesNotMatch(css, /\.assistant-body\s*\{[^}]*background/s, '回答不再加卡片背景');
  assert.doesNotMatch(css, /\.assistant-body\s*\{[^}]*border-radius/s, '回答不再有卡片圆角');
  assert.doesNotMatch(css, /\.assistant-body\s*\{[^}]*border:\s*1px/s, '回答不再有整圈边框');
  assert.match(css, /\.assistant-body\s*\{[^}]*font-size:\s*var\(--luna-font-body\)/s, '回答与用户消息同档字号');
  assert.match(css, /\.assistant-body\s*\{[^}]*border-left:\s*1px solid var\(--luna-border-weak\)/s, '回答靠一条极轻的左侧线区分');
  assert.match(css, /\.turn\.user\s+\.turn-content\s*\{[^}]*font-size:\s*var\(--luna-font-body\)/s, '用户消息同为正文档');
  assert.match(css, /\.assistant-body\s*>\s*p\s*\{[^}]*margin:\s*0 0 var\(--luna-space-4\)/s, '段落间距给正文留出呼吸');
  assert.equal(/\.assistant-body::?before/.test(css), false, '回答不再有文档式的行首标注');
  assert.match(css, /\.run-timeline\s*>\s*\*\s*\+\s*\.assistant-body\s*\{[^}]*margin-top:\s*var\(--luna-space-5\)/s, '回答与过程条目之间用留白拉开');
  // 工具卡片有可辨的卡片感：surface 表面 + 弱边框 + 尺度 token 的圆角，和"不加容器
  // 的推理条目"一眼分得开，但文字仍是 12px muted。
  assert.match(css, /\.tool-row\s*\{[^}]*background:\s*var\(--luna-surface\)/s);
  assert.match(css, /\.tool-row\s*\{[^}]*border:\s*1px solid var\(--luna-border-weak\)/s);
  assert.match(css, /\.tool-row\s*\{[^}]*border-radius:\s*var\(--luna-radius-md\)/s);
  // 阶段行只是标点级的标号：比过程条目还小一档，不再占一个控件高度。
  assert.match(css, /\.run-phase\s*\{[^}]*font-size:\s*var\(--luna-font-label\)/s);
  assert.match(css, /\.run-phase\s*\{[^}]*color:\s*var\(--luna-text-subtle\)/s);
  assert.doesNotMatch(css, /\.run-phase\s*\{[^}]*min-height/s, '阶段行不再占一个控件高度');
  // 折叠时那一行的实时预览是可见内容；展开后正文已经给出全文，预览不再重复一遍。
  assert.match(css, /\.run-reasoning\[open\][^{]*\.run-reasoning-preview\s*\{\s*display:\s*none/s);
  assert.equal(/gradient\s*\(/i.test(css), false);
  assert.doesNotMatch(css, /border-radius:\s*(?:1[0-9]|[2-9][0-9])px/, '圆角只来自尺度 token');
});

test('the run surface reuses the existing status element and invents no new state', () => {
  const html = source('index.html');
  const js = source('app.js');
  // 状态条就是标记里已有的那一个；Stop 就是输入区那个按钮。
  assert.match(html, /<div id="run-status" class="run-status" role="status" hidden><\/div>/);
  assert.equal(/id="stop"|stop-button|run-controls/.test(html), false, '不再另起一套控制栏');
  assert.match(js, /send\.addEventListener\('click'/);
  assert.match(js, /\/api\/runs\/\$\{runID\}\/cancel/);
  assert.match(js, /runStatus\.dataset\.state = liveRun\.state/);
  // 运行表面不自造思考态：没有“思考中/分析意图”这类状态，也没有另一套控制栏。
  // 档位是启动期的模型参数，只作为设置里的只读项出现，所以这里禁的是状态词汇，
  // 不是“思考”这个词本身——全文禁止会把它挡在产品之外。
  for (const text of ['思考中', '思考状态', '分析意图', 'thinking', 'reasoning 内容']) {
    assert.equal(html.includes(text), false, `标记里不出现 ${text}`);
    assert.equal(js.includes(text), false, `脚本里不出现 ${text}`);
  }
  assert.equal(html.includes('思考档位'), true, '档位只在设置里作为只读参数出现');
  assert.equal(js.includes('innerHTML'), false);
  const storedKeys = new Set([...js.matchAll(/localStorage\.(?:getItem|setItem)\('([^']+)'/g)].map((match) => match[1]));
  assert.deepEqual([...storedKeys].sort(), ['luna.sidebar', 'luna.sidebarWidth', 'luna.theme'],
    '轨迹的折叠状态留在会话里，不新增持久化键');
});

// --- The command table in the composer ---------------------------------------
// The table comes from the server; what is decided here is only how a draft is
// read against it. These are the rules the composer depends on, so they are
// pinned without a browser in the loop.

const COMMAND_TABLE = [
  { name: 'help', summary: '列出 Luna 知道的命令。', usage: '/help', category: '会话', args: 'none', busy: 'allow' },
  { name: 'model', summary: '切换这次会话使用的模型。', usage: '/model <模型>', category: '模型',
    aliases: ['m', 'mdl'], args: 'options', busy: 'reject',
    options: [{ value: 'flash', summary: '快速回答' }, { value: 'pro' }] }
];

test('a draft with a slash offers the table, in the order the server sent it', () => {
  const { commandList, commandCandidates } = require('./app.js');
  assert.deepStrictEqual(commandList(COMMAND_TABLE).map((item) => item.name), ['help', 'model']);
  const all = commandCandidates(COMMAND_TABLE, '/');
  assert.equal(all.mode, 'command');
  assert.deepStrictEqual(all.items.map((item) => item.name), ['help', 'model']);
  assert.equal(all.items[0].label, '/help');
  assert.equal(all.items[0].usage, '/help');
  assert.equal(all.items[0].summary, '列出 Luna 知道的命令。');
  assert.equal(all.items[0].insert, '/help ');
});

test('a command is matched by name or alias, ignoring case', () => {
  const { findCommand, commandCandidates } = require('./app.js');
  assert.equal(findCommand(COMMAND_TABLE, 'model').name, 'model');
  assert.equal(findCommand(COMMAND_TABLE, 'MDL').name, 'model');
  assert.equal(findCommand(COMMAND_TABLE, 'nope'), null);
  assert.deepStrictEqual(commandCandidates(COMMAND_TABLE, '/MDL').items.map((item) => item.name), ['model']);
  assert.deepStrictEqual(commandCandidates(COMMAND_TABLE, '/m').items.map((item) => item.name), ['model']);
  assert.deepStrictEqual(commandCandidates(COMMAND_TABLE, '/zzz').items, []);
});

test('prose never grows a menu, and a second word is not a candidate', () => {
  const { commandCandidates } = require('./app.js');
  assert.deepStrictEqual(commandCandidates(COMMAND_TABLE, 'hello /help'), { mode: 'none', items: [] });
  assert.deepStrictEqual(commandCandidates(COMMAND_TABLE, ''), { mode: 'none', items: [] });
  // The name of a command that takes no argument is settled; a space after it
  // means the user has moved on to writing their message.
  assert.deepStrictEqual(commandCandidates(COMMAND_TABLE, '/help ').items, []);
  // A written option is not a candidate for another one.
  assert.deepStrictEqual(commandCandidates(COMMAND_TABLE, '/model flash ').items, []);
  // A value that contains a space still stops taking candidates once a value is
  // written: a second word is a second value, not a longer first one. Without
  // this rule the prefix filter alone would offer "gpt 4 turbo" after "gpt 4 ".
  const spaced = [{ name: 'model', args: 'options', options: [{ value: 'gpt 4' }, { value: 'gpt 4 turbo' }] }];
  assert.deepStrictEqual(commandCandidates(spaced, '/model gpt 4 ').items, []);
});

test('a command that takes a fixed set offers its values, and completing one is text', () => {
  const { commandCandidates } = require('./app.js');
  const options = commandCandidates(COMMAND_TABLE, '/model fl');
  assert.equal(options.mode, 'options');
  assert.deepStrictEqual(options.items.map((item) => item.value), ['flash']);
  assert.equal(options.items[0].usage, '/model flash');
  assert.equal(options.items[0].summary, '快速回答');
  assert.equal(options.items[0].insert, '/model flash ');
  // With nothing typed after the space, every value is offered.
  assert.deepStrictEqual(commandCandidates(COMMAND_TABLE, '/model ').items.map((item) => item.value), ['flash', 'pro']);
});

test('a malformed table entry is dropped rather than rendered', () => {
  const { commandList, commandCandidates } = require('./app.js');
  assert.deepStrictEqual(commandList([null, {}, { name: '' }, { name: 'ok' }]).map((item) => item.name), ['ok']);
  assert.deepStrictEqual(commandList(undefined), []);
  assert.deepStrictEqual(commandCandidates(undefined, '/x').items, []);
  const withJunkOptions = [{ name: 'x', args: 'options', options: [null, { value: '' }, { value: 'y' }] }];
  assert.deepStrictEqual(commandCandidates(withJunkOptions, '/x ').items.map((item) => item.value), ['y']);
});
