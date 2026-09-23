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
  return `generation ${data.generation ?? '—'} · ${data.version ?? '—'} · PID ${data.plugin_pid ?? '—'}`;
}

// Tool copy is keyed by the model-visible tool name, so a card or a status row
// for one tool never borrows another tool's wording. An unknown name gets
// neutral copy rather than a guess, and the fallback never names a plugin.
const TOOL_COPY = {
  luna_text_transform: { noun: '文本转换', running: '正在转换文本…', finished: '文本转换完成', failed: '文本转换失败' },
  luna_read_file: { noun: '读取文件', running: '正在读取文件…', finished: '读取文件完成', failed: '读取文件失败' }
};
const TOOL_COPY_FALLBACK = { noun: '工具调用', running: '正在调用工具…', finished: '工具调用完成', failed: '工具调用失败' };

function toolLabel(name) {
  return TOOL_COPY[name] || TOOL_COPY_FALLBACK;
}

function toolActivityLabel(name, state) {
  const label = toolLabel(name);
  return label[state] || label.noun;
}

function valueOrDash(value) {
  return value === undefined || value === null || value === '' ? '—' : String(value);
}

function pluginStatusLabel(status) {
  return {
    active: '启用中',
    retiring: '退役中',
    failed: '不可用'
  }[status] || '状态未知';
}

// pluginRows turns the state payload's `plugins` array into display rows: one
// row per record, because a tool being replaced reports its retiring generation
// alongside its active one. A missing array or a malformed record produces an
// honest placeholder instead of an invented plugin.
function pluginRows(plugins) {
  if (!Array.isArray(plugins)) return [];
  return plugins.map((plugin) => {
    const record = plugin && typeof plugin === 'object' ? plugin : {};
    return {
      tool: valueOrDash(record.tool),
      label: toolLabel(record.tool).noun,
      status: pluginStatusLabel(record.status),
      identity: toolSummary(record)
    };
  });
}

function candidateLabel(value) {
  return {
    v1: '稳定版本 v1',
    v2: '候选版本 v2',
    broken: '故障演练 broken'
  }[value] || value;
}

function reloadCopy(state, candidate, technical = '') {
  if (state === 'pending') return { summary: `正在验证 ${candidate}…`, technical: '' };
  if (state === 'success') return { summary: `${candidate} 已启用。`, technical: '' };
  return { summary: `无法启用 ${candidate}，当前版本保持不变。`, technical };
}

function parseInline(text) {
  const source = String(text ?? '');
  const runs = [];
  const push = (type, value) => {
    if (!value) return;
    const previous = runs[runs.length - 1];
    if (type === 'text' && previous?.type === 'text') previous.text += value;
    else runs.push({ type, text: value });
  };
  let cursor = 0;
  while (cursor < source.length) {
    const strongStart = source.indexOf('**', cursor);
    const codeStart = source.indexOf('`', cursor);
    const starts = [strongStart, codeStart].filter((value) => value >= 0);
    if (!starts.length) {
      push('text', source.slice(cursor));
      break;
    }
    const start = Math.min(...starts);
    push('text', source.slice(cursor, start));
    if (start === strongStart) {
      const end = source.indexOf('**', start + 2);
      if (end < 0) {
        push('text', source.slice(start));
        break;
      }
      push('strong', source.slice(start + 2, end));
      cursor = end + 2;
    } else {
      const end = source.indexOf('`', start + 1);
      if (end < 0) {
        push('text', source.slice(start));
        break;
      }
      push('code', source.slice(start + 1, end));
      cursor = end + 1;
    }
  }
  return runs;
}

