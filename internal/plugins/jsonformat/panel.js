const instances = new WeakMap();

export function mount(target) {
  unmount(target);
  const make = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text) node.textContent = text;
    return node;
  };
  const style = make('link'); style.rel = 'stylesheet'; style.href = '/api/json-format/panel.css';
  const panel = make('section', 'json-format-panel');
  const note = make('p', 'luna-muted', '在本地插件进程中处理，不调用模型、不联网、不保存输入。');
  const inputLabel = make('label', '', 'JSON 输入');
  const input = make('textarea', 'json-format-input'); input.rows = 7; input.spellcheck = false;
  input.placeholder = '{"name":"Luna","items":[1,2,3]}'; inputLabel.append(input);
  const controls = make('div', 'json-format-controls');
  const mode = make('select', 'luna-input'); mode.setAttribute('aria-label', '处理方式');
  for (const [value, text] of [['pretty', '格式化'], ['compact', '压缩']]) { const option = make('option', '', text); option.value = value; mode.append(option); }
  mode.value = 'pretty';
  const example = make('button', 'luna-button', '填入示例'); example.type = 'button';
  const submit = make('button', 'luna-button', '处理 JSON'); submit.type = 'button';
  controls.append(mode, example, submit);
  const status = make('p', 'json-format-status'); status.setAttribute('role', 'status');
  const outputLabel = make('label', '', '结果');
  const output = make('textarea', 'json-format-output'); output.rows = 10; output.readOnly = true; output.spellcheck = false; outputLabel.append(output);
  const state = { active: true, controller: null };
  instances.set(target, state);
  example.addEventListener('click', () => { input.value = '{"name":"Luna","id":900719925474099312345,"items":[1,2,3]}'; });
  submit.addEventListener('click', async () => {
    if (state.controller) return;
    const controller = new AbortController(); state.controller = controller;
    submit.disabled = true; status.textContent = '正在处理…';
    try {
      const response = await fetch('/api/json-format/format', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ text: input.value, mode: mode.value }), signal: controller.signal });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error || '请求失败');
      if (!state.active) return;
      output.value = payload.text;
      status.textContent = '完成 · 版本 ' + payload.version + ' · 代次 ' + payload.generation + ' · PID ' + payload.plugin_pid;
    } catch (error) { if (state.active && error.name !== 'AbortError') status.textContent = '处理失败：' + error.message; }
    finally { if (state.active) submit.disabled = false; state.controller = null; }
  });
  panel.append(note, inputLabel, controls, status, outputLabel);
  target.replaceChildren(style, panel);
}

export function unmount(target) {
  const state = instances.get(target);
  if (state) { state.active = false; state.controller?.abort(); instances.delete(target); }
  target.replaceChildren();
}
