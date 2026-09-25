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

// --- Runtime UI plugins ---------------------------------------------------

// UI_PLUGIN_API_VERSION is the version of the mount/unmount contract itself.
// The host publishes no product version to the browser, so this names the
// contract a plugin is written against instead of inventing a host version.
const UI_PLUGIN_API_VERSION = '1';

// The reason given to a listed plugin this front end refuses to load. It only
// appears when the server listed something the loader cannot import, and the
// directory stays visible with it rather than disappearing.
const UI_PLUGIN_ENTRY_REASON = 'plugin.json 的 entry 不是插件目录内的相对路径';

// The exports a plugin must provide. mount is what is called on enable and
// unmount is the only thing that ends the plugin's own side effects, so a
// module missing either one cannot be mounted or left after being mounted.
const UI_PLUGIN_REQUIRED_EXPORTS = ['mount', 'unmount'];

function uiPluginText(value) {
  if (typeof value === 'string') return value;
  if (value === undefined || value === null) return '';
  try {
    return String(value);
  } catch (_) {
    return '';
  }
}

// uiPluginNameValid is the whole shape a plugin name may have, the same rule the
// server enforces: one directory name, no separator, no dot.
function uiPluginNameValid(name) {
  return typeof name === 'string' && /^[a-z0-9-]{1,32}$/.test(name);
}

