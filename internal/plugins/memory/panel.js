// The Memory panel, as the capability's own browser module.
//
// The host owns the panel itself — the header entry, the overlay, the title and
// the close control — and hands this module a container to mount into. What the
// panel *says* belongs here: the hint line, the rows, the retract call and the
// wording around all of it. That is what makes disabling the capability take the
// panel's content away with the route it talks to, instead of leaving a host
// panel that renders nothing.
//
// The panel is a product surface, not a lifecycle log: mounting, unmounting and
// how many requests went out are things the module knows and the user does not
// need. It has two layers instead of one flat list. The facts in effect are the
// surface: each one carries its text, where it came from, when it was recorded,
// and the one action that changes anything — retracting it, kept visually
// secondary because it is the exception, not the point of the row. Retracted
// facts are the archive behind that surface: one summary line by default, the
// entries only when asked for, because a store's internal shape is not what the
// reader opened the panel to see.
//
// It uses the same mount(target, api) / unmount(target) contract as every other
// browser module, and the host's own classes for the shared look; its layout
// rules come from the capability's own stylesheet route, so the host
// stylesheet has no memory-specific rules and disabling the capability takes
// the panel's look away with its content.

const FACTS_URL = '/api/memory';
const RETRACT_URL = '/api/memory/retract';

// The panel's rules are a separate asset of this capability, linked from the
// module's own URL so the two always move together. They are linked rather than
// injected as a <style> element because the service's CSP is
// `default-src 'self'`: a same-origin stylesheet is allowed, an inline style
// element is refused, and a refused one leaves the panel unstyled.
const STYLE_URL = new URL('panel.css', import.meta.url).href;

// How long an operation's own feedback stays before it takes itself away. The
// status line carries "what is happening right now" and "what went wrong" —
// never a running commentary. A settled state (a fact that moved to the
// retracted list) is already visible in the lists, so it is not repeated here.
const STATUS_LINGER = 6000;

// The retracted section's toggle points at its own list with aria-controls, so
// every mount needs its own id: mounting twice on one page is a host behaviour
// (re-enabling the capability), and a fixed id would address the other panel's
// list.
let mountSeq = 0;

// One mount's state. A module instance lives in the container the host created,
// so the state is keyed by that container and released on unmount.
const states = new WeakMap();

export function mount(target, api) {
  // api 留在签名里（宿主按契约传入），但这个面板不写生命周期日志：挂载、
  // 卸载和请求次数是模块自己的事，不是用户要看的内容。
  void api;
  const link = document.createElement('link');
  link.rel = 'stylesheet';
  link.href = STYLE_URL;
  document.head.append(link);

  const hint = document.createElement('p');
  hint.className = 'memory-panel-hint';
  hint.textContent = '这些是 Luna 会在之后的对话里用到的事实。撤回会把一条移出生效集合，记录仍留在本地文件里；要让它重新生效，只能由模型再记录一次。';

  // 生效中：当前真的会进入对话的那些事实，每条都能就地撤回。这是面板的主体。
  const activeHeading = document.createElement('h3');
  activeHeading.className = 'memory-panel-heading memory-panel-heading-active';
  const activeList = document.createElement('ul');
  activeList.className = 'memory-panel-list';
  const activeEmpty = document.createElement('p');
  activeEmpty.className = 'memory-panel-empty';
  const activeGroup = document.createElement('section');
  activeGroup.className = 'memory-panel-group';
  activeGroup.append(activeHeading, activeList, activeEmpty);

  // 已撤回：还在文件里、但不再生效的那些。默认只占一行摘要，点开才列出；
  // 没有撤回记录时整组不出现，所以第一次使用的面板就是一个安静的初始态。
  // 展开状态只活在这一次挂载里（state 随容器建、随容器销毁），所以它不会
  // 被持久化成某种"用户偏好"，也不会在重新打开面板时莫名其妙地记着。
  const goneHeading = document.createElement('h3');
  goneHeading.className = 'memory-panel-heading';
  const goneToggle = document.createElement('button');
  goneToggle.type = 'button';
  goneToggle.className = 'memory-panel-toggle';
  goneToggle.setAttribute('aria-expanded', 'false');
  goneToggle.setAttribute('aria-controls', `luna-memory-retracted-${(mountSeq += 1)}`);
  goneHeading.append(goneToggle);
  const goneList = document.createElement('ul');
  goneList.className = 'memory-panel-list memory-panel-retracted';
  goneList.id = goneToggle.getAttribute('aria-controls');
  goneList.hidden = true;
  const goneGroup = document.createElement('section');
  goneGroup.className = 'memory-panel-group memory-panel-group-retracted';
  goneGroup.append(goneHeading, goneList);

  const status = document.createElement('p');
  status.className = 'memory-panel-status';
  status.setAttribute('role', 'status');

  target.append(hint, activeGroup, goneGroup, status);

  const state = {
    link,
    activeHeading,
    activeList,
    activeEmpty,
    goneToggle,
    goneList,
    goneGroup,
    status,
    facts: [],
    retracted: [],
    // 展开状态是按挂载算的视图状态，不是数据：draw 每次重绘都读它，但从不写它。
    goneOpen: false,
    busy: false,
    pending: null,
    controller: null,
  };
  states.set(target, state);
  draw(state);

  // 展开与收起只改这一段的可访问状态和可见性：不重画条目，也就不发请求，
  // 点开已撤回不会让面板闪一下。
  goneToggle.addEventListener('click', () => {
    state.goneOpen = !state.goneOpen;
    syncGone(state);
  });

  load(state);
}

