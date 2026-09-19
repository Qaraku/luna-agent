function parseEventBlock(block) {
  let type = 'message';
  const data = [];
  for (const line of block.split(/\r?\n/)) {
    if (line.startsWith('event:')) type = line.slice(6).trim();
    if (line.startsWith('data:')) data.push(line.slice(5).trimStart());
  }
  if (!data.length) return null;
  return { type, data: JSON.parse(data.join('\n')) };
}

function toolSummary(data) {
  return `generation ${data.generation} · ${data.version} · PID ${data.plugin_pid}`;
}

if (typeof module !== 'undefined') module.exports = { parseEventBlock, toolSummary };

if (typeof document !== 'undefined') {
  const $ = (id) => document.getElementById(id);
  const transcript = $('transcript');
  const form = $('chat-form');
  const input = $('message');
  const send = $('send');
  const runStatus = $('run-status');
  const reloadForm = $('reload-form');
  const reloadButton = $('reload');
  const reloadStatus = $('reload-status');
  let running = false;
  let reloading = false;
  let assistantText = null;
  let currentTool = null;

  function make(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function addMessage(role, text) {
    const article = make('article', `message ${role}`);
    article.append(make('span', 'role', role === 'user' ? 'You' : 'Luna'));
    const body = make('p', '', text);
    article.append(body);
    transcript.append(article);
    transcript.scrollTop = transcript.scrollHeight;
    return body;
  }

  function addTool(argumentsValue) {
    const details = make('details', 'tool-card');
    details.open = true;
    const summary = make('summary', '', 'luna_text_transform · running');
    const pre = make('pre', '', JSON.stringify(argumentsValue, null, 2));
    details.append(summary, pre);
    transcript.append(details);
    transcript.scrollTop = transcript.scrollHeight;
    return { details, summary, pre };
  }

  function handleEvent(event) {
    const data = event.data;
    if (event.type === 'run.started') {
      assistantText = addMessage('assistant', '');
      runStatus.textContent = `Running · ${data.run_id}`;
    } else if (event.type === 'assistant.delta') {
      if (!assistantText) assistantText = addMessage('assistant', '');
      assistantText.textContent += data.text;
    } else if (event.type === 'tool.started') {
      currentTool = addTool(data.arguments);
    } else if (event.type === 'tool.finished') {
      if (!currentTool) currentTool = addTool({});
      currentTool.summary.textContent = `luna_text_transform · ${toolSummary(data)}`;
      currentTool.pre.textContent = `${data.result}\n\n${toolSummary(data)}`;
    } else if (event.type === 'tool.failed') {
      if (!currentTool) currentTool = addTool({});
      currentTool.summary.textContent = 'luna_text_transform · failed';
      currentTool.pre.textContent = data.error;
      currentTool.pre.classList.add('error');
    } else if (event.type === 'run.finished') {
      runStatus.textContent = 'Finished';
    } else if (event.type === 'run.failed') {
      runStatus.textContent = `Failed · ${data.error}`;
      runStatus.classList.add('error');
    }
  }

  async function streamRun(message) {
    const response = await fetch('/api/runs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ message }) });
    if (!response.ok) throw new Error((await response.json()).error || `HTTP ${response.status}`);
    if (!response.body) throw new Error('Streaming response unavailable');
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    for (;;) {
      const { value, done } = await reader.read();
      buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
      const blocks = buffer.split(/\r?\n\r?\n/);
      buffer = blocks.pop() || '';
      for (const block of blocks) {
        const event = parseEventBlock(block);
        if (event) handleEvent(event);
      }
      if (done) break;
    }
    if (buffer.trim()) {
      const event = parseEventBlock(buffer);
      if (event) handleEvent(event);
    }
  }

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (running) return;
    const message = input.value.trim();
    if (!message) return;
    addMessage('user', message);
    running = true;
    send.disabled = true;
    runStatus.classList.remove('error');
    runStatus.textContent = 'Connecting…';
    assistantText = null;
    currentTool = null;
    try { await streamRun(message); input.value = ''; }
    catch (error) { runStatus.textContent = `Error · ${error.message}`; runStatus.classList.add('error'); }
    finally { running = false; send.disabled = false; input.focus(); updateState(); }
  });

  reloadForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (reloading) return;
    reloading = true;
    reloadButton.disabled = true;
    reloadStatus.textContent = 'Building and validating candidate…';
    try {
      const response = await fetch('/api/reload', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ candidate: $('candidate').value }) });
      const body = await response.json();
      if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
      reloadStatus.textContent = `Activated ${body.active.version}, generation ${body.active.generation}`;
      renderState(body);
    } catch (error) { reloadStatus.textContent = `Reload failed · ${error.message}`; }
    finally { reloading = false; reloadButton.disabled = false; }
  });

  function renderState(state) {
    $('model').textContent = state.model || '—';
    $('provider').textContent = state.provider_host || '—';
    $('host-pid').textContent = String(state.host_pid || '—');
    $('busy').textContent = state.busy ? `busy · ${state.current_run_id}` : 'idle';
    const active = state.active || {};
    $('plugin-version').textContent = active.version || '—';
    $('plugin-generation').textContent = active.generation === undefined ? '—' : String(active.generation);
    $('plugin-pid').textContent = active.plugin_pid === undefined ? '—' : String(active.plugin_pid);
    const list = $('events');
    list.replaceChildren();
    for (const item of (state.events || []).slice(-12).reverse()) list.append(make('li', '', `${item.type} · ${item.message}`));
  }

  async function updateState() {
    try {
      const response = await fetch('/api/state', { cache: 'no-store' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      renderState(await response.json());
    } catch (error) {
      $('busy').textContent = `disconnected · ${error.message}`;
    }
  }

  updateState();
  setInterval(updateState, 2000);
}