// uiPluginEntrySafe reports whether a manifest entry is a relative path inside
// the plugin's own directory. The server refuses anything else when it lists
// plugins; this is the client's own check on top of that, so a hand-edited
// manifest can neither reach import() with an absolute URL nor climb out of the
// plugin root. `%` is refused outright because an encoded separator or dot is
// the one thing that could survive a naive segment split.
function uiPluginEntrySafe(entry) {
  if (typeof entry !== 'string') return false;
  const value = entry.trim();
  if (!value || value.length > 200) return false;
  if (value.startsWith('/') || value.includes('\\')) return false;
  if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(value)) return false;
  if (/[\u0000-\u001f\u007f?#%]/.test(value)) return false;
  return value.split('/').every((segment) => segment !== '' && segment !== '.' && segment !== '..');
}

// uiPluginEntryURL is the only place a plugin's import specifier is built. The
// name and the entry are validated first, so the specifier is always a path
// under /api/ui-plugins/ on this origin, and a caller that gets '' must not
// import at all.
function uiPluginEntryURL(name, entry) {
  if (!uiPluginNameValid(name) || !uiPluginEntrySafe(entry)) return '';
  const path = entry.trim().split('/').map(encodeURIComponent).join('/');
  return `/api/ui-plugins/${name}/${path}`;
}

// uiPluginRows turns GET /api/ui-plugins into display rows: the plugins the
// server discovered, then the directories it skipped. A skipped directory is
// shown with the server's own reason and is never hidden — a broken plugin
// directory is exactly what this section exists to make visible. A listed
// plugin whose name or entry this loader cannot use becomes a skipped row too,
// with a reason of the client's own, rather than being silently dropped.
function uiPluginRows(payload) {
  const body = payload && typeof payload === 'object' ? payload : {};
  const plugins = Array.isArray(body.plugins) ? body.plugins : [];
  const skipped = Array.isArray(body.skipped) ? body.skipped : [];
  const rows = [];
  for (const entry of plugins) {
    const record = entry && typeof entry === 'object' ? entry : {};
    const name = typeof record.name === 'string' ? record.name : '';
    const url = uiPluginEntryURL(name, record.entry);
    if (!url) {
      const label = name || '—';
      rows.push({
        name: label, title: label, description: '', entry: '', url: '', skipped: true, reason: UI_PLUGIN_ENTRY_REASON
      });
      continue;
    }
    rows.push({
      name,
      title: uiPluginText(record.title) || name,
      description: uiPluginText(record.description),
      entry: record.entry.trim(),
      url,
      skipped: false,
      reason: ''
    });
  }
  for (const entry of skipped) {
    const record = entry && typeof entry === 'object' ? entry : {};
    const name = uiPluginText(record.name) || '—';
    rows.push({
      name,
      title: name,
      description: '',
      entry: '',
      url: '',
      skipped: true,
      reason: uiPluginText(record.reason) || '未说明原因'
    });
  }
  return rows;
}

// uiPluginMissingExports names every required export a loaded module does not
// provide, so the message can say which one is missing instead of guessing.
function uiPluginMissingExports(pluginModule) {
  const loaded = pluginModule && typeof pluginModule === 'object' ? pluginModule : {};
  return UI_PLUGIN_REQUIRED_EXPORTS.filter((name) => typeof loaded[name] !== 'function');
}

// A plugin's throw can be anything at all, so one place turns it into the single
// line of text that goes on screen through textContent.
function uiPluginErrorDetail(error) {
  if (error && typeof error === 'object' && typeof error.message === 'string' && error.message) return error.message;
  const text = uiPluginText(error).trim();
  return text || '未知错误';
}

function uiPluginImportError(name, detail) {
  return `无法加载界面插件 ${name} 的入口文件：${detail}`;
}

function uiPluginMissingExportError(name, missing) {
  const list = Array.isArray(missing) ? missing.join('、') : uiPluginText(missing);
  return `界面插件 ${name} 缺少必需的导出 ${list}。`;
}

function uiPluginMountError(name, detail) {
  return `界面插件 ${name} 挂载失败，容器已移除：${detail}`;
}

function uiPluginUnmountError(name, detail) {
  return `界面插件 ${name} 停用时清理失败，容器已移除：${detail}`;
}

function uiPluginState(status, error = '') {
  return { status, error: error === '' ? '' : uiPluginErrorDetail(error) };
}

// uiPluginEnableFailureEvent is the event after a failed enable. Nothing is
// mounted, so the plugin is a failure carrying its message and not a disabled
// plugin, which is what the state machine is told.
function uiPluginEnableFailureEvent(message) {
  return { type: 'enable-failed', error: message };
}

// uiPluginDisableEvent is the event after unmount has been attempted. The host
// removes the container on both paths, so a throwing unmount is still a
// disabled plugin — one whose error is reported instead of swallowed.
function uiPluginDisableEvent(title, detail) {
  const text = uiPluginText(detail).trim();
  return text === '' ? { type: 'disabled' } : { type: 'disable-failed', error: uiPluginUnmountError(title, text) };
}

// uiPluginTeardown runs the plugin's own unmount and then removes the container.
// The removal happens on both paths and is not conditional on the plugin
// behaving: it is the host's guarantee, so it lives in one place instead of
// inside an event handler where a thrown error could skip it.
function uiPluginTeardown(pluginModule, target, title) {
  let detail = '';
  try {
    pluginModule.unmount(target);
  } catch (error) {
    detail = uiPluginErrorDetail(error);
  }
  target.remove();
  return uiPluginDisableEvent(title, detail);
}

// uiPluginAbandonMount is the host's own cleanup after an enable that did not
// finish: the container created for that attempt is removed and the stage that
// held it is hidden again, so nothing that never mounted stays on screen.
function uiPluginAbandonMount(stage, target) {
  target.remove();
  stage.hidden = true;
}

function uiPluginInitialState() {
  return uiPluginState('disabled');
}

// uiPluginTransition is the whole enable/disable state machine, as a pure
// function. The two failure events differ on purpose: a failed enable leaves
// nothing mounted, so it is a failure; a failed disable has already had its
// container removed by the host, so the plugin is off and the error is shown
// beside it instead of being swallowed.
function uiPluginTransition(state, event) {
  const current = state && typeof state === 'object' && typeof state.status === 'string' ? state : uiPluginInitialState();
  const type = event && typeof event === 'object' ? event.type : event;
  const error = event && typeof event === 'object' ? event.error : undefined;
  switch (type) {
    case 'enable':
      // An old failure is cleared here, because nothing is mounted yet.
      return current.status === 'loading' ? current : uiPluginState('loading');
    case 'enabled':
      return uiPluginState('enabled');
    case 'enable-failed':
      return uiPluginState('failed', error);
    case 'disable':
      return current.status === 'loading' ? current : uiPluginState('loading');
    case 'disabled':
      return uiPluginState('disabled');
    case 'disable-failed':
      return uiPluginState('disabled', error);
    default:
      return current;
  }
}

// uiPluginToggleAction says what a click on the control means right now, and ''
// while a transition is in flight, which is when the button is disabled.
function uiPluginToggleAction(status) {
  if (status === 'loading') return '';
  return status === 'enabled' ? 'disable' : 'enable';
}

function uiPluginToggleLabel(status) {
  if (status === 'loading') return '处理中…';
  return status === 'enabled' ? '停用' : '启用';
}

// uiPluginStatusText is the line under one plugin. It carries the last error
// whatever the status, so a plugin that failed to load or failed to clean up
// says so instead of looking idle.
function uiPluginStatusText(state) {
  const current = state && typeof state === 'object' ? state : uiPluginInitialState();
  if (current.error) return current.error;
  if (current.status === 'loading') return '正在加载插件入口…';
  if (current.status === 'enabled') return '已启用 · 停用时会调用 unmount';
  return '';
}

// uiPluginHostAPI is the entire host interface a plugin receives: the contract
// version and one bounded log line. No host state, no DOM reference outside the
// plugin's own container and no fetch wrapper, so a plugin cannot read the
// session, the tool plugins or anything else in the drawer.
function uiPluginHostAPI(version, log) {
  const write = typeof log === 'function' ? log : () => {};
  return Object.freeze({
    version: uiPluginText(version),
    log(message) {
      write(uiPluginText(message));
    }
  });
}

// --- Sessions -------------------------------------------------------------

// The current session lives in the URL hash and never in browser storage: a
// refresh restores it, a second tab starts a session of its own, and the link
// can be copied. A fragment is accepted only when it looks like a session id —
// the store's charset is lowercase alphanumeric, 8 to 64 characters — so a
// hand-edited or stale link is ignored instead of being sent as a request that
// could only come back 400.
function isSessionID(value) {
  return typeof value === 'string' && /^[0-9a-z]{8,64}$/.test(value);
}

function parseSessionHash(hash) {
  for (const part of String(hash ?? '').replace(/^#/, '').split('&')) {
    const separator = part.indexOf('=');
    if (separator < 0 || part.slice(0, separator) !== 'session') continue;
    const value = part.slice(separator + 1);
    return isSessionID(value) ? value : '';
  }
  return '';
}

function sessionHash(id) {
  return isSessionID(id) ? `#session=${id}` : '';
}

// A stored title is the first message of the session, cut to 80 runes by the
// store. An absent or blank one is labelled rather than rendered as a blank
// row.
function sessionTitle(value) {
  const text = typeof value === 'string' ? value.trim() : '';
  return text || '未命名会话';
}

// Timestamps are rendered exactly as the server wrote them (RFC 3339 carrying
// its own offset) instead of through the browser's timezone, so a stored time
// is never silently rewritten. A value that cannot be read is shown as missing.
function sessionTime(value) {
  const match = typeof value === 'string' ? value.match(/^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2})/) : null;
  return match ? `${match[1]} ${match[2]}` : '—';
}

