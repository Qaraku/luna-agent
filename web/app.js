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

// --- 设置里的能力清单：把声明翻译成人看得见的行 -----------------------------
//
// 内核只回答三件事：注册了哪些能力、它现在在不在服务、它声明了什么。这一层把它
// 变成界面模型：启用状态、部署形态，以及它贡献的工具 / 上下文 / 路由 / 面板。
// 声明与权限也在这里成型，但界面把它们放在次级层级——那是开发者信息。
//
// 一个能力“没有工具、没有权限”不是异常：只贡献一条上下文块的能力同样要在清单里
// 出现。所以这里不按“贡献多少”筛行，只丢掉无法寻址的行（没有 id 就没有路径和键）。

const CAPABILITY_STATE_LABELS = { enabled: '已启用', disabled: '已停用', registered: '已注册', failed: '启动失败' };
const CAPABILITY_DEPLOYMENT_LABELS = { builtin: '内置', process: '子进程', browser: '浏览器模块' };
const CAPABILITY_KIND_LABELS = { tool: '工具', context: '上下文', route: '路由', panel: '面板' };
// 贡献分组的固定显示顺序；payload 里出现这里没有的种类时排在后面，按它自己的名字。
const CAPABILITY_KIND_ORDER = ['tool', 'context', 'route', 'panel'];

// capabilityLabel 只查一张封闭的标签表。表里没有的取值原样带出来，而不是就近映射
// 成别的意思——界面看不懂的值必须留得下来，不能悄悄变成“未知”以外的某个档。
function capabilityLabel(value, labels, fallback) {
  const text = capabilityPanelText(value);
  if (text === '') return fallback;
  return labels[text] || `${text}（未识别）`;
}

function capabilityStateLabel(state) {
  return capabilityLabel(state, CAPABILITY_STATE_LABELS, '状态未知');
}

function capabilityDeploymentLabel(deployment) {
  return capabilityLabel(deployment, CAPABILITY_DEPLOYMENT_LABELS, '未说明');
}

function capabilityKindLabel(kind) {
  return capabilityLabel(kind, CAPABILITY_KIND_LABELS, '未说明');
}

// capabilityStatePath is the only place the browser builds the kernel's own state
// path. A descriptor id may only be a lowercase letter followed by lowercase
// letters, digits or hyphens, so anything else — a slash, a percent, an upper
// case letter — is a payload the kernel could not have registered, and it is
// refused here rather than turned into a request. A caller that gets '' must not
// fetch at all.
function capabilityStatePath(id, action) {
  // 这里刻意不做 trim 之类的“善意修正”：带尾空格或大写的 id 内核不可能注册过，
  // 修好它等于替内核接受一个它没有承认过的标识。不是字符串就直接拒。
  const value = typeof id === 'string' ? id : '';
  if (!/^[a-z][a-z0-9-]{0,31}$/.test(value)) return '';
  if (action !== 'enable' && action !== 'disable') return '';
  return `/api/plugins/${value}/${action}`;
}

// capabilityGroups groups a capability's contributions by kind, so the row reads
// as "what it contributes" instead of a flat dump. Empty groups are not created;
// a kind this front end does not know is kept under its own name. Every group
// carries whether it is actually in service: a disabled capability keeps its
// whole list and says 未在服务, instead of looking as if those contributions had
// never been declared.
function capabilityGroups(capability, outOfService) {
  const panels = new Map();
  if (Array.isArray(capability.panels)) {
    for (const value of capability.panels) {
      const panel = value && typeof value === 'object' ? value : null;
      const id = panel ? capabilityPanelText(panel.id) : '';
      if (id) panels.set(id, capabilityPanelText(panel.title) || id);
    }
  }
  const groups = new Map();
  const order = [];
  const list = Array.isArray(capability.contributions) ? capability.contributions : [];
  for (const value of list) {
    const item = value && typeof value === 'object' ? value : null;
    if (!item) continue;
    const kind = capabilityPanelText(item.kind);
    const key = kind || 'unknown';
    if (!groups.has(key)) {
      groups.set(key, { kind: key, label: kind ? capabilityKindLabel(kind) : '未说明', items: [] });
      order.push(key);
    }
    const id = capabilityPanelText(item.id);
    // 面板贡献显示用户会看到的那块面板的名字；没有对应面板时退回 id 本身。
    const label = kind === 'panel' && id ? panels.get(id) || id : id || '未声明';
    groups.get(key).items.push(label);
  }
  const rank = (key) => {
    const index = CAPABILITY_KIND_ORDER.indexOf(key);
    return index < 0 ? CAPABILITY_KIND_ORDER.length : index;
  };
  return order
    .map((key) => groups.get(key))
    .sort((left, right) => rank(left.kind) - rank(right.kind))
    .map((group) => ({ ...group, status: outOfService ? '未在服务' : '' }));
}

// claim 与 permission 保持内核自己的词汇（kind · id），不改写成普通用户的说法。
function capabilityClaimRows(claims) {
  const rows = [];
  const list = Array.isArray(claims) ? claims : [];
  for (const value of list) {
    const claim = value && typeof value === 'object' ? value : null;
    if (!claim) continue;
    const label = [capabilityPanelText(claim.kind), capabilityPanelText(claim.id)].filter(Boolean).join(' · ');
    if (label) rows.push(label);
  }
  return rows;
}

function capabilityPermissionRows(permissions) {
  const rows = [];
  const list = Array.isArray(permissions) ? permissions : [];
  for (const value of list) {
    const permission = value && typeof value === 'object' ? value : null;
    if (!permission) continue;
    const kind = capabilityPanelText(permission.kind);
    if (kind) rows.push(kind);
  }
  return rows;
}

// capabilityRows is the whole settings list, in the order the kernel registered
// the capabilities. Only a row without an id is dropped: nothing can address it.
function capabilityRows(capabilities) {
  if (!Array.isArray(capabilities)) return [];
  const rows = [];
  for (const value of capabilities) {
    const capability = value && typeof value === 'object' ? value : null;
    if (!capability) continue;
    const id = capabilityPanelText(capability.id);
    if (!id) continue;
    const state = capabilityPanelText(capability.state);
    const enabled = state === 'enabled';
    // “未在服务”是对这个能力此刻是否在服务的判断，只有内核明确报过的状态才配得上
    // 这句话。一个没见过的状态不能被写成“未在服务”——那等于替内核下一个它没下过的
    // 结论，正好和这一层存在的理由相反。
    const knownState = Object.prototype.hasOwnProperty.call(CAPABILITY_STATE_LABELS, state);
    const outOfService = knownState && !enabled;
    let action = enabled ? 'disable' : 'enable';
    if (capabilityStatePath(id, action) === '') action = '';
    rows.push({
      id,
      title: capabilityPanelText(capability.title) || id,
      deployment: capabilityPanelText(capability.deployment),
      deploymentLabel: capabilityDeploymentLabel(capability.deployment),
      state,
      stateLabel: capabilityStateLabel(state),
      enabled,
      groups: capabilityGroups(capability, outOfService),
      claims: capabilityClaimRows(capability.claims),
      permissions: capabilityPermissionRows(capability.permissions),
      error: capabilityPanelText(capability.error),
      action,
      actionLabel: enabled ? '停用' : '启用'
    });
  }
  return rows;
}

