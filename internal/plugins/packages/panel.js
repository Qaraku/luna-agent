// 包管理只在用户明确操作时导入、探测或切换版本，不把包代码当作安装钩子执行。
const states = new WeakMap();
const BASE = '/api/packages';
const STYLE = new URL('panel.css', import.meta.url).href;
const make = (tag, cls = '', text = '') => { const n = document.createElement(tag); n.className = cls; if (text) n.textContent = text; return n; };
const mark = (n, kind, value) => { n.setAttribute('data-' + kind, value); return n; };
const button = (text, id, fn) => { const n = mark(make('button', 'luna-button', text), 'action', id); n.type = 'button'; n.addEventListener('click', fn); return n; };
const option = (value, text) => { const n = make('option', '', text); n.value = value; return n; };
function labeled(parent, title, id, tag = 'input') { const label = make('label', 'package-field'); const input = mark(make(tag, 'luna-input'), 'field', id); label.append(make('span', '', title), input); parent.append(label); return input; }

export function mount(target) {
  const link = make('link'); link.rel = 'stylesheet'; link.href = STYLE; document.head.append(link);
  const form = mark(make('form', 'package-install-form'), 'action', 'install-form');
  const kind = labeled(form, '来源类型', 'source-kind', 'select'); kind.append(option('local', '本地目录'), option('git', '固定 Git 提交')); kind.value = 'local';
  const location = labeled(form, '本地绝对路径或 HTTPS Git 地址', 'source-location'); location.required = true; location.value = '';
  const revision = labeled(form, '完整 Git commit（40 或 64 位）', 'source-revision'); revision.value = ''; revision.parentNode.hidden = true;
  kind.addEventListener('change', () => { revision.parentNode.hidden = kind.value !== 'git'; });
  const submit = button('导入候选（不启用）', 'install', () => {}); submit.type = 'submit'; form.append(submit);
  const status = mark(make('p', 'luna-muted'), 'field', 'status'); status.setAttribute('role', 'status');
  const list = make('div', 'package-list'); const review = mark(make('section', 'package-review'), 'field', 'review'); review.hidden = true;
  const state = { target, link, form, kind, location, revision, status, list, review, entries: [], live: true, busy: false, controller: null, timer: null, reviewing: null, removeConfirm: '' };
  states.set(target, state);
  target.append(make('p', 'luna-muted', '导入只复制清单声明的文件。远程 Git 使用无交互 HTTPS 与完整提交标识，不自动读取凭据或安装依赖；私有源可先检出为本地目录。启用前请核对来源、版本和能力。停用或移除影响后续运行；已开始的运行保留原快照，需要立即结束时请使用停止按钮。'), form, button('刷新列表', 'refresh', () => perform(state, () => load(state))), status, list, review);
  form.addEventListener('submit', event => { event.preventDefault(); perform(state, async () => { const source = { kind: kind.value, location: location.value.trim(), ...(kind.value === 'git' ? { revision: revision.value.trim() } : {}) }; const result = await request(state, '/install', { source }); state.entries = result.packages || []; draw(state); await showReview(state, result.candidate.manifest.id, result.candidate.revision); }, 70000); });
  perform(state, () => load(state));
}
export function unmount(target) { const state = states.get(target); if (!state) return; state.live = false; state.controller?.abort(); clearTimeout(state.timer); state.link.remove(); states.delete(target); }
async function request(state, path, body) { const controller = state.controller; const response = await fetch(BASE + path, { cache: 'no-store', signal: controller?.signal, ...(body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) }); const payload = await response.json(); if (!response.ok) throw new Error(payload.error || '请求失败：' + response.status); if (!state.live || controller?.signal.aborted) throw new Error('请求已取消'); return payload; }
async function perform(state, operation, timeout = 20000) { if (!state.live || state.busy) return; state.busy = true; state.form.inert = true; state.review.inert = true; const controller = new AbortController(); state.controller = controller; state.timer = setTimeout(() => controller.abort(), timeout); state.status.textContent = '正在处理…'; try { await operation(); if (state.live) state.status.textContent = ''; } catch (error) { const stopped = controller.signal.aborted; controller.abort(); if (state.live) state.status.textContent = stopped ? '请求超时或取消，操作可能已保存；请刷新确认。' : error.message; } finally { clearTimeout(state.timer); if (state.controller === controller) state.controller = null; state.busy = false; state.form.inert = false; state.review.inert = false; } }
async function load(state) { const payload = await request(state, ''); state.entries = Array.isArray(payload.packages) ? payload.packages : []; draw(state); }
function draw(state) {
  if (!state.live) return; state.list.replaceChildren();
  if (!state.entries.length) state.list.append(make('p', 'luna-muted', '尚未导入插件包。'));
  for (const entry of state.entries) { const selected = entry.versions.find(v => v.revision === entry.current); const latest = entry.versions.at(-1); const row = make('article', 'package-row'); row.append(make('h3', '', selected?.title || entry.id), make('p', 'luna-muted', entry.id + ' · ' + (entry.removed ? '已移除，数据保留' : entry.runtime_state === 'enabled' ? '已启用' : entry.enabled ? '申请启用，当前不可用' : '未启用') + ' · ' + entry.current.slice(0, 12)));
    if (entry.problem) row.append(make('p', 'package-error', entry.problem));
    const actions = make('div', 'package-actions');
    if (!entry.removed) { actions.append(button('准备导入更新', 'update:' + entry.id, () => { if (state.busy) return; state.kind.value = selected.source.kind; state.location.value = selected.source.location; state.revision.value = selected.source.revision || ''; state.revision.parentNode.hidden = selected.source.kind !== 'git'; state.status.textContent = selected.source.kind === 'git' ? '已填入来源；更新时请提供新的完整提交，再导入候选。' : '已填入来源；导入会读取当前本地内容，不会直接替换正在使用的版本。'; state.location.focus(); })); actions.append(button('审阅 / 选择版本', 'review:' + entry.id, () => perform(state, () => showReview(state, entry.id, entry.current)))); if (latest?.revision !== entry.current) actions.append(button('审阅候选版本', 'candidate:' + entry.id, () => perform(state, () => showReview(state, entry.id, latest.revision))));
      if (entry.enabled || entry.runtime_state === 'enabled') actions.append(button('停用', 'disable:' + entry.id, () => perform(state, async () => { const payload = await request(state, '/disable', { id: entry.id, expected_current: entry.current }); state.entries = payload.packages || []; draw(state); state.review.hidden = true; })));
      const remove = button('移除', 'remove:' + entry.id, () => { if (state.busy) return; if (state.removeConfirm !== entry.id) { state.removeConfirm = entry.id; remove.textContent = '确认移除（保留数据与版本）'; return; } perform(state, async () => { const payload = await request(state, '/remove', { id: entry.id, expected_current: entry.current, confirm: true }); state.entries = payload.packages || []; state.removeConfirm = ''; state.review.hidden = true; draw(state); }); }); actions.append(remove);
    }
    row.append(actions); state.list.append(row);
  }
}
function accessOf(manifest) { const values = new Set(); if (manifest.tools?.length) values.add('exec'); for (const tool of manifest.tools || []) { const a = tool.access || {}; for (const name of ['read', 'write', 'network']) if (a[name]) values.add(name); if (a.state) values.add('state.' + a.state); } if (manifest.static?.length || manifest.panels?.length || manifest.widgets?.length) values.add('trusted-ui'); return [...values]; }
function checkbox(parent, title, id) { const label = make('label', 'package-check'); const input = mark(make('input'), 'field', id); input.type = 'checkbox'; input.checked = false; label.append(input, make('span', '', title)); parent.append(label); return input; }
async function showReview(state, id, revision) {
  const entry = state.entries.find(item => item.id === id); if (!entry) throw new Error('包已不在当前目录中，请刷新。'); const version = await request(state, '/detail?id=' + encodeURIComponent(id) + '&revision=' + encodeURIComponent(revision));
  if (!state.live) return; state.review.replaceChildren(); state.review.hidden = false; state.reviewing = { id, revision, expected: entry.current };
  state.review.append(make('h3', '', '审阅 ' + version.manifest.title));
  const select = labeled(state.review, '选择已保存版本（选择旧版即可回退）', 'review-version', 'select'); for (const item of entry.versions) select.append(option(item.revision, item.version + ' · ' + item.revision.slice(0, 12) + (item.revision === entry.current ? ' · 当前' : ''))); select.value = revision; select.addEventListener('change', () => perform(state, () => showReview(state, id, select.value)));
  const current = entry.versions.find(item => item.revision === entry.current); const access = accessOf(version.manifest); const added = access.filter(item => !current?.access?.includes(item)); const sourceChanged = current && (current.source.kind !== version.source.kind || current.source.location !== version.source.location); const schemaChanged = current && current.state_schema !== version.manifest.state_schema;
  state.review.append(make('pre', 'package-detail', JSON.stringify({ source: version.source, revision: version.revision, access, newly_requested: added, state_schema: version.manifest.state_schema }, null, 2)));
  state.review.append(make('p', 'luna-muted', '代码工具始终在声明范围内隔离运行；这里的安装审阅不授予会话执行权限。每次调用仍按会话策略审批。'));
  if (schemaChanged) state.review.append(make('p', 'package-error', '状态格式与当前版本不同，暂不支持自动迁移，不能直接启用。'));
  const manifest = make('details'); manifest.append(make('summary', '', '完整清单'), make('pre', 'package-detail', JSON.stringify(version.manifest, null, 2))); state.review.append(manifest);
  const fileArea = make('div', 'package-file-area'); const fileSelect = labeled(fileArea, '查看声明文件', 'review-file', 'select'); for (const file of version.files || []) fileSelect.append(option(file.path, file.path + ' · ' + file.bytes + ' B')); const preview = make('pre', 'package-detail');
  fileArea.append(button('读取文件预览', 'preview-file', () => perform(state, async () => { if (!fileSelect.value) return; const payload = await request(state, '/file?id=' + encodeURIComponent(id) + '&revision=' + encodeURIComponent(revision) + '&file=' + encodeURIComponent(fileSelect.value)); preview.textContent = payload.text ?? payload.preview_unavailable ?? ''; })), preview); state.review.append(fileArea);
  for (const preset of version.manifest.presets || []) state.review.append(button('复制预设“' + (preset.title || preset.id) + '”到个人目录', 'copy-preset:' + preset.id, () => perform(state, async () => { const definition = { ...preset, id: ('pkg-' + id + '-' + preset.id).slice(0, 39) + '-copy' }; delete definition.owner; delete definition.revision; const response = await fetch('/api/presets/save', { method: 'POST', headers: { 'Content-Type': 'application/json' }, signal: state.controller.signal, body: JSON.stringify({ definition, expected_revision: '' }) }); const result = await response.json(); if (!response.ok) throw new Error(result.error || '复制失败'); })));
  const confirm = checkbox(state.review, '我已核对该版本的来源、文件与能力声明', 'confirm-review');
  const ui = checkbox(state.review, '信任该版本的同源界面代码：它可访问页面与本地 API，不是安全沙箱', 'trust-ui'); ui.parentNode.hidden = !access.includes('trusted-ui');
  const source = checkbox(state.review, '确认更换插件包来源', 'confirm-source'); source.parentNode.hidden = !sourceChanged;
  const activate = button(revision === entry.current ? '启用此版本' : '切换到此版本', 'activate', () => { if (activate.disabled) return; perform(state, async () => { const payload = await request(state, '/activate', { id, revision, expected_current: entry.current, confirm: confirm.checked, trust_ui: ui.checked, confirm_source: source.checked }); state.entries = payload.packages || []; draw(state); await showReview(state, id, revision); }); });
  const refresh = () => { activate.disabled = schemaChanged || !confirm.checked || access.includes('trusted-ui') && !ui.checked || sourceChanged && !source.checked; }; for (const item of [confirm, ui, source]) item.addEventListener('change', refresh); refresh(); state.review.append(activate);
}