function runCountLabel(value) {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0) return '运行次数未知';
  return value === 0 ? '尚无运行' : `${value} 次运行`;
}

function runStatusLabel(status) {
  return {
    ok: '这次运行已完成',
    error: '这次运行失败了',
    cancelled: '这次运行被取消',
    interrupted: '这次运行中断了'
  }[status] || '这次运行的结果未知';
}

// sessionRows turns GET /api/sessions into display rows, in the order the
// server returned them (newest first; nothing is re-sorted here). A record
// without a usable id cannot be switched to, so it is dropped rather than
// rendered as a dead row, while a record with an id but missing fields is
// still listed and labelled honestly.
function sessionRows(payload, currentID) {
  const list = payload && typeof payload === 'object' && Array.isArray(payload.sessions) ? payload.sessions : [];
  const rows = [];
  for (const entry of list) {
    const record = entry && typeof entry === 'object' ? entry : {};
    if (!isSessionID(record.id)) continue;
    rows.push({
      id: record.id,
      shortId: record.id.slice(0, 8),
      title: sessionTitle(record.title),
      time: sessionTime(record.updated_at),
      runs: runCountLabel(record.run_count),
      current: record.id === currentID
    });
  }
  return rows;
}

// memoryRows turns GET /api/memory into display rows: the facts in effect,
// newest first, each carrying the exact text and timestamp a retraction posts
// back. A row that is not both a text and a timestamp is dropped rather than
// rendered as a fact with an invented field.
function memoryRows(payload) {
  const view = { facts: [], retracted: [] };
  if (!payload || typeof payload !== 'object') return view;
  const facts = Array.isArray(payload.facts) ? payload.facts : [];
  for (const entry of facts) {
    const fact = entry && typeof entry === 'object' ? entry : {};
    if (typeof fact.text !== 'string' || !fact.text) continue;
    if (typeof fact.at !== 'string' || !fact.at) continue;
    const session = typeof fact.source_session === 'string' && fact.source_session ? `#${fact.source_session.slice(0, 8)}` : '来源未知';
    view.facts.push({ text: fact.text, at: fact.at, time: sessionTime(fact.at), session });
  }
  view.facts.reverse();
  const retracted = Array.isArray(payload.retracted) ? payload.retracted : [];
  for (const entry of retracted) {
    const gone = entry && typeof entry === 'object' ? entry : {};
    if (typeof gone.text !== 'string' || !gone.text) continue;
    view.retracted.push({ text: gone.text, time: sessionTime(gone.retracted_at) });
  }
  return view;
}

// retractPayload is what a retraction posts: the stored text and the stored
// timestamp, unchanged, so the store can name exactly one fact. A row that
// cannot name its target is never sent.
function retractPayload(row) {
  if (!row || typeof row !== 'object') return null;
  if (typeof row.at !== 'string' || !row.at) return null;
  if (typeof row.text !== 'string' || !row.text) return null;
  return { at: row.at, text: row.text };
}

// Replay shows the frozen record facts and nothing else. The store has no field
// for plugin generation, version or process id, so a replayed tool call cannot
// show an execution identity and must not invent one.
function argumentsText(raw) {
  const text = typeof raw === 'string' ? raw : '';
  if (!text.trim()) return '—';
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch (_) {
    return text;
  }
}

function toolCallFacts(record) {
  const item = record && typeof record === 'object' ? record : {};
  const error = typeof item.error === 'string' ? item.error : '';
  const facts = [
    { label: '工具', value: valueOrDash(item.name) },
    { label: '参数', value: argumentsText(item.arguments) }
  ];
  if (error) facts.push({ label: '错误', value: error });
  else facts.push({ label: '结果', value: typeof item.result === 'string' ? item.result : valueOrDash(item.result) });
  return facts;
}