function parseMarkdownBlocks(markdown) {
  const lines = String(markdown ?? '').replace(/\r\n?/g, '\n').split('\n');
  const blocks = [];
  const isFence = (line) => /^\s*```/.test(line);
  const heading = (line) => line.match(/^\s{0,3}(#{1,3})\s+(.+)$/);
  const listItem = (line) => line.match(/^\s*([-*]|\d+[.)])\s+(.+)$/);
  let index = 0;
  while (index < lines.length) {
    if (!lines[index].trim()) {
      index += 1;
      continue;
    }
    const fence = lines[index].match(/^\s*```\s*([^\s`]*)/);
    if (fence) {
      const content = [];
      index += 1;
      while (index < lines.length && !isFence(lines[index])) {
        content.push(lines[index]);
        index += 1;
      }
      if (index < lines.length) index += 1;
      blocks.push({ type: 'code', language: fence[1] || '', text: content.join('\n') });
      continue;
    }
    const headingMatch = heading(lines[index]);
    if (headingMatch) {
      blocks.push({ type: 'heading', level: headingMatch[1].length, text: headingMatch[2] });
      index += 1;
      continue;
    }
    const firstItem = listItem(lines[index]);
    if (firstItem) {
      const ordered = /^\d/.test(firstItem[1]);
      const items = [];
      while (index < lines.length) {
        const match = listItem(lines[index]);
        if (!match || /^\d/.test(match[1]) !== ordered) break;
        items.push(match[2]);
        index += 1;
      }
      blocks.push({ type: 'list', ordered, items });
      continue;
    }
    const paragraph = [];
    while (index < lines.length && lines[index].trim() && !isFence(lines[index]) && !heading(lines[index]) && !listItem(lines[index])) {
      paragraph.push(lines[index]);
      index += 1;
    }
    blocks.push({ type: 'paragraph', text: paragraph.join('\n') });
  }
  return blocks;
}

if (typeof module !== 'undefined') {
  module.exports = { parseEventBlock, toolSummary, toolLabel, toolActivityLabel, candidateLabel, reloadCopy, valueOrDash, pluginStatusLabel, pluginRows, parseInline, parseMarkdownBlocks };
}

if (typeof document !== 'undefined') {
  const $ = (id) => document.getElementById(id);
  const transcript = $('transcript');
  const conversation = $('conversation');
  const emptyState = $('empty-state');
  const form = $('chat-form');
  const input = $('message');
  const send = $('send');
  const runStatus = $('run-status');
  const latest = $('latest');
  const runtimeToggle = $('runtime-toggle');
  const runtimeClose = $('runtime-close');
  const runtimeDrawer = $('runtime-drawer');
  const runtimeBackdrop = $('runtime-backdrop');
  const appShell = document.querySelector('.app-shell');
  const runtimeBrief = $('runtime-brief');
  const runtimeAvailability = $('runtime-availability');
  const reloadForm = $('reload-form');
  const reloadButton = $('reload');
  const reloadStatus = $('reload-status');
  const reloadTechnical = $('reload-technical');
  const reloadError = $('reload-error');

  let running = false;
  let reloading = false;
  let currentTurn = null;
  let openTools = [];
  let lastFocused = null;
  let drawerTimer = null;

  function make(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function appendInlineContent(parent, text) {
    for (const run of parseInline(text)) {
      if (run.type === 'strong') parent.append(make('strong', '', run.text));
      else if (run.type === 'code') parent.append(make('code', '', run.text));
      else parent.append(document.createTextNode(run.text));
    }
  }

  function renderMarkdown(container, markdown) {
    container.replaceChildren();
    for (const block of parseMarkdownBlocks(markdown)) {
      if (block.type === 'code') {
        const pre = make('pre');
        const code = make('code', '', block.text);
        if (block.language) code.dataset.language = block.language;
        pre.append(code);
        container.append(pre);
      } else if (block.type === 'list') {
        const list = make(block.ordered ? 'ol' : 'ul');
        for (const item of block.items) {
          const row = make('li');
          appendInlineContent(row, item);
          list.append(row);
        }
        container.append(list);
      } else {
        const tag = block.type === 'heading' ? `h${Math.min(block.level + 2, 5)}` : 'p';
        const node = make(tag);
        appendInlineContent(node, block.text);
        container.append(node);
      }
    }
  }

  function nearBottom() {
    const { scrollHeight, scrollTop, clientHeight } = transcript;
    return scrollHeight - scrollTop - clientHeight < 96;
  }

  function contentChanged(wasNearBottom = nearBottom()) {
    if (wasNearBottom) {
      transcript.scrollTop = transcript.scrollHeight;
      latest.hidden = true;
    } else {
      latest.hidden = false;
    }
  }

  function hideEmptyState() {
    emptyState.hidden = true;
  }

  function addUserTurn(text) {
    const stick = nearBottom();
    hideEmptyState();
    const turn = make('article', 'turn user');
    turn.setAttribute('aria-label', '你');
    turn.append(make('div', 'turn-content', text));
    conversation.append(turn);
    contentChanged(stick);
  }

  function addAssistantTurn(message) {
    const stick = nearBottom();
    hideEmptyState();
    const turn = make('article', 'turn assistant');
    turn.setAttribute('aria-label', 'Luna');
    const label = make('span', 'assistant-label', 'Luna');
    const tools = make('div', 'tool-list');
    const body = make('div', 'assistant-body placeholder', 'Luna 正在回应…');
    turn.append(label, tools, body);
    conversation.append(turn);
    contentChanged(stick);
    return { turn, tools, body, message, answer: '', hasAnswer: false, failed: false, terminal: false };
  }

  function appendDefinition(list, label, value) {
    const row = make('div');
    row.append(make('dt', '', label), make('dd', '', value));
    list.append(row);
  }

  function formatValue(value) {
    if (typeof value === 'string') return value;
    if (value === undefined) return '—';
    return JSON.stringify(value, null, 2);
  }

  function addToolRow(data) {
    const stick = nearBottom();
    const details = make('details', 'tool-row running');
    details.open = true;
    const summary = make('summary', '', toolActivityLabel(data.name, 'running'));
    const detail = make('div', 'tool-detail');
    const list = make('dl');
    appendDefinition(list, '工具', valueOrDash(data.name));
    appendDefinition(list, '参数', formatValue(data.arguments));
    detail.append(list);
    details.append(summary, detail);
    currentTurn.tools.append(details);
    const record = { details, summary, list, name: data.name, complete: false };
    openTools.push(record);
    contentChanged(stick);
    return record;
  }

  function nextOpenTool(name) {
    return openTools.find((tool) => !tool.complete && (!name || !tool.name || tool.name === name));
  }

  function finishTool(data, failed) {
    const stick = nearBottom();
    const tool = nextOpenTool(data.name) || addToolRow({ name: data.name, arguments: {} });
    tool.complete = true;
    tool.details.classList.remove('running');
    tool.details.classList.toggle('failed', failed);
    tool.summary.textContent = toolActivityLabel(tool.name, failed ? 'failed' : 'finished');
    appendDefinition(tool.list, failed ? '错误' : '结果', failed ? (data.error || '未知错误') : formatValue(data.result));
    appendDefinition(tool.list, '执行身份', toolSummary(data));
    tool.details.open = false;
    contentChanged(stick);
  }

  function appendAnswer(text) {
    if (!currentTurn || !text) return;
    const stick = nearBottom();
    if (!currentTurn.hasAnswer) {
      currentTurn.body.textContent = '';
      currentTurn.body.classList.remove('placeholder');
      currentTurn.hasAnswer = true;
    }
    currentTurn.answer += text;
    renderMarkdown(currentTurn.body, currentTurn.answer);
    contentChanged(stick);
  }

  function resolveOpenTools() {
    for (const tool of openTools) {
      if (tool.complete) continue;
      tool.complete = true;
      tool.details.classList.remove('running');
      tool.details.classList.add('failed');
      tool.summary.textContent = toolActivityLabel(tool.name, 'failed');
      appendDefinition(tool.list, '错误', '工具在完成前中断。');
      tool.details.open = false;
    }
  }

  function showRunFailure(error, copy = 'Luna 没能完成这次回应。') {
    if (!currentTurn || currentTurn.failed) return;
    const stick = nearBottom();
    currentTurn.failed = true;
    resolveOpenTools();
    if (!currentTurn.hasAnswer) {
      currentTurn.body.textContent = '';
      currentTurn.body.classList.remove('placeholder');
    }
    const block = make('div', 'error-block');
    block.append(make('p', 'error-copy', copy));
    const retryMessage = currentTurn.message;
    const retry = make('button', 'retry-button', '重试');
    retry.type = 'button';
    retry.addEventListener('click', () => submitMessage(retryMessage));
    const technical = make('details', 'run-error-detail');
    technical.append(make('summary', '', '技术详情'), make('pre', '', error || '未知错误'));
    block.append(retry, technical);
    currentTurn.turn.append(block);
    contentChanged(stick);
  }

  function setRunStatus(text) {
    runStatus.textContent = text;
    runStatus.hidden = !text;
  }

  function handleEvent(event) {
    const data = event.data || {};
    if (event.type === 'run.started') {
      setRunStatus('Luna 正在回应…');
    } else if (event.type === 'assistant.delta') {
      appendAnswer(data.text);
    } else if (event.type === 'tool.started') {
      addToolRow(data);
    } else if (event.type === 'tool.finished') {
      finishTool(data, false);
    } else if (event.type === 'tool.failed') {
      finishTool(data, true);
    } else if (event.type === 'run.finished') {
      currentTurn.terminal = true;
      resolveOpenTools();
      if (!currentTurn.hasAnswer && data.answer) appendAnswer(data.answer);
      if (!currentTurn.hasAnswer) currentTurn.body.remove();
      setRunStatus('');
    } else if (event.type === 'run.failed') {
      currentTurn.terminal = true;
      showRunFailure(data.error);
      setRunStatus('');
    }
  }

  async function errorMessage(response) {
    try {
      const body = await response.json();
      return body.error || `HTTP ${response.status}`;
    } catch (_) {
      return `HTTP ${response.status}`;
    }
  }

  async function streamRun(message, onAdmitted) {
    const response = await fetch('/api/runs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message })
    });
    if (!response.ok) throw new Error(await errorMessage(response));
    if (!response.body) throw new Error('Streaming response unavailable');
    onAdmitted();
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    for (;;) {
      const { value, done } = await reader.read();
      buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
      const blocks = buffer.split(/\r?\n\r?\n/);
      buffer = blocks.pop() || '';
      for (const block of blocks) {
        const parsed = parseEventBlock(block);
        if (parsed) handleEvent(parsed);
      }
      if (done) break;
    }
    if (buffer.trim()) {
      const parsed = parseEventBlock(buffer);
      if (parsed) handleEvent(parsed);
    }
  }

  async function submitMessage(rawMessage) {
    if (running) return;
    const message = rawMessage.trim();
    if (!message) return;
    let admitted = false;
    addUserTurn(message);
    currentTurn = addAssistantTurn(message);
    openTools = [];
    input.value = '';
    resizeInput();
    running = true;
    send.disabled = true;
    setRunStatus('正在连接…');
    try {
      await streamRun(message, () => { admitted = true; });
    } catch (error) {
      if (!admitted) input.value = message;
      resizeInput();
      if (!currentTurn || !currentTurn.terminal) showRunFailure(error.message);
      setRunStatus('');
    } finally {
      if (currentTurn && !currentTurn.terminal && !currentTurn.failed) {
        showRunFailure('SSE 流在收到终止事件前结束（未收到 run.finished 或 run.failed）。', '回应在完成前中断了。');
      }
      setRunStatus('');
      running = false;
      send.disabled = false;
      input.focus();
      updateState();
    }
  }

  form.addEventListener('submit', (event) => {
    event.preventDefault();
    submitMessage(input.value);
  });

  input.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      form.requestSubmit();
    }
  });

  function resizeInput() {
    input.style.height = 'auto';
    input.style.height = `${Math.min(input.scrollHeight, 144)}px`;
  }
  input.addEventListener('input', resizeInput);

  transcript.addEventListener('scroll', () => {
    if (nearBottom()) latest.hidden = true;
  }, { passive: true });
  latest.addEventListener('click', () => {
    transcript.scrollTo({ top: transcript.scrollHeight, behavior: 'smooth' });
    latest.hidden = true;
  });

  function setBackgroundInert(value) {
    if ('inert' in appShell) appShell.inert = value;
  }

  function openDrawer() {
    clearTimeout(drawerTimer);
    lastFocused = document.activeElement;
    runtimeDrawer.hidden = false;
    runtimeBackdrop.hidden = false;
    runtimeToggle.setAttribute('aria-expanded', 'true');
    setBackgroundInert(true);
    requestAnimationFrame(() => {
      runtimeDrawer.classList.add('is-open');
      runtimeBackdrop.classList.add('is-open');
      runtimeClose.focus();
    });
  }

  function closeDrawer() {
    if (runtimeDrawer.hidden) return;
    runtimeDrawer.classList.remove('is-open');
    runtimeBackdrop.classList.remove('is-open');
    runtimeToggle.setAttribute('aria-expanded', 'false');
    setBackgroundInert(false);
    drawerTimer = setTimeout(() => {
      runtimeDrawer.hidden = true;
      runtimeBackdrop.hidden = true;
    }, 180);
    if (lastFocused && typeof lastFocused.focus === 'function') lastFocused.focus();
  }

  runtimeToggle.addEventListener('click', openDrawer);
  runtimeClose.addEventListener('click', closeDrawer);
  runtimeBackdrop.addEventListener('click', closeDrawer);
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && !runtimeDrawer.hidden) closeDrawer();
  });

  function updateReloadStatus(copy, className = '') {
    reloadStatus.textContent = copy.summary;
    reloadStatus.className = `reload-status${className ? ` ${className}` : ''}`;
    reloadError.textContent = copy.technical;
    reloadTechnical.hidden = !copy.technical;
    reloadTechnical.open = false;
  }

  reloadForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (reloading) return;
    const candidate = $('candidate').value;
    reloading = true;
    reloadButton.disabled = true;
    updateReloadStatus(reloadCopy('pending', candidate));
    try {
      const response = await fetch('/api/reload', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ candidate })
      });
      if (!response.ok) throw new Error(await errorMessage(response));
      const body = await response.json();
      updateReloadStatus(reloadCopy('success', candidate), 'success');
      renderState(body);
    } catch (error) {
      updateReloadStatus(reloadCopy('failure', candidate, error.message), 'failure');
    } finally {
      reloading = false;
      reloadButton.disabled = false;
    }
  });

  function renderState(state) {
    $('model').textContent = valueOrDash(state.model);
    $('provider').textContent = valueOrDash(state.provider_host);
    $('host-pid').textContent = valueOrDash(state.host_pid);
    $('busy').textContent = state.busy ? `运行中 · ${valueOrDash(state.current_run_id)}` : '可用';

    // Each allowlisted tool gets its own row, and a tool mid-replacement can
    // report a retiring generation next to its active one. Nothing here is
    // invented: when the payload has no records the list stays empty and the
    // drawer says so.
    const plugins = $('plugins');
    const rows = pluginRows(state.plugins);
    plugins.replaceChildren();
    for (const row of rows) {
      const entry = make('div');
      entry.append(make('dt', '', `${row.tool} · ${row.label}`), make('dd', '', `${row.status} · ${row.identity}`));
      plugins.append(entry);
    }
    $('plugins-empty').hidden = rows.length > 0;

    runtimeAvailability.textContent = '本地运行状态可用';
    runtimeAvailability.className = 'availability ready';
    runtimeBrief.lastChild.textContent = state.busy ? 'Luna 正在运行' : '本地运行正常';
    runtimeBrief.className = 'runtime-brief ready';

    const list = $('events');
    list.replaceChildren();
    for (const item of (state.events || []).slice(-12).reverse()) {
      list.append(make('li', '', `${item.type} · ${item.message}`));
    }
  }

  async function updateState() {
    try {
      const response = await fetch('/api/state', { cache: 'no-store' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      renderState(await response.json());
    } catch (_) {
      runtimeAvailability.textContent = '运行详情暂不可用';
      runtimeAvailability.className = 'availability unavailable';
      runtimeBrief.lastChild.textContent = '状态暂不可用';
      runtimeBrief.className = 'runtime-brief unavailable';
    }
  }

  updateState();
  setInterval(updateState, 2000);
}
