// Memory 能力拥有的用户管理面板：范围、来源、纠错与恢复。
// 宿主只负责容器与生命周期；所有文本通过 DOM 文本接口渲染。
// 请求在切换范围或卸载时取消，编辑失败不丢弃输入。

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
  hint.textContent = '这里是你的记忆管理视图。模型只会读取全局与当前项目的记忆，并受权限和上下文预算限制；纠错与恢复会保留历史，不改变原条目的范围。';

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
    target, live: true, scopeValue: 'all', renderedScope: 'all', stale: false, loading: false, changes: [], scopes: [], workspaceNames: new Map(), editing: null, workspaceController: null, workspaceTimer: null,
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
  const management = buildMemoryManagement(state);
  target.replaceChildren(hint, management, state.editForm, state.restoreBox, activeGroup, goneGroup, state.historyGroup, state.exportBox, status);
  draw(state);
  loadMemoryWorkspaces(state);

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
  state.live = false;
  state.workspaceController?.abort();
  clearTimeout(state.workspaceTimer);
  clearTimeout(state.requestTimer);
  if (state.controller) state.controller.abort();
  if (state.pending) clearTimeout(state.pending);
  // The stylesheet travels with the module, so it leaves with it; the host still
  // removes the container it created.
  state.link.remove();
  target.replaceChildren();
}

function memoryURL(state, path = FACTS_URL) {
  if (state.scopeValue === 'all') return path;
  if (state.scopeValue === 'global') return path + '?scope=global';
  return path + '?scope=project&workspace=' + encodeURIComponent(state.scopeValue.slice(8));
}
async function load(state) {
  if (!state.live || state.busy) return;
  state.controller?.abort(); clearTimeout(state.requestTimer); const controller = new AbortController(); state.controller = controller;
  state.loading = true; state.stale = state.scopeValue !== state.renderedScope; const scope = state.scopeValue;
  const timer = setTimeout(() => controller.abort(), 15000); state.requestTimer = timer; draw(state);
  try {
    const response = await fetch(memoryURL(state), { headers: { accept: 'application/json' }, signal: controller.signal }); const payload = await response.json();
    if (!state.live || controller !== state.controller || controller.signal.aborted) return;
    if (!response.ok) throw new Error(errorText(payload) || '读取失败（' + response.status + '）');
    state.facts = Array.isArray(payload.facts) ? payload.facts : []; state.retracted = Array.isArray(payload.retracted) ? payload.retracted : [];
    state.changes = Array.isArray(payload.changes) ? payload.changes : []; state.scopes = Array.isArray(payload.scopes) ? payload.scopes : [];
    state.renderedScope = scope; state.stale = false; setStatus(state, '', '');
  } catch (error) { if (state.live && controller === state.controller) setStatus(state, controller.signal.aborted ? '读取超时，请重试。' : errorMessage(error) + (state.stale ? '；仍显示上一次范围，暂不能修改。' : ''), 'failure'); }
  finally { clearTimeout(timer); if (state.live && controller === state.controller) { state.controller = null; state.loading = false; draw(state); } }
}

