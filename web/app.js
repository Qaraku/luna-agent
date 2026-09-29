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

function reloadCopy(state, tool, technical = '') {
  const target = tool || '全部已注册工具';
  if (state === 'pending') return { summary: '正在重建 ' + target + '…', technical: '' };
  if (state === 'success') return { summary: target + ' 已重载。', technical: '' };
  return { summary: '重载 ' + target + ' 失败，旧实现继续服务。', technical };
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

// Widget 入口必须留在贡献能力自己的路由范围，不能借目录遍历导入其他模块。
function capabilityWidgets(capabilities) {
  const widgets = [];
  for (const capability of Array.isArray(capabilities) ? capabilities : []) {
    if (capability?.state !== 'enabled') continue;
    const prefixes = (Array.isArray(capability.claims) ? capability.claims : []).filter(claim => claim?.kind === 'route-prefix').map(claim => claim.id);
    for (const widget of Array.isArray(capability.widgets) ? capability.widgets : []) {
      if (!widget || !/^[a-z][a-z0-9_.:-]{0,63}$/.test(widget.id) || typeof widget.title !== 'string' || !widget.title.trim() || widget.title.length > 80) continue;
      const entry = capabilityPanelEntryURL(widget.entry);
      if (!entry || entry.split('/').some(part => part === '.' || part === '..') || !prefixes.some(prefix => typeof prefix === 'string' && entry.startsWith(prefix + '/'))) continue;
      let source='';
      if (widget.source !== undefined && widget.source !== '') {
        source=capabilityPanelEntryURL(widget.source);
        if (!source || source.split('/').some(part=>part==='.'||part==='..') || !prefixes.some(prefix=>typeof prefix==='string'&&source.startsWith(prefix+'/'))) continue;
      }
      widgets.push({ id: widget.id, title: widget.title, entry, source, owner: capability.id });
    }
  }
  return widgets;
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
const CAPABILITY_KIND_LABELS = { tool: '工具', context: '上下文', route: '路由', panel: '面板', widget: '运行组件' };
// 贡献分组的固定显示顺序；payload 里出现这里没有的种类时排在后面，按它自己的名字。
const CAPABILITY_KIND_ORDER = ['tool', 'context', 'route', 'panel', 'widget'];

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

// --- 设置里的技能清单：把发现结果翻译成人看得见的行 -------------------------
//
// 服务端回答两件事：本机发现了哪些技能、每一个现在是否启用。来源（scope）与
// 停用原因是技能自己的属性，有就照原样显示。和技能目录里没有的东西一律不补：
// 一个连启用状态都没报的条目没有可点的控件，也不会被写成“已停用”。

const SKILLS_PATH = '/api/skills';
const SKILL_SCOPE_LABELS = { user: '用户级', project: '项目级', builtin: '内置' };
// 描述最长 1024 字符（README 里写明的上限），整段塞进一行会把模态撑开。行里只
// 显示截断后的文字，完整描述留在 title 上。
const SKILL_DESCRIPTION_MAX_CHARS = 120;

// skillDescriptionText collapses whitespace and clips a description for the row.
// The clip is written as an ellipsis, never as a silent cut.
function skillDescriptionText(value) {
  const text = capabilityPanelText(value).replace(/\s+/g, ' ');
  if (text.length <= SKILL_DESCRIPTION_MAX_CHARS) return text;
  return `${text.slice(0, SKILL_DESCRIPTION_MAX_CHARS - 1)}…`;
}

function skillScopeLabel(scope) {
  return capabilityLabel(scope, SKILL_SCOPE_LABELS, '来源未知');
}

// skillActionPath is the only place the browser builds a skill's state path. A
// skill name is a directory name: letters, digits, dots, underscores and
// hyphens, with no path separator. Anything else could not have been
// discovered, so it is refused here instead of being turned into a request; a
// caller that gets '' must not fetch at all.
function skillActionPath(name, action) {
  const value = typeof name === 'string' ? name : '';
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(value)) return '';
  if (action !== 'enable' && action !== 'disable') return '';
  return `${SKILLS_PATH}/${encodeURIComponent(value)}/${action}`;
}

// skillRows reads the payload of GET /api/skills into one row per skill, in the
// order the server listed them. Only a row without a name is dropped: nothing
// can address it, and a nameless row would need an invented name to be shown.
function skillRows(payload) {
  const list = payload && typeof payload === 'object' ? payload.skills : null;
  if (!Array.isArray(list)) return [];
  const rows = [];
  for (const value of list) {
    const skill = value && typeof value === 'object' ? value : null;
    if (!skill) continue;
    const name = capabilityPanelText(skill.name);
    if (!name) continue;
    // `enabled` is a boolean the server either sent or did not. A missing or
    // non-boolean value is an unknown state, which is not the same as disabled:
    // it gets no state word of its own and no control.
    const reported = typeof skill.enabled === 'boolean';
    const enabled = skill.enabled === true;
    let action = reported ? (enabled ? 'disable' : 'enable') : '';
    if (skillActionPath(name, action) === '') action = '';
    const description = capabilityPanelText(skill.description);
    rows.push({
      name,
      description,
      descriptionText: skillDescriptionText(description) || '没有写描述。',
      descriptionMissing: description === '',
      scope: capabilityPanelText(skill.scope),
      scopeLabel: skill.managed === true ? '个人技能 · ' + capabilityPanelText(skill.revision).slice(0, 12) : skillScopeLabel(skill.scope),
      reported,
      enabled,
      stateLabel: reported ? (enabled ? '已启用' : '已停用') : '状态未报',
      disabledReason: capabilityPanelText(skill.disabled_reason),
      error: capabilityPanelText(skill.error),
      action,
      actionLabel: reported ? (enabled ? '停用' : '启用') : ''
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
//
// 两个运行预算（一轮最多多少轮模型回合、最多多长时间）也是进程启动时定下的，
// 但它们会真的结束一次运行，所以必须看得见；界面只转述服务端报出的数字。

const REASONING_EFFORT_LABELS = { none: '不思考', minimal: '极简', low: '低', medium: '中', high: '高', xhigh: '更高', max: '最高' };
const REASONING_EFFORT_LEVELS = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];

// runBudgetView 把两个预算翻成一行字。缺任何一个都不编造：服务端没有报出的数字
// 就是"这个进程没有报告"，而不是零。
function runBudgetView(state) {
  const payload = state && typeof state === 'object' ? state : {};
  const turns = Number(payload.max_iterations);
  const timeout = Number(payload.run_timeout_ms);
  const parts = [];
  if (Number.isFinite(turns) && turns > 0) parts.push(`${turns} 轮模型回合`);
  if (Number.isFinite(timeout) && timeout > 0) parts.push(`${Math.round(timeout / 60000)} 分钟`);
  if (!parts.length) {
    return { label: '这个进程没有报告运行预算', note: '运行预算由服务端施加；没有报出数字时界面不猜一个。' };
  }
  return {
    label: parts.join(' · '),
    note: '一轮运行最多用掉这些轮次与时长，触到任一个都会结束这次运行并说明原因。要改：设置 LUNA_MAX_ITERATIONS / LUNA_RUN_TIMEOUT，或在 config.yaml 里写 max_iterations / run_timeout，然后重启 Luna。'
  };
}

function reasoningEffortView(value) {
  const text = capabilityPanelText(value);
  if (text === '') {
    return {
      present: false,
      value: '',
      label: '不发送思考档位',
      note: '未发送 reasoning_effort，使用模型服务的默认档位；可通过 /reasoning 或输入区设置切换。'
    };
  }
  return {
    present: true,
    value: text,
    label: REASONING_EFFORT_LEVELS.includes(text)
      ? `${REASONING_EFFORT_LABELS[text]}（${text}）`
      : `${text}（未识别）`,
    note: '可通过 /reasoning 或输入区设置切换，后续运行生效。它决定模型怎么想，与推理过程是否展示无关。支持范围由所选提供方和模型决定，不支持时明确报错，不静默降档。'
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

// A Workspace is the set of directories the current work touches: a session may
// name one, several, or none. workspaceBadge turns a session's read-only
// workspace field into the marker shown next to the title, or null when the
// session is not tied to any workspace — no placeholder is drawn in that case.
// The label stays one short line whatever the count (it carries the number of
// directories, not their names), so a workspace with ten directories does not
// widen the header; the paths are listed one per line in the element's title.
const WORKSPACE_NAME_MAX_CHARS = 24;
const WORKSPACE_DIR_MAX_LINES = 6;

function workspaceBadge(workspace) {
  if (!workspace || typeof workspace !== 'object') return null;
  const name = typeof workspace.name === 'string' ? workspace.name.trim() : '';
  const dirs = Array.isArray(workspace.dirs)
    ? workspace.dirs.filter((dir) => typeof dir === 'string' && dir !== '')
    : [];
  if (!name && dirs.length === 0) return null;
  const shownName = name.length > WORKSPACE_NAME_MAX_CHARS
    ? `${name.slice(0, WORKSPACE_NAME_MAX_CHARS - 1)}…`
    : name;
  const label = [shownName, dirs.length ? `${dirs.length} 个目录` : ''].filter(Boolean).join(' · ');
  // 绝对路径只在悬停时出现：同一项目的两份检出让路径比目录名更有区分度，
  // 而这里是用户自己的机器。行数有上限，长清单不会变成一个巨大的提示框。
  const listed = dirs.slice(0, WORKSPACE_DIR_MAX_LINES);
  const title = dirs.length > listed.length
    ? `${listed.join('\n')}\n…还有 ${dirs.length - listed.length} 个目录`
    : listed.join('\n');
  return { label, title };
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
    if (record.type === 'session' || record.type === 'config') continue;
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
        const reported = usageSnapshot(record.usage);
        if (reported) open.usage = reported;
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

// 内联时间线里那几行标号：阶段行、运行说明、推理条目。推理条目只在
// assistant.reasoning 真的到达时才出现，没有这个事件时界面上不会出现任何相关
// 文案，也不预留位置。回答本身不再有一行文档式的标注：它靠整宽正文、位置（时间线
// 收尾）和一档更大的留白成为回合的主体（见 style.css 的 .assistant-body），
// 答案正文里只有真正渲染出来的那些节点。
const RUN_NOTE_LABEL = '运行说明';
const RUN_REASONING_LABEL = '推理';
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
// 用量只接受可精确表示的非负整数。旧事件缺少完整性字段时按部分报告显示。
function usageSnapshot(data) {
  if (!data || typeof data !== 'object' || (data.scope && data.scope !== 'run')) return null;
  const valid = value => Number.isSafeInteger(value) && value >= 0;
  if (!valid(data.input_tokens) || !valid(data.output_tokens)) return null;
  const total = data.input_tokens + data.output_tokens;
  if (!valid(total)) return null;
  const result = { input_tokens: data.input_tokens, output_tokens: data.output_tokens, total_tokens: total,
    model_calls: valid(data.model_calls) ? data.model_calls : 0, reported_calls: valid(data.reported_calls) ? data.reported_calls : 0, complete: false };
  for (const key of ['cached_tokens', 'reasoning_tokens']) {
    if (data[key] !== undefined) { if (!valid(data[key])) return null; result[key] = data[key]; }
  }
  result.complete = data.complete === true && result.model_calls > 0 && result.reported_calls === result.model_calls;
  return result;
}

function usageFromRecords(records) {
  const runs = new Map();
  let latest = '';
  (Array.isArray(records) ? records : []).forEach((record, index) => {
    if (!record || !['message', 'tool_call', 'run'].includes(record.type)) return;
    const id = typeof record.run_id === 'string' && record.run_id ? record.run_id : record.type === 'run' ? 'legacy-' + index : '';
    if (!id) return;
    if (!runs.has(id)) runs.set(id, null);
    latest = id;
    if (record.type === 'run') runs.set(id, usageSnapshot(record.usage));
  });
  return { runs, latest };
}

function usageBarView(runs, currentID, waiting = false) {
  const current = runs.get(currentID);
  const describe = (value, label) => !value ? label + ' 未报告' : label + (value.complete ? ' ' : ' 已报告 ') + formatTokenCount(value.total_tokens) + ' tokens' + (value.complete ? '' : '（不完整）');
  const currentText = currentID ? describe(current, '本轮') : waiting ? '本轮 等待报告' : '本轮 尚无运行';
  let total = 0, known = 0, incomplete = false, overflow = false;
  for (const value of runs.values()) {
    if (!value) { incomplete = true; continue; }
    known += 1;
    total += value.total_tokens;
    if (!Number.isSafeInteger(total)) overflow = true;
    if (!value.complete) incomplete = true;
  }
  const session = overflow ? '会话 用量超出可精确统计范围' : !runs.size ? '会话 尚无运行' : !known ? '会话 用量未报告'
    : '会话 ' + (incomplete ? '已报告 ' : '') + formatTokenCount(total) + ' tokens' + (incomplete ? '（含未报告或不完整用量）' : '');
  const detail = current ? '本轮 ' + usageText(current) + (current.complete ? '' : ' · 仅为已报告小计') : currentText;
  return { text: currentText + ' · ' + session, title: detail + '\n' + session };
}

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

// --- The command table -------------------------------------------------------
// The server owns the table, so nothing here invents a command name or a meaning
// for one. These helpers only answer what a draft means against the table; the
// composer renders what they return and never calls the model for a command.

// Every name a command answers to: `name` plus its aliases. A draft reaches a
// command through any of them.
function commandNames(command) {
  if (!command || typeof command !== 'object') return [];
  const names = [command.name, ...(Array.isArray(command.aliases) ? command.aliases : [])];
  return names.filter((name) => typeof name === 'string' && name !== '');
}

// Look a word up by name or alias, ignoring case: `/HELP` and `/help` are the
// same entry, and a word the table does not carry is simply not a command.
function findCommand(commands, word) {
  const wanted = typeof word === 'string' ? word.toLowerCase() : '';
  if (!wanted) return null;
  for (const command of Array.isArray(commands) ? commands : []) {
    if (commandNames(command).some((name) => name.toLowerCase() === wanted)) return command;
  }
  return null;
}

// One candidate row. `insert` is the whole draft the composer should hold after
// completing it, which is what keeps completion a plain text replacement.
function commandMenuItem(command) {
  const names = commandNames(command);
  if (!names.length) return null;
  const name = names[0];
  return {
    name,
    label: `/${name}`,
    usage: typeof command.usage === 'string' ? command.usage : '',
    summary: typeof command.summary === 'string' ? command.summary : '',
    insert: `/${name} `
  };
}

// The whole table, in the order the server reported it.
function commandList(commands) {
  return (Array.isArray(commands) ? commands : []).map(commandMenuItem).filter(Boolean);
}

// What a draft means right now:
//   mode 'command' — `/nam` is still one word, so the names are the candidates;
//   mode 'options' — `/name ` where that command declares args=options;
//   mode 'none'    — anything else, including prose. A plain sentence never
//                    grows a menu.
function commandCandidates(commands, draft) {
  const text = typeof draft === 'string' ? draft : '';
  if (!text.startsWith('/')) return { mode: 'none', items: [] };
  const body = text.slice(1);
  const gap = body.search(/\s/);
  if (gap < 0) {
    const prefix = body.toLowerCase();
    const items = [];
    for (const command of Array.isArray(commands) ? commands : []) {
      if (!commandNames(command).some((name) => name.toLowerCase().startsWith(prefix))) continue;
      const item = commandMenuItem(command);
      if (item) items.push(item);
    }
    return { mode: 'command', items };
  }
  const word = body.slice(0, gap);
  // A further space means the argument is written: a second value is another
  // argument, not a candidate for this one.
  const rest = body.slice(gap + 1);
  const command = findCommand(commands, word);
  if (!command || command.args !== 'options' || /\s/.test(rest)) return { mode: 'none', items: [] };
  const prefix = rest.toLowerCase();
  const items = [];
  for (const option of Array.isArray(command.options) ? command.options : []) {
    if (!option || typeof option.value !== 'string' || !option.value) continue;
    if (!option.value.toLowerCase().startsWith(prefix)) continue;
    items.push({
      name: command.name,
      value: option.value,
      label: option.value,
      usage: `/${command.name} ${option.value}`,
      summary: typeof option.summary === 'string' ? option.summary : '',
      insert: `/${command.name} ${option.value} `
    });
  }
  return { mode: 'options', items };
}

// provider 是一份可命名的设置：名字 → 接口地址、密钥与模型。下面这几个纯函数就是
// 界面与服务端之间的那份形状：整份文件替换，密钥字段空着表示沿用同名 provider 已
// 保存的那个，所以"没重填"永远不等于"清空"。
function providerEndpointHost(baseURL) {
  const text = String(baseURL ?? '').trim();
  if (!text) return '';
  const match = text.match(/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\/([^/?#]*)/);
  return match ? match[1] : text;
}

// providerModelsList 收的是"其他模型"：去掉空白、去掉与默认模型重复的名字，顺序照
// 用户给的来。
function providerModelsList(model, models) {
  const wanted = String(model ?? '').trim();
  const list = [];
  for (const raw of models || []) {
    const name = String(raw ?? '').trim();
    if (!name || name === wanted || list.includes(name)) continue;
    list.push(name);
  }
  return list;
}

// providerRequestBody 出的永远是整份文件：active 加全部 providers。不在数组里的
// provider 就是被删掉的，所以改名字等于删掉旧的再加上新的。draft 是表单里正在编辑
// 的那一份（可能还没保存过），按原名替换同名条目；没在编辑谁就传 null。
function providerRequestBody(view, active, draft) {
  const providers = [];
  let draftBody = null;
  if (draft) {
    draftBody = {
      name: String(draft.name ?? '').trim(),
      base_url: String(draft.base_url ?? '').trim(),
      model: String(draft.model ?? '').trim(),
      models: providerModelsList(draft.model, draft.models),
      api_key: String(draft.api_key ?? ''),
      clear_api_key: false
    };
  }
  let placed = false;
  for (const row of (view && view.providers) || []) {
    if (draftBody && draft.originalName && row.name === draft.originalName) {
      placed = true;
      providers.push(draftBody);
      continue;
    }
    providers.push({
      name: row.name,
      base_url: row.base_url || '',
      model: row.model || '',
      models: [...(row.models || [])],
      api_key: '',
      clear_api_key: false
    });
  }
  if (draftBody && !placed) providers.push(draftBody);
  return { active: String(active ?? ''), providers };
}

// 「允许写入的目录」是用户在设置里定下的一份清单：模型只能在这些目录里新建或覆盖
// 文件，所以它只会比工作区更窄。下面这几个纯函数是界面与这份清单之间的形状，服务端
// 仍然会规范化、去重并拒绝不合法的条目 —— 界面上的检查只是让用户少跑一趟请求。
const WRITE_DIR_MAX_CHARS = 44;

// 路径就是路径：这里不拼接、不做相对解析，只认绝对路径（以 / 开头）。非绝对路径的
// 条目服务端也会拒，所以本地这一下检查的意义是就地报错、不发请求。
function writeDirAbsolute(value) {
  return typeof value === 'string' && value.trim().startsWith('/');
}

// 一行放不下很长的路径：中段省略，长度有上限；原样的路径留在元素的 title 上。
function writeDirText(dir) {
  const text = String(dir ?? '');
  if (text.length <= WRITE_DIR_MAX_CHARS) return text;
  const head = Math.ceil((WRITE_DIR_MAX_CHARS - 1) / 2);
  return `${text.slice(0, head)}…${text.slice(text.length - (WRITE_DIR_MAX_CHARS - 1 - head))}`;
}

// 候选 = 工作区里的目录中还没有被允许的那些：顺序照工作区的顺序，同一个目录只出现
// 一次。两个集合都由调用方给，这里不猜路径之间的关系。
function writeDirCandidates(dirs, workspaceDirs) {
  const allowed = new Set((Array.isArray(dirs) ? dirs : []).map((dir) => String(dir ?? '')));
  const out = [];
  for (const dir of Array.isArray(workspaceDirs) ? workspaceDirs : []) {
    const text = String(dir ?? '');
    if (!text || allowed.has(text) || out.includes(text)) continue;
    out.push(text);
  }
  return out;
}

// 允许与不再允许都提交整份列表：这里只算那一份列表，结果以服务端的答复为准。
function writeDirsWith(dirs, dir) {
  const list = (Array.isArray(dirs) ? dirs : []).map((item) => String(item ?? '')).filter(Boolean);
  if (dir && !list.includes(dir)) list.push(dir);
  return list;
}

function writeDirsWithout(dirs, dir) {
  return (Array.isArray(dirs) ? dirs : [])
    .map((item) => String(item ?? ''))
    .filter((item) => item && item !== dir);
}

if (typeof module !== 'undefined') {
  module.exports = {
    parseEventBlock, toolSummary, toolLabel, toolActivityLabel, reloadCopy, valueOrDash,
    pluginStatusLabel, pluginRows, parseInline, parseMarkdownBlocks,
    isSessionID, parseSessionHash, sessionHash, sessionTitle, sessionTime, relativeTime, runCountLabel,
    workspaceBadge, WORKSPACE_NAME_MAX_CHARS, WORKSPACE_DIR_MAX_LINES,
    runStatusLabel, sessionRows, argumentsText, toolCallFacts, replaySession, runPayload,
    clipText, toolArgumentsText, toolResultText, formatElapsed, formatDuration, toolFailureKind,
    toolRefusalReason, toolRefusedLabel, toolStateLabel, runPhaseText, runPhaseEntryText, runPhaseVisible,
    runOutcomeLabel, runTraceMeta, replayRunState, formatTokenCount, usageText, usageSnapshot, usageFromRecords, usageBarView, reasoningPreview,
    TOOL_TEXT_MAX_CHARS, TOOL_TEXT_MAX_LINES, TOOL_REFUSAL_PREFIX,
    RUN_NOTE_LABEL, RUN_REASONING_LABEL, RUN_CANCELLED_COPY, REASONING_PREVIEW_CHARS,
    uiPluginText, uiPluginNameValid, uiPluginEntrySafe, uiPluginEntryURL, uiPluginRows, uiPluginMissingExports,
    uiPluginErrorDetail, uiPluginImportError, uiPluginMissingExportError, uiPluginMountError, uiPluginUnmountError,
    uiPluginState, uiPluginInitialState, uiPluginTransition, uiPluginEnableFailureEvent, uiPluginDisableEvent,
    uiPluginTeardown, uiPluginAbandonMount, uiPluginToggleAction, uiPluginToggleLabel, uiPluginStatusText,
    uiPluginHostAPI, UI_PLUGIN_API_VERSION, UI_PLUGIN_ENTRY_REASON, UI_PLUGIN_REQUIRED_EXPORTS,
    capabilityPanelText, capabilityPanelEntryURL, capabilityPanelElementID, capabilityPanels,
    capabilityPanelEntryError, capabilityPanelImportError, capabilityPanelMissingExportError,
    capabilityPanelMountError, capabilityPanelUnmountError, capabilityWidgets,
    capabilityStatePath, capabilityStateLabel, capabilityDeploymentLabel, capabilityKindLabel,
    capabilityRows, capabilityGroups, capabilityClaimRows, capabilityPermissionRows,
    SKILLS_PATH, SKILL_SCOPE_LABELS, SKILL_DESCRIPTION_MAX_CHARS,
    skillDescriptionText, skillScopeLabel, skillActionPath, skillRows,
    reasoningEffortView, REASONING_EFFORT_LEVELS,
    commandNames, findCommand, commandMenuItem, commandList, commandCandidates,
    providerEndpointHost, providerModelsList, providerRequestBody,
    WRITE_DIR_MAX_CHARS, writeDirAbsolute, writeDirText, writeDirCandidates, writeDirsWith, writeDirsWithout
  };
}

if (typeof document !== 'undefined') {
  const $ = (id) => document.getElementById(id);
  let widgetStorage;
  try { widgetStorage = window.localStorage; } catch (_) {}
  const runtimeWidgets = LunaRuntimeWidgets.createHost({
    document, window, root: $('runtime-widget-layer'), menu: $('widget-menu'), storage: widgetStorage,
    getBounds: () => ({ width: window.innerWidth, height: Math.max(64, $('chat-form').getBoundingClientRect().top - 12) }),
    onError: () => { const node = $('conversation-status'); node.textContent = '某个运行组件无法更新，可隐藏后重新打开。'; node.hidden = false; }
  });
  runtimeWidgets.register({ id: 'usage', title: 'Token 用量', mount(body) {
    const node = $('session-usage'); body.append(node);
    return { update(view) { node.textContent = view?.text || '尚无运行'; node.title = view?.title || ''; } };
  } });
  runtimeWidgets.register({ id: 'activity', title: '运行活动', mount(body) { body.append($('run-status')); } });
  // 外观和组件布局属于浏览器偏好；会话内容与执行授权仍由服务端管理。
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
  const conversationStatus = $('conversation-status');
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
  // 贡献面板的入口插在运行详情之前，所以这里拿着那个节点作为插入锚点：页头的
  // 顺序是"能力贡献的面板，然后运行详情"，宿主不决定有哪些能力。
  const runtimeActions = document.querySelector('.runtime-actions');
  const sessionMedia = window.matchMedia('(max-width: 800px)');
  // 打开的面板登记在案：运行详情是内核自己的，能力贡献的面板在 syncCapabilityPanels
  // 里按 /api/state 增减。界面插件与插件诊断不再是页头面板，它们是设置里的分类。
  const panels = {
    execution: { element: $('execution-panel'), toggle: $('session-execution'), close: $('execution-close'), refresh: refreshExecutionPanel, teardown: () => { $('execution-confirm').checked = false; } },
    runtime: { element: runtimeDrawer, toggle: runtimeToggle, close: runtimeClose, refresh: updateState },
    sessions: { element: sessionSidebar, toggle: sessionToggle, close: sessionClose, refresh: updateSessions },
    // 打开设置时重新读一次状态：能力清单与模型服务参数都是这一刻的事实，不是页
    // 面首次加载时的旧值。
    settings: { element: $('settings-panel'), toggle: $('settings-toggle'), close: $('settings-close'), refresh: () => { updateState(); updateVisibleSettingsPane(); } }
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
  // 设置里的工作区与模型清单：两页都在被打开时才读一次服务端，节点在这里拿到
  // 一次，后面只改内容。
  const workspaceList = $('workspace-list');
  const workspaceEmpty = $('workspace-empty');
  const workspaceStatus = $('workspace-status');
  const workspaceRefresh = $('workspace-refresh');
  const modelList = $('settings-model-list');
  const modelListEmpty = $('settings-model-empty');
  const modelSwitchStatus = $('settings-model-status');

  let running = false;
  let reloading = false;
  let reloadableTools = [];
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
  const approvalCards = new Map();
  let approvalRevision = 0;
  let lastCompletedRunID = '';
  let remoteCancelID = '';
  // 当前会话绑定的工作区 id（空表示没有绑定）。它来自会话回放或绑定接口的答复，
  // 所以设置里的工作区一页能把"正在用"标出来，而不是自己猜。
  let currentWorkspaceID = '';
  let sessionModelChoice = null;
  let sessionReasoningChoice = null;
  let sessionSetupChoice = null;
  let sessionSetupProblem = '';
  let setupRevision = 0;
  let setupController = null;
  let presetPickerController = null;
  let sessionControlsProblem = '';
  let sessionControlsRevision = 0;
  let controlPickerRevision = 0;
  let lastRuntimeState = null;
  let sessionExecution = null;
  let executionError = '';
  let permissionDraftDirty = false;
  let executionRevision = 0;
  let executionDialogSessionID = '';
  let sessionUsageByRun = new Map();
  let usageRunID = '';


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
    // 用户做了一次操作：上一次的错误提示不再挂着。
    setConversationStatus('');
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
    if (!turnState.runID || data.run_id !== turnState.runID) return;
    const snapshot = usageSnapshot(data);
    sessionUsageByRun.set(turnState.runID, snapshot);
    usageRunID = turnState.runID;
    turnState.usage = snapshot ? usageText(data) : '';
    if (data.scope === 'run' && data.complete === false && turnState.usage) turnState.usage += ' · 已报告部分用量';
    updateRunMeta(turnState);
    renderUsageBar();
  }

  function renderUsageBar() {
    const view = usageBarView(sessionUsageByRun, usageRunID, running);
    runtimeWidgets.update('usage', view);
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
    send.type = action === 'send' ? 'submit' : 'button';
    const label = action === 'send' ? '发送' : action === 'cancelling' ? '正在取消' : '停止';
    send.dataset.action = action;
    send.setAttribute('aria-label', label);
    send.setAttribute('title', label);
    send.replaceChildren(action === 'send' ? sendIcon : stopIconNode());
  }

  function beginRun() {
    closeComposerPopover(false);
    closeCommandMenu();
    running = true;
    usageRunID = '';
    liveRun = { runID: '', state: 'connecting', toolName: '', startedAt: 0, notice: '', cancelling: false };
    applyRunStatus();
    startStatusTicker();
    setSendAction('cancel');
    setSessionControls();
  }

  // 收到终止事件就立刻退出运行状态，不留一个还在转的 loading。
  function endRun() {
    if (!running && !liveRun) return;
    if(liveRun?.runID)lastCompletedRunID=liveRun.runID;
    clearApprovals();
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
      usageRunID = runID;
      if (runID && !sessionUsageByRun.has(runID)) sessionUsageByRun.set(runID, null);
      renderUsageBar();
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
    } else if (type === 'approval.requested') {
      showApproval(data);
      if (liveRun) liveRun.notice = '等待你的审批';
      applyRunStatus();
    } else if (type === 'approval.resolved') {
      if (data.session_id === currentSessionID) removeApproval(data.id);
      applyRunStatus();
    } else if (type === 'tool.started') {
      if (currentTurn) startToolCard(currentTurn, data);
      if (liveRun) {
        liveRun.state = 'tool';
        liveRun.toolName = typeof data.name === 'string' ? data.name : '';
      }
      applyRunStatus();
    } else if (type === 'tool.finished') {
      refreshWidgetSources();
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
    if(remoteBusy()){setConversationStatus('已有运行进行中，请先停止它或等待完成。',true);return;}
    if (!executionReady()) { setConversationStatus('请先读取执行权限，或在权限面板中确认 Full access / 切回隔离。', true); return; }
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
    // A draft that opens with `/` is the composer's business, not the model's:
    // either it is a command this front end answers itself, or it is refused
    // here. Neither path reaches /api/runs.
    const draft = input.value.trim();
    if (draft.startsWith('/')) {
      submitCommand(draft);
      return;
    }
    submitMessage(input.value);
  });

  // 运行期间同一个发送按钮就是 Stop：点击它取消这次运行，而不是再发一条消息。
  send.addEventListener('click', (event) => {
    if (!liveRun) {if(remoteRunID()){event.preventDefault();cancelRemoteRun();}return;}
    event.preventDefault();
    cancelRun();
  });

  input.addEventListener('keydown', (event) => {
    // 候选打开时方向键选择，Tab 只补全；Enter 选中模型/思考参数后直接保存设置，
    // 不启动模型。列表关闭时 Enter 保持原有发送语义。
    if (commandItems.length) {
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault();
        moveCommandSelection(event.key === 'ArrowDown' ? 1 : -1);
        return;
      }
      if (event.key === 'Escape') {
        event.preventDefault();
        closeCommandMenu();
        return;
      }
      if (event.key === 'Tab' || (event.key === 'Enter' && !event.shiftKey && !event.isComposing)) {
        // 命令名进入参数选择；参数确认仅调用设置接口。Tab 始终不提交。
        event.preventDefault();
        completeCommandSelection(event.key !== 'Tab');
        return;
      }
    }
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      if (!running && !switching) form.requestSubmit();
    }
  });

  function resizeInput() {
    input.style.height = 'auto';
    input.style.height = `${Math.min(input.scrollHeight, 144)}px`;
    runtimeWidgets.reflow();
  }
  input.addEventListener('input', resizeInput);

  // --- The composer's command list ---------------------------------------------
  // The table is read once at startup. A failure is not an error state: the
  // composer still sends messages, it just has no candidates to offer, and no
  // error copy is written for a table the user never asked about.
  const commandMenuNode = $('command-menu');
  let commandTable = [];
  // The rows the open list is showing, in order. Empty means the list is closed:
  // that single fact drives both the keyboard layer and the rendering.
  let commandItems = [];
  let commandSelection = -1;

  async function loadCommands() {
    try {
      const response = await fetch('/api/commands', { cache: 'no-store' });
      if (!response.ok) return;
      const payload = await response.json();
      commandTable = Array.isArray(payload?.commands) ? payload.commands : [];
    } catch (_) {
      commandTable = [];
    }
  }

  function closeCommandMenu() {
    controlPickerRevision += 1;
    presetPickerController?.abort();
    presetPickerController = null;
    $('session-model').setAttribute('aria-expanded', 'false');
    $('session-reasoning').setAttribute('aria-expanded', 'false');
    $('session-preset').setAttribute('aria-expanded', 'false');
    commandItems = [];
    commandSelection = -1;
    commandMenuNode.replaceChildren();
    commandMenuNode.hidden = true;
  }

  function markCommandSelection() {
    [...commandMenuNode.children].forEach((row, index) => {
      const selected = index === commandSelection;
      row.className = selected ? 'command-menu-row is-selected' : 'command-menu-row';
      row.setAttribute('aria-selected', selected ? 'true' : 'false');
    });
  }

  function renderCommandList(items) {
    commandItems = items;
    commandSelection = items.length ? 0 : -1;
    commandMenuNode.replaceChildren();
    items.forEach((item, index) => {
      const row = make('li', 'command-menu-row');
      // 点击与 Enter 一致：选中命令名后展开选项，选中参数后保存会话设置。
      row.setAttribute('role', 'option');
      row.setAttribute('aria-selected', index === commandSelection ? 'true' : 'false');
      row.append(make('span', 'command-menu-name', item.label));
      if (item.usage) row.append(make('span', 'command-menu-usage', item.usage));
      if (item.summary) row.append(make('span', 'command-menu-summary', item.summary));
      row.addEventListener('mousedown', (event) => {
        event.preventDefault();
        commandSelection = index;
        completeCommandSelection(true);
      });
      commandMenuNode.append(row);
    });
    commandMenuNode.hidden = items.length === 0;
  }

  // Every keystroke re-reads the draft: the draft is the only source of truth,
  // so completion, deletion and paste all leave the list showing what the line
  // means now instead of what it meant before.
  function refreshCommandMenu() {
    const view = commandCandidates(commandTable, input.value.trimStart());
    if (view.mode === 'none') {
      closeCommandMenu();
      return;
    }
    renderCommandList(view.items);
  }

  function moveCommandSelection(step) {
    const count = commandItems.length;
    if (!count) return;
    commandSelection = (commandSelection + step + count) % count;
    markCommandSelection();
    // The selected row has to be visible: with more candidates than the list
    // can show, this is the only feedback that the arrow key did anything.
    const row = [...commandMenuNode.children][commandSelection];
    if (row && typeof row.scrollIntoView === 'function') row.scrollIntoView({ block: 'nearest' });
  }

  function completeCommandSelection(execute = false) {
    const item = commandItems[commandSelection];
    if (!item) { closeCommandMenu(); return; }
    closeCommandMenu();
    if (execute && item.value !== undefined && ['model', 'reasoning', 'permissions', 'preset'].includes(item.name)) {
      if (item.name === 'model') applyModelChoice(item.value);
      else if(item.name==='permissions') applyPermissionCommand(item.value);
      else if(item.name==='preset') applyPresetChoice(item.value);
      else applyReasoningChoice(item.value);
      input.focus();
      return;
    }
    input.value = item.insert;
    resizeInput();
    input.focus();
    if (execute && ['model', 'reasoning', 'preset'].includes(item.name)) openControlPicker(item.name);
  }

  // The answer to `/help` is the table itself: every command, in the order the
  // server reported it, with nothing sent anywhere.
  function openCommandMenu() {
    renderCommandList(commandList(commandTable));
    if (!commandItems.length) setConversationStatus('暂时读不到命令表。', true);
  }

  // A refused command is answered where the user is looking — the conversation
  // area's status line — and never as a turn: no message is added to the
  // transcript and no run is started.
  // `/model` asks the server two questions or tells it one thing: what the
  // models are, or which of them this session should use. The answer is a
  // session record, so the choice survives a reload and travels with the
  // session — the browser keeps no copy of it.
  function sessionQuery() {
    return isSessionID(currentSessionID) ? '?session=' + encodeURIComponent(currentSessionID) : '';
  }

  const permissionKinds=['read','write','network','exec'];
  const defaultPermissions={read:'allow',write:'ask',network:'ask',exec:'ask'};
  function validPermissions(value) {return value&&permissionKinds.every(kind=>['allow','ask','deny'].includes(value[kind]));}
  function permissionSummary(value) {
    if (permissionKinds.some(kind=>value[kind]==='ask')) return '需审批 ▾';
    if (value.read==='allow'&&['write','network','exec'].every(kind=>value[kind]==='deny')) return '只读 ▾';
    return permissionKinds.every(kind=>value[kind]==='allow')?'已授权 ▾':'自定义权限 ▾';
  }
  function executionReady() {
    return Boolean(sessionExecution && ['sandbox', 'full_access'].includes(sessionExecution.mode) && !executionError && !sessionExecution.needs_confirmation && !sessionExecution.problem);
  }

  function resetExecutionState() {
    permissionDraftDirty=false;
    executionRevision += 1;
    sessionExecution = null;
    executionError = '';
    $('execution-confirm').checked = false;
    executionDialogSessionID = currentSessionID;
  }

  function renderExecutionState() {
    const control = $('session-execution');
    const view = sessionExecution;
    const full = view?.mode === 'full_access' && !view.needs_confirmation;
    let label = executionError ? '权限读取失败' : !view ? '读取权限…' : view.problem ? '权限需修复' : view.needs_confirmation ? 'Full access 待确认' : full ? 'Full access' : validPermissions(view.permissions) ? permissionSummary(view.permissions) : '隔离 ▾';
    control.textContent = label;
    control.classList.toggle('is-full-access', full);
    control.title = full ? '当前会话已获本次服务中的宿主用户文件与网络权限' : '查看并更改当前会话的命令执行权限';
    control.disabled = switching;
    if (activePanel === panels.execution && executionDialogSessionID !== currentSessionID) {
      $('execution-confirm').checked = false;
      executionDialogSessionID = currentSessionID;
    }
    $('execution-current').textContent = executionError ? '读取失败：' + executionError : view?.needs_confirmation ? '此会话保存了 Full access 偏好，但本次服务尚未授权。请重新确认或切回隔离。' : view?.problem || label;
    const busy = running || switching || Boolean(lastRuntimeState?.busy);
    $('execution-confirm').disabled = busy || !view || Boolean(executionError);
    $('execution-full').disabled = busy || !view || Boolean(executionError) || !$('execution-confirm').checked;
    $('execution-sandbox').disabled = busy;
    const valid=validPermissions(view?.permissions);
    for (const kind of permissionKinds) {
      const node=$('permission-'+kind);node.disabled=busy||!valid;
      if (!permissionDraftDirty) node.value=valid?view.permissions[kind]:'';
    }
    $('permission-save').disabled=busy||!valid;
    $('permission-default').disabled=busy;
    $('permission-note').textContent=!valid?'服务端尚未返回有效的权限矩阵。':view.permissions_need_confirmation?'此前的提升授权已失效；当前使用上方的有效策略，可重新选择并应用。':full?'Full access 绕过细粒度限制；应用矩阵会退出 Full access。':busy?'本轮使用已核准的策略；可以查看，停止后才能修改。':'允许、询问、拒绝分别生效；询问会在操作发生前暂停。';
    const scope=view?.scopes;
    const project=Array.isArray(scope?.project_dirs)?scope.project_dirs:[];
    const automatic=Array.isArray(scope?.automatic_write_dirs)?scope.automatic_write_dirs:[];
    $('permission-scope').textContent=!scope?'范围尚未返回。':[
      full?'Full access 命令可访问宿主；下列目录仅是文件工具的项目范围。':'文件工具与受限命令的项目目录：',
      project.length?project.join('\n'):'未绑定项目目录',
      '自动写入范围（仍受会话策略约束）：',automatic.length?automatic.join('\n'):'未设置；项目内写入需逐次批准',
      scope.network==='host'?'命令使用宿主网络。':'受限联网仅通过公共 HTTP(S)/CONNECT 代理；拒绝宿主与私网地址，不提供直接套接字或 UDP。',
      scope.write_problem||''
    ].filter(Boolean).join('\n');
    $('execution-pending').hidden=!approvalCards.size;
    $('execution-pending').textContent='查看待审批操作（'+approvalCards.size+'）';
  }

  async function updateExecutionState() {
    const revision = ++executionRevision;
    const id = currentSessionID;
    try {
      const response = await fetch('/api/execution' + sessionQuery(), { cache: 'no-store' });
      if (!response.ok) throw new Error(await errorMessage(response));
      const view = await response.json();
      if (!['sandbox', 'full_access'].includes(view.mode)) throw new Error('服务端没有返回有效的执行权限');
      if (revision !== executionRevision || id !== currentSessionID) return false;
      sessionExecution = view;
      executionError = '';
    } catch (error) {
      if (revision !== executionRevision || id !== currentSessionID) return false;
      sessionExecution = null;
      executionError = error.message;
    }
    setSessionControls();
    return !executionError;
  }

  function refreshExecutionPanel() {
    permissionDraftDirty=false;
    executionDialogSessionID = currentSessionID;
    $('execution-confirm').checked = false;
    $('execution-status').textContent = '';
    renderExecutionState();
    updateExecutionState();
  }

  async function chooseExecution(mode) {
    if (running || switching || lastRuntimeState?.busy) return;
    if (executionDialogSessionID !== currentSessionID) {
      $('execution-confirm').checked = false;
      $('execution-status').textContent = '会话已改变，请重新查看并确认权限。';
      renderExecutionState();
      return;
    }
    if (mode === 'full_access' && !$('execution-confirm').checked) {
      $('execution-status').textContent = '请先阅读风险说明并勾选确认。';
      return;
    }
    try {
      const body = mode === 'full_access' ? { mode, confirm_full_access: true } : { mode };
      const view = await saveSessionSetting('execution', body);
      if (!['sandbox', 'full_access'].includes(view.mode)) throw new Error('服务端未确认有效执行权限');
      executionRevision += 1;
      sessionExecution = view;
      permissionDraftDirty=false;
      executionError = '';
      $('execution-confirm').checked = false;
      setSessionControls();
      closeDrawer();
    } catch (error) {
      $('execution-confirm').checked = false;
      $('execution-status').textContent = '权限设置未成功确认：' + error.message;
      await updateExecutionState();
    }
  }

  async function savePermissionPolicy(policy) {
    if (running||switching||lastRuntimeState?.busy) {setConversationStatus('请先停止当前运行，再修改权限。',true);return false;}
    if (!validPermissions(policy)) {setConversationStatus('权限矩阵不完整。',true);return false;}
    try {
      const view=await saveSessionSetting('execution',{permissions:policy,confirm_permissions:true});
      if (!validPermissions(view.permissions)) throw new Error('服务端没有确认完整权限矩阵');
      sessionExecution=view;executionError='';permissionDraftDirty=false;
      $('execution-confirm').checked=false;setSessionControls();closeDrawer();
      clearControlDraft('permissions');return true;
    } catch(error) { $('execution-status').textContent='权限未保存：'+error.message;setConversationStatus('权限未保存：'+error.message,true);return false; }
  }
  async function applyPermissionCommand(argument) {
    if (!argument) {openDrawer(panels.execution);return;}
    if (argument==='--default') {await savePermissionPolicy({...defaultPermissions});return;}
    const parts=argument.trim().split(/[=\s]+/);
    if (parts.length!==2||!permissionKinds.includes(parts[0])||!['allow','ask','deny'].includes(parts[1])) {setConversationStatus('用法：/permissions read|write|network|exec allow|ask|deny，或 --default。',true);return;}
    if (!validPermissions(sessionExecution?.permissions)) {setConversationStatus('请先重新读取权限，不能猜测未返回的策略。',true);return;}
    await savePermissionPolicy({...sessionExecution.permissions,[parts[0]]:parts[1]});
  }
  for(const kind of permissionKinds) $('permission-'+kind).addEventListener('change',()=>{permissionDraftDirty=true;});
  $('permission-form').addEventListener('submit',event=>{event.preventDefault();savePermissionPolicy(Object.fromEntries(permissionKinds.map(kind=>[kind,$('permission-'+kind).value])));});
  $('permission-default').addEventListener('click',()=>savePermissionPolicy({...defaultPermissions}));
  $('execution-workspace').addEventListener('click',()=>openComposerSettings('workspace'));
  $('execution-pending').addEventListener('click',()=>{closeDrawer(false);const first=approvalCards.values().next().value;if(first){first.card.scrollIntoView?.({block:'nearest'});first.allow.focus();}});

  $('execution-confirm').addEventListener('change', renderExecutionState);
  $('execution-full').addEventListener('click', () => chooseExecution('full_access'));
  $('execution-sandbox').addEventListener('click', () => chooseExecution('sandbox'));
  $('execution-capabilities').addEventListener('click', () => { openDrawer(panels.settings); $('settings-tab-capabilities').click(); });

  function removeApproval(id) {
    const record=approvalCards.get(id);
    if (!record) return;
    const focused=record.card.contains(document.activeElement);
    record.card.remove();approvalCards.delete(id);
    $('approval-list').hidden=!approvalCards.size;
    if (focused) input.focus();
  }
  function clearApprovals() {
    approvalRevision++;
    for (const id of [...approvalCards.keys()]) removeApproval(id);
  }
  function showApproval(view) {
    if (!view || view.session_id!==currentSessionID || typeof view.run_id!=='string' || !/^[a-zA-Z0-9_-]{1,80}$/.test(view.id) || approvalCards.has(view.id)) return;
    const operation=view.operation;
    if (!operation || typeof operation.tool!=='string') return;
    const card=make('section','approval-card');card.id='approval-'+view.id;
    card.append(make('h3','approval-title','需要你的批准'),make('p','',textOr(operation.summary,operation.tool)));
    for (const [label,value] of [['目标',operation.target],['工作目录',operation.cwd],['命令',operation.command]]) {
      if (typeof value==='string'&&value) {card.append(make('div','approval-label',label),make('pre','approval-operation',value));}
    }
    if (Array.isArray(operation.read_roots)&&operation.read_roots.length) card.append(make('p','approval-scope','读取范围：'+operation.read_roots.join('、')));
    if (Array.isArray(operation.write_roots)&&operation.write_roots.length) card.append(make('p','approval-scope','写入范围：'+operation.write_roots.join('、')));
    const names={read:'文件读取',write:'文件修改',network:'联网',exec:'命令执行'};
    const requested=(Array.isArray(operation.ask)?operation.ask:operation.permissions||[]).map(kind=>names[kind]||kind).join('、');
    if (requested) card.append(make('p','approval-scope','本次请求：'+requested));
    if (operation.scope_approval) card.append(make('p','approval-scope','仅批准这一次的目标范围，不会加入自动写入目录。'));
    if (typeof operation.preview==='string'&&operation.preview) {
      const detail=make('details');detail.append(make('summary','','内容预览'),make('pre','approval-operation',operation.preview));card.append(detail);
    }
    const actions=make('div','approval-actions');
    const allow=make('button','luna-button','批准这一次'),deny=make('button','luna-button','拒绝');
    allow.id='approval-approve-'+view.id;deny.id='approval-deny-'+view.id;allow.type=deny.type='button';
    const status=make('p','luna-status');status.setAttribute('role','status');
    actions.append(allow,deny);card.append(actions,status);
    const record={view,card,allow,deny,status,busy:false};approvalCards.set(view.id,record);
    allow.addEventListener('click',()=>decideApproval(record,'approve'));
    deny.addEventListener('click',()=>decideApproval(record,'deny'));
    const stick=nearBottom();$('approval-list').append(card);$('approval-list').hidden=false;contentChanged(stick);
  }
  async function decideApproval(record,decision) {
    const {view}=record;
    if (record.busy||view.session_id!==currentSessionID||approvalCards.get(view.id)!==record) return;
    record.busy=true;record.allow.disabled=record.deny.disabled=true;record.status.textContent='正在提交…';
    try {
      const response=await fetch('/api/approvals/'+encodeURIComponent(view.id),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({run_id:view.run_id,session_id:view.session_id,decision})});
      if (!response.ok) {
        if (response.status===404||response.status===409) {removeApproval(view.id);setConversationStatus('这项审批已结束或失效。');return;}
        throw new Error(await errorMessage(response));
      }
      removeApproval(view.id);
    } catch (error) {
      if (approvalCards.get(view.id)===record) record.status.textContent='审批未提交：'+error.message;
    } finally {record.busy=false;record.allow.disabled=record.deny.disabled=false;}
  }
  async function refreshApprovals() {
    const id=currentSessionID,revision=++approvalRevision;
    if (!id || (!running&&!lastRuntimeState?.busy)) {for(const key of [...approvalCards.keys()])removeApproval(key);return;}
    try {
      const response=await fetch('/api/approvals?session='+encodeURIComponent(id),{cache:'no-store'});
      if (!response.ok) return;
      const payload=await response.json();
      if (revision!==approvalRevision||id!==currentSessionID) return;
      const list=Array.isArray(payload.approvals)?payload.approvals:[];
      const wanted=new Set(list.map(view=>view.id));
      for (const key of [...approvalCards.keys()]) if(!wanted.has(key)) removeApproval(key);
      for (const view of list) showApproval(view);
    } catch (_) { /* 现有审批卡保留，单次轮询失败不冒充取消。 */ }
  }

  function renderSessionControls() {
    renderUsageBar();
    renderExecutionState();
    const model = sessionModelChoice?.name || '未配置';
    const effort = sessionReasoningChoice ? reasoningEffortView(sessionReasoningChoice.reasoning_effort).label : '未读取';
    const modelButton = $('session-model');
    const effortButton = $('session-reasoning');
    modelButton.textContent = '模型：' + model;
    effortButton.textContent = '思考：' + effort;
    const source = (choice) => choice?.origin === 'session' ? '当前会话选择' : choice?.origin === 'setup' ? '继承工作预设' : '跟随全局默认';
    const presetButton = $('session-preset');
    presetButton.textContent = '预设：' + (sessionSetupChoice?.title || sessionSetupChoice?.id || '未选择');
    presetButton.title = [sessionSetupChoice?.revision ? '固定版本 ' + sessionSetupChoice.revision.slice(0, 12) : '为当前会话选择工作方式；不会授予权限', sessionSetupProblem].filter(Boolean).join(' · ');
    presetButton.disabled = running || switching;
    $('session-preset-detail').textContent = sessionSetupProblem;
    $('session-preset-detail').hidden = !sessionSetupProblem;
    modelButton.title = model + ' · ' + source(sessionModelChoice) + (sessionControlsProblem ? ' · 更新失败：' + sessionControlsProblem : '');
    effortButton.title = effort + ' · ' + source(sessionReasoningChoice) + (sessionControlsProblem ? ' · 更新失败：' + sessionControlsProblem : '');
    modelButton.disabled = effortButton.disabled = running || switching;
    if (sessionModelChoice?.name) $('model').textContent = sessionModelChoice.name;
    if (sessionReasoningChoice) $('effort').textContent = effort;
    const summary = $('session-settings');
    const shortEffort = sessionReasoningChoice?.reasoning_effort || '自动';
    summary.textContent = model + ' · ' + shortEffort + ' ▾';
    summary.title = modelButton.title + ' · ' + effortButton.title;
    summary.disabled = switching;
    summary.classList.toggle('has-error', Boolean(sessionControlsProblem));
  }

  async function updateSessionControls() {
    updateExecutionState();
    updateSetupState();
    const revision = ++sessionControlsRevision;
    const id = currentSessionID;
    const query = sessionQuery();
    try {
      const responses = await Promise.all([fetch('/api/models' + query, { cache: 'no-store' }), fetch('/api/reasoning' + query, { cache: 'no-store' })]);
      for (const response of responses) if (!response.ok) throw new Error(await errorMessage(response));
      const [models, reasoning] = await Promise.all(responses.map(response => response.json()));
      if (revision !== sessionControlsRevision || id !== currentSessionID) return;
      sessionModelChoice = models.current || null;
      sessionReasoningChoice = reasoning.current || null;
      sessionControlsProblem = '';
    } catch (error) {
      if (revision !== sessionControlsRevision || id !== currentSessionID) return;
      sessionControlsProblem = error.message;
    }
    renderSessionControls();
  }

  async function openControlPicker(kind) {
    if (running || switching) return;
    if (kind === 'preset') return openPresetPicker();
    closeComposerPopover(false);
    const revision = ++controlPickerRevision;
    const id = currentSessionID;
    try {
      const response = await fetch('/api/' + (kind === 'model' ? 'models' : 'reasoning') + sessionQuery(), { cache: 'no-store' });
      if (!response.ok) throw new Error(await errorMessage(response));
      const payload = await response.json();
      if (revision !== controlPickerRevision || id !== currentSessionID || running || switching) return;
      const current = payload.current || {};
      let options;
      if (kind === 'model') {
        sessionModelChoice = current;
        options = (payload.models || []).map(model => ({ value: model.name, summary: model.provider || '' }));
        options.push({ value: '--default', summary: '继承预设或全局模型' });
      } else {
        sessionReasoningChoice = current;
        options = [{ value: '--default', summary: '继承预设或全局设置' }, { value: '--off', summary: '不发送思考字段（不同于 none）' }, ...(payload.levels || []).map(value => ({ value }))];
      }
      const chosen = current.origin !== 'session' ? '--default' : kind === 'model' ? current.name : current.reasoning_effort || '--off';
      options = options.map(option => ({ ...option, summary: [option.value === chosen ? '当前' : '', option.summary].filter(Boolean).join(' · ') }));
      const items = commandCandidates([{ name: kind, args: 'options', options }], '/' + kind + ' ').items;
      renderCommandList(items);
      $('session-' + kind).setAttribute('aria-expanded', items.length ? 'true' : 'false');
      renderSessionControls();
      input.focus();
    } catch (error) { setConversationStatus('读取选项失败：' + error.message, true); }
  }

  // 首次设置即分配会话身份，不需要先花一次模型调用；hash 用 replaceState 更新，
  // 避免随后触发回放清空当前草稿。调用方持有 switching，防止交叉创建或发送。
  async function ensureSession() {
    if (isSessionID(currentSessionID)) return currentSessionID;
    const response = await fetch('/api/sessions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
    if (!response.ok) throw new Error(await errorMessage(response));
    const session = await response.json();
    if (!isSessionID(session.id)) throw new Error('服务端没有返回有效的会话标识');
    currentSessionID = session.id;
    syncCapabilityWidgets(capabilityWidgets(lastRuntimeState?.capabilities));
    resetExecutionState();
    history.replaceState(null, '', location.pathname + location.search + sessionHash(session.id));
    rerenderSessions();
    await updateSessions();
    return session.id;
  }

  async function saveSessionSetting(field, body) {
    if (running || switching) throw new Error('运行或会话更新期间不能修改设置');
    switching = true;
    sessionControlsRevision += 1;
    executionRevision += 1;
    modelListRevision += 1;
    closeCommandMenu();
    setSessionControls();
    try {
      const id = await ensureSession();
      const response = await fetch('/api/sessions/' + id + '/' + field, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      if (!response.ok) throw new Error(await errorMessage(response));
      return await response.json();
    } finally {
      switching = false;
      setSessionControls();
    }
  }

  function clearControlDraft(kind) {
    const text = input.value.trimStart();
    if (text === '/' + kind || text.startsWith('/' + kind + ' ')) { input.value = ''; resizeInput(); }
  }

  async function applyModelChoice(name) {
    if (!name) return openControlPicker('model');
    try {
      const chosen = await saveSessionSetting('model', name === '--default' ? { reset: true } : { model: name });
      sessionModelChoice = { name: chosen.model, origin: chosen.origin };
      if (modelPayload && modelListed) {
        modelPayload = { ...modelPayload, current: sessionModelChoice };
        modelPayloadSessionID = currentSessionID;
        renderModelList();
      }
      clearControlDraft('model');
      renderSessionControls();
      setConversationStatus('这个会话的后续运行使用 ' + (chosen.model || '全局默认模型') + '。');
      await updateSessionControls();
      if (modelListed) await updateModelList();
      return true;
    } catch (error) {
      setConversationStatus('切换失败：' + error.message, true);
      if (modelListed) setModelSwitchStatus('切换失败：' + error.message, 'failure');
      return false;
    }
  }

  async function applyReasoningChoice(value) {
    if (!value) return openControlPicker('reasoning');
    try {
      const body = value === '--default' ? { reset: true } : { reasoning_effort: value === '--off' ? '' : value };
      const chosen = await saveSessionSetting('reasoning', body);
      sessionReasoningChoice = chosen.current || null;
      clearControlDraft('reasoning');
      renderSessionControls();
      setConversationStatus('后续运行的思考设置：' + reasoningEffortView(sessionReasoningChoice?.reasoning_effort).label + '。具体支持情况由模型服务决定。');
      await updateSessionControls();
    } catch (error) { setConversationStatus('切换失败：' + error.message, true); }
  }

  async function updateSetupState() {
    const revision = ++setupRevision;
    const id = currentSessionID;
    setupController?.abort();
    const controller = new AbortController();
    setupController = controller;
    const deadline = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await fetch('/api/setup' + sessionQuery(), { cache: 'no-store', signal: controller.signal });
      if (!response.ok) throw new Error(await errorMessage(response));
      const payload = await response.json();
      if (revision !== setupRevision || id !== currentSessionID || controller.signal.aborted) return;
      sessionSetupChoice = payload.selection || null;
      sessionSetupProblem = payload.problem || (payload.unavailable_capabilities?.length ? '尚未启用：' + payload.unavailable_capabilities.join('、') : '');
    } catch (error) {
      if (revision !== setupRevision || id !== currentSessionID) return;
      sessionSetupProblem = controller.signal.aborted ? '预设读取超时，可重新打开菜单' : '预设读取失败：' + error.message;
    } finally {
      clearTimeout(deadline);
      if (setupController === controller) setupController = null;
    }
    if (revision === setupRevision && id === currentSessionID) renderSessionControls();
  }

  async function openPresetPicker() {
    if (running || switching) return;
    closeComposerPopover(false);
    const revision = ++controlPickerRevision;
    const id = currentSessionID;
    presetPickerController?.abort();
    const controller = new AbortController();
    presetPickerController = controller;
    const deadline = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await fetch('/api/presets', { cache: 'no-store', signal: controller.signal });
      if (!response.ok) throw new Error(await errorMessage(response));
      const payload = await response.json();
      if (revision !== controlPickerRevision || id !== currentSessionID || running || switching) return;
      const options = (Array.isArray(payload.presets) ? payload.presets : []).map(preset => ({
        value: preset.id,
        summary: [preset.title, preset.id === sessionSetupChoice?.id && sessionSetupChoice?.owner === 'presets' ? preset.revision === sessionSetupChoice.revision ? '当前版本' : '会话仍使用旧版本；点击采用新版' : '', preset.builtin ? '内置' : '自定义'].filter(Boolean).join(' · ')
      }));
      options.push({ value: '--default', summary: '清除预设，保留其他会话设置' });
      const items = commandCandidates([{ name: 'preset', args: 'options', options }], '/preset ').items;
      renderCommandList(items);
      $('session-preset').setAttribute('aria-expanded', 'true');
      input.focus();
    } catch (error) {
      if (revision !== controlPickerRevision || id !== currentSessionID) return;
      setConversationStatus(controller.signal.aborted ? '读取预设超时，请重试。' : '读取预设失败：' + error.message + '。可清除旧绑定，或在设置中启用工作预设能力。', true);
      if (sessionSetupChoice) {
        renderCommandList(commandCandidates([{ name: 'preset', args: 'options', options: [{ value: '--default', summary: '清除当前预设，保留其他会话设置' }] }], '/preset ').items);
        $('session-preset').setAttribute('aria-expanded', 'true');
      }
    } finally { clearTimeout(deadline); if (presetPickerController === controller) presetPickerController = null; }
  }

  async function applyPresetChoice(value) {
    if (!value) return openPresetPicker();
    try {
      const split = value.indexOf(':');
      const body = value === '--default' ? { reset: true } : { owner: split < 0 ? 'presets' : value.slice(0, split), id: split < 0 ? value : value.slice(split + 1) };
      const result = await saveSessionSetting('setup', body);
      sessionSetupChoice = result.selection || null;
      sessionSetupProblem = '';
      clearControlDraft('preset');
      renderSessionControls();
      setConversationStatus(sessionSetupChoice ? '已选择“' + (sessionSetupChoice.title || sessionSetupChoice.id) + '”。权限保持不变，已有模型/思考选择优先。' : '已清除预设，其他会话设置保持不变。');
      await updateSessionControls();
    } catch (error) { setConversationStatus('切换预设失败：' + error.message, true); }
  }

  let composerPopover = null;
  function closeComposerPopover(restoreFocus = true) {
    if (!composerPopover) return;
    const { panel, toggle } = composerPopover;
    composerPopover = null;
    panel.hidden = true;
    toggle.setAttribute('aria-expanded', 'false');
    if (restoreFocus) toggle.focus();
  }
  function toggleComposerPopover(panel, toggle) {
    const wasOpen = composerPopover?.panel === panel;
    closeComposerPopover(false);
    closeCommandMenu();
    if (wasOpen) return;
    composerPopover = { panel, toggle };
    panel.hidden = false;
    toggle.setAttribute('aria-expanded', 'true');
    panel.querySelector('button')?.focus();
  }
  for (const [button, panel] of [['composer-add', 'composer-actions'], ['session-settings', 'composer-settings'], ['conversation-workspace', 'composer-project']]) {
    $(button).addEventListener('click', () => toggleComposerPopover($(panel), $(button)));
  }
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && composerPopover) { event.preventDefault(); closeComposerPopover(); }
  });
  document.addEventListener('click', event => {
    if (composerPopover && !composerPopover.panel.contains(event.target) && !composerPopover.toggle.contains(event.target)) closeComposerPopover(false);
    if (commandItems.length && !commandMenuNode.contains(event.target) && !form.contains(event.target)) closeCommandMenu();
  });
  document.addEventListener('focusin', event => {
    if (composerPopover && !composerPopover.panel.contains(event.target) && !composerPopover.toggle.contains(event.target)) closeComposerPopover(false);
  });
  function openComposerSettings(pane) {
    openDrawer(panels.settings);
    $('settings-tab-' + pane).click();
  }
  $('composer-capabilities').addEventListener('click', () => openComposerSettings('capabilities'));
  $('composer-workspace').addEventListener('click', () => openComposerSettings('workspace'));
  $('composer-presets').addEventListener('click', () => {
    closeComposerPopover(false);
    const record = capabilityPanelNodes.get('presets');
    if (record) openDrawer(record.panel);
    else { openComposerSettings('capabilities'); setConversationStatus('请先启用工作预设能力。'); }
  });
  $('composer-project-change').addEventListener('click', () => openComposerSettings('workspace'));
  $('skill-manage').addEventListener('click', () => {
    const record = capabilityPanelNodes.get('skill-library');
    if (record) openDrawer(record.panel);
    else { openComposerSettings('capabilities'); setConversationStatus('请先启用技能能力。'); }
  });

  $('session-preset').addEventListener('click', () => openControlPicker('preset'));
  $('session-model').addEventListener('click', () => openControlPicker('model'));
  $('session-reasoning').addEventListener('click', () => openControlPicker('reasoning'));

  function submitCommand(draft) {
    const parts = draft.slice(1).split(/\s+/);
    const word = parts[0];
    const argument = parts.slice(1).filter(Boolean).join(' ');
    const command = findCommand(commandTable, word);
    // `/help` is the composer's own answer; it needs no round trip even when
    // the table has not arrived yet.
    if (command ? command.name === 'help' : word.toLowerCase() === 'help') {
      openCommandMenu();
      return;
    }
    if (!command) {
      // A misspelled command must never become a model call.
      setConversationStatus(`没有 /${word} 这个命令。可以输入 / 看一下列表。`, true);
      return;
    }
    // 运行中的策略来自命令自己：标注 busy=reject 的命令在这时不执行。
    if (command.busy === 'reject' && (running || switching)) {
      setConversationStatus(`Luna 正在运行，/${command.name} 现在不能执行。等这次运行结束后再试。`, true);
      return;
    }
    if (command.name === 'permissions') { applyPermissionCommand(argument); return; }
    if (command.name === 'reasoning') { applyReasoningChoice(argument); return; }
    if (command.name === 'preset') { applyPresetChoice(argument); return; }
    if (command.name === 'model') {
      applyModelChoice(argument);
      return;
    }
    setConversationStatus(`/${command.name} 暂时还不能在这个界面上执行。`, true);
  }

  input.addEventListener('input', refreshCommandMenu);

  transcript.addEventListener('scroll', () => {
    if (nearBottom()) latest.hidden = true;
  }, { passive: true });
  latest.addEventListener('click', () => {
    transcript.scrollTo({ top: transcript.scrollHeight, behavior: 'smooth' });
    latest.hidden = true;
  });

  function setBackgroundInert(value) {
    if ('inert' in appShell) appShell.inert = value;
    $('runtime-widget-layer').inert = value;
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
    closeComposerPopover(false);
    closeCommandMenu();
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
      // 每一页的数据只在这一页被打开时读：切到"技能"就是一次技能清单的读取，
      // 切到"工作区"或"界面扩展"同理。
      if (active) loadSettingsPane(item.dataset.pane);
    }
  }

  // loadSettingsPane 是"打开这一页要读什么"的唯一一处：一页自己的读取逻辑写在
  // 各自的小节里，这里只负责在它可见时触发一次。
  function loadSettingsPane(pane) {
    if (pane === 'settings-pane-skills') updateSkills();
    else if (pane === 'settings-pane-workspace') {
      updateWorkspaces();
      // 允许写入的目录是同一分类里的另一节：它读自己的接口，候选区复用上面那份工作区。
      updateWriteDirs();
    }
    else if (pane === 'settings-pane-extensions') updateUIPlugins();
    else if (pane === 'settings-pane-model') {
      updateModelList();
      updateProvider();
    }
  }

  // updateVisibleSettingsPane 在设置被重新打开时只刷新当前可见的那一页，而不是
  // 把所有页都读一遍。
  function updateVisibleSettingsPane() {
    const active = settingsTabs.find((tab) => tab.getAttribute('aria-selected') === 'true');
    if (active) loadSettingsPane(active.dataset.pane);
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

  function refreshReloadButton() {
    const selected = $('reload-tool').value;
    reloadButton.disabled = reloading || reloadableTools.length === 0 || (selected !== '' && !reloadableTools.includes(selected));
  }

  function renderReloadTools(state) {
    const offered = Array.isArray(state.reloadable_tools) ? state.reloadable_tools : (state.plugins || []).map(record => record.tool);
    const names = [...new Set(offered.filter(name => typeof name === 'string' && /^[a-zA-Z][a-zA-Z0-9_-]{0,63}$/.test(name)))];
    if (names.join('\n') !== reloadableTools.join('\n')) {
      const select = $('reload-tool');
      const previous = select.value;
      select.replaceChildren();
      for (const name of ['', ...names]) {
        const option = make('option', '', name || '全部已注册工具');
        option.setAttribute('value', name);
        option.value = name;
        select.append(option);
      }
      // 注册项消失时保留不可提交的选中项，不悄悄把“一个工具”改成“全部”。
      if (previous && !names.includes(previous)) {
        const missing = make('option', '', previous + '（已移除）');
        missing.setAttribute('value', previous);
        missing.value = previous;
        missing.disabled = true;
        select.append(missing);
      }
      select.value = previous;
      reloadableTools = names;
    }
    refreshReloadButton();
  }

  $('reload-tool').addEventListener('change', refreshReloadButton);
  reloadForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (reloading || reloadButton.disabled) return;
    const tool = $('reload-tool').value;
    reloading = true;
    refreshReloadButton();
    updateReloadStatus(reloadCopy('pending', tool));
    try {
      const response = await fetch('/api/reload', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tool })
      });
      if (!response.ok) throw new Error(await errorMessage(response));
      const body = await response.json();
      updateReloadStatus(reloadCopy('success', tool), 'success');
      renderState(body);
    } catch (error) {
      updateReloadStatus(reloadCopy('failure', tool, error.message), 'failure');
    } finally {
      reloading = false;
      refreshReloadButton();
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

    // 有能力贡献面板时，这一行就是它的入口：能力自己的面板由宿主挂载，这里只是
    // 一个能到达它的按钮，宿主不知道那个面板里有什么。
    const open = make('button', 'luna-button capability-open');
    open.type = 'button';
    open.hidden = true;
    open.addEventListener('click', () => {
      const row = item.capabilityRow;
      const record = row ? capabilityPanelNodes.get(row.id) : null;
      if (record) openDrawer(record.panel);
    });
    meta.append(open);

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

    // 只有真的挂上了入口的能力才有"打开面板"：内核没报面板就不给一个点了没反应的
    // 按钮。
    const open = item.querySelector('.capability-open');
    const record = capabilityPanelNodes.get(row.id);
    open.hidden = !record;
    if (record) {
      open.textContent = `打开${record.panel.title}`;
      open.setAttribute('aria-label', `打开${record.panel.title}`);
    }

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

  // --- 设置：模型服务（可命名的多 provider）-----------------------------------
  // provider 是一份可命名的设置：名字 → 接口地址、密钥与模型，整份文件保存在 Luna
  // 自己的 provider.yaml 里。每次写入提交的都是完整文件（active 加全部 providers），
  // 服务端答复的视图就是重绘的依据。密钥只写不读 —— GET 只回答"是否已设置"和末四位，
  // 所以这个输入框永远是空的，空的意思是"沿用同名 provider 已保存的那个"。
  const providerForm = $('provider-form');
  const providerFormTitle = $('provider-form-title');
  const providerList = $('provider-list');
  const providerEmpty = $('provider-empty');
  const providerAdd = $('provider-add');
  const providerName = $('provider-name');
  const providerBaseURL = $('provider-base-url');
  const providerAPIKey = $('provider-api-key');
  const providerModel = $('provider-model');
  const providerModelExtra = $('provider-model-extra');
  const providerModelExtraAdd = $('provider-model-extra-add');
  const providerModels = $('provider-models');
  const providerCandidates = $('provider-candidates');
  const providerKeyState = $('provider-key-state');
  const providerProbe = $('provider-probe');
  const providerStatus = $('provider-status');
  // 最近一次从服务端读到的设置：列表、行内动作与提交都以它为基础。
  let providerView = null;
  // 正在编辑的那一份（表单内容）：整页唯一还没提交的本地状态。
  let providerDraft = null;
  // 最近一次探测到的候选：只显示，点一行才设成默认模型。
  let providerProbeModels = [];
  // 已经点过一次删除的那一行：第二次点击同一行才真的删，不用浏览器原生的确认对话框
  // （它会把页面交出去，也没法在这套界面里被断言）。
  let providerDeleteArmed = '';
  // 一次操作后自己消失的瞬时反馈；需要用户处理的那类保留到下一次操作。
  const PROVIDER_STATUS_LINGER = 4000;
  let providerStatusTimer = null;

  function setProviderStatus(text, persist = false) {
    if (providerStatusTimer !== null) {
      clearTimeout(providerStatusTimer);
      providerStatusTimer = null;
    }
    providerStatus.textContent = text;
    providerStatus.className = `luna-status provider-status${persist ? ' failure' : ''}`;
    if (!text || persist) return;
    providerStatusTimer = setTimeout(() => {
      providerStatusTimer = null;
      providerStatus.textContent = '';
      providerStatus.className = 'luna-status provider-status';
    }, PROVIDER_STATUS_LINGER);
  }

  // readProviderForm 把表单当下的内容读进草稿：表单是这一份的唯一真相，用户名一栏
  // 改了什么，后面显示与提交都跟着走。
  function readProviderForm() {
    if (!providerDraft) return null;
    providerDraft.name = providerName.value;
    providerDraft.base_url = providerBaseURL.value;
    providerDraft.model = providerModel.value;
    providerDraft.api_key = providerAPIKey.value;
    return providerDraft;
  }

  function providerDraftFromSaved(row) {
    return {
      originalName: row.name || '',
      name: row.name || '',
      base_url: row.base_url || '',
      model: row.model || '',
      models: [...(row.models || [])],
      key_set: Boolean(row.key_set),
      key_hint: row.key_hint || '',
      api_key: '',
      isNew: false,
    };
  }

  function providerDraftNew() {
    return {
      originalName: '', name: '', base_url: '', model: '', models: [],
      key_set: false, key_hint: '', api_key: '', isNew: true,
    };
  }

  function providerTitleText(draft) {
    if (!draft) return '新建 provider';
    const name = draft.name.trim();
    if (!draft.originalName) return name ? `新建 provider「${name}」` : '新建 provider';
    if (name && name !== draft.originalName) return `编辑 provider「${draft.originalName}」→「${name}」`;
    return `编辑 provider「${name}」`;
  }

  // 密钥只写不读：这一行能说的是"有没有"和末四位。名字是密钥的一部分 —— 服务端按
  // 同名沿用，所以改了名字等于新建一个 provider，旧密钥不会跟过去。
  function providerKeyStateText(draft) {
    if (!draft) return '';
    const name = draft.name.trim();
    const hint = draft.key_hint ? `（${draft.key_hint}）` : '';
    if (draft.key_set && draft.originalName && name !== draft.originalName) {
      return `「${draft.originalName}」已有一个密钥${hint}，但密钥跟着名字走：改成「${name}」要重新填一遍。`;
    }
    if (draft.key_set) return `已保存一个密钥${hint}。换密钥就填新的；界面取不回原值。`;
    return '还没有密钥：填一个再保存。';
  }

  // providerRowMeta 只显示端点的主机名：完整地址在表单里编辑，列表这一行回答的是
  // "这是哪个端点、有几个模型、有没有密钥"。
  function providerRowMeta(row) {
    const host = providerEndpointHost(row.base_url);
    const count = (row.model ? 1 : 0) + (row.models || []).length;
    const key = row.key_set ? `密钥已保存${row.key_hint ? ` ${row.key_hint}` : ''}` : '没有密钥';
    return [host || '没有接口地址', `${count} 个模型`, key].join(' · ');
  }

  function renderProviderModels() {
    providerModels.replaceChildren();
    for (const name of providerDraft ? providerDraft.models : []) {
      const item = make('li', 'provider-model-row');
      item.append(make('span', 'provider-model-name', name));
      const remove = make('button', 'luna-button provider-model-remove', '移除');
      remove.type = 'button';
      remove.addEventListener('click', () => {
        providerDraft.models = providerDraft.models.filter((model) => model !== name);
        renderProviderModels();
      });
      item.append(remove);
      providerModels.append(item);
    }
  }

  function renderProviderCandidates() {
    providerCandidates.replaceChildren();
    providerCandidates.hidden = providerProbeModels.length === 0;
    for (const name of providerProbeModels) {
      const item = make('li', 'provider-candidate');
      const use = make('button', 'luna-button provider-candidate-use', name);
      use.type = 'button';
      use.addEventListener('click', () => {
        if (!providerDraft) return;
        providerDraft.model = name;
        providerModel.value = name;
        setProviderStatus(`「${name}」成为默认模型；保存后写进文件。`);
      });
      const add = make('button', 'luna-button provider-candidate-add', '+ 其他模型');
      add.type = 'button';
      add.addEventListener('click', () => addProviderModel(name));
      item.append(use, add);
      providerCandidates.append(item);
    }
  }

  function addProviderModel(name) {
    if (!providerDraft) return;
    const wanted = String(name || '').trim();
    if (!wanted) return;
    if (!providerDraft.models.includes(wanted)) providerDraft.models.push(wanted);
    renderProviderModels();
    setProviderStatus(`「${wanted}」加进其他模型；保存后写进文件。`);
  }

  // renderProviderForm 把草稿写回表单：输入框不重建，只改值，重绘不会把焦点移走。
  function renderProviderForm() {
    const draft = providerDraft;
    providerFormTitle.textContent = providerTitleText(draft);
    providerName.value = draft ? draft.name : '';
    providerBaseURL.value = draft ? draft.base_url : '';
    providerModel.value = draft ? draft.model : '';
    // 界面从来没有密钥原值，所以这个框永远空的；空的意思是沿用，不是清空。
    providerAPIKey.value = '';
    providerAPIKey.placeholder = draft && draft.key_set
      ? `留空 = 沿用已保存的密钥（${draft.key_hint}）`
      : '留空 = 沿用已保存的密钥';
    providerKeyState.textContent = providerKeyStateText(draft);
    renderProviderModels();
    renderProviderCandidates();
  }

  function providerRowNode(row) {
    const item = make('li', 'provider-row');
    const active = Boolean(providerView) && providerView.active === row.name;
    if (active) item.classList.add('is-active');
    if (providerDraft && providerDraft.originalName === row.name) item.classList.add('is-editing');
    const title = make('div', 'provider-title');
    title.append(make('span', 'provider-name', row.name));
    if (active) title.append(make('span', 'provider-badge is-active', '使用中'));
    const actions = make('div', 'provider-actions');
    const name = row.name;
    if (!active) {
      const use = make('button', 'luna-button provider-use', '设为使用中');
      use.type = 'button';
      use.addEventListener('click', () => setProviderActive(name));
      actions.append(use);
    }
    const edit = make('button', 'luna-button provider-edit', '编辑');
    edit.type = 'button';
    edit.addEventListener('click', () => selectProviderForEdit(name));
    const remove = make('button', `luna-button provider-remove${providerDeleteArmed === name ? ' is-armed' : ''}`,
      providerDeleteArmed === name ? '再点一次删除' : '删除');
    remove.type = 'button';
    remove.addEventListener('click', () => armOrDeleteProvider(name));
    actions.append(edit, remove);
    item.append(title, make('p', 'provider-meta', providerRowMeta(row)), actions);
    return item;
  }

  // renderProviderList 每次按数据重绘：行显示什么由服务端答复决定，"使用中"跟着
  // active 走，密钥只有是否已设置与末四位。
  function renderProviderList() {
    const rows = (providerView && providerView.providers) || [];
    providerList.replaceChildren();
    for (const row of rows) providerList.append(providerRowNode(row));
    providerEmpty.hidden = rows.length > 0;
  }

  // renderProvider 是读与写共用的落点：视图换成服务端刚报的那一份，草稿按需对账。
  // 读（fromSave=false）不动正在编辑的内容；一次成功的写（fromSave=true）之后，草稿
  // 换成服务端存下来的那一份 —— 除非调用方说这一份草稿还没被写出去（keepDraft）。
  function renderProvider(view, { fromSave = false, keepDraft = false } = {}) {
    providerView = view;
    const providers = view.providers || [];
    if (providerDraft && fromSave && !keepDraft) {
      const saved = providers.find((row) => row.name === providerDraft.name.trim());
      // 表单里的名字在服务端答复里有对应的一份：草稿换成它，密钥的"有没有"跟着刷新。
      if (saved) providerDraft = providerDraftFromSaved(saved);
    }
    // 正在编辑的那一份在文件里已经不存在了（被删掉、或改过名字后被替换）：换回还在
    // 的一份。还没保存过的新 provider（没有原名）不在此列。
    if (providerDraft && providerDraft.originalName
      && !providers.some((row) => row.name === providerDraft.originalName || row.name === providerDraft.name.trim())) {
      providerDraft = null;
    }
    if (!providerDraft) {
      const wanted = providers.find((row) => row.name === view.active) || providers[0];
      providerDraft = wanted ? providerDraftFromSaved(wanted) : providerDraftNew();
    }
    providerDeleteArmed = '';
    renderProviderList();
    renderProviderForm();
  }

  // providerNote 说的是"现在能不能聊"：还缺什么、以及有没有使用中的那个。没有可说的
  // 就返回空 —— 空态由列表那一段自己说。
  function providerNote(view) {
    const missing = (view.missing || []).filter(Boolean);
    if (missing.length) return `还缺 ${missing.join('、')}。`;
    if (!view.active && (view.providers || []).length) return '现在没有使用中的 provider：在列表里点一个「设为使用中」。';
    return '';
  }

  // 读不出来时优先显示服务端自己的那句话：一个读不出或不合法的 provider.yaml 会给出
  // 原因（哪条 provider、什么地方不对），而 "HTTP 500" 对要动手修它的人没有用。
  async function providerFailure(response) {
    try {
      const payload = await response.json();
      if (payload && payload.error) return payload.error;
    } catch (error) {
      // 不是 JSON 就退回状态码，但那句话仍然要说清是哪一次请求没成。
    }
    return `HTTP ${response.status}`;
  }

  async function updateProvider({ quiet = false } = {}) {
    if (!quiet) setProviderStatus('正在读取…');
    try {
      const response = await fetch('/api/provider', { cache: 'no-store' });
      if (!response.ok) throw new Error(await providerFailure(response));
      const view = await response.json();
      renderProvider(view, { fromSave: false });
      const note = providerNote(view);
      setProviderStatus(note, Boolean(note));
      return view;
    } catch (error) {
      setProviderStatus(`读不到模型服务设置：${error.message}`, true);
      return null;
    }
  }

  // 探测由服务端发起：密钥在它手里，浏览器只收到一张模型名单。失败时把接口自己的
  // 话原样显示出来 —— 一个人要改的是他刚填的那个地址或密钥。
  async function probeProviderModels() {
    const draft = readProviderForm();
    if (!draft) return;
    providerProbe.disabled = true;
    setProviderStatus('正在向接口要模型列表…');
    try {
      const response = await fetch('/api/provider/models', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        // api_key 空 = 沿用这个 provider 已保存的密钥：界面拿不到原值。
        body: JSON.stringify({ name: draft.name.trim(), base_url: draft.base_url.trim(), api_key: draft.api_key }),
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const payload = await response.json();
      providerProbeModels = (payload.models || []).map((name) => String(name)).filter(Boolean);
      renderProviderCandidates();
      // 探测失败是答案，不是错误页：把接口自己的话放在用户正在看的地方。
      if (payload.problem) setProviderStatus(`接口说：${payload.problem}`, true);
      else setProviderStatus(`接口给出了 ${providerProbeModels.length} 个模型：点一个设为默认模型，或加进其他模型。`);
    } catch (error) {
      setProviderStatus(`探测失败：${error.message}`, true);
    } finally {
      providerProbe.disabled = false;
    }
  }
  providerProbe.addEventListener('click', probeProviderModels);

  // commitProvider 是这一页唯一的写入路径：一次 PUT 提交完整文件，接着用服务端答复
  // 的视图重绘、再读一次服务端，最后重新读一遍命令表 —— /model 的候选跟着 provider
  // 一起变。失败时把服务端那句 error 显示在用户正在看的地方。
  async function commitProvider(body, { keepDraft = false, success } = {}) {
    setProviderStatus('正在保存…');
    try {
      const response = await fetch('/api/provider', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || `HTTP ${response.status}`);
      renderProvider(payload, { fromSave: true, keepDraft });
    } catch (error) {
      setProviderStatus(`保存失败：${error.message}`, true);
      return false;
    }
    const fresh = await updateProvider({ quiet: true });
    if (fresh && success) setProviderStatus(success(fresh));
    loadCommands();
    return true;
  }

  // 保存成功说的只有一件事：这份设置写下去就是接下来在用的。这句是一次操作的结果，
  // 自己消失。
  function providerSavedMessage(view) {
    return `已写入 ${view.file || 'provider.yaml'}：存下来就是接下来在用的。`;
  }

  // 行内「设为使用中」：整份文件照服务端当前的样子提交，只换 active。表单里还没保存
  // 的编辑留在原处（keepDraft），不会被这一下顺手写进去，也不会被答复冲掉。
  async function setProviderActive(name) {
    if (!providerView || providerView.active === name) return;
    await commitProvider(providerRequestBody(providerView, name, null), {
      keepDraft: true,
      success: () => `接下来用的是「${name}」。`,
    });
  }

  // 删除要两次点击：第一下只把这一行变成"再点一次删除"，第二下才真的删。删的正好是
  // 使用中的那个时，先把"使用中"换成另一个 —— 没有别的就先让用户建一个，绝不提交
  // 一份 active 指向不存在名字的文件。
  function armOrDeleteProvider(name) {
    if (providerDeleteArmed !== name) {
      providerDeleteArmed = name;
      renderProviderList();
      return;
    }
    providerDeleteArmed = '';
    deleteProvider(name);
  }

  async function deleteProvider(name) {
    if (!providerView) return;
    const rest = (providerView.providers || []).filter((row) => row.name !== name);
    let active = providerView.active || '';
    if (active === name) {
      if (!rest.length) {
        renderProviderList();
        setProviderStatus(`「${name}」是正在使用的那个：先建另一个 provider，再删它。`, true);
        return;
      }
      active = rest[0].name;
    }
    await commitProvider(providerRequestBody({ providers: rest }, active, null), { keepDraft: true });
  }

  // 编辑一行的内容：草稿换成它，表单跟着填。这一下不发请求，改动要等"保存"。
  function selectProviderForEdit(name) {
    const row = (providerView && providerView.providers || []).find((item) => item.name === name);
    if (!row) return;
    providerDraft = providerDraftFromSaved(row);
    providerProbeModels = [];
    providerDeleteArmed = '';
    renderProviderList();
    renderProviderForm();
  }

  // 「添加 provider」只是把表单清成一份新的：不写文件，名字必填，存了才存在。
  providerAdd.addEventListener('click', () => {
    providerDraft = providerDraftNew();
    providerProbeModels = [];
    providerDeleteArmed = '';
    renderProviderList();
    renderProviderForm();
    providerName.value = '';
    providerName.focus();
  });

  providerModelExtraAdd.addEventListener('click', () => {
    addProviderModel(providerModelExtra.value);
    providerModelExtra.value = '';
  });

  // 名字一栏改了就更新派生文案（标题与密钥那一行）：密钥跟着名字走，改名字要说出来。
  providerName.addEventListener('input', () => {
    if (!providerDraft) return;
    providerDraft.name = providerName.value;
    providerFormTitle.textContent = providerTitleText(providerDraft);
    providerKeyState.textContent = providerKeyStateText(providerDraft);
  });

  providerForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!providerView) {
      setProviderStatus('还没读到这台机器的设置：等这一页读出来再保存。', true);
      return;
    }
    const draft = readProviderForm();
    if (!draft) return;
    const name = draft.name.trim();
    if (!name) {
      setProviderStatus('保存失败：provider 要有名字。', true);
      return;
    }
    if ((providerView.providers || []).some((row) => row.name === name && row.name !== draft.originalName)) {
      setProviderStatus(`保存失败：已经有一个叫「${name}」的 provider 了。`, true);
      return;
    }
    // active 不能指向不存在的名字：改名字的人正好在用这一份时，跟着新名字走；全新装
    // 的那一份存下来就是接下来要用的。
    let active = providerView.active || '';
    if (!active || active === draft.originalName) active = name;
    await commitProvider(providerRequestBody(providerView, active, draft), { success: providerSavedMessage });
  });

  // 模型服务参数是这台机器启动时定下的，界面只读显示：模型、提供方与思考档位来自
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

  // --- 设置：技能清单 ---------------------------------------------------------
  // 技能清单不来自 /api/state：它只在用户打开这一页（或点重读）时读一次
  // GET /api/skills。启停是一次真的 POST，然后重读服务端的答复再重绘——界面
  // 显示的状态始终是服务端报过的那个。

  const skillList = $('skill-list');
  const skillEmpty = $('skill-empty');
  const skillStatus = $('skill-status');
  const skillRefresh = $('skill-refresh');
  // 最近一次成功读到的载荷：重绘与按钮文案都来自它。
  let skillPayload = null;
  // 成功读到过清单才谈得上“一个技能都没有”。读失败不算空态。
  let skillListed = false;
  // 正在等服务端答复的技能名：这段时间里按钮不可点，重复点击不会发第二次请求。
  const skillPending = new Set();

  function setSkillStatus(text, className = '') {
    skillStatus.textContent = text;
    skillStatus.className = `luna-status skill-status${className ? ` ${className}` : ''}`;
  }

  // skillRowNode 建一次节点，updateSkillRow 之后只改内容：重读不会把行重建，
  // 键盘焦点与按钮状态留在原处。
  function skillRowNode() {
    const item = make('li', 'skill-row luna-list-item');
    const head = make('div', 'skill-head');
    const title = make('div', 'skill-title');
    title.append(make('span', 'skill-name'), make('span', 'skill-scope'));
    const toggle = make('button', 'luna-button skill-toggle');
    toggle.type = 'button';
    toggle.addEventListener('click', () => setSkillState(item.skillRow));
    head.append(title, toggle);

    const description = make('p', 'skill-description');
    const meta = make('div', 'skill-meta');
    const state = make('span', 'skill-state');
    const reason = make('span', 'skill-reason');
    reason.hidden = true;
    meta.append(state, reason);

    const error = make('p', 'skill-error');
    error.hidden = true;

    item.append(head, meta, description, error);
    return item;
  }

  function updateSkillRow(item, row) {
    item.skillRow = row;
    item.dataset.skill = row.name;
    const name = item.querySelector('.skill-name');
    name.textContent = row.name;
    name.setAttribute('title', row.name);
    item.querySelector('.skill-scope').textContent = row.scopeLabel;

    const state = item.querySelector('.skill-state');
    state.textContent = row.stateLabel;
    state.classList.toggle('is-on', row.reported && row.enabled);
    state.classList.toggle('is-off', row.reported && !row.enabled);
    // 整行也要有这个状态：只说“已停用”而整行看起来照常，用户扫一眼分不出哪几条
    // 没在用。行淡化、按钮保持清晰，改的还是这一个状态。
    item.classList.toggle('is-off', row.reported && !row.enabled);

    const reason = item.querySelector('.skill-reason');
    reason.hidden = row.disabledReason === '';
    reason.textContent = row.disabledReason;

    const description = item.querySelector('.skill-description');
    description.textContent = row.descriptionText;
    // 行里显示的是截断后的文字：完整描述挂在 title 上，没有被丢掉。
    description.setAttribute('title', row.description);
    description.classList.toggle('is-missing', row.descriptionMissing);

    const toggle = item.querySelector('.skill-toggle');
    const pending = skillPending.has(row.name);
    toggle.hidden = row.action === '';
    toggle.disabled = pending;
    toggle.textContent = pending ? `${row.actionLabel}中…` : row.actionLabel;
    toggle.setAttribute('aria-label', `${row.actionLabel}技能 ${row.name}`);

    const error = item.querySelector('.skill-error');
    error.hidden = row.error === '';
    error.textContent = row.error;
  }

  function renderSkills(payload) {
    if (payload !== undefined) skillPayload = payload;
    const rows = skillRows(skillPayload);
    reconcileList(skillList, rows, (row) => row.name, skillRowNode, updateSkillRow, skillRefresh);
    skillEmpty.hidden = !(skillListed && rows.length === 0);
  }

  // updateSkills is the only place the browser asks for the skill list. A read
  // that fails keeps the last known rows: an unreachable server is not the same
  // as a machine with no skills. Returns whether a payload was read.
  async function updateSkills() {
    try {
      const response = await fetch(SKILLS_PATH, { cache: 'no-store' });
      if (!response.ok) throw new Error(await errorMessage(response));
      skillListed = true;
      renderSkills(await response.json());
      setSkillStatus(`已发现 ${skillRows(skillPayload).length} 个技能。`);
      return true;
    } catch (error) {
      renderSkills();
      setSkillStatus(`读取技能清单失败：${error.message}`, 'failure');
      return false;
    }
  }

  // setSkillState is the only place the browser asks the server to change a
  // skill's state. Nothing is marked as changed here: a rejected request says so
  // and keeps the last reported state, and a successful one is followed by a
  // fresh read, so the row never shows a state the server did not report.
  async function setSkillState(row) {
    if (!row || !row.name || !row.action || skillPending.has(row.name)) return;
    const path = skillActionPath(row.name, row.action);
    if (!path) return;
    const { action, actionLabel: label, name } = row;
    skillPending.add(name);
    setSkillStatus(`正在${label} ${name}…`);
    renderSkills();
    let failure = '';
    try {
      const response = await fetch(path, { method: 'POST' });
      if (!response.ok) failure = await errorMessage(response);
    } catch (error) {
      failure = error.message;
    }
    skillPending.delete(name);
    if (failure) {
      renderSkills();
      setSkillStatus(`${label} ${name} 失败：${failure}（它仍按上一次读到的状态显示）`, 'failure');
      return;
    }
    // 重读服务端的清单：这一行只有在服务端确实改了状态时才会变。
    if (!(await updateSkills())) {
      setSkillStatus(`${label} ${name} 的请求已经发出，但重新读取技能清单失败：界面仍按上一次读到的状态显示。`, 'failure');
      return;
    }
    setSkillStatus(`${name} 已${action === 'disable' ? '停用' : '启用'}。`);
  }

  skillRefresh.addEventListener('click', () => updateSkills());

  // --- 设置：工作区 -----------------------------------------------------------
  // 工作区是会话的属性：这一页列出本机定义过的工作区（一组目录），并把当前会话
  // 绑到其中一个。列表与服务端的答复是唯一的事实来源：点"这个会话用它"之后重读
  // 一次，界面不自己记下"大概改好了"。

  let workspacePayload = null;
  let workspaceListed = false;
  let workspaceBusy = false;

  function setWorkspaceStatus(text, className = '') {
    workspaceStatus.textContent = text;
    workspaceStatus.className = `luna-status workspace-status${className ? ` ${className}` : ''}`;
  }

  function workspaceRowNode() {
    const item = make('li', 'workspace-row luna-list-item');
    const head = make('div', 'workspace-head');
    const title = make('div', 'workspace-title');
    title.append(make('span', 'workspace-name'), make('span', 'workspace-badge'));
    const use = make('button', 'luna-button workspace-use');
    use.type = 'button';
    use.addEventListener('click', () => bindWorkspace(item.workspaceRow));
    head.append(title, use);
    item.append(head, make('ul', 'workspace-dirs'));
    return item;
  }

  function updateWorkspaceRow(item, row) {
    item.workspaceRow = row;
    item.dataset.workspace = row.id;
    item.querySelector('.workspace-name').textContent = row.name;
    const badge = item.querySelector('.workspace-badge');
    badge.textContent = row.current ? '这个会话正在用' : '';
    badge.hidden = !row.current;

    const dirs = item.querySelector('.workspace-dirs');
    dirs.replaceChildren();
    for (const dir of row.dirs) dirs.append(make('li', 'workspace-dir', dir));
    if (row.dirs.length === 0) dirs.append(make('li', 'workspace-dir workspace-dir-missing', '这个工作区没有目录。'));

    const use = item.querySelector('.workspace-use');
    use.hidden = row.current;
    use.disabled = workspaceBusy || running || switching;
    use.textContent = '这个会话用它';
    use.setAttribute('aria-label', `让这个会话使用工作区 ${row.name}`);
  }

  function renderWorkspaces(payload) {
    if (payload !== undefined) workspacePayload = payload;
    const listed = workspaceListed && workspacePayload && Array.isArray(workspacePayload.workspaces) ? workspacePayload.workspaces : null;
    const rows = listed ? listed.map((item) => ({
      id: textOr(item.id), name: textOr(item.name), dirs: Array.isArray(item.dirs) ? item.dirs.map(textOr) : [],
      current: textOr(item.id) === currentWorkspaceID
    })) : [];
    workspaceList.replaceChildren();
    for (const row of rows) {
      const item = workspaceRowNode();
      updateWorkspaceRow(item, row);
      workspaceList.append(item);
    }
    workspaceEmpty.hidden = !listed || rows.length > 0;
    // 写入那一节的候选区跟着这份工作区数据走：它在同一处刷新，不自己再读一次
    // /api/workspaces（下面有两个提前返回，所以刷新点放在它们之前）。
    renderWriteDirCandidates();
    // 会话还没有 id 时说清为什么不能绑：绑定必须写进一次会话的记录里。
    if (listed && rows.length && !isSessionID(currentSessionID)) {
      setWorkspaceStatus('这个会话还没有消息，还没有可以记录绑定的地方。先发一条消息，再回到这一页。');
      return;
    }
    // 有会话但没有任何绑定：这不是"列表为空"，而是这个会话现在不工作在某个工作区
    // 里——文件工具这时回退到 Luna 启动时的读取根。说清楚，否则两行都像"可以直接
    // 用"，看不出当前状态。
    if (listed && rows.length && !currentWorkspaceID) {
      setWorkspaceStatus('这个会话目前没有绑定工作区：文件工具回到 Luna 启动时的读取根。绑定之后，这个会话只在这些目录里读。');
    }
  }

  async function updateWorkspaces() {
    if (workspaceBusy) return false;
    try {
      const response = await fetch('/api/workspaces', { cache: 'no-store' });
      if (!response.ok) throw new Error(await errorMessage(response));
      workspacePayload = await response.json();
      workspaceListed = true;
      renderWorkspaces();
      setWorkspaceStatus('');
      return true;
    } catch (error) {
      workspaceListed = false;
      renderWorkspaces();
      setWorkspaceStatus(`读取工作区失败：${error.message}`, 'failure');
      return false;
    }
  }

  // bindWorkspace 是这一页唯一的写操作。服务端的答复带着绑定之后的完整工作区，
  // 所以页头标识与这一页的标记同时更新，两边不会各说一套。
  async function bindWorkspace(row) {
    if (!row || !row.id || workspaceBusy || running || switching) return;
    workspaceBusy = true;
    renderWorkspaces();
    try {
      const payload = await saveSessionSetting('workspace', { workspace: row.id });
      currentWorkspaceID = payload.workspace ? textOr(payload.workspace.id) : '';
      setConversationWorkspace(payload.workspace || null);
      workspaceBusy = false;
      await updateWorkspaces();
      setWorkspaceStatus('这个会话现在工作在 ' + row.name + '。下一次运行就能读到里面的项目规则与文件。');
    } catch (error) {
      workspaceBusy = false;
      renderWorkspaces();
      setWorkspaceStatus('绑定失败：' + error.message, 'failure');
    }
  }

  workspaceRefresh.addEventListener('click', () => updateWorkspaces());

  // --- 设置：允许写入的目录 ---------------------------------------------------
  // 工作区说的是"这次工作在哪些目录里"，这一节说的是"Luna 可以改哪些目录里的文件"，
  // 而且它只会比工作区更窄。服务端是唯一的事实来源：打开这一页读一次
  // GET /api/write-dirs；每一次变更（移除一行、允许一个工作区目录、手填一个绝对路径）
  // 立刻把整份列表 PUT 回去，界面按答复里的 dirs 重绘 —— 从不先改成乐观状态。

  const writeDirsList = $('write-dirs-list');
  const writeDirsEmpty = $('write-dirs-empty');
  const writeDirsStatus = $('write-dirs-status');
  const writeDirsAdd = $('write-dirs-add');
  const writeDirsAddButton = $('write-dirs-add-button');
  const writeDirsCandidates = $('write-dirs-candidates');
  const writeDirsCandidatesEmpty = $('write-dirs-candidates-empty');
  // 最近一次成功读到的载荷：重绘与按钮的可用性都来自它。
  let writeDirsPayload = null;
  // 成功读到过清单才谈得上"一个目录都没有"。读失败不算空态。
  let writeDirsListed = false;
  // 一次变更在途：这段时间里按钮不可点，重复点击不会发第二次请求。
  let writeDirsBusy = false;
  let writeDirsStatusTimer = null;

  // 状态行兼错误行：说明性的话（正在保存…）自己消失，失败的那句留着 —— 一个人要改的
  // 是他刚做的那件事，原因不能被定时器吃掉。
  function setWriteDirsStatus(text, persist = false) {
    if (writeDirsStatusTimer !== null) {
      clearTimeout(writeDirsStatusTimer);
      writeDirsStatusTimer = null;
    }
    writeDirsStatus.textContent = text;
    writeDirsStatus.className = `luna-status write-dirs-status${persist ? ' failure' : ''}`;
    if (!text || persist) return;
    writeDirsStatusTimer = setTimeout(() => {
      writeDirsStatusTimer = null;
      writeDirsStatus.textContent = '';
      writeDirsStatus.className = 'luna-status write-dirs-status';
    }, 4000);
  }

  // 服务端最近一次报过的清单。读失败时它仍是上一次读到的那份：读不到不等于没有。
  function allowedWriteDirs() {
    if (!writeDirsListed || !writeDirsPayload || !Array.isArray(writeDirsPayload.dirs)) return [];
    return writeDirsPayload.dirs.map((dir) => String(dir ?? '')).filter(Boolean);
  }

  // 候选区复用工作区那一页已经读到的那份数据（同一个分类里的一次读取），不另外请求
  // 一次 /api/workspaces。
  function workspaceDirectoryList() {
    const rows = workspaceListed && workspacePayload && Array.isArray(workspacePayload.workspaces)
      ? workspacePayload.workspaces : [];
    const dirs = [];
    for (const row of rows) {
      for (const dir of Array.isArray(row.dirs) ? row.dirs : []) {
        const text = textOr(dir);
        if (text && !dirs.includes(text)) dirs.push(text);
      }
    }
    return dirs;
  }

  // 一个目录一行：路径缩略显示、原文留在 title 上；按钮点一下就是一次提交。
  function writeDirNode({ dir, className, label, action }) {
    const item = make('li', `${className} luna-list-item`);
    item.dataset.writeDir = dir;
    const path = make('span', 'write-dirs-path', writeDirText(dir));
    path.setAttribute('title', dir);
    const button = make('button', `luna-button ${action === 'allow' ? 'write-dirs-allow' : 'write-dirs-remove'}`, label);
    button.type = 'button';
    button.disabled = writeDirsBusy;
    button.setAttribute('aria-label', action === 'allow' ? `允许自动写入 ${dir}` : `移出自动写入范围 ${dir}`);
    button.addEventListener('click', () => (action === 'allow' ? allowWriteDir(dir) : removeWriteDir(dir)));
    item.append(path, button);
    return item;
  }

  function renderWriteDirs(payload) {
    if (payload !== undefined) writeDirsPayload = payload;
    const dirs = allowedWriteDirs();
    writeDirsList.replaceChildren();
    for (const dir of dirs) {
      writeDirsList.append(writeDirNode({ dir, className: 'write-dirs-row', label: '移除', action: 'remove' }));
    }
    // "还没有允许任何目录"只在确实读到过一份空清单时出现。
    writeDirsEmpty.hidden = !(writeDirsListed && dirs.length === 0);
    renderWriteDirCandidates();
  }

  // 候选区列出工作区里的目录中还没有被允许的那些：它跟着工作区那份数据走，所以两个
  // 数据都到齐之后才谈得上"没有可选的"。
  function renderWriteDirCandidates() {
    const workspaceDirs = workspaceDirectoryList();
    const candidates = writeDirCandidates(allowedWriteDirs(), workspaceDirs);
    writeDirsCandidates.replaceChildren();
    for (const dir of candidates) {
      writeDirsCandidates.append(writeDirNode({ dir, className: 'write-dirs-candidate', label: '允许', action: 'allow' }));
    }
    writeDirsCandidatesEmpty.hidden = candidates.length > 0;
    writeDirsCandidatesEmpty.textContent = workspaceDirs.length
      ? '当前项目目录都已列入自动写入范围。'
      : '还没有读到工作区目录：这一节照样可以直接填一个绝对路径。';
  }

  async function updateWriteDirs() {
    if (writeDirsBusy) return false;
    try {
      const response = await fetch('/api/write-dirs', { cache: 'no-store' });
      if (!response.ok) throw new Error(await errorMessage(response));
      writeDirsListed = true;
      renderWriteDirs(await response.json());
      setWriteDirsStatus('');
      return true;
    } catch (error) {
      // 读失败不把清单清空：上一次读到的那份留在屏幕上，原因写在状态行里。
      renderWriteDirs();
      setWriteDirsStatus(`读取自动写入范围失败：${error.message}`, true);
      return false;
    }
  }

  // commitWriteDirs 是这一节唯一的写入路径：整份列表一次 PUT，答复里的 dirs 就是新的
  // 清单。失败时服务端那句 error 原样显示，列表保持上一次读到的那份 —— 界面不先改成
  // 乐观状态，也不替服务端猜它会怎么规范化。
  async function commitWriteDirs(dirs, success) {
    if (writeDirsBusy) return false;
    writeDirsBusy = true;
    renderWriteDirs();
    setWriteDirsStatus('正在保存…');
    let failure = '';
    let payload = null;
    try {
      const response = await fetch('/api/write-dirs', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ dirs })
      });
      payload = await response.json().catch(() => ({}));
      if (!response.ok) failure = payload.error || `HTTP ${response.status}`;
    } catch (error) {
      failure = error.message;
    }
    writeDirsBusy = false;
    if (failure) {
      renderWriteDirs();
      setWriteDirsStatus(`保存失败：${failure}（列表仍是上一次读到的那份）`, true);
      return false;
    }
    // 答复带着新的清单：它是这一节的唯一事实来源。答复里没有清单就留着原来那份。
    if (payload && Array.isArray(payload.dirs)) renderWriteDirs(payload);
    else renderWriteDirs();
    setWriteDirsStatus(success || '');
    return true;
  }

  async function removeWriteDir(dir) {
    if (writeDirsBusy) return false;
    return commitWriteDirs(writeDirsWithout(allowedWriteDirs(), dir), `已将 ${dir} 移出自动写入范围，下一轮生效。`);
  }

  // 候选与手填的绝对路径走同一条路：加进现有清单，整份提交。
  async function allowWriteDir(dir) {
    if (writeDirsBusy) return false;
    return commitWriteDirs(writeDirsWith(allowedWriteDirs(), dir), `已将 ${dir} 加入自动写入范围，下一轮生效；仍遵循会话写入策略。`);
  }

  // 手填的那一栏先做一次本地检查：不是绝对路径就地说清、不发请求（服务端也会拒，
  // 没必要跑一趟）；是就提交，成功之后才把输入框清掉。
  async function submitWriteDir() {
    const dir = writeDirsAdd.value.trim();
    if (!writeDirAbsolute(dir)) {
      setWriteDirsStatus('请填一个绝对路径，例如 /home/j/project：相对路径说不清它到底是哪一个目录。', true);
      return;
    }
    if (allowedWriteDirs().includes(dir)) {
      setWriteDirsStatus(`${dir} 已经在自动写入范围里了。`, true);
      return;
    }
    if (await allowWriteDir(dir)) writeDirsAdd.value = '';
  }

  writeDirsAddButton.addEventListener('click', submitWriteDir);
  // 回车与「允许」是同一个入口：这一行不是一份需要"保存"的表单，填完就是一次操作。
  writeDirsAdd.addEventListener('keydown', (event) => {
    if (event.key !== 'Enter') return;
    event.preventDefault();
    submitWriteDir();
  });

  // --- 设置：本会话用哪个模型 -------------------------------------------------
  // /model 命令与这一页做的是同一件事，走同一个接口：切换写进会话自己的记录，
  // 只影响这个会话接下来的运行。列表与"当前用的是哪一个、这个选择来自哪里"都
  // 来自 /api/models，界面不自己推断。

  let modelPayload = null;
  let modelPayloadSessionID = null;
  let modelListRevision = 0;
  let modelListed = false;
  let modelBusy = false;

  function setModelSwitchStatus(text, className = '') {
    modelSwitchStatus.textContent = text;
    modelSwitchStatus.className = `luna-status model-switch-status${className ? ` ${className}` : ''}`;
  }

  function modelRowNode() {
    const item = make('li', 'model-row luna-list-item');
    const head = make('div', 'model-head');
    const title = make('div', 'model-title');
    title.append(make('span', 'model-name'), make('span', 'model-badge'));
    const use = make('button', 'luna-button model-use');
    use.type = 'button';
    use.addEventListener('click', () => chooseModel(item.modelRow));
    head.append(title, use);
    item.append(head, make('p', 'model-provider'));
    return item;
  }

  function updateModelRow(item, row) {
    item.modelRow = row;
    item.dataset.model = row.name;
    item.querySelector('.model-name').textContent = row.name;
    const badge = item.querySelector('.model-badge');
    badge.textContent = row.current ? '这个会话在用' : row.isDefault ? '配置里的默认' : '';
    badge.hidden = badge.textContent === '';
    item.querySelector('.model-provider').textContent = row.provider ? `提供方 ${row.provider}` : '';
    const use = item.querySelector('.model-use');
    use.hidden = row.current;
    use.disabled = modelBusy || running || switching;
    use.textContent = '这个会话用它';
    use.setAttribute('aria-label', `让这个会话使用模型 ${row.name}`);
  }

  function renderModelList(payload) {
    if (payload !== undefined) modelPayload = payload;
    const listed = modelListed && modelPayload && modelPayloadSessionID === currentSessionID ? modelPayload : null;
    const current = listed && listed.current ? textOr(listed.current.name) : '';
    const rows = listed && Array.isArray(listed.models) ? listed.models.map((model) => ({
      name: textOr(model.name), provider: textOr(model.provider), isDefault: Boolean(model.default), current: textOr(model.name) === current
    })) : [];
    modelList.replaceChildren();
    for (const row of rows) {
      const item = modelRowNode();
      updateModelRow(item, row);
      modelList.append(item);
    }
    modelListEmpty.hidden = !listed || rows.length > 1;
    if (listed) {
      const origin = listed.current && listed.current.origin === 'session' ? '这个会话选的' : '配置里的默认';
      setModelSwitchStatus(`当前：${current || '未报告'}（${origin}）。`);
    }

  }

  async function updateModelList() {
    if (modelBusy) return false;
    const id = currentSessionID;
    const revision = ++modelListRevision;
    if (modelPayloadSessionID !== id) {
      renderModelList();
      setModelSwitchStatus('正在读取当前会话的模型…');
    }
    try {
      const response = await fetch('/api/models' + sessionQuery(), { cache: 'no-store' });
      if (!response.ok) throw new Error(await errorMessage(response));
      const payload = await response.json();
      if (id !== currentSessionID || revision !== modelListRevision) return false;
      modelPayload = payload;
      modelPayloadSessionID = id;
      modelListed = true;
      renderModelList();
      return true;
    } catch (error) {
      if (id !== currentSessionID || revision !== modelListRevision) return false;
      modelListed = false;
      renderModelList();
      setModelSwitchStatus('读取模型清单失败：' + error.message, 'failure');
      return false;
    }
  }

  async function chooseModel(row) {
    if (!row || !row.name || modelBusy || running || switching) return;
    await applyModelChoice(row.name);
  }

  // textOr 把服务端字段翻成字符串：缺字段渲染成空，而不是 "undefined"。
  function textOr(value) {
    return capabilityPanelText(value);
  }


  function renderState(state) {
    lastRuntimeState = state;
    renderReloadTools(state);
    $('model').textContent = valueOrDash(state.model);
    $('provider').textContent = valueOrDash(state.provider_host);
    $('host-pid').textContent = valueOrDash(state.host_pid);
    $('busy').textContent = state.busy ? `运行中 · ${valueOrDash(state.current_run_id)}` : '可用';
    $('current-session').textContent = valueOrDash(state.current_session_id);
    // 运行预算是这一次运行真正的余地：一轮能走多少轮、能走多久。两者都由服务端
    // 施加并如实报告，界面只转述；运行详情与模型服务两处读的是同一份状态。
    const budgets = runBudgetView(state);
    $('effort').textContent = reasoningEffortView(state.reasoning_effort).label;
    $('budgets').textContent = budgets.label;
    $('settings-budgets').textContent = budgets.label;
    $('settings-budgets').title = budgets.note;

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
    syncCapabilityWidgets(capabilityWidgets(state.capabilities));
    refreshApprovals();
    // 设置里的能力清单与模型服务参数读的是同一份状态：停用后入口、面板与这里的
    // "未在服务"一起变，不会各说一套。
    renderCapabilities(state.capabilities);
    renderModelFacts(state);
    setSessionControls();
  }

  async function updateState() {
    try {
      const response = await fetch('/api/state', { cache: 'no-store' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      renderState(await response.json());
      if (!running && !switching) updateSessionControls();
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

  // 侧栏的主体是会话导航，不是日志。它唯一会写状态的情况是自己的列表读不到——
  // 那是这一列自身的故障，报在这里才对得上主体。会话切换期间的加载状态与成功后的
  // 确认文案一律不写进这里：加载表现在主区的占位里（见 setTranscriptBusy），
  // 而会话内容的读取错误在用户正在看的地方（见 setConversationStatus）。
  // 错误保留到下一次操作，不会自己消失。
  function setSessionStatus(text, className = '') {
    sessionStatus.textContent = text;
    sessionStatus.className = `session-status${className ? ` ${className}` : ''}`;
  }

  // 主区状态行：会话区域里唯一会说话的一行，位置就在输入区上方，所以不论对话
  // 滚到哪都看得见。它承载两类东西——一次操作后自己消失的瞬时提示，和需要用户
  // 处理的错误（保留到下一次操作）。每次写入先撤掉上一个定时器，一个旧的瞬时
  // 提示不会顺手把后来写入的错误抹掉。
  const CONVERSATION_STATUS_LINGER = 4000;
  let conversationStatusTimer = null;

  function setConversationStatus(text, failure = false) {
    if (conversationStatusTimer !== null) {
      clearTimeout(conversationStatusTimer);
      conversationStatusTimer = null;
    }
    conversationStatus.textContent = text;
    conversationStatus.className = failure ? 'conversation-status failure' : 'conversation-status';
    conversationStatus.hidden = !text;
    if (!text || failure) return;
    conversationStatusTimer = setTimeout(() => {
      conversationStatusTimer = null;
      conversationStatus.textContent = '';
      conversationStatus.className = 'conversation-status';
      conversationStatus.hidden = true;
    }, CONVERSATION_STATUS_LINGER);
  }

  // 会话切换的加载表现在用户正在看的主区：一段占位骨架 + aria-busy，而不是在
  // 侧栏追加一句状态文字。占位与真实内容在同一次同步写入里换手（先渲染，再收
  // 占位），所以浏览器只画一帧，不会出现"占位高度 → 内容高度"的跳动；外壳的
  // 高度由转录区自己撑开，页头与输入区不参与内容的高度。
  function setTranscriptBusy(busy) {
    $('transcript-skeleton').hidden = !busy;
    transcript.setAttribute('aria-busy', busy ? 'true' : 'false');
    if (busy) {
      emptyState.hidden = true;
      sessionNotices.hidden = true;
      conversation.hidden = true;
      return;
    }
    conversation.hidden = false;
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
      // 读到了：上一次的列表故障不再挂着。
      setSessionStatus('');
    } catch (error) {
      // 只有这一列自身的故障写在这里：主体是导航，报错的对象就是它。
      setSessionStatus(`无法读取会话列表：${error.message}`, 'failure');
    }
  }

  // A single place decides whether the composer and the session controls accept
  // input: a run and a replay in flight both block the controls that would mix
  // two states. 运行期间发送按钮是 Stop，它必须保持可点：取消就是它的用途。
  function remoteBusy(){return Boolean(lastRuntimeState?.busy&&lastRuntimeState.current_run_id!==lastCompletedRunID);}
  function remoteRunID(){return !running&&currentSessionID&&remoteBusy()&&lastRuntimeState.current_session_id===currentSessionID?lastRuntimeState.current_run_id||'':'';}
  async function cancelRemoteRun(){
    const id=remoteRunID();if(!id||remoteCancelID)return;
    remoteCancelID=id;setSessionControls();
    try {
      const response=await fetch('/api/runs/'+encodeURIComponent(id)+'/cancel',{method:'POST'});
      if(!response.ok&&response.status!==404)throw new Error(await errorMessage(response));
      if(response.status===404){lastCompletedRunID=id;remoteCancelID='';}
      await updateState();
    }catch(error){remoteCancelID='';setConversationStatus('停止失败：'+error.message,true);}
    setSessionControls();
  }
  function setSessionControls() {
    renderSessionControls();
    const remote=remoteRunID();
    if(remoteCancelID&&remoteCancelID!==remote)remoteCancelID='';
    const cancelling=Boolean(running&&liveRun?.cancelling)||Boolean(remote&&remoteCancelID===remote);
    const action=cancelling?'cancelling':running||remote?'cancel':'send';
    if(send.dataset.action!==action)setSendAction(action);
    send.disabled=switching||cancelling||(!running&&!remote&&(!executionReady()||remoteBusy()));
    if(!running&&!remote&&remoteBusy())send.title='另一个会话正在运行';
    sessionNew.disabled = running || switching;
    rerenderSessions();
  }

  function setConversationTitle(text = '新会话') {
    const title = sessionTitle(text);
    $('conversation-title').textContent = title;
    $('conversation-title').setAttribute('title', title);
  }

  // The workspace marker is read from the same session payload that names the
  // title, so switching sessions recomputes it instead of it being fetched once
  // at startup. A session with no workspace clears the element: there is no
  // "not set" placeholder, and no request is made for it.
  function setConversationWorkspace(workspace) {
    const badge = workspaceBadge(workspace);
    const node = $('conversation-workspace');
    $('composer-project-detail').textContent = badge ? [badge.label, badge.title].filter(Boolean).join('\n') : '';
    if (!badge) {
      node.textContent = '';
      node.removeAttribute('title');
      node.hidden = true;
      return;
    }
    node.textContent = badge.label;
    if (badge.title) node.setAttribute('title', badge.title);
    else node.removeAttribute('title');
    node.hidden = false;
  }

  function resetConversation() {
    clearApprovals();
    closeComposerPopover(false);
    runtimeWidgets.resetData();
    resetCapabilityWidgets();
    currentWorkspaceID = '';
    if (workspaceListed) renderWorkspaces();
    if (modelListed || (activePanel === panels.settings && !$('settings-pane-model').hidden)) { renderModelList(); updateModelList(); }
    sessionUsageByRun = new Map();
    usageRunID = '';
    renderUsageBar();
    setConversationTitle();
    setConversationWorkspace(null);
    conversation.replaceChildren();
    sessionNotices.replaceChildren();
    sessionNotices.hidden = true;
    // 上一段会话的错误提示不跟着换到新会话里。
    setConversationStatus('');
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
    if (record.tools.length || record.usage) {
      // 回放出来的调用也排在同一条时间线里：它同样是"过程"，不是回答。
      const state = replayRunState(record.status);
      node.meta.textContent = [runTraceMeta(record.tools.length, null, state), record.usage ? usageText(record.usage) : ''].filter(Boolean).join(' · ');
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
    const usage = usageFromRecords(detail?.records);
    sessionUsageByRun = usage.runs;
    usageRunID = usage.latest;
    renderUsageBar();
    const replay = replaySession(detail);
    setConversationTitle(replay.title);
    setConversationWorkspace(detail ? detail.workspace : null);
    currentWorkspaceID = detail && detail.workspace ? textOr(detail.workspace.id) : '';
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
  // 等待期间的表现发生在主区：一段占位骨架 + aria-busy。侧栏只做导航，这里既不
  // 写"正在恢复会话…"，也不在成功时补一句"已恢复"。
  async function loadSession(id) {
    clearApprovals();
    currentSessionID = id;
    resetCapabilityWidgets();
    resetExecutionState();
    modelListRevision += 1;
    if (modelListed || (activePanel === panels.settings && !$('settings-pane-model').hidden)) {
      renderModelList();
      setModelSwitchStatus('正在读取当前会话的模型…');
    }
    sessionModelChoice = sessionReasoningChoice = sessionSetupChoice = null;
    sessionSetupProblem = '';
    setupRevision += 1;
    setupController?.abort();
    sessionControlsRevision += 1;
    rerenderSessions();
    switching = true;
    setSessionControls();
    setTranscriptBusy(true);
    try {
      const response = await fetch(`/api/sessions/${id}`, { cache: 'no-store' });
      if (!response.ok) throw new Error(await errorMessage(response));
      const detail = await response.json();
      // 先画出真实内容，再收走占位：两件事在同一次同步写入里完成，浏览器只画
      // 一帧，所以没有"占位高度 → 内容高度"的中间帧。
      renderReplayedSession(detail);
      setTranscriptBusy(false);
    } catch (error) {
      dropSession();
      setTranscriptBusy(false);
      // 错误留在用户正在看的地方：会话内容读不出来是这一片区域的事。
      setConversationStatus(`无法读取这个会话：${error.message}`, true);
    } finally {
      switching = false;
      setSessionControls();
      updateSessionControls();
      if (modelListed || (activePanel === panels.settings && !$('settings-pane-model').hidden)) updateModelList();
    }
  }

  // dropSession leaves the URL with no session in it and the area empty, which
  // is the truth after a new session is started or a stored one is gone.
  function dropSession() {
    currentSessionID = '';
    resetExecutionState();
    sessionModelChoice = sessionReasoningChoice = sessionSetupChoice = null;
    sessionSetupProblem = '';
    setupRevision += 1;
    setupController?.abort();
    closeCommandMenu();
    updateSessionControls();
    history.replaceState(null, '', `${location.pathname}${location.search}`);
    resetConversation();
    rerenderSessions();
  }

  function newSession() {
    if (running || switching) return;
    if (activePanel === panels.sessions) closeDrawer();
    dropSession();
    setConversationStatus('新会话：可以先选择模型、思考档位和工作区。');
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
    syncCapabilityWidgets(capabilityWidgets(lastRuntimeState?.capabilities));
    resetExecutionState();
    location.hash = sessionHash(id);
    rerenderSessions();
    updateSessionControls();
    // 会话 id 与标题已经出现在列表里，这里不再重复一句"已开始记录"。
  }

  async function applySessionHash() {
    const id = parseSessionHash(location.hash);
    if (id === currentSessionID) {
      // A fragment that cannot be a session id is not left in the address bar
      // to be copied or refreshed into a request that would be rejected.
      if (!id && location.hash.startsWith('#session')) {
        history.replaceState(null, '', `${location.pathname}${location.search}`);
        setConversationStatus('链接里的会话 id 无法识别，已按新会话开始。', true);
      }
      return;
    }
    if (running || switching) {
      // A run is streaming into the current session; honouring the fragment now
      // would draw two sessions into one area. The hash is put back instead.
      const restored = sessionHash(currentSessionID);
      history.replaceState(null, '', restored ? `${location.pathname}${location.search}${restored}` : `${location.pathname}${location.search}`);
      setConversationStatus('运行中无法切换会话。', true);
      return;
    }
    if (!id) {
      dropSession();
      setConversationStatus('新会话：发送第一条消息后开始记录。');
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
  const capabilityWidgetNodes = new Map();
  function resetCapabilityWidgets() {
    for (const record of capabilityWidgetNodes.values()) record.dispose();
    capabilityWidgetNodes.clear();
  }
  function mountRuntimeInstance(widget,key,data) {
    const sessionID=currentSessionID;
    const dispose=runtimeWidgets.register({id:key,title:widget.title,mount(target){
      let live=true,loaded=null,latest=data;
      const widgetURL=capabilityPanelEntryURL(widget.entry);
      const base=uiPluginHostAPI(UI_PLUGIN_API_VERSION,()=>{});
      (async()=>{
        try {
          const imported=await import(widgetURL);if(!live)return;
          const missing=uiPluginMissingExports(imported);if(missing.length)throw new Error('组件缺少导出：'+missing.join('、'));
          loaded=imported;imported.mount(target,Object.freeze({...base,sessionID,data:latest}));runtimeWidgets.reflow();
        } catch(error) {
          if(!live)return;try{loaded?.unmount(target);}catch(_){};loaded=null;
          target.replaceChildren(make('p','runtime-widget-error','组件加载失败：'+uiPluginErrorDetail(error)));
        }
      })();
      return {update(value){latest=value;if(loaded&&typeof loaded.update==='function')loaded.update(target,value);},unmount(){live=false;loaded?.unmount(target);loaded=null;}};
    }});
    if(data!==undefined)runtimeWidgets.update(key,data);
    return {title:widget.title,dispose,update(value){runtimeWidgets.update(key,value);},suggest(layout){
      if(!layout||typeof layout!=='object')return;
      runtimeWidgets.move(key,layout,'model');
      if(layout.visible===true)runtimeWidgets.show(key,'model');
      else if(layout.visible===false)runtimeWidgets.hide(key,'model');
    }};
  }
  function widgetSource(widget) {
    let live=true,revision=0,active=null;const children=new Map(),sessionID=currentSessionID;
    return {...widget,sessionID,
      dispose(){live=false;revision++;if(active){clearTimeout(active.timer);active.controller.abort();active=null;}for(const child of children.values())child.dispose();children.clear();},
      async refresh(){
        if(!live||active||!sessionID||sessionID!==currentSessionID)return;
        const request=++revision;
        const controller=new AbortController();let expired=false;
        const timer=setTimeout(()=>{expired=true;controller.abort();},15000);active={controller,timer};
        try {
          const response=await fetch(widget.source+'?session='+encodeURIComponent(sessionID),{cache:'no-store',signal:controller.signal});if(!response.ok)throw new Error(await errorMessage(response));
          const payload=await response.json();if(!live||request!==revision||sessionID!==currentSessionID)return;
          const list=(Array.isArray(payload.instances)?payload.instances:[]).slice(0,32).filter(item=>item&&/^[a-z][a-z0-9_-]{0,47}$/.test(item.id)&&typeof item.title==='string'&&item.title.trim()&&item.title.length<=80);
          const wanted=new Map(list.map(item=>[item.id,item]));
          for(const [id,child] of children){if(!wanted.has(id)||wanted.get(id).title!==child.title){child.dispose();children.delete(id);}}
          for(const item of wanted.values()){
            let child=children.get(item.id);
            if(!child){child=mountRuntimeInstance({...widget,title:item.title},'instance:'+widget.id+':'+sessionID+':'+item.id,item.data);children.set(item.id,child);}
            else child.update(item.data);
            child.suggest(item.layout);
          }
        } catch(error) {if(live&&request===revision&&sessionID===currentSessionID&&(error.name!=='AbortError'||expired))setConversationStatus(expired?'运行组件数据请求超时，可稍后重试。':'运行组件数据暂不可用：'+error.message,true);}
        finally{clearTimeout(timer);if(active?.controller===controller)active=null;}
      }
    };
  }
  function refreshWidgetSources(){for(const record of capabilityWidgetNodes.values())record.refresh?.();}
  function syncCapabilityWidgets(list) {
    const wanted=new Map(list.map(widget=>[widget.id,widget]));
    for(const [id,record] of capabilityWidgetNodes){const widget=wanted.get(id);if(!widget||widget.entry!==record.entry||widget.source!==record.source||widget.title!==record.title||widget.owner!==record.owner||record.sessionID!==currentSessionID){record.dispose();capabilityWidgetNodes.delete(id);}}
    for(const widget of wanted.values()){
      if(capabilityWidgetNodes.has(widget.id))continue;
      try{
        if(widget.source)capabilityWidgetNodes.set(widget.id,widgetSource(widget));
        else {const instance=mountRuntimeInstance(widget,'cap:'+widget.id);capabilityWidgetNodes.set(widget.id,{...widget,sessionID:currentSessionID,dispose:instance.dispose});}
      }catch(error){setConversationStatus('运行组件注册失败：'+uiPluginErrorDetail(error),true);}
    }
    refreshWidgetSources();
  }

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
    // 贡献面板固定在运行详情之前：页头顺序与它们被声明的顺序一致。
    runtimeActions.insertBefore(button, runtimeToggle);
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
  loadCommands();
  updateSessions();
  updateState();
  setInterval(updateState, 2000);
}
