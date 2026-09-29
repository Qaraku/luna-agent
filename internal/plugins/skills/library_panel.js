// 学习内容、来源和修订由 Skills 能力管理，不把全文堆进 composer。
const states = new WeakMap();
const BASE = '/api/skill-library';
const STYLE = new URL('panel.css', import.meta.url).href;
const make = (tag, cls = '', text = '') => { const n = document.createElement(tag); n.className = cls; if (text) n.textContent = text; return n; };
const mark = (n, kind, id) => { n.setAttribute('data-' + kind, id); return n; };
const button = (text, id, fn) => { const n = mark(make('button', 'luna-button', text), 'action', id); n.type = 'button'; n.addEventListener('click', fn); return n; };

export function mount(target) {
  const link = make('link'); link.rel = 'stylesheet'; link.href = STYLE; document.head.append(link);
  const toolbar = make('div', 'skill-library-actions'); const status = mark(make('p', 'luna-muted'), 'field', 'status'); status.setAttribute('role', 'status');
  const list = make('div', 'skill-library-list'); const editor = mark(make('form', 'skill-library-editor'), 'action', 'editor'); editor.hidden = true;
  const review = make('section', 'skill-library-review'); review.hidden = true; const detail = make('section', 'skill-library-history'); detail.hidden = true;
  const state = { target, link, status, list, editor, review, detail, live: true, busy: false, controller: null, timer: null, fields: {}, expected: '', candidate: null, enabled: new Map() };
  states.set(target, state);
  toolbar.append(button('新建个人技能', 'new', () => { if (!state.busy) edit(state, { name: '', description: '', body: '', files: {} }, '', ''); }), button('刷新目录', 'refresh', () => perform(state, () => load(state))));
  editor.addEventListener('submit', event => { event.preventDefault(); previewEditor(state); });
  target.append(make('p', 'luna-muted', '把可复用的方法保存在个人技能库。先查看差异，再确认保存；不会覆盖已安装技能的源码。模型保存还需要运行中的写入审批，当前运行继续使用原版本。'), toolbar, status, list, editor, review, detail);
  perform(state, () => load(state));
}
export function unmount(target) { const state = states.get(target); if (!state) return; state.live = false; state.controller?.abort(); clearTimeout(state.timer); state.link.remove(); states.delete(target); }
async function request(state, path, body) {
  const controller = state.controller;
  const response = await fetch(path, { cache: 'no-store', signal: controller?.signal, ...(body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) });
  const payload = await response.json(); if (!response.ok) throw new Error(payload.error || '请求失败：' + response.status);
  if (!state.live || controller?.signal.aborted) throw new Error('请求已取消'); return payload;
}
async function perform(state, work) {
  if (!state.live || state.busy) return; state.busy = true; state.editor.inert = true;
  const controller = new AbortController(); state.controller = controller; state.timer = setTimeout(() => controller.abort(), 15000); state.status.textContent = '正在处理…';
  try { await work(); if (state.live) state.status.textContent = ''; }
  catch (error) { const stopped = controller.signal.aborted; controller.abort(); if (state.live) state.status.textContent = stopped ? '请求超时，请重试。' : error.message; }
  finally { clearTimeout(state.timer); if (state.controller === controller) state.controller = null; state.busy = false; state.editor.inert = false; }
}
async function load(state) {
  const [library, catalog] = await Promise.all([request(state, BASE), request(state, '/api/skills')]); if (!state.live) return;
  state.enabled = new Map((catalog.skills || []).map(item => [item.name, item.enabled === true])); state.list.replaceChildren();
  if (!(library.entries || []).length) state.list.append(make('p', 'luna-muted', '尚未保存个人技能。可以手动新建，或在对话中教 Luna 一次可复用的方法。'));
  for (const entry of library.entries || []) {
    const row = make('article', 'skill-library-row'); row.append(make('h3', '', entry.name), make('p', '', entry.description));
    const source = entry.origin?.kind === 'model' ? '模型' : entry.origin?.kind === 'import' ? '导入' : '用户';
    const origin = mark(make('p', 'luna-muted', [source, entry.origin?.session_id ? '会话 ' + entry.origin.session_id : '', entry.at, (entry.revision || '').slice(0, 12), state.enabled.get(entry.name) ? '已启用' : '已停用'].filter(Boolean).join(' · ')), 'field', 'origin:' + entry.name); row.append(origin);
    if (entry.origin?.reason) row.append(make('p', 'luna-muted', entry.origin.reason));
    const actions = make('div', 'skill-library-actions');
    actions.append(button('编辑', 'edit:' + entry.name, () => openEditor(state, entry, false)), button('复制', 'copy:' + entry.name, () => openEditor(state, entry, true)), button('修订历史', 'history:' + entry.name, () => history(state, entry)), button('导出定义', 'export:' + entry.name, () => exportEntry(state, entry)), button(state.enabled.get(entry.name) ? '停用' : '启用', 'toggle:' + entry.name, () => perform(state, async () => { await request(state, '/api/skills/' + encodeURIComponent(entry.name) + '/' + (state.enabled.get(entry.name) ? 'disable' : 'enable'), {}); await load(state); })));
    row.append(actions); state.list.append(row);
  }
}
function field(state, name, labelText, tag = 'input') { const label = make('label', 'skill-library-field'); const input = mark(make(tag, 'luna-input'), 'field', name); label.append(make('span', '', labelText), input); state.fields[name] = input; state.editor.append(label); return input; }
function edit(state, definition, expected, reason) {
  if (!state.live) return; state.fields = {}; state.expected = expected; state.candidate = null; state.review.hidden = true; state.detail.hidden = true; state.editor.replaceChildren(); state.editor.hidden = false;
  state.editor.append(make('h3', '', expected ? '编辑个人技能' : '新建个人技能'));
  const name = field(state, 'name', '标识（小写字母、数字与连字符）'); name.value = definition.name; name.disabled = Boolean(expected); name.required = true; name.maxLength = 64;
  const description = field(state, 'description', '什么时候使用这个技能'); description.value = definition.description; description.required = true; description.maxLength = 1024;
  const body = field(state, 'body', '方法与注意事项（Markdown，最多 32 KiB）', 'textarea'); body.rows = 12; body.value = definition.body; body.required = true;
  const why = field(state, 'reason', '修改原因'); why.value = reason; why.maxLength = 1024;
  const refs = field(state, 'references', '附加参考文档（可选 JSON：references/name.md → 文本）', 'textarea'); refs.rows = 4; refs.value = JSON.stringify(definition.files || {}, null, 2);
  const actions = make('div', 'skill-library-actions'); const preview = button('查看差异', 'preview', () => {}); preview.type = 'submit'; actions.append(preview, button('取消编辑', 'cancel', () => { if (!state.busy) { state.editor.hidden = true; state.review.hidden = true; state.candidate = null; } })); state.editor.append(actions); body.focus();
}
function openEditor(state, entry, duplicate) {
  perform(state, async () => {
    const payload = await request(state, BASE + '/detail?name=' + encodeURIComponent(entry.name) + '&revision=' + encodeURIComponent(entry.revision));
    const definition = payload.definition; if (duplicate) definition.name = definition.name.slice(0, 56) + '-copy';
    edit(state, definition, duplicate ? '' : entry.revision, duplicate ? '改编自 ' + entry.name + ' @ ' + entry.revision.slice(0, 12) : '');
  });
}
function editorRequest(state) {
  const fields = state.fields; const files = JSON.parse(fields.references.value.trim() || '{}');
  if (!files || Array.isArray(files) || typeof files !== 'object' || Object.values(files).some(value => typeof value !== 'string')) throw new Error('参考文档必须是路径到文本的 JSON 对象。');
  return { definition: { name: fields.name.value.trim(), description: fields.description.value.trim(), body: fields.body.value, files }, expected_revision: state.expected, reason: fields.reason.value };
}
function showReview(state, preview) {
  state.review.replaceChildren(make('h3', '', '确认本次修改'), make('pre', 'skill-library-diff', preview.diff || '没有正文差异。'));
  if (preview.truncated) state.review.append(make('p', 'luna-muted', '差异超过显示上限；请导出旧定义并检查完整内容后再决定。'));
  state.review.append(button('确认保存新修订', 'confirm', () => confirm(state))); state.review.hidden = false;
}
function previewEditor(state) {
  perform(state, async () => { const body = editorRequest(state); const preview = await request(state, BASE + '/preview', body); state.candidate = { kind: 'save', body, fingerprint: JSON.stringify(body) }; showReview(state, preview); });
}
function confirm(state) {
  perform(state, async () => {
    const candidate = state.candidate; if (!candidate) throw new Error('请先预览差异。');
    if (candidate.kind === 'save' && JSON.stringify(editorRequest(state)) !== candidate.fingerprint) throw new Error('内容已改变，请重新预览差异。');
    await request(state, BASE + '/' + candidate.kind, candidate.body); state.editor.hidden = true; state.review.hidden = true; state.detail.hidden = true; state.candidate = null; await load(state);
  });
}
function history(state, entry) {
  perform(state, async () => {
    const payload = await request(state, BASE + '/history?name=' + encodeURIComponent(entry.name)); state.detail.replaceChildren(make('h3', '', '修订历史 · ' + entry.name)); state.detail.hidden = false; state.review.hidden = true; state.candidate = null;
    for (const revision of payload.revisions || []) {
      const row = make('article', 'skill-library-row'); row.append(make('p', 'luna-muted', [revision.revision.slice(0, 12), revision.at, revision.origin?.reason].filter(Boolean).join(' · ')));
      row.append(button('查看正文', 'view:' + revision.revision, () => exportEntry(state, revision)));
      if (revision.revision !== entry.revision) row.append(button('预览恢复', 'restore:' + revision.revision, () => perform(state, async () => {
        const source = await request(state, BASE + '/detail?name=' + encodeURIComponent(entry.name) + '&revision=' + encodeURIComponent(revision.revision));
        const reason = '恢复修订 ' + revision.revision; const preview = await request(state, BASE + '/preview', { definition: source.definition, expected_revision: entry.revision, reason });
        state.editor.hidden = true; state.candidate = { kind: 'restore', body: { name: entry.name, revision: revision.revision, expected_revision: entry.revision, reason } }; showReview(state, preview);
      })));
      state.detail.append(row);
    }
  });
}
function exportEntry(state, entry) {
  perform(state, async () => {
    const payload = await request(state, BASE + '/export?name=' + encodeURIComponent(entry.name) + '&revision=' + encodeURIComponent(entry.revision));
    state.detail.replaceChildren(make('h3', '', '标准技能文件 · ' + entry.name), make('p', 'luna-muted', '只包含技能定义，不包含会话与授权。分享前仍请检查正文中的私人内容。'), make('pre', 'skill-library-diff', JSON.stringify(payload.files || {}, null, 2))); state.detail.hidden = false;
  });
}
