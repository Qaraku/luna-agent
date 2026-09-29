// 预设定义与修订由能力拥有；宿主只提供面板容器。
const states = new WeakMap();
const STYLE = new URL('panel.css', import.meta.url).href;
const PREFIX = '/api/presets';
const make = (tag, cls = '', text = '') => { const n = document.createElement(tag); n.className = cls; if (text) n.textContent = text; return n; };
const mark = (node, key, value) => { node.setAttribute('data-' + key, value); return node; };
const button = (label, action, fn) => { const n = mark(make('button', 'luna-button', label), 'action', action); n.type = 'button'; n.addEventListener('click', fn); return n; };
const copy = value => JSON.parse(JSON.stringify(value));

export function mount(target) {
  const link = make('link'); link.rel = 'stylesheet'; link.href = STYLE; document.head.append(link);
  const status = mark(make('p', 'luna-muted'), 'field', 'status'); status.setAttribute('role', 'status');
  const toolbar = make('div', 'preset-actions');
  const list = make('div', 'preset-list');
  const editor = mark(make('form', 'preset-editor'), 'action', 'editor'); editor.hidden = true;
  const detail = make('section', 'preset-history'); detail.hidden = true;
  const state = { target, link, status, list, editor, detail, entries: [], capabilities: [], skills: [], models: [], levels: [], live: true, busy: false, controller: null, timer: null, fields: {}, groups: {}, editing: null, pendingArchive: '' };
  states.set(target, state);
  toolbar.append(button('新建预设', 'new', () => edit(state, null, false)), button('刷新目录', 'refresh', () => perform(state, () => load(state))));
  target.append(make('p', 'luna-muted', '预设组合工作方式与资源选择，不授予权限。内置预设可复制；修改不会自动改变已经绑定旧版本的会话。'), toolbar, status, list, editor, detail);
  editor.addEventListener('submit', event => { event.preventDefault(); save(state); });
  perform(state, () => load(state));
}

export function unmount(target) {
  const state = states.get(target); if (!state) return;
  state.live = false; state.controller?.abort(); clearTimeout(state.timer); state.link.remove(); states.delete(target);
}