// replaySession turns GET /api/sessions/{id} into an ordered draw list. Records
// are replayed in file order, so the area shows what is on disk: a user message
// opens a turn, tool calls attach to the assistant turn that answers it, and
// the run record closes it. The `session` record is the file's own header and
// carries no conversation. Nothing is guessed: a record type, a role or a run
// that is not part of the frozen vocabulary is counted and reported, and a run
// that ended without leaving a turn still says so in its own words.
function replaySession(detail) {
  const payload = detail && typeof detail === 'object' ? detail : {};
  const records = Array.isArray(payload.records) ? payload.records : [];
  const turns = [];
  const notices = [];
  let open = null;
  let unknown = 0;
  const openAssistant = () => {
    open = { role: 'assistant', answer: '', tools: [], status: '', failed: false };
    turns.push(open);
    return open;
  };
  for (const entry of records) {
    const record = entry && typeof entry === 'object' ? entry : null;
    if (!record) {
      unknown += 1;
      continue;
    }
    if (record.type === 'session') continue;
    if (record.type === 'message') {
      const text = typeof record.text === 'string' ? record.text : '';
      if (record.role === 'user') {
        turns.push({ role: 'user', text });
        open = null;
      } else if (record.role === 'assistant') {
        (open || openAssistant()).answer += text;
      } else {
        unknown += 1;
      }
      continue;
    }
    if (record.type === 'tool_call') {
      const error = typeof record.error === 'string' ? record.error : '';
      (open || openAssistant()).tools.push({
        name: record.name,
        arguments: record.arguments,
        result: record.result,
        error,
        failed: error !== ''
      });
      continue;
    }
    if (record.type === 'run') {
      const status = typeof record.status === 'string' ? record.status : '';
      const failed = status !== '' && status !== 'ok';
      if (open) {
        open.status = status;
        open.failed = failed;
        open = null;
      } else if (failed) {
        turns.push({ role: 'note', text: runStatusLabel(status) });
      }
      continue;
    }
    unknown += 1;
  }
  if (payload.truncated === true) notices.push('这个会话的最后一行没有写完，已按可读的部分回放。');
  if (unknown > 0) notices.push(`有 ${unknown} 条记录无法识别，未回放。`);
  return { title: sessionTitle(payload.title), truncated: payload.truncated === true, turns, notices };
}