function draw(state) {
  if (!state.live) return;
  // 生效中
  state.activeList.replaceChildren();
  state.facts.forEach((fact, index) => state.activeList.append(factNode(state, fact, index)));
  state.activeHeading.textContent = `生效中 · ${state.facts.length} 条`;
  state.activeEmpty.hidden = state.facts.length > 0;
  state.activeEmpty.textContent =
    state.retracted.length > 0
      ? '没有生效中的记忆：都已经撤回。展开下面的「已撤回」可以看到它们。'
      : '还没有记录任何事实。Luna 在对话里记下一条时（它调用 luna_remember），那条事实会出现在这里。';

  // 已撤回：一行摘要加一组默认收起的条目，展开状态由 state.goneOpen 决定。
  state.goneList.replaceChildren();
  state.retracted.forEach((entry) => state.goneList.append(retractedNode(state, entry)));
  state.goneGroup.hidden = state.retracted.length === 0;
  state.goneToggle.textContent = `已撤回 · ${state.retracted.length} 条`;
  syncGone(state);
  drawMemoryManagement(state);
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
    scopeLabel(state, fact),
    fact.origin === 'user' ? '用户记录或修订' : fact.origin === 'model' ? '模型记录' : '',
    textOf(fact.source_session) ? `来自会话 ${textOf(fact.source_session)}` : '',
    textOf(fact.source_run) ? '运行 ' + textOf(fact.source_run) : '',
    `记录于 ${whenOf(fact.at)}`,
  ]);

  // 操作与来源作为次级信息；撤回只影响生效状态，纠错保留历史。
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'memory-panel-retract luna-button';
  button.textContent = '撤回';
  // 每行的按钮文字都一样，读屏时它们会变成一串"撤回"；标签把这条事实带上。
  button.setAttribute('aria-label', `撤回这条记忆${labelOf(fact.text)}`);
  button.disabled = state.busy || state.loading || state.stale;
  button.addEventListener('click', () => retract(state, index));

  const foot = document.createElement('div');
  foot.className = 'memory-panel-foot';
  foot.append(meta, button);
  if (fact.ref) foot.append(memoryButton('纠错', 'memory-panel-correct', () => editMemory(state, fact), state));
  item.append(foot);

  return item;
}

// A retracted entry carries the fact and when it was retracted, and nothing
// else: /api/memory does not name a source session for one. What is not on the
// wire is not shown, so the line says what actually happened to it.
function retractedNode(state, entry) {
  const item = document.createElement('li');
  item.className = 'memory-panel-item memory-panel-item-retracted';

  const text = document.createElement('p');
  text.className = 'memory-panel-text';
  text.textContent = textOf(entry.text);
  item.append(text);

  const meta = document.createElement('p');
  meta.className = 'memory-panel-meta';
  meta.textContent = joinMeta([scopeLabel(state, entry), `记录于 ${whenOf(entry.at)}`, `撤回于 ${whenOf(entry.retracted_at)}`]);
  item.append(meta);
  if (entry.ref) item.append(memoryButton('恢复此内容', 'memory-history-restore', () => confirmMemoryRestore(state, entry), state));

  return item;
}

function retract(state, index) {
  const fact = state.facts[index]; if (!fact || state.busy || state.loading || state.stale) return;
  mutateMemory(state, RETRACT_URL, fact.ref ? { ref: fact.ref } : { at: fact.at, text: fact.text }, '正在撤回…');
}
async function mutateMemory(state, path, body, message = '正在保存…') {
  if (!state.live || state.busy || state.loading || state.stale) return;
  state.controller?.abort(); clearTimeout(state.requestTimer); const controller = new AbortController(); state.controller = controller; state.busy = true;
  const timer = setTimeout(() => controller.abort(), 15000); state.requestTimer = timer; draw(state); setStatus(state, message, '');
  try {
    const response = await fetch(path, { method: 'POST', headers: { 'content-type': 'application/json', accept: 'application/json' }, signal: controller.signal, body: JSON.stringify(body) });
    const payload = await response.json().catch(() => ({})); if (!state.live || controller !== state.controller) return;
    if (!response.ok) throw new Error(errorText(payload) || '保存失败（' + response.status + '）');
    state.editing = null; state.editForm.hidden = true; state.restoreBox.hidden = true;
    if (path === '/api/memory/add') state.addText.value = '';
    state.busy = false; setStatus(state, '', ''); await load(state); if (state.live) (path === '/api/memory/add' ? state.addText : state.scopeSelect).focus();
  } catch (error) { if (state.live && controller === state.controller) setStatus(state, controller.signal.aborted ? '操作超时，可能已保存；请刷新确认。' : errorMessage(error), 'failure'); }
  finally { clearTimeout(timer); if (state.live) { state.busy = false; if (controller === state.controller) state.controller = null; draw(state); } }
}

