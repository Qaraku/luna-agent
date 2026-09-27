// The Memory panel, as the capability's own browser module.
//
// The host owns the panel itself — the header entry, the overlay, the title and
// the close control — and hands this module a container to mount into. What the
// panel *says* belongs here: the hint line, the rows, the retract call and the
// wording around all of it. That is what makes disabling the capability take the
// panel's content away with the route it talks to, instead of leaving a host
// panel that renders nothing.
//
// It uses the same mount(target, api) / unmount(target) contract as every other
// browser module, and the host's own classes for the shared look; its layout
// rules travel with it so the host stylesheet has no memory-specific rules.

const FACTS_URL = '/api/memory';
const RETRACT_URL = '/api/memory/retract';

const STYLE = `
.memory-panel-hint { margin: 0 0 12px; }
.memory-panel-list { list-style: none; margin: 0; padding: 0; display: grid; gap: 8px; }
.memory-panel-item { display: grid; gap: 4px; padding: 10px 12px; border: 1px solid var(--luna-border-weak); border-radius: var(--luna-radius-6); background: var(--luna-surface); }
.memory-panel-text { margin: 0; overflow-wrap: anywhere; }
.memory-panel-meta { margin: 0; font-size: var(--luna-font-12); color: var(--luna-text-muted); }
.memory-panel-retract { justify-self: start; }
.memory-panel-empty, .memory-panel-status { margin: 8px 0 0; font-size: var(--luna-font-12); color: var(--luna-text-muted); }
`;

// One mount's state. A module instance lives in the container the host created,
// so the state is keyed by that container and released on unmount.
const states = new WeakMap();

export function mount(target, api) {
  const log = api && typeof api.log === 'function' ? api.log : () => {};
  const style = document.createElement('style');
  style.textContent = STYLE;
  document.head.append(style);

  const hint = document.createElement('p');
  hint.className = 'memory-panel-hint';
  hint.textContent = '这里只能撤回，不能编辑或新增；撤回只把事实移出生效集合，记录仍在本地文件里。';

  const list = document.createElement('ul');
  list.className = 'memory-panel-list';

  const empty = document.createElement('p');
  empty.className = 'memory-panel-empty';
  empty.textContent = '还没有记录任何事实。';
  empty.hidden = true;

  const status = document.createElement('p');
  status.className = 'memory-panel-status';
  status.setAttribute('role', 'status');

  target.append(hint, list, empty, status);

  const state = {
    style,
    list,
    empty,
    status,
    facts: [],
    busy: false,
    pending: null,
    controller: null,
  };
  states.set(target, state);
  render(status, 0, []);

  load(state);
  log('记忆面板已挂载');
}

export function unmount(target) {
  const state = states.get(target);
  if (!state) return;
  states.delete(target);
  if (state.controller) state.controller.abort();
  if (state.pending) clearTimeout(state.pending);
  // The stylesheet travels with the module, so it leaves with it; the host still
  // removes the container it created.
  state.style.remove();
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
      const retracted = Array.isArray(payload.retracted) ? payload.retracted : [];
      draw(state);
      render(state.status, state.facts.length, retracted);
    })
    .catch((error) => {
      if (error && error.name === 'AbortError') return;
      state.status.textContent = errorMessage(error);
    });
}

function draw(state) {
  state.list.replaceChildren();
  state.empty.hidden = state.facts.length > 0;
  state.facts.forEach((fact, index) => {
    const item = document.createElement('li');
    item.className = 'memory-panel-item';

    const text = document.createElement('p');
    text.className = 'memory-panel-text';
    text.textContent = textOf(fact.text);
    item.append(text);

    const meta = document.createElement('p');
    meta.className = 'memory-panel-meta';
    meta.textContent = `${whenOf(fact.at)}${fact.source_session ? ` · 来自 ${textOf(fact.source_session)}` : ''}`;
    item.append(meta);

    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'memory-panel-retract luna-button';
    button.textContent = '撤回';
    button.disabled = state.busy;
    button.addEventListener('click', () => retract(state, index));
    item.append(button);

    state.list.append(item);
  });
}

function retract(state, index) {
  const fact = state.facts[index];
  if (!fact || state.busy) return;
  state.busy = true;
  draw(state);
  state.status.textContent = '正在撤回…';
  fetch(RETRACT_URL, {
    method: 'POST',
    headers: { 'content-type': 'application/json', accept: 'application/json' },
    body: JSON.stringify({ at: fact.at, text: fact.text }),
  })
    .then((response) => response.json().then((payload) => ({ response, payload })).catch(() => ({ response, payload: {} })))
    .then(({ response, payload }) => {
      state.busy = false;
      if (!response.ok) {
        state.status.textContent = errorText(payload) || `撤回失败（${response.status}）`;
        draw(state);
        return;
      }
      state.status.textContent = '已撤回一条；若还要恢复，只能由模型再次记录。';
      load(state);
    })
    .catch((error) => {
      state.busy = false;
      state.status.textContent = errorMessage(error);
      draw(state);
    });
}

function render(status, count, retracted) {
  const parts = [];
  if (count > 0) parts.push(`生效中 ${count} 条`);
  if (retracted.length > 0) parts.push(`已撤回 ${retracted.length} 条`);
  status.textContent = parts.join(' · ');
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