// --- 设置里的模型服务：启动期参数只读 ---------------------------------------
//
// `reasoning_effort` 是启动时决定的模型运行参数（模型怎么想），与“推理过程是否
// 展示”是两件事。它在进程内不能热切换（模型只构建一次），所以界面只读显示。
// 字段不出现不是 medium，而是这个进程没有发送该档位、provider 自己的默认值生效：
// 文案必须把这两件事分开说，不能替进程猜一个档位。

const REASONING_EFFORT_LABELS = { minimal: '极简', low: '低', medium: '中', high: '高', none: '不思考' };
const REASONING_EFFORT_LEVELS = ['minimal', 'low', 'medium', 'high', 'none'];

function reasoningEffortView(value) {
  const text = capabilityPanelText(value);
  if (text === '') {
    return {
      present: false,
      value: '',
      label: '进程未发送该档位',
      note: '这个进程没有发送思考档位，模型服务自己的默认档位生效。要固定它，设置 LUNA_REASONING_EFFORT 后重启 Luna。'
    };
  }
  return {
    present: true,
    value: text,
    label: REASONING_EFFORT_LEVELS.includes(text)
      ? `${REASONING_EFFORT_LABELS[text]}（${text}）`
      : `${text}（未识别）`,
    note: '思考档位在启动时决定，本进程内不能切换；要改需要设置 LUNA_REASONING_EFFORT 后重启 Luna。它决定模型怎么想，与推理过程是否展示无关。'
  };
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

// --- 一次运行的展示语义 -----------------------------------------------------
//
// 前端只从真实事件派生状态：等待模型、生成回答、正在使用工具、正在取消。
// 它不推断模型在想什么，也不为没有发生的事写一行字。

// 参数与结果可能很长，卡片默认只给一段有界的预览，全文放在可展开的次级层里。
// 这两个数是"默认看多少"，不是业务判断。
const TOOL_TEXT_MAX_CHARS = 1200;
const TOOL_TEXT_MAX_LINES = 16;

// 内联时间线里那几行标号：阶段行、运行说明、推理条目、回答。
// 推理条目只在 assistant.reasoning 真的到达时才出现，没有这个事件时界面上不会
// 出现任何相关文案，也不预留位置。回答那一行的标注挂在答案容器自己的 data-label
// 上（见 style.css 的 .assistant-body::before），所以答案正文里仍然只有渲染出来
// 的那些节点，标注不会混进它的文本。
const RUN_NOTE_LABEL = '运行说明';
const RUN_REASONING_LABEL = '推理';
const RUN_ANSWER_LABEL = '回答';
const RUN_CANCELLED_COPY = '这次运行被取消了，上面的内容没有写完。';

// 推理默认折叠（回答才是这个回合的主体），折叠时那一行给一段有界的文字做预览。
// 预览的方向跟着"这一段是否还在产生"取：进行中给最新到达的一段（前面省略），所以
// 它一直滑向最新内容，用户折起来也看得出推理在长；已经结束的条目给开头（后面省略），
// 不再看起来像被切掉两头的残片。这个数是"默认看多少"，不是业务判断。
const REASONING_PREVIEW_CHARS = 72;

// 工具拒绝某次调用与工具失败在事件上都是 tool.failed，唯一区别是错误前缀。
// 拒绝是一次正常答复，不是崩溃，所以界面上它们是两档，不共用一句话。
const TOOL_REFUSAL_PREFIX = 'the tool refused this call: ';

// clipText gives the bounded preview a card starts with, and says whether
// anything was left out, so the caller can offer the full text.
function clipText(value, maxChars = TOOL_TEXT_MAX_CHARS, maxLines = TOOL_TEXT_MAX_LINES) {
  const text = typeof value === 'string' ? value : value === undefined || value === null ? '' : String(value);
  const lines = text.split(/\r?\n/);
  let preview = lines.slice(0, maxLines).join('\n');
  let clipped = lines.length > maxLines;
  if (preview.length > maxChars) {
    preview = preview.slice(0, maxChars);
    clipped = true;
  }
  return { text: clipped ? `${preview.replace(/\s+$/, '')}…` : text, clipped };
}

// 参数可能是 JSON 对象（后端直接给出）或原始字符串，两者都要如实展示。
function toolArgumentsText(value) {
  if (typeof value === 'string') return argumentsText(value);
  if (value && typeof value === 'object') {
    try {
      return JSON.stringify(value, null, 2);
    } catch (_) {
      return '—';
    }
  }
  return '—';
}

function toolResultText(value) {
  if (typeof value === 'string') return value === '' ? '—' : value;
  if (value === undefined || value === null) return '—';
  if (typeof value === 'object') {
    try {
      return JSON.stringify(value, null, 2);
    } catch (_) {
      return '—';
    }
  }
  return String(value);
}

// 运行状态条的计时：不足一分钟给秒，再长给 m:ss。
function formatElapsed(ms) {
  const total = Math.max(0, Math.floor((typeof ms === 'number' && Number.isFinite(ms) ? ms : 0) / 1000));
  if (total < 60) return `${total}s`;
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`;
}

// 工具耗时由 Runtime 给出（duration_ms）：毫秒级给 ms，秒级给一位小数。
// 没有这个字段时返回空串，不编一个数字出来。
function formatDuration(ms) {
  if (typeof ms !== 'number' || !Number.isFinite(ms) || ms < 0) return '';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
  return formatElapsed(ms);
}

function toolFailureKind(error) {
  return typeof error === 'string' && error.startsWith(TOOL_REFUSAL_PREFIX) ? 'refused' : 'failed';
}

// 拒绝的原因就在前缀之后；这一句是给用户看的，原始错误另行保留。
function toolRefusalReason(error) {
  return toolFailureKind(error) === 'refused' ? error.slice(TOOL_REFUSAL_PREFIX.length) : '';
}

function toolRefusedLabel(name) {
  return `${toolLabel(name).noun}被拒绝`;
}

// 事件用的是结果词汇（`ok`），文案表用的是活动词汇（`finished`）：
// 两个词汇表在这一处对齐，别处不再各自翻译。
const TOOL_STATE_ACTIVITY = { ok: 'finished', running: 'running', failed: 'failed', refused: 'refused' };

// 每个工具状态一档文案：运行中/完成/失败沿用按工具写好的那套，拒绝另说一句。
function toolStateLabel(name, state) {
  if (state === 'refused') return toolRefusedLabel(name);
  return toolActivityLabel(name, TOOL_STATE_ACTIVITY[state] || state);
}

// 状态条的文案只由真实事件派生：还没受理 / 已开始等待模型 / 生成中 / 正在用工具 / 正在取消。
function runPhaseText(state, toolName = '') {
  if (state === 'connecting') return '正在连接…';
  if (state === 'waiting') return '已开始，等待模型回应…';
  if (state === 'streaming') return 'Luna 正在回应…';
  if (state === 'tool') return `正在使用工具：${toolLabel(toolName).noun}`;
  if (state === 'cancelling') return '正在取消…';
  return '';
}

// 轨迹里的模型阶段行标的是"模型这一段输出开始了"。它只给段号，不写"开始/继续"
// 这类冗余措辞，但"这是第几段"必须留在字面上。
function runPhaseEntryText(leg) {
  const index = typeof leg === 'number' && Number.isFinite(leg) && leg >= 0 ? Math.floor(leg) + 1 : 1;
  return `第 ${index} 段回应`;
}

// 阶段行只在真的多段时出现：一次运行的第一段没有上一段可对照，它是不是"第 1 段"
// 这件事由它自己的推理、说明、工具条目说明就够了，那一行对一次短运行只是噪声。
// 从第二段起才给标号（"第 2 段回应"），把"模型在工具之后又接着说"这件事留在字面上。
function runPhaseVisible(leg) {
  return typeof leg === 'number' && Number.isFinite(leg) && leg >= 1;
}

function runOutcomeLabel(state) {
  return {
    ok: '已完成',
    failed: '失败',
    cancelled: '已取消',
    interrupted: '已中断',
    running: '进行中'
  }[state] || '结果未知';
}

// 折叠时那一行的概况：工具调用数、总耗时、最终状态。只写真实拿到的东西。
function runTraceMeta(tools, durationMs, state) {
  const parts = [];
  if (typeof tools === 'number' && tools > 0) parts.push(`${tools} 次工具调用`);
  if (state !== 'running' && typeof durationMs === 'number' && Number.isFinite(durationMs) && durationMs >= 0) {
    parts.push(formatElapsed(durationMs));
  }
  parts.push(runOutcomeLabel(state));
  return parts.join(' · ');
}

// 回放的一条 run 记录只在 status 上说明结果；没有记录时不给结论。
function replayRunState(status) {
  return { ok: 'ok', error: 'failed', cancelled: 'cancelled', interrupted: 'interrupted' }[status] || '';
}

// 用量只在服务端给出来时显示：拿不到就不显示，不写 0，也不留占位。
const USAGE_FIELDS = [
  ['input_tokens', '输入'],
  ['output_tokens', '输出'],
  ['cached_tokens', '缓存'],
  ['reasoning_tokens', '推理']
];

function formatTokenCount(value) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return '';
  if (value < 1000) return String(Math.round(value));
  const thousands = value / 1000;
  return `${thousands >= 100 ? Math.round(thousands) : thousands.toFixed(1).replace(/\.0$/, '')}k`;
}

// usageText 是一行次级信息（本轮元信息行），不是独立面板：只列出这次真的收到的字段。
function usageText(data) {
  const source = data && typeof data === 'object' ? data : {};
  const parts = [];
  for (const [key, label] of USAGE_FIELDS) {
    const text = formatTokenCount(source[key]);
    if (text) parts.push(`${label} ${text}`);
  }
  return parts.length ? `tokens：${parts.join(' · ')}` : '';
}

// 折叠的推理那一行里的预览：一段有界的文字，换行压成空格，所以一行就够。方向按
// 这一段是否还在产生取——进行中给最新到达的一段（前面省略），它会一直滑动，折起来
// 也看得到推理在长；已经结束的条目给开头（后面省略），不再像一段被切掉两头的残片。
// 没有文本时给空串。
function reasoningPreview(value, streaming = false, max = REASONING_PREVIEW_CHARS) {
  const text = typeof value === 'string' ? value : '';
  const flat = text.replace(/\s+/g, ' ').trim();
  if (!flat) return '';
  if (flat.length <= max) return flat;
  return streaming ? `…${flat.slice(flat.length - max)}` : `${flat.slice(0, max)}…`;
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
    clipText, toolArgumentsText, toolResultText, formatElapsed, formatDuration, toolFailureKind,
    toolRefusalReason, toolRefusedLabel, toolStateLabel, runPhaseText, runPhaseEntryText, runPhaseVisible,
    runOutcomeLabel, runTraceMeta, replayRunState, formatTokenCount, usageText, reasoningPreview,
    TOOL_TEXT_MAX_CHARS, TOOL_TEXT_MAX_LINES, TOOL_REFUSAL_PREFIX,
    RUN_NOTE_LABEL, RUN_REASONING_LABEL, RUN_ANSWER_LABEL, RUN_CANCELLED_COPY, REASONING_PREVIEW_CHARS,
    uiPluginText, uiPluginNameValid, uiPluginEntrySafe, uiPluginEntryURL, uiPluginRows, uiPluginMissingExports,
    uiPluginErrorDetail, uiPluginImportError, uiPluginMissingExportError, uiPluginMountError, uiPluginUnmountError,
    uiPluginState, uiPluginInitialState, uiPluginTransition, uiPluginEnableFailureEvent, uiPluginDisableEvent,
    uiPluginTeardown, uiPluginAbandonMount, uiPluginToggleAction, uiPluginToggleLabel, uiPluginStatusText,
    uiPluginHostAPI, UI_PLUGIN_API_VERSION, UI_PLUGIN_ENTRY_REASON, UI_PLUGIN_REQUIRED_EXPORTS,
    capabilityPanelText, capabilityPanelEntryURL, capabilityPanelElementID, capabilityPanels,
    capabilityPanelEntryError, capabilityPanelImportError, capabilityPanelMissingExportError,
    capabilityPanelMountError, capabilityPanelUnmountError,
    capabilityStatePath, capabilityStateLabel, capabilityDeploymentLabel, capabilityKindLabel,
    capabilityRows, capabilityGroups, capabilityClaimRows, capabilityPermissionRows,
    reasoningEffortView, REASONING_EFFORT_LEVELS
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
    // 打开设置时重新读一次状态：能力清单与模型服务参数都是这一刻的事实，不是页
    // 面首次加载时的旧值。
    settings: { element: $('settings-panel'), toggle: $('settings-toggle'), close: $('settings-close'), refresh: () => { updateState(); } }
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
  // 推理条目的展开状态只在会话内记住（进程内变量）：用户展开过一次，后面的推理
  // 条目沿用同一个选择；刷新后回到默认折叠，不新增浏览器存储键。
  let reasoningExpanded = false;
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

  // 一个 assistant 回合只有两块：一行标题（模型名 + 这次运行的概况），以及一条
  // 内联的时间线。时间线里的条目按它们真实发生的顺序出现；回答主体永远是最后
  // 一条，也是这个回合的视觉主体。
  function assistantTurnNode() {
    const turn = make('article', 'turn assistant');
    turn.setAttribute('aria-label', 'Luna');
    const head = make('div', 'assistant-head');
    const meta = make('span', 'run-meta');
    meta.hidden = true;
    head.append(make('span', 'assistant-label', 'Luna'), meta);
    const timeline = make('div', 'run-timeline');
    const body = make('div', 'assistant-body placeholder', 'Luna 正在回应…');
    // 回答有自己的一行标注。标注是容器上的 data-label，由样式用 attr() 取出来，
    // 所以答案正文里仍然只有渲染出来的节点，标注不会混进它的文本。
    body.dataset.label = RUN_ANSWER_LABEL;
    timeline.append(body);
    turn.append(head, timeline);
    return { turn, timeline, meta, body };
  }

  // 每一步都插在回答主体之前，所以时间线里的顺序就是事情发生的顺序，回答永远
  // 收尾。withPhase 为真时这一步同时说明"模型这一段输出开始了"：阶段行先补上，
  // 于是它总在它这一段的第一条条目之前。
  function appendStep(turnState, node, withPhase = true) {
    if (withPhase) ensureLegPhase(turnState);
    turnState.timeline.insertBefore(node, stepAnchor(turnState));
    updateRunMeta(turnState);
    return node;
  }

  // 回答主体被移除之后（取消或空答案）没有锚点，剩下的条目直接接在后面。
  function stepAnchor(turnState) {
    return turnState.body.parentElement === turnState.timeline ? turnState.body : null;
  }

  // 一次运行的界面状态，只从真实事件推进。它同时是回答主体和这条时间线的来源。
  function newRunTurn(message) {
    return {
      message,
      answer: '',
      hasAnswer: false,
      terminal: false,
      failed: false,
      runID: '',
      startedAt: 0,
      finishedAt: 0,
      outcome: '',
      leg: 0,
      legPhase: -1,
      awaitingModel: false,
      toolCount: 0,
      openCards: [],
      // provider 自愿暴露的推理内容只累积在这里，绝不写进 answer，也不参与对齐。
      reasoning: '',
      reasoningEntry: null,
      // 用量是这一轮元信息行上的一段文字，服务端给了才有；它不是时间线里的条目。
      usage: ''
    };
  }

  function addAssistantTurn(message) {
    const stick = nearBottom();
    hideEmptyState();
    const node = assistantTurnNode();
    conversation.append(node.turn);
    contentChanged(stick);
    return Object.assign(node, newRunTurn(message));
  }

  function appendDefinition(list, label, value) {
    const row = make('div');
    row.append(make('dt', '', label), make('dd', '', value));
    list.append(row);
  }

  // 参数与结果默认有界：卡片先给一段预览，被截断的值下面接一个可展开的全文，
  // 展开时的高度再由样式兜住，页面不会被一次读取拉爆。
  // 预览单独成一个元素：量「默认给多少」时量的是它本身，而不是整行——整行的
  // textContent 也包含折叠层里的全文（真实 DOM 与适配器都如此）。
  function appendBoundedValue(list, label, value) {
    const full = typeof value === 'string' ? value : value === undefined || value === null ? '' : String(value);
    const text = full === '' ? '—' : full;
    const clip = clipText(text);
    const row = make('div');
    const dd = make('dd');
    dd.append(make('span', 'value-preview', clip.text));
    if (clip.clipped) {
      const more = make('details', 'value-more');
      more.append(make('summary', '', `展开全文（${text.length} 字符）`), make('pre', '', text));
      dd.append(more);
    }
    row.append(make('dt', '', label), dd);
    list.append(row);
  }

  // 执行身份（generation / version / plugin_pid）与原始错误是开发诊断信息，
  // 只在卡片的次级层里出现，不做主视觉。它是卡片的第二层，挂在事实行旁边，
  // 不混进 `.tool-detail` 的那份事实清单里（那里只有工具、参数、结果/错误）。
  function toolDetailNode(tool) {
    if (!tool.identity && !tool.rawError) return null;
    const more = make('details', 'tool-more');
    const list = make('dl');
    if (tool.identity) appendDefinition(list, '执行身份', tool.identity);
    more.append(make('summary', '', '详情'), list);
    if (tool.rawError) more.append(make('pre', '', tool.rawError));
    return more;
  }

  function toolIdentityText(data) {
    if (!data || (data.generation === undefined && data.version === undefined && data.plugin_pid === undefined)) return '';
    return toolSummary(data);
  }

  // 卡片的事实行来自同一个读法：参数是字符串或 JSON 对象，结果/错误只出现一个。
  // 拒绝与失败都只是错误，区别在前缀，所以拒绝那一行另起一个说法。
  function toolCardFacts(tool) {
    return toolCallFacts({
      name: tool.name,
      arguments: tool.arguments,
      result: tool.result,
      error: tool.error
    }).map((fact) => (fact.label === '错误' && tool.state === 'refused'
      ? { label: '拒绝原因', value: toolRefusalReason(tool.error) || fact.value }
      : fact));
  }

  // 一张工具卡片：一行摘要（状态点、工具、耗时）+ 可展开的细节。
  // 成功、失败、拒绝用语义色与文案区分，拒绝不会被写成一次崩溃。
  function toolCardRecord(tool) {
    const card = make('details', `tool-row ${tool.state}`);
    const dot = make('span', 'tool-state');
    dot.dataset.state = tool.state;
    dot.setAttribute('aria-hidden', 'true');
    const name = make('span', 'tool-name', toolStateLabel(tool.name, tool.state));
    const meta = make('span', 'tool-meta', tool.meta || '');
    const summary = make('summary');
    summary.append(dot, name, meta);
    const detail = make('div', 'tool-detail');
    const list = make('dl');
    for (const fact of toolCardFacts(tool)) {
      // 运行中的调用还没有结果或错误：那一行在它结束时才补上。
      if (tool.state === 'running' && fact.label !== '工具' && fact.label !== '参数') continue;
      appendBoundedValue(list, fact.label, fact.value);
    }
    detail.append(list);
    card.append(summary, detail);
    return { card, dot, name, meta, detail, list, toolName: typeof tool.name === 'string' ? tool.name : '', arguments: tool.arguments, state: tool.state };
  }

  function applyToolCardState(record, state) {
    record.state = state;
    record.card.className = `tool-row ${state}`;
    record.dot.dataset.state = state;
    record.name.textContent = toolStateLabel(record.toolName, state);
  }

  function startToolCard(turnState, data) {
    const stick = nearBottom();
    // 顺序就是事情发生的顺序：阶段行 → 运行说明 → 工具卡片。
    ensureLegPhase(turnState);
    demoteRunNote(turnState);
    const record = toolCardRecord({
      name: data.name,
      state: 'running',
      arguments: toolArgumentsText(data.arguments)
    });
    appendStep(turnState, record.card, false);
    turnState.toolCount += 1;
    turnState.openCards.push(record);
    updateRunMeta(turnState);
    contentChanged(stick);
    return record;
  }

  function finishToolCard(turnState, data, state) {
    const stick = nearBottom();
    const toolName = typeof data.name === 'string' ? data.name : '';
    const record = turnState.openCards.find((item) => item.state === 'running' && (!toolName || !item.toolName || item.toolName === toolName))
      || turnState.openCards.find((item) => item.state === 'running')
      || null;
    // 没有对应的开始事件时补一张完整的卡片，而不是把这次调用丢掉。
    if (!record) turnState.toolCount += 1;
    const target = record || toolCardRecord({ name: data.name, state: 'running', arguments: toolArgumentsText(data.arguments) });
    if (!record) appendStep(turnState, target.card);
    const facts = toolCardFacts({
      name: target.toolName,
      arguments: target.arguments,
      result: toolResultText(data.result),
      error: typeof data.error === 'string' ? data.error : '',
      state
    });
    const outcome = facts[facts.length - 1];
    if (outcome) appendBoundedValue(target.list, outcome.label, outcome.value);
    const more = toolDetailNode({
      identity: toolIdentityText(data),
      // 拒绝的原始错误带着协议前缀，它是权威原文，放在次级层里。
      rawError: state === 'refused' ? data.error : ''
    });
    if (more) target.card.append(more);
    target.meta.textContent = formatDuration(data.duration_ms);
    applyToolCardState(target, state);
    turnState.openCards = turnState.openCards.filter((item) => item !== target);
    // 工具返回之后，接下来的一段输出属于模型的下一段。
    turnState.awaitingModel = true;
    updateRunMeta(turnState);
    contentChanged(stick);
  }

  // 阶段行：这一刻模型又开始输出了。它只标边界，不描述模型在想什么。第一段不给
  // 阶段行（见 runPhaseVisible）：那一段没有上一段可对照，标号只是噪声；从第二段
  // 起才给，把"模型在工具之后又接着说"留在字面上。
  function appendPhaseRow(turnState) {
    turnState.legPhase = turnState.leg;
    if (!runPhaseVisible(turnState.leg)) return;
    turnState.timeline.insertBefore(
      make('div', 'run-phase', runPhaseEntryText(turnState.leg)),
      stepAnchor(turnState)
    );
    updateRunMeta(turnState);
  }

  // 有过程可看时，当前这一段的第一条条目之前先补上它的阶段行。
  function ensureLegPhase(turnState) {
    if (turnState.legPhase === turnState.leg) return;
    appendPhaseRow(turnState);
  }

  // 工具返回之后的第一段输出属于模型的下一段：新的一段有自己的阶段行，推理也
  // 另起一条，不跟上一条混在一起。上一段的推理到这里结束，它的预览换成开头一段。
  function beginModelLeg(turnState) {
    if (!turnState.awaitingModel) return;
    turnState.awaitingModel = false;
    turnState.leg += 1;
    settleReasoningPreview(turnState);
    turnState.reasoning = '';
    turnState.reasoningEntry = null;
    appendPhaseRow(turnState);
  }

  // 一段模型输出结束时（工具返回后的下一段，或整轮终止）它的推理条目就定型了：
  // 预览从"最新到达的一段"换成"开头的一段"，所以一个已经结束的条目不会看起来
  // 像被切掉了开头的残片。流式期间反过来，始终给最新内容。
  function settleReasoningPreview(turnState) {
    if (!turnState.reasoningEntry) return;
    turnState.reasoningEntry.preview.textContent = reasoningPreview(turnState.reasoning);
  }

  // 时间线里除回答之外的条目：一次直接回答的运行没有过程可概括。
  function hasRunSteps(turnState) {
    return turnState.timeline.children.length > 1;
  }

  // 标题那一行上的概况：工具调用数、总耗时（结束时才有）、最终状态，加上这一轮
  // 真的收到的用量。用量属于整轮，不属于某一段模型输出，所以它留在这条元信息行
  // 上，不再夹在推理与工具卡片之间。只写真实拿到的东西。
  function updateRunMeta(turnState) {
    const state = turnState.outcome || 'running';
    const durationMs = state === 'running' || !turnState.startedAt ? null : turnState.finishedAt - turnState.startedAt;
    const parts = [runTraceMeta(turnState.toolCount, durationMs, state)];
    if (turnState.usage) parts.push(turnState.usage);
    turnState.meta.textContent = parts.join(' · ');
    turnState.meta.dataset.state = state;
    // 一次直接回答、又没有用量时，这一行不出现。
    turnState.meta.hidden = !hasRunSteps(turnState) && !turnState.usage;
  }

  // 模型在调工具前说的话不是回答，而是"运行说明"：从消息主体移到时间线里保留，
  // 位置就在它真实发生的地方——阶段行之后、它引出的工具卡片之前。
  function demoteRunNote(turnState) {
    if (!turnState.answer) return;
    const note = make('p', 'run-note');
    note.append(make('span', 'run-note-label', RUN_NOTE_LABEL), document.createTextNode(`：${turnState.answer}`));
    appendStep(turnState, note);
    turnState.answer = '';
    turnState.hasAnswer = false;
    turnState.body.textContent = 'Luna 正在回应…';
    turnState.body.classList.add('placeholder');
  }

  // 用量是可选的：只在服务端给出来时显示，拿不到就不显示，不写 0，也不为它单开
  // 一块面板。它属于整轮运行，落在标题那一行的元信息里，所以它不再夹在推理与
  // 工具卡片之间，也不再是一条会随事件插进时间线中间的行。
  function applyUsage(turnState, data) {
    const text = usageText(data);
    if (!text) return;
    turnState.usage = text;
    updateRunMeta(turnState);
  }

  // 推理条目：一行标号 + 折叠时的实时预览 + 展开后的全文。默认折叠，因为回答才是
  // 这个回合的视觉主体；折叠时那一行里仍然跟着事件增长，用户看得到推理在长。展开
  // 状态只在会话内记住，刷新后回到默认折叠。
  function reasoningEntryNode() {
    const entry = make('details', 'run-reasoning');
    const summary = make('summary');
    summary.append(make('span', 'run-reasoning-label', RUN_REASONING_LABEL));
    const preview = make('span', 'run-reasoning-preview');
    summary.append(preview);
    const text = make('pre', 'run-reasoning-body');
    entry.append(summary, text);
    entry.open = reasoningExpanded;
    // 用户自己折叠或展开一次，后面的推理条目沿用同一个选择。
    entry.addEventListener('toggle', () => { reasoningExpanded = entry.open; });
    return { entry, text, preview };
  }

  // assistant.reasoning 是 provider 自愿暴露的推理增量。它只在自己那一条条目里
  // 增长，绝不写进回答主体，也不参与终态答案对齐；整轮都没有这个事件时，界面上
  // 不出现任何相关文案，也不留占位。
  function appendReasoning(text) {
    if (!currentTurn) return;
    const chunk = typeof text === 'string' ? text : '';
    if (!chunk) return;
    const stick = nearBottom();
    beginModelLeg(currentTurn);
    currentTurn.reasoning += chunk;
    if (!currentTurn.reasoningEntry) {
      currentTurn.reasoningEntry = reasoningEntryNode();
      appendStep(currentTurn, currentTurn.reasoningEntry.entry);
    }
    const body = currentTurn.reasoningEntry.text;
    // 正文自己滚动时跟着最新一行走；用户往回翻过就不再打断他。
    const follow = body.scrollHeight - body.scrollTop - body.clientHeight < 24;
    body.textContent = currentTurn.reasoning;
    if (follow) body.scrollTop = body.scrollHeight;
    // 折叠时那一行的预览跟着同一份文本走，而且是"进行中"的读法：给最新到达的一段。
    currentTurn.reasoningEntry.preview.textContent = reasoningPreview(currentTurn.reasoning, true);
    updateRunMeta(currentTurn);
    contentChanged(stick);
  }

  function appendAnswer(text) {
    if (!currentTurn || !text) return;
    const stick = nearBottom();
    // 工具返回之后的第一次输出属于模型的下一段：阶段行与推理条目都从这里另起。
    beginModelLeg(currentTurn);
    if (!currentTurn.hasAnswer) {
      currentTurn.body.textContent = '';
      currentTurn.body.classList.remove('placeholder');
      currentTurn.hasAnswer = true;
    }
    currentTurn.answer += text;
    renderMarkdown(currentTurn.body, currentTurn.answer);
    contentChanged(stick);
  }

  // 终态时还没有结束的调用不能停在"运行中"，也不能被说成完成。
  function resolveTurnTools(turnState) {
    for (const record of turnState.openCards) {
      if (record.state !== 'running') continue;
      appendBoundedValue(record.list, '错误', '工具在完成前中断。');
      applyToolCardState(record, 'failed');
    }
    turnState.openCards = [];
  }

  function resolveOpenTools() {
    if (currentTurn) resolveTurnTools(currentTurn);
  }

  // 终止时用 run.finished.answer 对齐已经渲染的文本：替换而不是追加，
  // 所以一段回答不会出现两遍。答案为空时保留已渲染的部分，不凭空清空。
  function alignAnswer(answer) {
    if (!currentTurn || typeof answer !== 'string' || answer === '') return;
    const stick = nearBottom();
    currentTurn.answer = answer;
    currentTurn.hasAnswer = true;
    currentTurn.body.classList.remove('placeholder');
    renderMarkdown(currentTurn.body, currentTurn.answer);
    contentChanged(stick);
  }

  // 取消时已经渲染的部分文本保留，并说清楚它没有写完。
  function markCancelled() {
    if (!currentTurn) return;
    const stick = nearBottom();
    if (!currentTurn.hasAnswer) currentTurn.body.remove();
    currentTurn.turn.append(make('p', 'run-incomplete', RUN_CANCELLED_COPY));
    contentChanged(stick);
  }

  function finishRunTurn(turnState, state) {
    turnState.finishedAt = Date.now();
    turnState.outcome = state;
    resolveTurnTools(turnState);
    // 这一轮到此为止：还在折着的那一条推理预览换成开头读法，不再像残片。
    settleReasoningPreview(turnState);
    updateRunMeta(turnState);
  }

  function showRunFailure(error, copy = 'Luna 没能完成这次回应。') {
    if (!currentTurn || currentTurn.failed) return;
    const stick = nearBottom();
    currentTurn.failed = true;
    resolveOpenTools();
    // 还没有写出答案时这一格不是回答：移除它，而不是在"回答"标注下留一个空容器。
    if (!currentTurn.hasAnswer) currentTurn.body.remove();
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

  // --- 状态条、Stop 与一次运行的生命周期 -------------------------------------
  //
  // 状态条复用标记里已有的 #run-status：当前状态文案 + 已运行时长。
  // 时长由前端计时（起点是收到的事件），它只用来显示，不参与判断状态。

  let liveRun = null;
  let statusTicker = null;
  let statusNodes = null;

  function buildStatusNodes() {
    const dot = make('span', 'run-status-dot');
    dot.setAttribute('aria-hidden', 'true');
    const label = make('span', 'run-status-label');
    // 秒表每秒都在变，不该被读屏一次次播报。
    const elapsed = make('span', 'run-status-elapsed');
    elapsed.setAttribute('aria-hidden', 'true');
    runStatus.replaceChildren(dot, label, elapsed);
    return { dot, label, elapsed };
  }

  function applyRunStatus() {
    if (!liveRun) {
      runStatus.hidden = true;
      return;
    }
    statusNodes = statusNodes || buildStatusNodes();
    statusNodes.label.textContent = liveRun.notice || runPhaseText(liveRun.state, liveRun.toolName);
    statusNodes.elapsed.textContent = liveRun.startedAt ? formatElapsed(Date.now() - liveRun.startedAt) : '';
    runStatus.dataset.state = liveRun.state;
    runStatus.hidden = false;
  }

  function tickRunStatus() {
    if (!liveRun) {
      stopStatusTicker();
      return;
    }
    applyRunStatus();
  }

  function startStatusTicker() {
    if (statusTicker !== null) return;
    statusTicker = setInterval(tickRunStatus, 1000);
  }

  function stopStatusTicker() {
    if (statusTicker === null) return;
    clearInterval(statusTicker);
    statusTicker = null;
  }

  function clearRunStatus() {
    runStatus.hidden = true;
    runStatus.replaceChildren();
    statusNodes = null;
  }

  // 运行期间发送按钮就是 Stop：同一个控件、同一个位置，不再另起一套控制栏。
  const sendIcon = send.querySelector('svg');

  function stopIconNode() {
    const svg = document.createElementNS(SVG_NAMESPACE, 'svg');
    svg.setAttribute('class', 'ui-icon stop-icon');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('fill', 'currentColor');
    svg.setAttribute('aria-hidden', 'true');
    const square = document.createElementNS(SVG_NAMESPACE, 'rect');
    square.setAttribute('x', '7');
    square.setAttribute('y', '7');
    square.setAttribute('width', '10');
    square.setAttribute('height', '10');
    square.setAttribute('rx', '2');
    svg.append(square);
    return svg;
  }

  function setSendAction(action) {
    const label = action === 'send' ? '发送' : action === 'cancelling' ? '正在取消' : '停止';
    send.dataset.action = action;
    send.setAttribute('aria-label', label);
    send.setAttribute('title', label);
    send.replaceChildren(action === 'send' ? sendIcon : stopIconNode());
  }

  function beginRun() {
    running = true;
    liveRun = { runID: '', state: 'connecting', toolName: '', startedAt: 0, notice: '', cancelling: false };
    applyRunStatus();
    startStatusTicker();
    setSendAction('cancel');
    setSessionControls();
  }

  // 收到终止事件就立刻退出运行状态，不留一个还在转的 loading。
  function endRun() {
    if (!running && !liveRun) return;
    running = false;
    liveRun = null;
    stopStatusTicker();
    clearRunStatus();
    setSendAction('send');
    setSessionControls();
  }

  // 取消入口：POST /api/runs/{run_id}/cancel。重复点击不再发请求；
  // 404 表示这次运行已经结束，按"已经结束"处理，而不是报一次错。
  async function cancelRun() {
    if (!liveRun || liveRun.cancelling) return;
    const runID = typeof liveRun.runID === 'string' ? liveRun.runID : '';
    // 这一次运行还没被受理时没有可取消的对象。
    if (!runID) return;
    const previous = liveRun.state;
    liveRun.cancelling = true;
    liveRun.state = 'cancelling';
    liveRun.notice = '';
    applyRunStatus();
    setSessionControls();
    try {
      const response = await fetch(`/api/runs/${runID}/cancel`, { method: 'POST' });
      if (!response.ok && response.status !== 404) throw new Error(await errorMessage(response));
    } catch (error) {
      // 取消失败说一次就够，不当成取消成功；下一个真实事件会覆盖这句话。
      if (liveRun) {
        liveRun.cancelling = false;
        liveRun.state = previous;
        liveRun.notice = `取消失败：${error.message}`;
        applyRunStatus();
      }
      setSessionControls();
    }
  }

  // SSE 事件的唯一入口。界面状态只在这里推进，每一条都对应一个真实发生的事：
  // 等待模型 / 生成回答 / 正在使用工具 / 正在取消，以及恰好一个终止事件。
  function handleEvent(event) {
    const data = event.data || {};
    const type = event.type;
    if (type === 'run.started') {
      const startedAt = Date.now();
      const runID = typeof data.run_id === 'string' ? data.run_id : '';
      if (currentTurn) {
        currentTurn.runID = runID;
        currentTurn.startedAt = startedAt;
      }
      if (liveRun) {
        liveRun.runID = runID;
        liveRun.startedAt = startedAt;
        liveRun.state = 'waiting';
        liveRun.notice = '';
      }
      applyRunStatus();
      // A session that did not exist before this run is created by the server;
      // its id arrives here and goes into the hash, so the address bar names the
      // session the answer is being written into, and a refresh returns to it.
      adoptSession(data.session_id);
      return;
    }
    // 一条新的真实事件覆盖上一次取消失败留下的那句话。
    if (liveRun) liveRun.notice = '';
    if (type === 'assistant.delta') {
      if (liveRun) {
        liveRun.state = 'streaming';
        liveRun.toolName = '';
      }
      applyRunStatus();
      appendAnswer(data.text);
    } else if (type === 'assistant.reasoning') {
      // provider 自愿暴露的推理增量：它有自己的条目，不是回答的一部分。
      // 状态条跟着事实走——它到了就说明模型已经在产生输出，所以这里是"生成中"，
      // 这个状态来自"收到了输出"，而不是来自推理里写了什么。
      if (liveRun && liveRun.state !== 'tool') {
        liveRun.state = 'streaming';
        liveRun.toolName = '';
      }
      applyRunStatus();
      appendReasoning(data.text);
    } else if (type === 'tool.started') {
      if (currentTurn) startToolCard(currentTurn, data);
      if (liveRun) {
        liveRun.state = 'tool';
        liveRun.toolName = typeof data.name === 'string' ? data.name : '';
      }
      applyRunStatus();
    } else if (type === 'tool.finished') {
      if (currentTurn) finishToolCard(currentTurn, data, 'ok');
      if (liveRun) {
        liveRun.state = 'waiting';
        liveRun.toolName = '';
      }
      applyRunStatus();
    } else if (type === 'tool.failed') {
      // 拒绝与失败都是 tool.failed，区别只在错误前缀：拒绝不是一次崩溃。
      if (currentTurn) finishToolCard(currentTurn, data, toolFailureKind(data.error));
      if (liveRun) {
        liveRun.state = 'waiting';
        liveRun.toolName = '';
      }
      applyRunStatus();
    } else if (type === 'usage.updated') {
      // 用量是可选的，不改变运行状态，只是轨迹里多一行事实。
      if (currentTurn) applyUsage(currentTurn, data);
      applyRunStatus();
    } else if (type === 'run.finished') {
      // 终止事件恰好一个。先用权威答案对齐已渲染的文本（替换，不追加），
      // 再退出运行状态。
      if (currentTurn) {
        currentTurn.terminal = true;
        finishRunTurn(currentTurn, 'ok');
        alignAnswer(data.answer);
        if (!currentTurn.hasAnswer) currentTurn.body.remove();
      }
      endRun();
    } else if (type === 'run.failed') {
      if (currentTurn) {
        currentTurn.terminal = true;
        finishRunTurn(currentTurn, 'failed');
        showRunFailure(data.error);
      }
      endRun();
    } else if (type === 'run.cancelled') {
      if (currentTurn) {
        currentTurn.terminal = true;
        finishRunTurn(currentTurn, 'cancelled');
        markCancelled();
      }
      endRun();
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
    input.value = '';
    resizeInput();
    beginRun();
    try {
      await streamRun(message, () => { admitted = true; });
    } catch (error) {
      if (!admitted) input.value = message;
      resizeInput();
      if (!currentTurn || !currentTurn.terminal) showRunFailure(error.message);
    } finally {
      if (currentTurn && !currentTurn.terminal && !currentTurn.failed) {
        showRunFailure('SSE 流在收到终止事件前结束（未收到 run.finished 或 run.failed）。', '回应在完成前中断了。');
      }
      endRun();
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

  // 运行期间同一个发送按钮就是 Stop：点击它取消这次运行，而不是再发一条消息。
  send.addEventListener('click', (event) => {
    if (!liveRun) return;
    event.preventDefault();
    cancelRun();
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

  // --- 设置：能力清单与只读的模型服务参数 ---------------------------------

  const capabilityList = $('capability-list');
  const capabilityEmpty = $('capability-empty');
  const capabilityStatus = $('capability-status');
  const capabilityRetry = $('capability-retry');
  // 最近一次成功读到的 payload：停用按钮触发的那次重绘、以及读不到状态时的显示都用
  // 它。一次读不到状态不会把清单清空，也不会把能力说成“停用”。
  let capabilityPayload = null;
  // 正在等内核答复的能力 id：这段时间里按钮不可点，重复点击不会发出第二次请求。
  const capabilityPending = new Set();

  function setCapabilityStatus(text, className = '') {
    capabilityStatus.textContent = text;
    capabilityStatus.className = `luna-status capability-status${className ? ` ${className}` : ''}`;
  }

  function capabilityGroupNode(group) {
    const entry = make('div', 'capability-group');
    const term = make('dt');
    term.append(document.createTextNode(group.label));
    // 停用的能力仍然列全它声明的每一组，“未在服务”标在那一组上。
    if (group.status) term.append(make('span', 'capability-off', group.status));
    const items = make('ul', 'capability-items');
    for (const label of group.items) items.append(make('li', 'capability-item', label));
    const details = make('dd');
    details.append(items);
    entry.append(term, details);
    return entry;
  }

  function capabilityDeveloperNode() {
    const developer = make('details', 'capability-developer');
    // 声明与权限是开发者信息：默认收起，展开才看。
    developer.open = false;
    developer.append(make('summary', '', '声明与权限'), make('dl', 'capability-developer-list'));
    return developer;
  }

  // capabilityRowNode 建一次节点，updateCapabilityRow 之后只改内容：轮询不会重建
  // 行，所以键盘焦点和已经展开的细节都留在原处。
  function capabilityRowNode() {
    const item = make('li', 'capability-row luna-list-item');
    const head = make('div', 'capability-head');
    const title = make('div', 'capability-title');
    title.append(make('span', 'capability-name'), make('span', 'capability-id'));
    const toggle = make('button', 'luna-button capability-toggle');
    toggle.type = 'button';
    toggle.addEventListener('click', () => setCapabilityState(item.capabilityRow));
    head.append(title, toggle);

    const meta = make('div', 'capability-meta');
    meta.append(make('span', 'capability-badge'), make('span', 'capability-state'));

    const error = make('p', 'capability-error');
    error.hidden = true;

    item.append(head, meta, error, make('dl', 'capability-contributions'), capabilityDeveloperNode());
    return item;
  }

  function updateCapabilityRow(item, row) {
    item.capabilityRow = row;
    item.dataset.capability = row.id;
    item.querySelector('.capability-name').textContent = row.title;
    item.querySelector('.capability-id').textContent = row.id;

    const toggle = item.querySelector('.capability-toggle');
    const pending = capabilityPending.has(row.id);
    // 内核没有把这个 id 报成可寻址的能力时不给出控件：按不对的路径发请求只会失败。
    toggle.hidden = row.action === '';
    toggle.disabled = pending;
    toggle.textContent = pending ? `${row.actionLabel}中…` : row.actionLabel;
    toggle.setAttribute('aria-label', `${row.actionLabel}${row.title}`);

    item.querySelector('.capability-badge').textContent = row.deploymentLabel;
    const state = item.querySelector('.capability-state');
    state.textContent = row.stateLabel;
    state.classList.toggle('is-on', row.enabled);
    state.classList.toggle('is-off', !row.enabled);

    const error = item.querySelector('.capability-error');
    error.hidden = row.error === '';
    error.textContent = row.error;

    const contributions = item.querySelector('.capability-contributions');
    contributions.replaceChildren();
    if (row.groups.length === 0) contributions.append(make('p', 'capability-none luna-muted', '没有声明任何贡献。'));
    for (const group of row.groups) contributions.append(capabilityGroupNode(group));

    const developer = item.querySelector('.capability-developer');
    const lines = [];
    if (row.claims.length) lines.push(['声明', row.claims.join('、')]);
    if (row.permissions.length) lines.push(['权限', row.permissions.join('、')]);
    const list = developer.querySelector('.capability-developer-list');
    list.replaceChildren();
    for (const [label, text] of lines) {
      const entry = make('div', 'capability-developer-row');
      entry.append(make('dt', '', label), make('dd', '', text));
      list.append(entry);
    }
    // 没有声明也没有权限的能力（例如只贡献一条上下文）不显示这个折叠块。
    developer.hidden = lines.length === 0;
  }

  function renderCapabilities(payload) {
    if (payload !== undefined) capabilityPayload = payload;
    const rows = capabilityRows(capabilityPayload);
    reconcileList(capabilityList, rows, (row) => row.id, capabilityRowNode, updateCapabilityRow, capabilityRetry);
    capabilityEmpty.hidden = rows.length > 0;
  }

  // setCapabilityState is the only place the browser asks the kernel to change a
  // capability's state. Nothing is marked as changed here: the request either
  // fails — and says so, keeping the last reported state — or it succeeds, and
  // then the list is rebuilt from a fresh /api/state read. The interface never
  // shows a state the kernel did not report.
  async function setCapabilityState(row) {
    if (!row || !row.id || !row.action || capabilityPending.has(row.id)) return;
    const path = capabilityStatePath(row.id, row.action);
    if (!path) return;
    const { action, actionLabel: label, title } = row;
    capabilityPending.add(row.id);
    setCapabilityStatus(`正在${label} ${title}…`);
    renderCapabilities();
    let failure = '';
    try {
      const response = await fetch(path, { method: 'POST' });
      if (!response.ok) failure = await errorMessage(response);
    } catch (error) {
      failure = error.message;
    }
    capabilityPending.delete(row.id);
    renderCapabilities();
    setCapabilityStatus(
      failure
        ? `${label} ${title} 失败：${failure}（它仍按上一次读到的状态显示）`
        : `${title} 已${action === 'disable' ? '停用' : '启用'}。`,
      failure ? 'failure' : ''
    );
    // 以内核的实际状态重绘：工具、上下文、路由与面板都跟着这一份状态走。
    await updateState();
  }

  // 模型服务参数是这个进程启动时定下的，界面只读显示：模型、提供方与思考档位来自
  // 同一份状态；档位的说明与“推理过程是否展示”分开写。
  function renderModelFacts(state) {
    const payload = state && typeof state === 'object' ? state : {};
    $('settings-model').textContent = valueOrDash(payload.model);
    $('settings-provider').textContent = valueOrDash(payload.provider_host);
    const effort = reasoningEffortView(payload.reasoning_effort);
    $('settings-effort').textContent = effort.label;
    $('settings-effort-note').textContent = effort.note;
  }

  capabilityRetry.addEventListener('click', () => updateState());

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
    // 设置里的能力清单与模型服务参数读的是同一份状态：停用后入口、面板与这里的
    // "未在服务"一起变，不会各说一套。
    renderCapabilities(state.capabilities);
    renderModelFacts(state);
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
  // two states. 运行期间发送按钮是 Stop，它必须保持可点：取消就是它的用途。
  function setSessionControls() {
    send.disabled = switching || Boolean(running && liveRun && liveRun.cancelling);
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
    clearRunStatus();
  }

  // 回放一个会话时读的是冻结的记录，和 live 的卡片共用同一套外观；
  // 记录里没有耗时与执行身份，就不显示这两样。
  function toolRowNode(tool) {
    return toolCardRecord({
      name: tool.name,
      state: tool.failed ? toolFailureKind(tool.error) : 'ok',
      arguments: toolArgumentsText(tool.arguments),
      result: toolResultText(tool.result),
      error: typeof tool.error === 'string' ? tool.error : ''
    });
  }

  function assistantReplayNode(record) {
    const node = assistantTurnNode();
    for (const tool of record.tools) node.timeline.insertBefore(toolRowNode(tool).card, node.body);
    if (record.tools.length) {
      // 回放出来的调用也排在同一条时间线里：它同样是"过程"，不是回答。
      const state = replayRunState(record.status);
      node.meta.textContent = runTraceMeta(record.tools.length, null, state);
      node.meta.dataset.state = state;
      node.meta.hidden = false;
    }
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