// setStatus carries exactly one thing at a time. A failure stays until the next
// operation or an explicit empty status; feedback for an operation in flight
// takes itself away, and cannot push a failure out, because the timer that
// would clear the line is dropped when someone else writes to it.
function setStatus(state, text, className) {
  if (!state.live) return;
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

function memoryNode(tag, className = '', text = '') { const node = document.createElement(tag); node.className = className; if (text) node.textContent = text; return node; }
function memoryButton(text, className, callback, state) { const button = memoryNode('button', className + ' luna-button', text); button.type = 'button'; button.disabled = state.busy || state.loading || state.stale; button.addEventListener('click', callback); return button; }
function scopeLabel(state, fact) { if (!fact.scope) return ''; return fact.scope === 'global' ? '全局' : '项目：' + (state.workspaceNames.get(fact.workspace_id) || fact.workspace_id); }
function buildMemoryManagement(state) {
  const area = memoryNode('section', 'memory-management');
  const label = memoryNode('label', 'memory-scope-label', '查看范围'); const select = memoryNode('select', 'memory-scope-select luna-input'); select.setAttribute('aria-label', '记忆范围'); select.value = 'all'; state.scopeSelect = select;
  label.append(select); area.append(label, memoryButton('刷新', 'memory-refresh', () => load(state), state), memoryButton('导出当前范围', 'memory-export', () => exportMemory(state), state));
  select.addEventListener('change', () => { if (state.busy || state.editing) return; state.scopeValue = select.value; load(state); });
  const add = memoryNode('form', 'memory-add-form'); const text = memoryNode('textarea', 'memory-add-text luna-input'); text.rows = 2; text.maxLength = 500; text.value = ''; text.setAttribute('aria-label', '新记忆内容'); text.placeholder = '添加一条长期有效的偏好或项目知识'; state.addText = text;
  const addButton = memoryNode('button', 'memory-add-button luna-button'); addButton.type = 'submit'; state.addButton = addButton; add.append(text, addButton); area.append(add);
  add.addEventListener('submit', event => { event.preventDefault(); const project = state.scopeValue.startsWith('project:'); mutateMemory(state, '/api/memory/add', { text: text.value, scope: project ? 'project' : 'global', ...(project ? { workspace_id: state.scopeValue.slice(8) } : {}) }); });
  const editor = memoryNode('form', 'memory-edit-form'); editor.hidden = true; state.editForm = editor;
  state.editBefore = memoryNode('p', 'memory-edit-before'); state.editText = memoryNode('textarea', 'memory-edit-text luna-input'); state.editText.rows = 3; state.editText.maxLength = 500; state.editText.setAttribute('aria-label', '纠正后的记忆内容');
  const save = memoryNode('button', 'memory-edit-save luna-button', '保存纠错（保留原文）'); save.type = 'submit';
  editor.append(state.editBefore, state.editText, save, memoryButton('取消', 'memory-edit-cancel', () => { state.editing = null; editor.hidden = true; drawMemoryManagement(state); }, state));
  editor.addEventListener('submit', event => { event.preventDefault(); if (state.editing) mutateMemory(state, '/api/memory/correct', { ref: state.editing.ref, text: state.editText.value, reason: '用户纠错' }); });
  state.restoreBox = memoryNode('div', 'memory-restore-box'); state.restoreBox.hidden = true;
  state.historyGroup = memoryNode('details', 'memory-history-group'); state.historyGroup.hidden = true;
  state.exportBox = memoryNode('section', 'memory-export-box'); state.exportBox.hidden = true;
  return area;
}
function drawMemoryManagement(state) {
  if (!state.scopeSelect || !state.live) return;
  const options = new Map([['all', '全部记忆（用户视图）'], ['global', '全局偏好']]);
  for (const [id, name] of state.workspaceNames) options.set('project:' + id, '项目：' + name);
  for (const item of state.scopes) if (item.scope === 'project' && !options.has('project:' + item.workspace_id)) options.set('project:' + item.workspace_id, '项目：' + item.workspace_id);
  if (!options.has(state.scopeValue)) options.set(state.scopeValue, state.scopeValue);
  const key = JSON.stringify([...options]); if (key !== state.scopeOptionsKey) { state.scopeOptionsKey = key; state.scopeSelect.replaceChildren(); for (const [value, label] of options) { const option = memoryNode('option', '', label); option.value = value; state.scopeSelect.append(option); } state.scopeSelect.value = state.scopeValue; }
  state.scopeSelect.disabled = state.busy || Boolean(state.editing); state.addButton.disabled = state.busy || state.loading || state.stale; state.addButton.textContent = state.scopeValue.startsWith('project:') ? '添加项目记忆' : '添加全局记忆'; state.editForm.inert = state.busy || state.loading;
  state.historyGroup.replaceChildren(memoryNode('summary', '', '纠错与恢复历史 · ' + state.changes.length)); state.historyGroup.hidden = state.changes.length === 0;
  for (const change of state.changes) { const before = change.before || {}, after = change.after || {}; const row = memoryNode('div', 'memory-history-row'); row.append(memoryNode('p', 'memory-panel-meta', joinMeta([scopeLabel(state, after), change.origin?.kind === 'model' ? '模型修订' : '用户修订', before.source_session ? '原来源会话 ' + before.source_session : '', change.origin?.session_id ? '修订会话 ' + change.origin.session_id : '', whenOf(after.at)])), memoryNode('p', '', '原内容：' + textOf(before.text)), memoryNode('p', '', '新内容：' + textOf(after.text))); if (change.origin?.reason) row.append(memoryNode('p', 'memory-panel-meta', change.origin.reason)); if (before.ref) row.append(memoryButton('恢复原内容', 'memory-history-restore', () => confirmMemoryRestore(state, before), state)); state.historyGroup.append(row); }
}
function editMemory(state, fact) { if (state.busy || state.loading || state.stale) return; state.editing = { ...fact }; state.editBefore.textContent = joinMeta([scopeLabel(state, fact), '原内容：' + textOf(fact.text)]); state.editText.value = textOf(fact.text); state.editForm.hidden = false; state.restoreBox.hidden = true; drawMemoryManagement(state); state.editText.focus(); }
function confirmMemoryRestore(state, source) {
  if (state.busy || state.loading || state.stale) return;
  const current = state.facts.find(fact => fact.lineage === source.lineage); if (current?.ref === source.ref) { setStatus(state, '这已经是当前生效内容。', ''); return; }
  state.restoreBox.replaceChildren(memoryNode('p', '', '恢复为：' + textOf(source.text)), memoryButton('确认恢复（生成新记录）', 'memory-restore-confirm', () => mutateMemory(state, '/api/memory/restore', { ref: source.ref, expected_ref: current?.ref || '', reason: '用户恢复历史内容' }), state), memoryButton('取消', 'memory-restore-cancel', () => { state.restoreBox.hidden = true; }, state)); state.restoreBox.hidden = false;
}
async function loadMemoryWorkspaces(state) {
  const controller = new AbortController(); state.workspaceController = controller; state.workspaceTimer = setTimeout(() => controller.abort(), 15000);
  try { const response = await fetch('/api/workspaces', { signal: controller.signal }); if (!response.ok) return; const payload = await response.json(); if (!state.live || controller.signal.aborted) return; state.workspaceNames = new Map((payload.workspaces || []).map(item => [item.id, item.name || item.id])); drawMemoryManagement(state); }
  catch (_) { /* 项目名称不可用时仍显示已有项目标识，不影响记忆内容。 */ }
  finally { clearTimeout(state.workspaceTimer); }
}
async function exportMemory(state) {
  if (state.busy || state.loading || state.stale) return;
  state.exportBox.replaceChildren(memoryNode('p', 'memory-panel-meta', '导出内容包含私人记忆，分享前请自行检查。')); const data = { scope: state.scopeValue, facts: state.facts, retracted: state.retracted, changes: state.changes }; const text = memoryNode('textarea', 'memory-export-text luna-input'); text.rows = 12; text.readOnly = true; text.value = JSON.stringify(data, null, 2); text.setAttribute('aria-label', '当前记忆范围导出'); state.exportBox.append(text); state.exportBox.hidden = false;
}
