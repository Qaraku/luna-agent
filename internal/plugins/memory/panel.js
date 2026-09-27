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
// need. What it shows is the two halves of the store that actually differ for a
// reader — the facts in effect and the facts that were retracted — with where
// each one came from and when, plus the one thing the reader can do about it.
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
  hint.textContent = '这些是 Luna 正在使用的记忆。撤回会把一条事实移出生效集合，记录仍留在本地文件里；要让它重新生效，只能由模型再记录一次。';

  // 生效中：当前真的会进入对话的那些事实，每条都能就地撤回。
  const activeHeading = document.createElement('h3');
  activeHeading.className = 'memory-panel-heading';
  const activeList = document.createElement('ul');
  activeList.className = 'memory-panel-list';
  const activeEmpty = document.createElement('p');
  activeEmpty.className = 'memory-panel-empty';
  const activeGroup = document.createElement('section');
  activeGroup.className = 'memory-panel-group';
  activeGroup.append(activeHeading, activeList, activeEmpty);

  // 已撤回：还在文件里、但不再生效的那些。整组在没有撤回记录时不出现，
  // 所以第一次使用的面板就是一个安静的初始态。
  const goneHeading = document.createElement('h3');
  goneHeading.className = 'memory-panel-heading';
  const goneList = document.createElement('ul');
  goneList.className = 'memory-panel-retracted';
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
    goneHeading,
    goneList,
    goneGroup,
    status,
    facts: [],
    retracted: [],
    busy: false,
    pending: null,
    controller: null,
  };
  states.set(target, state);
  draw(state);

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
  state.activeHeading.textContent = `生效中 · ${state.facts.length} 条`;
  state.activeEmpty.hidden = state.facts.length > 0;
  state.activeEmpty.textContent = state.retracted.length > 0 ? '没有生效中的记忆。' : '还没有记录任何事实。';

  // 已撤回
  state.goneList.replaceChildren();
  state.retracted.forEach((entry) => state.goneList.append(retractedNode(entry)));
  state.goneHeading.textContent = `已撤回 · ${state.retracted.length} 条`;
  state.goneGroup.hidden = state.retracted.length === 0;
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
  item.append(meta);

  // 这一条能做什么：只有生效中的记忆有可点的动作；已撤回的没有。
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'memory-panel-retract luna-button';
  button.textContent = '撤回';
  button.disabled = state.busy;
  button.addEventListener('click', () => retract(state, index));
  item.append(button);

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