// The run body carries the current session when there is one, so the turn
// continues that session's history; a session that does not exist yet sends no
// session_id at all and lets the server create it and name it on run.started.
function runPayload(message, sessionID) {
  const payload = { message };
  if (isSessionID(sessionID)) payload.session_id = sessionID;
  return payload;
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
  module.exports = { parseEventBlock, toolSummary, toolLabel, toolActivityLabel, candidateLabel, reloadCopy, valueOrDash, pluginStatusLabel, pluginRows, parseInline, parseMarkdownBlocks, isSessionID, parseSessionHash, sessionHash, sessionTitle, sessionTime, runCountLabel, runStatusLabel, sessionRows, memoryRows, retractPayload, argumentsText, toolCallFacts, replaySession, runPayload, uiPluginText, uiPluginNameValid, uiPluginEntrySafe, uiPluginEntryURL, uiPluginRows, uiPluginMissingExports, uiPluginErrorDetail, uiPluginImportError, uiPluginMissingExportError, uiPluginMountError, uiPluginUnmountError, uiPluginState, uiPluginInitialState, uiPluginTransition, uiPluginEnableFailureEvent, uiPluginDisableEvent, uiPluginTeardown, uiPluginAbandonMount, uiPluginToggleAction, uiPluginToggleLabel, uiPluginStatusText, uiPluginHostAPI, UI_PLUGIN_API_VERSION, UI_PLUGIN_ENTRY_REASON, UI_PLUGIN_REQUIRED_EXPORTS };
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
  const sessionList = $('session-list');
  const sessionNew = $('session-new');
  const sessionStatus = $('session-status');
  const sessionNotices = $('session-notices');
  const uiPluginList = $('ui-plugin-list');
  const memoryList = $('memory-list');
  const memoryEmpty = $('memory-empty');
  const memoryStatus = $('memory-status');
  const uiPluginsEmpty = $('ui-plugins-empty');
  const uiPluginStatus = $('ui-plugins-status');
  const uiPluginsRetry = $('ui-plugins-retry');

  let running = false;
  let reloading = false;
  let currentTurn = null;
  let openTools = [];
  let lastFocused = null;
  let drawerTimer = null;
  // switching guards a session replay in flight; sessionsPayload is the last
  // good list, so a busy flag can re-render the rows without a second request.
  let switching = false;
  let sessionsPayload = null;
  let currentSessionID = '';

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

  function userTurnNode(text) {
    const turn = make('article', 'turn user');
    turn.setAttribute('aria-label', '你');
    turn.append(make('div', 'turn-content', text));
    return turn;
  }

  function addUserTurn(text) {
    const stick = nearBottom();
    hideEmptyState();
    conversation.append(userTurnNode(text));
    contentChanged(stick);
  }

  // The three parts of an assistant turn are built in one place so a live turn
  // and a replayed one are the same DOM shape.
  function assistantTurnNode() {
    const turn = make('article', 'turn assistant');
    turn.setAttribute('aria-label', 'Luna');
    turn.append(make('span', 'assistant-label', 'Luna'));
    const tools = make('div', 'tool-list');
    const body = make('div', 'assistant-body placeholder', 'Luna 正在回应…');
    turn.append(tools, body);
    return { turn, tools, body };
  }

  function addAssistantTurn(message) {
    const stick = nearBottom();
    hideEmptyState();
    const node = assistantTurnNode();
    conversation.append(node.turn);
    contentChanged(stick);
    return { turn: node.turn, tools: node.tools, body: node.body, message, answer: '', hasAnswer: false, failed: false, terminal: false };
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
      // A session that did not exist before this run is created by the server;
      // its id arrives here and goes into the hash, so the address bar names the
      // session the answer is being written into, and a refresh returns to it.
      adoptSession(data.session_id);
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
      body: JSON.stringify(runPayload(message, currentSessionID))
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
    if (running || switching) return;
    const message = rawMessage.trim();
    if (!message) return;
    let admitted = false;
    addUserTurn(message);
    currentTurn = addAssistantTurn(message);
    openTools = [];
    input.value = '';
    resizeInput();
    running = true;
    setSessionControls();
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
      setSessionControls();
      input.focus();
      updateState();
      // The run changed the session's title, time and run count.
      updateSessions();
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
    // Reading every session file has a cost, so the list is refreshed when the
    // drawer that shows it is opened and while it stays open.
    updateSessions();
    updateMemory();
    updateUIPlugins();
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

  // --- Runtime UI plugins ------------------------------------------------

  // Enable state lives in this page and nowhere else: a refresh starts with
  // every plugin disabled, and nothing about a running plugin is written to
  // browser storage. The list is read once, so a plugin that is running is
  // never torn out of the section by a later read of the same list; a listing
  // that failed to arrive is the one case with a retry, and then nothing is
  // mounted yet.
  let uiPluginsPayload = null;
  let uiPluginsReading = false;
  const uiPluginMounts = new Map();

  // A plugin's own log line, bounded, into a list that is not a live region:
  // a chatty experimental plugin must not be announced line by line.
  function uiPluginLogLine(node, message) {
    const text = uiPluginText(message).trim();
    if (!text) return;
    node.log.append(make('li', 'ui-plugin-log-line', text));
    while (node.log.childElementCount > 50) node.log.firstElementChild.remove();
    node.log.hidden = false;
  }

  function applyUIPluginState(node, state) {
    node.state = state;
    const action = uiPluginToggleAction(state.status);
    node.button.textContent = uiPluginToggleLabel(state.status);
    node.button.disabled = action === '';
    node.button.className = `ui-plugin-toggle${state.status === 'enabled' ? ' is-on' : ''}`;
    node.button.setAttribute('aria-label', action === 'disable'
      ? `停用界面插件 ${node.row.title}`
      : action === 'enable' ? `启用界面插件 ${node.row.title}` : `界面插件 ${node.row.title} 正在处理`);
    const status = uiPluginStatusText(state);
    node.status.textContent = status;
    node.status.className = `ui-plugin-status${state.error ? ' failure' : ''}`;
    node.status.hidden = status === '';
    // The plugin's container is only on screen while the plugin is mounted, so
    // nothing that looks mounted is ever left behind.
    node.stage.hidden = state.status !== 'enabled';
  }

  function uiPluginRowNode(row) {
    const item = make('li', `ui-plugin-row${row.skipped ? ' skipped' : ''}`);
    const head = make('div', 'ui-plugin-head');
    head.append(make('span', 'ui-plugin-title', row.title));
    if (row.skipped) {
      // A skipped directory is shown with the server's reason and no control:
      // there is nothing to load, and hiding it is exactly the failure this
      // section is meant to prevent.
      item.append(head);
      const reason = make('p', 'ui-plugin-reason');
      reason.append(make('span', 'ui-plugin-reason-label', '已跳过'), document.createTextNode(`：${row.reason}`));
      item.append(reason);
      return item;
    }
    const node = {};
    const button = make('button', 'ui-plugin-toggle');
    button.type = 'button';
    button.addEventListener('click', () => toggleUIPlugin(node));
    head.append(button);
    const description = make('p', 'ui-plugin-description', row.description || '这个插件没有写描述。');
    const stage = make('div', 'ui-plugin-stage');
    stage.hidden = true;
    const status = make('p', 'ui-plugin-status');
    status.hidden = true;
    const log = make('ul', 'ui-plugin-log');
    log.hidden = true;
    item.append(head, description, stage, status, log);
    Object.assign(node, { row, button, stage, status, log, state: uiPluginInitialState() });
    applyUIPluginState(node, node.state);
    return item;
  }

  function renderUIPlugins(payload) {
    const rows = uiPluginRows(payload);
    uiPluginList.replaceChildren();
    for (const row of rows) uiPluginList.append(uiPluginRowNode(row));
    uiPluginsEmpty.hidden = rows.length > 0;
  }

  async function updateUIPlugins(force = false) {
    if (uiPluginsReading || (uiPluginsPayload !== null && !force)) return;
    uiPluginsReading = true;
    uiPluginsRetry.hidden = true;
    try {
      const response = await fetch('/api/ui-plugins', { cache: 'no-store' });
      if (!response.ok) throw new Error(await errorMessage(response));
      uiPluginsPayload = await response.json();
      uiPluginStatus.textContent = '';
      uiPluginStatus.className = 'ui-plugin-status';
      uiPluginStatus.hidden = true;
      renderUIPlugins(uiPluginsPayload);
    } catch (error) {
      // A listing that could not be read is said so plainly: an empty section
      // would otherwise claim there are no plugins.
      uiPluginsPayload = null;
      uiPluginStatus.textContent = `无法读取界面插件列表：${uiPluginErrorDetail(error)}`;
      uiPluginStatus.className = 'ui-plugin-status failure';
      uiPluginStatus.hidden = false;
      uiPluginsRetry.hidden = false;
    } finally {
      uiPluginsReading = false;
    }
  }

  // enableUIPlugin fails closed at every step: the container is created for this
  // attempt and removed again on any failure, so the plugin is either fully
  // mounted or gone, never half there.
  async function enableUIPlugin(node) {
    applyUIPluginState(node, uiPluginTransition(node.state, 'enable'));
    const target = make('div', 'ui-plugin-target');
    node.stage.append(target);
    node.stage.hidden = false;
    const api = uiPluginHostAPI(UI_PLUGIN_API_VERSION, (message) => uiPluginLogLine(node, message));
    let pluginModule = null;
    try {
      pluginModule = await import(node.row.url);
    } catch (error) {
      failUIPlugin(node, target, uiPluginImportError(node.row.title, uiPluginErrorDetail(error)));
      return;
    }
    const missing = uiPluginMissingExports(pluginModule);
    if (missing.length) {
      failUIPlugin(node, target, uiPluginMissingExportError(node.row.title, missing));
      return;
    }
    try {
      pluginModule.mount(target, api);
    } catch (error) {
      // mount threw, so the plugin's own cleanup cannot be relied on here; what
      // the host owns is the container it created, and it removes that.
      failUIPlugin(node, target, uiPluginMountError(node.row.title, uiPluginErrorDetail(error)));
      return;
    }
    uiPluginMounts.set(node.row.name, { target, pluginModule });
    applyUIPluginState(node, uiPluginTransition(node.state, 'enabled'));
  }

  function failUIPlugin(node, target, message) {
    uiPluginAbandonMount(node.stage, target);
    applyUIPluginState(node, uiPluginTransition(node.state, uiPluginEnableFailureEvent(message)));
  }

  // disableUIPlugin calls the plugin's unmount and then removes the container
  // whatever unmount did. A plugin whose cleanup throws is still off the screen,
  // and the error is shown rather than swallowed.
  async function disableUIPlugin(node) {
    applyUIPluginState(node, uiPluginTransition(node.state, 'disable'));
    const mounted = uiPluginMounts.get(node.row.name);
    uiPluginMounts.delete(node.row.name);
    if (!mounted) {
      applyUIPluginState(node, uiPluginTransition(node.state, 'disabled'));
      return;
    }
    const event = uiPluginTeardown(mounted.pluginModule, mounted.target, node.row.title);
    node.stage.hidden = true;
    applyUIPluginState(node, uiPluginTransition(node.state, event));
  }

  async function toggleUIPlugin(node) {
    const action = uiPluginToggleAction(node.state.status);
    if (action === 'enable') await enableUIPlugin(node);
    else if (action === 'disable') await disableUIPlugin(node);
  }

  uiPluginsRetry.addEventListener('click', () => updateUIPlugins(true));

  function renderState(state) {
    $('model').textContent = valueOrDash(state.model);
    $('provider').textContent = valueOrDash(state.provider_host);
    $('host-pid').textContent = valueOrDash(state.host_pid);
    $('busy').textContent = state.busy ? `运行中 · ${valueOrDash(state.current_run_id)}` : '可用';
    $('current-session').textContent = valueOrDash(state.current_session_id);

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
    // The list is only visible in the drawer, so it is only polled while that
    // drawer is open; the store reads every session file to answer it, and the
    // facts only change when a run writes one.
    if (!runtimeDrawer.hidden) {
      updateSessions();
      updateMemory();
    }
  }

  // --- Sessions in the drawer and the address bar -------------------------

  function setSessionStatus(text, className = '') {
    sessionStatus.textContent = text;
    sessionStatus.className = `session-status${className ? ` ${className}` : ''}`;
  }

  function sessionRowNode(row) {
    const item = make('li');
    const button = make('button', `session-row${row.current ? ' is-current' : ''}`);
    button.type = 'button';
    if (row.current) button.setAttribute('aria-current', 'true');
    button.append(make('span', 'session-title', row.title));
    const meta = make('span', 'session-meta', `${row.time} · ${row.runs} · #${row.shortId}`);
    // A title is the session's first message, so two sessions can share one.
    // The id prefix is what tells them apart, and the current one is said in
    // words rather than only in colour.
    if (row.current) meta.append(make('span', 'session-current', '当前'));
    button.append(meta);
    button.disabled = running || switching;
    button.addEventListener('click', () => switchSession(row.id));
    item.append(button);
    return item;
  }

  function renderSessions(payload) {
    sessionsPayload = payload;
    const rows = sessionRows(payload, currentSessionID);
    sessionList.replaceChildren();
    for (const row of rows) sessionList.append(sessionRowNode(row));
    $('sessions-empty').hidden = rows.length > 0;
  }

  function rerenderSessions() {
    if (sessionsPayload === null) return;
    renderSessions(sessionsPayload);
  }

  async function updateSessions() {
    try {
      const response = await fetch('/api/sessions', { cache: 'no-store' });
      if (!response.ok) throw new Error(await errorMessage(response));
      renderSessions(await response.json());
    } catch (error) {
      setSessionStatus(`无法读取会话列表：${error.message}`, 'failure');
    }
  }

  // A single place decides whether the composer and the session controls accept
  // input: a run and a replay in flight both block the controls that would mix
  // two states.
  function setSessionControls() {
    send.disabled = running || switching;
    sessionNew.disabled = running || switching;
    rerenderSessions();
  }

  function resetConversation() {
    conversation.replaceChildren();
    sessionNotices.replaceChildren();
    sessionNotices.hidden = true;
    emptyState.hidden = false;
    latest.hidden = true;
    currentTurn = null;
    openTools = [];
    setRunStatus('');
  }

  function toolRowNode(tool) {
    const details = make('details', `tool-row${tool.failed ? ' failed' : ''}`);
    const summary = make('summary', '', toolActivityLabel(tool.name, tool.failed ? 'failed' : 'finished'));
    const detail = make('div', 'tool-detail');
    const list = make('dl');
    for (const fact of toolCallFacts(tool)) appendDefinition(list, fact.label, fact.value);
    detail.append(list);
    details.append(summary, detail);
    return details;
  }

  function assistantReplayNode(record) {
    const node = assistantTurnNode();
    for (const tool of record.tools) node.tools.append(toolRowNode(tool));
    if (!record.tools.length) node.tools.remove();
    node.body.classList.remove('placeholder');
    if (record.answer) {
      renderMarkdown(node.body, record.answer);
    } else if (record.failed) {
      // A run that ended without an answer still has to be visible as such.
      node.body.classList.add('failed');
      node.body.textContent = runStatusLabel(record.status);
    } else if (!record.tools.length) {
      node.body.textContent = '这条记录没有内容。';
    } else {
      node.body.remove();
    }
    return node.turn;
  }

  function renderReplayedSession(detail) {
    const replay = replaySession(detail);
    conversation.replaceChildren();
    sessionNotices.replaceChildren();
    for (const notice of replay.notices) sessionNotices.append(make('p', 'session-notice', notice));
    if (!replay.turns.length && !replay.notices.length) {
      sessionNotices.append(make('p', 'session-notice', '这个会话还没有可回放的记录。'));
    }
    sessionNotices.hidden = sessionNotices.childElementCount === 0;
    for (const turn of replay.turns) {
      if (turn.role === 'user') conversation.append(userTurnNode(turn.text));
      else if (turn.role === 'note') conversation.append(make('p', 'turn-note', turn.text));
      else conversation.append(assistantReplayNode(turn));
    }
    currentTurn = null;
    openTools = [];
    // The empty state is shown only when there is genuinely nothing to read.
    emptyState.hidden = sessionNotices.childElementCount > 0 || replay.turns.length > 0;
    latest.hidden = true;
    transcript.scrollTop = transcript.scrollHeight;
  }

  // loadSession replays one session into the conversation area. Until the read
  // returns, no session is claimed: the id only becomes current when the server
  // has confirmed it, and a failure states itself instead of showing an empty
  // conversation as if the session were empty.
  async function loadSession(id) {
    currentSessionID = id;
    rerenderSessions();
    switching = true;
    setSessionControls();
    setSessionStatus('正在恢复会话…');
    try {
      const response = await fetch(`/api/sessions/${id}`, { cache: 'no-store' });
      if (!response.ok) throw new Error(await errorMessage(response));
      renderReplayedSession(await response.json());
      setSessionStatus(`已恢复会话 #${id.slice(0, 8)}。`);
    } catch (error) {
      dropSession();
      setSessionStatus(`无法读取这个会话：${error.message}`, 'failure');
    } finally {
      switching = false;
      setSessionControls();
    }
  }

  // dropSession leaves the URL with no session in it and the area empty, which
  // is the truth after a new session is started or a stored one is gone.
  function dropSession() {
    currentSessionID = '';
    history.replaceState(null, '', `${location.pathname}${location.search}`);
    resetConversation();
    rerenderSessions();
  }

  function newSession() {
    if (running || switching) return;
    dropSession();
    setSessionStatus('新会话：发送第一条消息后开始记录。');
  }

  function switchSession(id) {
    if (running || switching || !isSessionID(id) || id === currentSessionID) return;
    // The hash is what carries the session, so a switch is a URL change; the
    // hashchange handler is what performs the replay.
    location.hash = sessionHash(id);
  }

  // adoptSession records the id a fresh session was given. It arrives on
  // run.started, so the hash names the session the answer belongs to.
  function adoptSession(id) {
    if (!isSessionID(id) || id === currentSessionID) return;
    currentSessionID = id;
    location.hash = sessionHash(id);
    rerenderSessions();
    setSessionStatus(`已开始新会话 #${id.slice(0, 8)}。`);
  }

  async function applySessionHash() {
    const id = parseSessionHash(location.hash);
    if (id === currentSessionID) {
      // A fragment that cannot be a session id is not left in the address bar
      // to be copied or refreshed into a request that would be rejected.
      if (!id && location.hash.startsWith('#session')) {
        history.replaceState(null, '', `${location.pathname}${location.search}`);
        setSessionStatus('链接里的会话 id 无法识别，已按新会话开始。', 'failure');
      }
      return;
    }
    if (running || switching) {
      // A run is streaming into the current session; honouring the fragment now
      // would draw two sessions into one area. The hash is put back instead.
      const restored = sessionHash(currentSessionID);
      history.replaceState(null, '', restored ? `${location.pathname}${location.search}${restored}` : `${location.pathname}${location.search}`);
      setSessionStatus('运行中无法切换会话。', 'failure');
      return;
    }
    if (!id) {
      dropSession();
      setSessionStatus('新会话：发送第一条消息后开始记录。');
      return;
    }
    await loadSession(id);
  }

  sessionNew.addEventListener('click', newSession);
  window.addEventListener('hashchange', applySessionHash);

  // --- Memory in the drawer ------------------------------------------------
  //
  // The user's own view of the durable facts: what is stored, and a way to
  // retract one. There is deliberately no way to add or edit a fact here — a
  // fact is written by the model through luna_remember — and retracting posts
  // the stored text and timestamp back unchanged, so the store can name exactly
  // one fact rather than trusting the page to point at "the fourth one".

  let memoryView = { facts: [], retracted: [] };
  let memoryReading = false;

  function setMemoryStatus(text, className = '') {
    memoryStatus.textContent = text;
    memoryStatus.className = `ui-plugin-status${className ? ` ${className}` : ''}`;
    memoryStatus.hidden = !text;
  }

  function memoryRowNode(row, index) {
    const item = make('li', 'memory-item');
    item.append(make('p', 'memory-text', row.text));
    item.append(make('p', 'memory-meta', `${row.time} · 来自 ${row.session}`));
    const button = make('button', 'memory-retract', '撤回');
    button.type = 'button';
    button.dataset.memoryIndex = String(index);
    button.addEventListener('click', () => retractFact(row, button));
    item.append(button);
    return item;
  }

  function renderMemory(note = '') {
    memoryList.replaceChildren();
    memoryView.facts.forEach((row, index) => memoryList.append(memoryRowNode(row, index)));
    memoryEmpty.hidden = memoryView.facts.length > 0;
    const parts = [];
    if (memoryView.retracted.length > 0) parts.push(`已撤回 ${memoryView.retracted.length} 条`);
    if (note) parts.push(note);
    setMemoryStatus(parts.join(' · '));
  }

  async function updateMemory() {
    if (memoryReading) return;
    memoryReading = true;
    try {
      const response = await fetch('/api/memory', { cache: 'no-store' });
      if (!response.ok) throw new Error(await errorMessage(response));
      memoryView = memoryRows(await response.json());
      renderMemory();
    } catch (error) {
      setMemoryStatus(`无法读取记忆：${error.message}`, 'failure');
    } finally {
      memoryReading = false;
    }
  }

  async function retractFact(row, button) {
    const payload = retractPayload(row);
    if (payload === null) {
      setMemoryStatus('这一条缺少时间或内容，无法撤回。', 'failure');
      return;
    }
    button.disabled = true;
    try {
      const response = await fetch('/api/memory/retract', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      });
      if (!response.ok) throw new Error(await errorMessage(response));
      await updateMemory();
      renderMemory('这一条已撤回，下一次运行不再注入它。');
    } catch (error) {
      button.disabled = false;
      setMemoryStatus(`撤回失败：${error.message}`, 'failure');
    }
  }

  applySessionHash();
  updateSessions();
  updateState();
  setInterval(updateState, 2000);
}