async function request(state, url, body) {
  const controller = state.controller;
  const response = await fetch(url, { cache: 'no-store', signal: controller?.signal, ...(body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) });
  const value = await response.json();
  if (!response.ok) throw new Error(value.error || '请求失败：' + response.status);
  if (!state.live || controller?.signal.aborted) throw new Error('请求已取消');
  return value;
}
async function perform(state, work) {
  if (!state.live || state.busy) return;
  state.busy = true; const controller = new AbortController(); state.controller = controller; state.editor.inert = true;
  state.timer = setTimeout(() => controller.abort(), 15000);
  state.status.textContent = '正在处理…';
  try { await work(); if (state.live) state.status.textContent = ''; }
  catch (error) { const timedOut = controller.signal.aborted; controller.abort(); if (state.live) state.status.textContent = timedOut ? '请求超时，请重试。' : error.message; }
  finally { clearTimeout(state.timer); if (state.controller === controller) state.controller = null; state.busy = false; state.editor.inert = false; }
}
async function load(state) {
  const [catalog, runtime, skills, models, reasoning] = await Promise.all([
    request(state, PREFIX + '?archived=true'), request(state, '/api/state'), request(state, '/api/skills'), request(state, '/api/models'), request(state, '/api/reasoning')
  ]);
  if (!state.live) return;
  state.entries = Array.isArray(catalog.presets) ? catalog.presets : [];
  state.capabilities = Array.isArray(runtime.capabilities) ? runtime.capabilities : [];
  state.skills = Array.isArray(skills.skills) ? skills.skills : [];
  state.models = Array.isArray(models.models) ? models.models : [];
  state.levels = Array.isArray(reasoning.levels) ? reasoning.levels : [];
  draw(state);
}
function draw(state) {
  if (!state.live) return;
  state.list.replaceChildren();
  for (const entry of state.entries) {
    const row = make('article', 'preset-row');
    row.append(make('h3', '', entry.title || entry.id), make('p', 'luna-muted', entry.id + ' · ' + (entry.builtin ? '内置 · 只读' : '自定义') + (entry.archived ? ' · 已归档' : '') + ' · ' + (entry.revision || '').slice(0, 12)));
    const actions = make('div', 'preset-actions');
    if (!entry.builtin && !entry.archived) actions.append(button('编辑', 'edit:' + entry.id, () => edit(state, entry, false)));
    actions.append(button('复制', 'copy:' + entry.id, () => edit(state, entry, true)), button('修订历史', 'history:' + entry.id, () => history(state, entry)), button('导出定义', 'export:' + entry.id, () => exportEntry(state, entry)));
    if (!entry.builtin && !entry.archived) {
      const archive = button('归档', 'archive:' + entry.id, () => {
        if (state.busy) return;
        if (state.pendingArchive !== entry.id) { state.pendingArchive = entry.id; archive.textContent = '确认归档（不删除历史）'; return; }
        perform(state, async () => { await request(state, PREFIX + '/archive', { id: entry.id, expected_revision: entry.revision }); state.pendingArchive = ''; await load(state); });
      });
      actions.append(archive);
    }
    row.append(actions); state.list.append(row);
  }
}
function field(state, name, title, kind = 'input') {
  const label = make('label', 'preset-field'); const input = mark(make(kind, 'luna-input'), 'field', name);
  label.append(make('span', '', title), input); state.editor.append(label); state.fields[name] = input; return input;
}
function resourceGroup(state, key, title, values, choices) {
  const box = make('fieldset', 'preset-resource-group'); box.append(make('legend', '', title));
  const inherited = mark(make('input'), 'field', 'inherit-' + key); inherited.type = 'checkbox'; inherited.checked = values == null;
  const label = make('label', 'preset-check'); label.append(inherited, make('span', '', '继承已启用资源')); box.append(label);
  const rows = new Map(choices.map(item => [item.id, item]));
  for (const id of values || []) if (!rows.has(id)) rows.set(id, { id, title: id + '（当前不可用）' });
  const checks = [];
  for (const item of rows.values()) {
    const input = mark(make('input'), 'field', (key === 'capabilities' ? 'capability:' : 'skill:') + item.id); input.type = 'checkbox'; input.checked = (values || []).includes(item.id);
    const row = make('label', 'preset-check'); row.append(input, make('span', '', item.title)); box.append(row); checks.push({ id: item.id, input });
  }
  const refresh = () => { for (const { input } of checks) input.disabled = inherited.checked; };
  inherited.addEventListener('change', refresh); refresh();
  state.groups[key] = { inherited, checks }; state.editor.append(box);
}
function edit(state, entry, duplicate) {
  if (!state.live || state.busy) return;
  state.editing = entry && !duplicate ? copy(entry) : null;
  state.source = entry ? copy(entry) : { resources: {} };
  state.fields = {}; state.groups = {}; state.editor.replaceChildren(); state.editor.hidden = false; state.detail.hidden = true;
  state.editor.append(make('h3', '', state.editing ? '编辑预设' : '新建预设'));
  const id = field(state, 'id', '标识（小写字母、数字与连字符）'); id.value = duplicate ? entry.id.slice(0, 40) + '-copy' : entry?.id || ''; id.required = true; id.maxLength = 48; id.disabled = Boolean(state.editing);
  const title = field(state, 'title', '名称'); title.value = entry?.title || ''; title.maxLength = 120;
  const instructions = field(state, 'instructions', '工作方式', 'textarea'); instructions.value = entry?.instructions || ''; instructions.rows = 8;
  const model = field(state, 'model', '模型偏好（留空继承全局）', 'select'); model.append(option('', '继承全局'));
  const names = new Set(state.models.map(item => item.name)); if (entry?.model) names.add(entry.model); for (const name of names) model.append(option(name, name)); model.value = entry?.model || '';
  const reasoning = field(state, 'reasoning', '思考档位偏好', 'select'); reasoning.append(option('__inherit', '继承全局'), option('__off', '不发送思考字段'));
  for (const level of state.levels) reasoning.append(option(level, level)); reasoning.value = entry?.reasoning_effort == null ? '__inherit' : entry.reasoning_effort || '__off';
  resourceGroup(state, 'capabilities', '能力选择（不会自动启用）', entry?.capabilities, state.capabilities.map(item => ({ id: item.id, title: (item.title || item.id) + (item.state === 'enabled' ? '' : ' · 未启用') })));
  resourceGroup(state, 'skills', '技能选择', entry?.resources?.skills, state.skills.map(item => ({ id: item.name, title: item.name + (item.enabled ? '' : ' · 已停用') })));
  const toolGroup = make('label', 'preset-check'); const inheritTools = mark(make('input'), 'field', 'inherit-tools'); inheritTools.type = 'checkbox'; inheritTools.checked = entry?.tools == null; toolGroup.append(inheritTools, make('span', '', '继承已选能力提供的工具')); state.editor.append(toolGroup); state.fields.inheritTools = inheritTools;
  const tools = field(state, 'tools', '工具白名单（逗号分隔；不继承且留空表示不选工具）'); tools.value = (entry?.tools || []).join(', '); tools.disabled = inheritTools.checked; inheritTools.addEventListener('change', () => { tools.disabled = inheritTools.checked; });
  const actions = make('div', 'preset-actions'); const saveButton = button('保存新修订', 'save', () => {}); saveButton.type = 'submit';
  actions.append(saveButton, button('取消编辑', 'cancel', () => { if (!state.busy) state.editor.hidden = true; })); state.editor.append(actions); instructions.focus();
}
function option(value, title) { const n = make('option', '', title); n.value = value; return n; }
function resourceValue(group) { return group.inherited.checked ? null : group.checks.filter(item => item.input.checked).map(item => item.id); }
function save(state) {
  if (!state.live || state.busy) return;
  const fields = state.fields;
  const definition = { id: fields.id.value.trim(), title: fields.title.value.trim(), instructions: fields.instructions.value, model: fields.model.value, capabilities: resourceValue(state.groups.capabilities), tools: fields.inheritTools.checked ? null : fields.tools.value.split(',').map(x => x.trim()).filter(Boolean), resources: { ...(state.source.resources || {}), skills: resourceValue(state.groups.skills) } };
  if (fields.reasoning.value !== '__inherit') definition.reasoning_effort = fields.reasoning.value === '__off' ? '' : fields.reasoning.value;
  perform(state, async () => {
    await request(state, PREFIX + '/save', { definition, expected_revision: state.editing?.revision || '' });
    if (!state.live) return; state.editor.hidden = true; await load(state);
  });
}
function history(state, entry) {
  perform(state, async () => {
    const payload = await request(state, PREFIX + '/history?id=' + encodeURIComponent(entry.id));
    if (!state.live) return;
    state.detail.replaceChildren(make('h3', '', '修订历史 · ' + entry.id)); state.detail.hidden = false;
    for (const revision of payload.revisions || []) {
      const row = make('article', 'preset-row'); row.append(make('p', 'luna-muted', (revision.revision || '').slice(0, 12) + (revision.archived ? ' · 已归档' : '')));
      const details = make('details'); details.append(make('summary', '', '查看这一版本的定义'), make('pre', 'preset-definition', JSON.stringify(revision, null, 2))); row.append(details);
      if (!entry.builtin && !revision.archived && (revision.revision !== entry.revision || entry.archived)) row.append(button('恢复为新修订', 'restore:' + revision.revision, () => perform(state, async () => {
        await request(state, PREFIX + '/restore', { id: entry.id, revision: revision.revision, expected_revision: entry.revision }); state.detail.hidden = true; await load(state);
      })));
      state.detail.append(row);
    }
  });
}
function exportEntry(state, entry) {
  perform(state, async () => {
    const definition = await request(state, PREFIX + '/export?id=' + encodeURIComponent(entry.id) + '&revision=' + encodeURIComponent(entry.revision));
    if (!state.live) return;
    const text = make('textarea', 'luna-input preset-export'); text.readOnly = true; text.rows = 12; text.value = JSON.stringify(definition, null, 2); text.setAttribute('aria-label', '可分享的预设定义');
    state.detail.replaceChildren(make('h3', '', '导出定义'), make('p', 'luna-muted', '不包含会话或授权。分享前请检查自己写入的指令是否包含私人内容。'), text); state.detail.hidden = false;
  });
}