export function unmount(target) {
  const state = states.get(target);
  if (!state) return;
  states.delete(target);
  if (state.controller) state.controller.abort();
  if (state.pending) clearTimeout(state.pending);
  // The stylesheet travels with the module, so it leaves with it; the host still
  // removes the container it created.
  state.link.remove();
  target.replaceChildren();
}

function load(state) {
  if (state.controller) state.controller.abort();
  state.controller = new AbortController();
  const signal = state.controller.signal;
  return fetch(FACTS_URL, { headers: { accept: 'application/json' }, signal })
    .then((response) => response.json().then((payload) => ({ response, payload })))
    .then(({ response, payload }) => {
      if (!response.ok) throw new Error(errorText(payload) || `读取失败（${response.status}）`);
      state.facts = Array.isArray(payload.facts) ? payload.facts : [];
      state.retracted = Array.isArray(payload.retracted) ? payload.retracted : [];
      draw(state);
    })
    .catch((error) => {
      if (error && error.name === 'AbortError') return;
      setStatus(state, errorMessage(error), 'failure');
    });
}

function draw(state) {
  // 生效中
  state.activeList.replaceChildren();
  state.facts.forEach((fact, index) => state.activeList.append(factNode(state, fact, index)));
  state.activeHeading.textContent = `Luna 记得的 · ${state.facts.length} 条`;
  state.activeEmpty.hidden = state.facts.length > 0;
  state.activeEmpty.textContent =
    state.retracted.length > 0
      ? '没有生效中的记忆：都已经撤回。展开下面的「已撤回」可以看到它们。'
      : '还没有记录任何事实。Luna 在对话里记下一条时（它调用 luna_remember），那条事实会出现在这里。';

  // 已撤回：一行摘要加一组默认收起的条目，展开状态由 state.goneOpen 决定。
  state.goneList.replaceChildren();
  state.retracted.forEach((entry) => state.goneList.append(retractedNode(entry)));
  state.goneGroup.hidden = state.retracted.length === 0;
  state.goneToggle.textContent = `已撤回 · ${state.retracted.length} 条`;
  syncGone(state);
}

// syncGone is the whole of the collapse behaviour: the button says whether the
// list is showing, and the list follows. It is deliberately not part of draw —
// toggling must not rebuild rows or touch the network.
function syncGone(state) {
  state.goneToggle.setAttribute('aria-expanded', state.goneOpen ? 'true' : 'false');
  state.goneList.hidden = !state.goneOpen;
}

