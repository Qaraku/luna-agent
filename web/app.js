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

// 宿主只主动提供契约版本与有界日志，不提供会话 API 或状态。
// 同源模块不是沙箱：只应启用可信的本地插件，样式作用域也不提供权限隔离。
function uiPluginHostAPI(version, log) {
  const write = typeof log === 'function' ? log : () => {};
  return Object.freeze({
    version: uiPluginText(version),
    log(message) {
      write(uiPluginText(message));
    }
  });
}

// --- 能力贡献的浏览器面板 -------------------------------------------------
//
// 宿主不认识任何一个具体能力。它只从 /api/state 读到：某个能力是否在用，以及
// 它是否贡献了浏览器面板、面板模块在哪个入口。面板说什么、怎么布局由模块自己
// 决定，能力停用时入口与面板一起消失，因为那时它什么都不再贡献。

// capabilityPanelText is the one reading of a payload string, so a panel that
// arrives with a number or an object where a string belongs becomes an empty
// value instead of a control with "[object Object]" on it.
function capabilityPanelText(value) {
  return typeof value === 'string' ? value.trim() : '';
}

// capabilityPanelEntryURL is the only place a panel module's import specifier
// is built. The kernel serves the module from this origin, so only an absolute
// same-origin path is importable here: a relative specifier would be resolved
// against the page, and a protocol-relative one would leave the host entirely.
// `%`, `?`, `#` and backslashes are refused so a hand-edited payload cannot
// smuggle a different path past the check, and a caller that gets '' must not
// import at all.
function capabilityPanelEntryURL(entry) {
  const value = capabilityPanelText(entry);
  if (!value || value.length > 200) return '';
  if (!value.startsWith('/') || value.startsWith('//')) return '';
  if (/[\u0000-\u001f\u007f\\?#%]/.test(value)) return '';
  return value;
}

// capabilityPanelElementID is the fixed derivation the fixtures rely on: the
// panel a capability contributes under the id `notes` is `capability-panel-notes`.
// The header entry points at it with aria-controls and carries no id of its own.
function capabilityPanelElementID(id) {
  return `capability-panel-${id}`;
}

// capabilityPanels turns the state payload's `capabilities` array into the panel
// entries the header should offer, in the order the kernel registered them.
// Only an enabled capability contributes anything, so a disabled one is skipped
// whole. A capability without a panels array, a payload that is not an array,
// and a panel missing an id or an entry are all dropped rather than rendered as
// a control that could not work.
function capabilityPanels(capabilities) {
  if (!Array.isArray(capabilities)) return [];
  const panels = [];
  for (const entry of capabilities) {
    const capability = entry && typeof entry === 'object' ? entry : null;
    if (!capability || capability.state !== 'enabled') continue;
    const list = Array.isArray(capability.panels) ? capability.panels : [];
    for (const value of list) {
      const panel = value && typeof value === 'object' ? value : null;
      if (!panel) continue;
      const id = capabilityPanelText(panel.id);
      const panelEntry = capabilityPanelText(panel.entry);
      if (!id || !panelEntry) continue;
      panels.push({ id, title: capabilityPanelText(panel.title) || id, entry: panelEntry });
    }
  }
  return panels;
}

// 一个贡献面板的失败文字：哪一块面板、哪一步没走通。宿主不为失败的面板编内容，
// 也不把别的能力的名字借给它。
function capabilityPanelEntryError(title) {
  return `能力面板 ${title} 的入口地址无法识别，未加载。`;
}

function capabilityPanelImportError(title, detail) {
  return `无法加载能力面板 ${title} 的模块：${detail}`;
}

function capabilityPanelMissingExportError(title, missing) {
  const list = Array.isArray(missing) ? missing.join('、') : capabilityPanelText(missing);
  return `能力面板 ${title} 缺少必需的导出 ${list}。`;
}

function capabilityPanelMountError(title, detail) {
  return `能力面板 ${title} 挂载失败：${detail}`;
}

function capabilityPanelUnmountError(title, detail) {
  return `能力面板 ${title} 关闭时清理失败，容器已移除：${detail}`;
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

// 会话列表里的时间是次要信息，用相对量级更容易扫读。
// 解析失败按缺失处理，不猜一个时间出来；now 可注入，便于测试。
function relativeTime(value, now = Date.now()) {
  const at = Date.parse(typeof value === 'string' ? value : '');
  if (Number.isNaN(at)) return '—';
  const seconds = Math.max(0, Math.round((now - at) / 1000));
  if (seconds < 60) return '刚刚';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} 天前`;
  const months = Math.floor(days / 30);
  if (months < 12) return `${months} 个月前`;
  return `${Math.floor(months / 12)} 年前`;
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
function sessionRows(payload, currentID, now = Date.now()) {
  const list = payload && typeof payload === 'object' && Array.isArray(payload.sessions) ? payload.sessions : [];
  const rows = [];
  for (const entry of list) {
    const record = entry && typeof entry === 'object' ? entry : {};
    if (!isSessionID(record.id)) continue;
    rows.push({
      id: record.id,
      shortId: record.id.slice(0, 8),
      title: sessionTitle(record.title),
      time: relativeTime(record.updated_at, now),
      runs: runCountLabel(record.run_count),
      current: record.id === currentID
    });
  }
  return rows;
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
  module.exports = {
    parseEventBlock, toolSummary, toolLabel, toolActivityLabel, candidateLabel, reloadCopy, valueOrDash,
    pluginStatusLabel, pluginRows, parseInline, parseMarkdownBlocks,
    isSessionID, parseSessionHash, sessionHash, sessionTitle, sessionTime, relativeTime, runCountLabel,
    runStatusLabel, sessionRows, argumentsText, toolCallFacts, replaySession, runPayload,
    uiPluginText, uiPluginNameValid, uiPluginEntrySafe, uiPluginEntryURL, uiPluginRows, uiPluginMissingExports,
    uiPluginErrorDetail, uiPluginImportError, uiPluginMissingExportError, uiPluginMountError, uiPluginUnmountError,
    uiPluginState, uiPluginInitialState, uiPluginTransition, uiPluginEnableFailureEvent, uiPluginDisableEvent,
    uiPluginTeardown, uiPluginAbandonMount, uiPluginToggleAction, uiPluginToggleLabel, uiPluginStatusText,
    uiPluginHostAPI, UI_PLUGIN_API_VERSION, UI_PLUGIN_ENTRY_REASON, UI_PLUGIN_REQUIRED_EXPORTS,
    capabilityPanelText, capabilityPanelEntryURL, capabilityPanelElementID, capabilityPanels,
    capabilityPanelEntryError, capabilityPanelImportError, capabilityPanelMissingExportError,
    capabilityPanelMountError, capabilityPanelUnmountError
  };
}

if (typeof document !== 'undefined') {
  const $ = (id) => document.getElementById(id);
  // 外观是唯一持久化的浏览器设置；切换只改根 token，不重建会话或插件。
  const themeMedia = window.matchMedia('(prefers-color-scheme: dark)');
  // 外观只有一个入口：设置面板里的 #theme-select。
  const themeControls = [$('theme-select')];
  const validTheme = (value) => ['light', 'dark', 'system'].includes(value) ? value : 'system';
  let themePreference = 'system';
  try { themePreference = validTheme(window.localStorage.getItem('luna.theme')); } catch (_) {}
  function applyTheme() {
    document.documentElement.dataset.theme = themePreference === 'system'
      ? (themeMedia.matches ? 'dark' : 'light') : themePreference;
    for (const control of themeControls) control.value = themePreference;
  }
  for (const control of themeControls) {
    control.addEventListener('change', () => {
      themePreference = validTheme(control.value);
      // 存储被禁用时仍尊重本页选择；刷新后按系统恢复，不让设置操作失效。
      try { window.localStorage.setItem('luna.theme', themePreference); } catch (_) {}
      applyTheme();
    });
  }
  themeMedia.addEventListener('change', () => { if (themePreference === 'system') applyTheme(); });
  applyTheme();
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
  const sessionSidebar = $('session-sidebar');
  const sessionToggle = $('session-toggle');
  const sessionClose = $('session-close');
  // 贡献面板的入口插在扩展之前，所以这里要拿着这两个节点：一个是插入锚点，
  // 一个说明页头顺序（贡献的面板都在扩展左边）。
  const extensionsToggle = $('extensions-toggle');
  const runtimeActions = document.querySelector('.runtime-actions');
  const sessionMedia = window.matchMedia('(max-width: 800px)');
  const panels = {
    runtime: { element: runtimeDrawer, toggle: runtimeToggle, close: runtimeClose, refresh: updateState },
    extensions: { element: $('extensions-panel'), toggle: extensionsToggle, close: $('extensions-close'), refresh: updateUIPlugins },
    sessions: { element: sessionSidebar, toggle: sessionToggle, close: sessionClose, refresh: updateSessions },
    settings: { element: $('settings-panel'), toggle: $('settings-toggle'), close: $('settings-close'), refresh: () => {} }
  };
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
  const uiPluginsEmpty = $('ui-plugins-empty');
  const uiPluginStatus = $('ui-plugins-status');
  const uiPluginsRetry = $('ui-plugins-retry');

  let running = false;
  let reloading = false;
  let currentTurn = null;
  let openTools = [];
  let lastFocused = null;
  let activePanel = null;
  // switching guards a session replay in flight; sessionsPayload is the last
  // good list, so a busy flag can re-render the rows without a second request.
  let switching = false;
  let sessionsPayload = null;
  let currentSessionID = '';

  // --- 侧栏折叠与宽度 ---------------------------------------------------------
  // 只存在浏览器本地；桌面端生效，窄屏始终走 drawer（见 CSS 的 min-width 查询）。
  const sidebarCollapse = $('sidebar-collapse');
  const sidebarResizer = $('sidebar-resizer');
  const SIDEBAR_MIN = 200;
  const SIDEBAR_MAX = 420;
  const SIDEBAR_DEFAULT = 244;
  const clampSidebarWidth = (value) =>
    Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, Math.round(value)));
  const readStoredSidebar = (key, fallback) => {
    try {
      const saved = window.localStorage.getItem(key);
      return saved === null ? fallback : saved;
    } catch (_) {
      return fallback;
    }
  };
  const storedWidth = Number(readStoredSidebar('luna.sidebarWidth', String(SIDEBAR_DEFAULT)));
  let sidebarWidth = Number.isFinite(storedWidth) && storedWidth > 0 ? clampSidebarWidth(storedWidth) : SIDEBAR_DEFAULT;
  let sidebarCollapsed = readStoredSidebar('luna.sidebar', 'expanded') === 'collapsed';
  let resizingSidebar = false;

  function applySidebar() {
    // 折叠就是把这条宽度归零，主内容自然接管整块空间，不留空白。
    document.documentElement.style.setProperty('--luna-sidebar-w', sidebarCollapsed ? '0px' : `${sidebarWidth}px`);
    document.documentElement.dataset.sidebar = sidebarCollapsed ? 'collapsed' : 'expanded';
    sidebarResizer.setAttribute('aria-valuenow', String(sidebarWidth));
    sidebarResizer.setAttribute('aria-valuemin', String(SIDEBAR_MIN));
    sidebarResizer.setAttribute('aria-valuemax', String(SIDEBAR_MAX));
    syncSessionLayout();
  }

  function setSidebarCollapsed(value) {
    sidebarCollapsed = Boolean(value);
    try { window.localStorage.setItem('luna.sidebar', sidebarCollapsed ? 'collapsed' : 'expanded'); } catch (_) {}
    applySidebar();
  }

  function setSidebarWidth(value) {
    sidebarWidth = clampSidebarWidth(value);
    try { window.localStorage.setItem('luna.sidebarWidth', String(sidebarWidth)); } catch (_) {}
    applySidebar();
  }

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
    if (!currentSessionID) setConversationTitle(text);
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
    sessionSidebar.inert = value && activePanel !== panels.sessions;
  }

  function canFocus(node) {
    return node?.isConnected && !node.disabled && !node.closest('[inert]') && node.getClientRects().length > 0;
  }

  function panelFocusables() {
    return [...activePanel.element.querySelectorAll('button, [href], input, select, textarea, summary, [tabindex]')]
      .filter((node) => node.tabIndex >= 0 && canFocus(node));
  }

  function openDrawer(panel) {
    if (panel === panels.sessions && !sessionMedia.matches) return;
    if (activePanel === panel) return;
    // 面板共用一个模态层；立即隐藏旧面板，避免关闭动画留下可聚焦的控件。
    closeDrawer(false);
    activePanel = panel;
    lastFocused = panel.toggle;
    panel.element.hidden = false;
    runtimeBackdrop.hidden = false;
    panel.toggle.setAttribute('aria-expanded', 'true');
    setBackgroundInert(true);
    panel.close.focus();
    panel.refresh();
    requestAnimationFrame(() => {
      if (activePanel !== panel) return;
      panel.element.classList.add('is-open');
      runtimeBackdrop.classList.add('is-open');
    });
  }

  function closeDrawer(restoreFocus = true) {
    if (!activePanel) return;
    const panel = activePanel;
    activePanel = null;
    // 面板可以带自己的收尾：贡献面板就在这里跑模块的 unmount 并移除容器。
    if (typeof panel.teardown === 'function') panel.teardown();
    panel.element.classList.remove('is-open');
    panel.element.hidden = true;
    runtimeBackdrop.classList.remove('is-open');
    runtimeBackdrop.hidden = true;
    panel.toggle.setAttribute('aria-expanded', 'false');
    setBackgroundInert(false);
    if (restoreFocus) (canFocus(lastFocused) ? lastFocused : input).focus();
  }

  function syncSessionLayout() {
    const focused = document.activeElement;
    const wasInSidebar = sessionSidebar.contains(focused);
    if (!sessionMedia.matches && activePanel === panels.sessions) closeDrawer(false);
    sessionSidebar.hidden = sessionMedia.matches && activePanel !== panels.sessions;
    // 窄屏常驻（drawer 入口）；桌面端只在侧栏折叠时出现，作为展开入口。
    sessionToggle.hidden = sessionMedia.matches ? false : !sidebarCollapsed;
    sessionClose.hidden = !sessionMedia.matches;
    if (sessionMedia.matches) {
      sessionSidebar.setAttribute('role', 'dialog');
      sessionSidebar.setAttribute('aria-modal', 'true');
    } else {
      sessionSidebar.removeAttribute('role');
      sessionSidebar.removeAttribute('aria-modal');
    }
    setBackgroundInert(Boolean(activePanel));
    if (wasInSidebar && !canFocus(focused)) {
      (sessionMedia.matches ? sessionToggle : sessionNew).focus();
    } else if (wasInSidebar && document.activeElement !== focused && canFocus(focused)) {
      focused.focus();
    }
  }

  for (const panel of Object.values(panels)) {
    panel.toggle.addEventListener('click', () => openDrawer(panel));
    panel.close.addEventListener('click', () => closeDrawer());
  }
  // 设置面板是统一详情入口：详细的运行信息仍由运行详情呈现。
  $('settings-runtime').addEventListener('click', () => openDrawer(panels.runtime));
  runtimeBackdrop.addEventListener('click', () => closeDrawer());

  // 折叠、拖拽与展开都由同一个宽度变量驱动。
  sidebarCollapse.addEventListener('click', () => setSidebarCollapsed(true));
  sessionToggle.addEventListener('click', () => {
    // 桌面端折叠后，页头左边这个按钮就是展开入口；窄屏仍然是打开 drawer。
    if (!sessionMedia.matches && sidebarCollapsed) setSidebarCollapsed(false);
  });
  sidebarResizer.addEventListener('pointerdown', (event) => {
    if (sessionMedia.matches || event.button !== 0) return;
    event.preventDefault();
    resizingSidebar = true;
    document.documentElement.dataset.resizing = 'true';
    if (sidebarResizer.setPointerCapture) sidebarResizer.setPointerCapture(event.pointerId);
  });
  sidebarResizer.addEventListener('pointermove', (event) => {
    if (!resizingSidebar) return;
    // 侧栏贴着窗口左边，指针的 x 就是想要的宽度。
    setSidebarWidth(event.clientX);
  });
  const endSidebarResize = () => {
    if (!resizingSidebar) return;
    resizingSidebar = false;
    delete document.documentElement.dataset.resizing;
  };
  sidebarResizer.addEventListener('pointerup', endSidebarResize);
  sidebarResizer.addEventListener('pointercancel', endSidebarResize);
  sidebarResizer.addEventListener('keydown', (event) => {
    // 键盘也能调整：方向键 8px，按住 Shift 32px，Home/End 到两端。
    const step = event.shiftKey ? 32 : 8;
    const moves = {
      ArrowLeft: -step,
      ArrowRight: step,
      Home: SIDEBAR_MIN - sidebarWidth,
      End: SIDEBAR_MAX - sidebarWidth
    };
    if (!(event.key in moves)) return;
    event.preventDefault();
    setSidebarWidth(sidebarWidth + moves[event.key]);
  });

  // 设置模态的分类导航：同一时刻只有一个 pane 可见，方向键在同一组 tab 内移动。
  const settingsTabs = [...$('settings-panel').querySelectorAll('[role="tab"]')];
  function selectSettingsTab(tab) {
    if (!tab) return;
    for (const item of settingsTabs) {
      const active = item === tab;
      item.setAttribute('aria-selected', active ? 'true' : 'false');
      // 用属性而不是 tabIndex 属性赋值：两者在浏览器里等价，但属性写法更明确。
      item.setAttribute('tabindex', active ? '0' : '-1');
      const pane = $(item.dataset.pane);
      if (pane) pane.hidden = !active;
    }
  }
  for (const tab of settingsTabs) tab.addEventListener('click', () => selectSettingsTab(tab));
  $('settings-panel').addEventListener('keydown', (event) => {
    const index = settingsTabs.indexOf(document.activeElement);
    if (index < 0) return;
    const target =
      event.key === 'ArrowDown' ? settingsTabs[(index + 1) % settingsTabs.length]
      : event.key === 'ArrowUp' ? settingsTabs[(index - 1 + settingsTabs.length) % settingsTabs.length]
      : event.key === 'Home' ? settingsTabs[0]
      : event.key === 'End' ? settingsTabs[settingsTabs.length - 1]
      : null;
    if (!target) return;
    event.preventDefault();
    target.focus();
    selectSettingsTab(target);
  });
  // 模态占满视口，点对话框之外的空白就关闭。
  $('settings-panel').addEventListener('click', (event) => {
    if (event.target === $('settings-panel')) closeDrawer();
  });
  selectSettingsTab(settingsTabs[0]);
  applySidebar();
  document.addEventListener('keydown', (event) => {
    if (!activePanel) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      closeDrawer();
    } else if (event.key === 'Tab') {
      const nodes = panelFocusables();
      const index = nodes.indexOf(document.activeElement);
      if (!nodes.length || index < 0 || (event.shiftKey ? index === 0 : index === nodes.length - 1)) {
        event.preventDefault();
        (nodes[event.shiftKey ? nodes.length - 1 : 0] || activePanel.element).focus();
      }
    }
  });
  document.addEventListener('focusin', (event) => {
    if (activePanel && !activePanel.element.contains(event.target)) activePanel.close.focus();
  });
  sessionMedia.addEventListener('change', () => {
    syncSessionLayout();
    if (!sessionSidebar.hidden) updateSessions();
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

    // 页头入口与面板容器只随这一份状态变化。一次读不到状态时这里不会被调用，
    // 所以"读不到"不会被当成"停用"，页头保持原样。
    syncCapabilityPanels(capabilityPanels(state.capabilities));
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
    // 各列表只跟随自己的可见性刷新，不再依赖运行详情。
    if (!sessionSidebar.hidden) updateSessions();
  }

  // --- 会话侧栏与地址栏 ---------------------------------------------------

  function setSessionStatus(text, className = '') {
    sessionStatus.textContent = text;
    sessionStatus.className = `session-status${className ? ` ${className}` : ''}`;
  }

  // 用稳定标识复用列表控件，避免轮询在按下与点击之间替换节点或丢失键盘焦点。
  function reconcileList(list, rows, keyFor, createNode, updateNode, fallback) {
    const focused = document.activeElement;
    const hadFocus = list.contains(focused);
    const existing = new Map([...list.children].map((node) => [node.dataset.rowKey, node]));
    rows.forEach((row, index) => {
      const key = keyFor(row);
      const node = existing.get(key) || createNode(row);
      existing.delete(key);
      node.dataset.rowKey = key;
      updateNode(node, row, index);
      if (list.children[index] !== node) list.insertBefore(node, list.children[index] || null);
    });
    for (const node of existing.values()) node.remove();
    if (hadFocus && document.activeElement !== focused) {
      const target = canFocus(focused) ? focused : fallback;
      if (canFocus(target)) target.focus();
    }
  }

  function sessionRowNode(row) {
    const item = make('li');
    const button = make('button', 'session-row');
    button.type = 'button';
    button.append(make('span', 'session-title'), make('span', 'session-meta'));
    button.addEventListener('click', () => switchSession(row.id));
    item.append(button);
    return item;
  }

  function updateSessionRow(item, row) {
    const button = item.querySelector('button');
    button.classList.toggle('is-current', row.current);
    if (row.current) button.setAttribute('aria-current', 'true');
    else button.removeAttribute('aria-current');
    item.querySelector('.session-title').textContent = row.title;
    const meta = item.querySelector('.session-meta');
    meta.textContent = row.time === '—' ? '' : row.time;
    button.setAttribute('title', `${row.title}\n#${row.id} · ${row.runs}`);
    button.disabled = running || switching;
  }

  function renderSessions(payload) {
    sessionsPayload = payload;
    const rows = sessionRows(payload, currentSessionID);
    const current = rows.find((row) => row.current);
    if (current) setConversationTitle(current.title);
    reconcileList(sessionList, rows, (row) => row.id, sessionRowNode, updateSessionRow, sessionNew);
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

  function setConversationTitle(text = '新会话') {
    const title = sessionTitle(text);
    $('conversation-title').textContent = title;
    $('conversation-title').setAttribute('title', title);
  }

  function resetConversation() {
    setConversationTitle();
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
    setConversationTitle(replay.title);
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
      setSessionStatus('已恢复会话。');
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
    if (activePanel === panels.sessions) closeDrawer();
    dropSession();
    setSessionStatus('新会话：发送第一条消息后开始记录。');
  }

  function switchSession(id) {
    if (running || switching || !isSessionID(id)) return;
    if (activePanel === panels.sessions) closeDrawer();
    if (id === currentSessionID) return;
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
    setSessionStatus('会话已开始记录。');
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

  // --- 能力贡献的面板 ------------------------------------------------------
  //
  // 宿主不认识任何一个具体能力：它从 /api/state 只知道某个启用中的能力声明了
  // 面板，以及面板模块的入口。入口与容器都跟着这份状态走；开合、背景遮罩、
  // Escape、焦点约束和"一次只开一个"都由上面那套面板机制负责，这里只做三件事：
  // 按状态建/拆入口与容器、打开时挂载模块、关闭时卸载。

  const SVG_NAMESPACE = 'http://www.w3.org/2000/svg';

  // 面板 id 由能力自己给。加一层前缀再登记进 panels，就不会撞上宿主自己的
  // runtime / extensions / sessions / settings。
  const capabilityPanelKey = (id) => `capability:${id}`;

  // 键是面板 id；每条记录是宿主为这个面板拥有的全部 DOM 与当前挂载状态。
  const capabilityPanelNodes = new Map();

  // 贡献面板的页头入口共用一个中性图标：具体是什么面板由 title 与 aria-label
  // 说明，宿主不为某个能力画它自己的标志。
  function capabilityPanelIcon() {
    const svg = document.createElementNS(SVG_NAMESPACE, 'svg');
    svg.setAttribute('class', 'ui-icon');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', '1.6');
    svg.setAttribute('aria-hidden', 'true');
    const frame = document.createElementNS(SVG_NAMESPACE, 'rect');
    frame.setAttribute('x', '4');
    frame.setAttribute('y', '5');
    frame.setAttribute('width', '16');
    frame.setAttribute('height', '14');
    frame.setAttribute('rx', '2');
    const divider = document.createElementNS(SVG_NAMESPACE, 'path');
    divider.setAttribute('d', 'M4 10h16');
    svg.append(frame, divider);
    return svg;
  }

  // 一次挂载是否还算数：面板仍然开着，而且没有被"关闭后重新打开"打断过。
  function capabilityPanelLive(record, generation) {
    return record.open && record.generation === generation;
  }

  // 一次没有走通的挂载：容器由宿主移除，面板 body 里留下这一条失败本身，
  // 而不是让整页报错。
  function failCapabilityPanel(record, target, message) {
    record.target = null;
    record.module = null;
    target.remove();
    record.body.replaceChildren(make('p', 'capability-panel-error', message), record.log);
  }

  // 打开时才挂载：模块随每一次打开重新 import 并 mount，所以面板上的数据总是
  // 这一次读到的，而不是上一次留下的。
  async function mountCapabilityPanel(record) {
    record.open = true;
    if (record.module) return;
    record.generation += 1;
    const generation = record.generation;
    const target = make('div', 'capability-panel-target');
    record.body.replaceChildren(target, record.log);
    record.target = target;
    const url = capabilityPanelEntryURL(record.entry);
    if (!url) {
      failCapabilityPanel(record, target, capabilityPanelEntryError(record.title));
      return;
    }
    const api = uiPluginHostAPI(UI_PLUGIN_API_VERSION, (message) => uiPluginLogLine(record, message));
    let capabilityModule = null;
    try {
      capabilityModule = await import(url);
    } catch (error) {
      if (!capabilityPanelLive(record, generation)) return;
      failCapabilityPanel(record, target, capabilityPanelImportError(record.title, uiPluginErrorDetail(error)));
      return;
    }
    // 模块还没挂上就被关掉了：它已经被丢掉，这里不再挂载，免得它把自己的样式
    // 或定时器留在一个已经不存在的容器上。
    if (!capabilityPanelLive(record, generation)) return;
    const missing = uiPluginMissingExports(capabilityModule);
    if (missing.length) {
      failCapabilityPanel(record, target, capabilityPanelMissingExportError(record.title, missing));
      return;
    }
    try {
      capabilityModule.mount(target, api);
    } catch (error) {
      failCapabilityPanel(record, target, capabilityPanelMountError(record.title, uiPluginErrorDetail(error)));
      return;
    }
    record.module = capabilityModule;
  }

  // 关闭时的收尾：先跑模块自己的 unmount，再移除宿主创建的容器并丢掉模块引用，
  // 所以下次打开是一次全新的挂载。容器由宿主移除，不取决于模块是否听话；模块
  // 清理失败时把这一条记进面板自己的日志，而不是被吞掉。
  function teardownCapabilityPanel(record) {
    record.open = false;
    record.generation += 1;
    const target = record.target;
    const capabilityModule = record.module;
    record.target = null;
    record.module = null;
    if (!target) return;
    let detail = '';
    if (capabilityModule && typeof capabilityModule.unmount === 'function') {
      try {
        capabilityModule.unmount(target);
      } catch (error) {
        detail = uiPluginErrorDetail(error);
      }
    }
    target.remove();
    record.body.replaceChildren(record.log);
    if (detail) uiPluginLogLine(record, capabilityPanelUnmountError(record.title, detail));
  }

  function createCapabilityPanel(panel) {
    const elementID = capabilityPanelElementID(panel.id);
    const drawer = make('aside', 'runtime-drawer');
    drawer.id = elementID;
    drawer.setAttribute('role', 'dialog');
    drawer.setAttribute('aria-modal', 'true');
    drawer.setAttribute('aria-labelledby', `${elementID}-title`);
    drawer.setAttribute('tabindex', '-1');
    drawer.hidden = true;

    const header = make('header', 'drawer-header');
    const heading = make('h2', '', panel.title);
    heading.id = `${elementID}-title`;
    const close = make('button', 'icon-button', '关闭');
    close.type = 'button';
    close.setAttribute('aria-label', `关闭${panel.title}`);
    header.append(heading, close);

    const body = make('div', 'drawer-body');
    // 模块自己的日志走宿主已有的那一条路径：有界，默认隐藏，不逐行播报。
    const log = make('ul', 'ui-plugin-log');
    log.hidden = true;
    body.append(log);
    drawer.append(header, body);

    const button = make('button', 'icon-button capability-panel-toggle');
    button.type = 'button';
    button.setAttribute('aria-controls', elementID);
    button.setAttribute('aria-expanded', 'false');
    button.setAttribute('aria-label', panel.title);
    button.setAttribute('title', panel.title);
    button.append(capabilityPanelIcon());

    const record = {
      key: capabilityPanelKey(panel.id),
      id: panel.id,
      title: panel.title,
      entry: panel.entry,
      button,
      drawer,
      heading,
      close,
      body,
      log,
      target: null,
      module: null,
      open: false,
      generation: 0,
      panel: null
    };
    record.panel = {
      element: drawer,
      toggle: button,
      close,
      refresh: () => { mountCapabilityPanel(record); },
      teardown: () => { teardownCapabilityPanel(record); }
    };
    button.addEventListener('click', () => openDrawer(record.panel));
    close.addEventListener('click', () => closeDrawer());
    capabilityPanelNodes.set(panel.id, record);
    panels[record.key] = record.panel;
    // 贡献面板固定在扩展之前：页头顺序与它们被声明的顺序一致。
    runtimeActions.insertBefore(button, extensionsToggle);
    document.body.append(drawer);
    return record;
  }

  // 每一轮状态只更新它已经认识的面板：已存在的入口与容器不动，所以一个打开中
  // 的面板不会被轮询重置。
  function updateCapabilityPanel(record, panel) {
    const titleChanged = record.title !== panel.title;
    const entryChanged = record.entry !== panel.entry;
    record.title = panel.title;
    record.entry = panel.entry;
    if (titleChanged) {
      record.heading.textContent = panel.title;
      record.button.setAttribute('aria-label', panel.title);
      record.button.setAttribute('title', panel.title);
      record.close.setAttribute('aria-label', `关闭${panel.title}`);
    }
    // 入口换了就重来一次：已经挂载的模块不能继续代表新的入口。
    if (entryChanged && activePanel === record.panel) {
      teardownCapabilityPanel(record);
      mountCapabilityPanel(record);
    }
  }

  // 能力停用或消失：入口与面板一起走。开着的先关掉，挂载过的先 unmount，
  // 然后宿主把自己建的按钮、容器和登记一并移除。
  function removeCapabilityPanel(id, record) {
    if (activePanel === record.panel) closeDrawer(false);
    teardownCapabilityPanel(record);
    record.button.remove();
    record.drawer.remove();
    delete panels[record.key];
    capabilityPanelNodes.delete(id);
  }

  // syncCapabilityPanels makes the header entries and the panel containers match
  // the capabilities the kernel reports as enabled. It runs on every state read
  // and is idempotent: an entry that is already there is left alone (an open
  // panel is not reset), one that disappeared is removed together with its
  // panel, and a new one is created in the order the kernel registered it.
  function syncCapabilityPanels(list) {
    const wanted = new Map(list.map((panel) => [panel.id, panel]));
    for (const [id, record] of [...capabilityPanelNodes]) {
      if (!wanted.has(id)) removeCapabilityPanel(id, record);
    }
    for (const [id, panel] of wanted) {
      const record = capabilityPanelNodes.get(id);
      if (record) updateCapabilityPanel(record, panel);
      else createCapabilityPanel(panel);
    }
  }

  syncSessionLayout();
  applySessionHash();
  updateSessions();
  updateState();
  setInterval(updateState, 2000);
}