function factNode(state, fact, index) {
  const item = document.createElement('li');
  item.className = 'memory-panel-item';

  const text = document.createElement('p');
  text.className = 'memory-panel-text';
  text.textContent = textOf(fact.text);
  item.append(text);

  const meta = document.createElement('p');
  meta.className = 'memory-panel-meta';
  meta.textContent = joinMeta([
    textOf(fact.source_session) ? `来自会话 ${textOf(fact.source_session)}` : '',
    `记录于 ${whenOf(fact.at)}`,
  ]);

  // 这一条能做什么：只有生效中的记忆有可点的动作；已撤回的没有。按钮和来源、
  // 时间同处一行，读起来是这张卡片的脚注而不是主体。
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'memory-panel-retract luna-button';
  button.textContent = '撤回';
  // 每行的按钮文字都一样，读屏时它们会变成一串"撤回"；标签把这条事实带上。
  button.setAttribute('aria-label', `撤回这条记忆${labelOf(fact.text)}`);
  button.disabled = state.busy;
  button.addEventListener('click', () => retract(state, index));

  const foot = document.createElement('div');
  foot.className = 'memory-panel-foot';
  foot.append(meta, button);
  item.append(foot);

  return item;
}

// A retracted entry carries the fact and when it was retracted, and nothing
// else: /api/memory does not name a source session for one. What is not on the
// wire is not shown, so the line says what actually happened to it.
function retractedNode(entry) {
  const item = document.createElement('li');
  item.className = 'memory-panel-item memory-panel-item-retracted';

  const text = document.createElement('p');
  text.className = 'memory-panel-text';
  text.textContent = textOf(entry.text);
  item.append(text);

  const meta = document.createElement('p');
  meta.className = 'memory-panel-meta';
  meta.textContent = joinMeta([`记录于 ${whenOf(entry.at)}`, `撤回于 ${whenOf(entry.retracted_at)}`]);
  item.append(meta);

  return item;
}

function retract(state, index) {
  const fact = state.facts[index];
  if (!fact || state.busy) return;
  state.busy = true;
  draw(state);
  setStatus(state, '正在撤回…', '');
  fetch(RETRACT_URL, {
    method: 'POST',
    headers: { 'content-type': 'application/json', accept: 'application/json' },
    body: JSON.stringify({ at: fact.at, text: fact.text }),
  })
    .then((response) => response.json().then((payload) => ({ response, payload })).catch(() => ({ response, payload: {} })))
    .then(({ response, payload }) => {
      state.busy = false;
      if (!response.ok) {
        setStatus(state, errorText(payload) || `撤回失败（${response.status}）`, 'failure');
        draw(state);
        return;
      }
      // 成功的证据是这条事实真的移到了「已撤回」，不再另写一条状态文案。
      setStatus(state, '', '');
      load(state);
    })
    .catch((error) => {
      state.busy = false;
      setStatus(state, errorMessage(error), 'failure');
      draw(state);
    });
}

// setStatus carries exactly one thing at a time. A failure stays until the next
// operation or an explicit empty status; feedback for an operation in flight
// takes itself away, and cannot push a failure out, because the timer that
// would clear the line is dropped when someone else writes to it.
function setStatus(state, text, className) {
  if (state.pending !== null) {
    clearTimeout(state.pending);
    state.pending = null;
  }
  state.status.textContent = text;
  state.status.className = `memory-panel-status${className ? ` ${className}` : ''}`;
  if (!text || className) return;
  state.pending = setTimeout(() => {
    state.pending = null;
    state.status.textContent = '';
    state.status.className = 'memory-panel-status';
  }, STATUS_LINGER);
}

function joinMeta(parts) {
  return parts.filter(Boolean).join(' · ');
}

function textOf(value) {
  return typeof value === 'string' ? value : '';
}

// labelOf turns a fact into the tail of a retract button's label: enough to
// tell two rows apart, short enough not to read out a whole paragraph.
function labelOf(value) {
  const text = textOf(value).trim().replace(/\s+/g, ' ');
  if (!text) return '';
  return `：${text.length > 40 ? `${text.slice(0, 40)}…` : text}`;
}

function whenOf(value) {
  const at = new Date(textOf(value));
  return Number.isNaN(at.getTime()) ? '' : at.toLocaleString();
}

function errorText(payload) {
  return payload && typeof payload.error === 'string' ? payload.error : '';
}

function errorMessage(error) {
  return error && typeof error.message === 'string' ? error.message : '读取失败';
}
