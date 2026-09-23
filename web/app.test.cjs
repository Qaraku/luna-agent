const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const root = __dirname;
const source = (name) => fs.readFileSync(path.join(root, name), 'utf8');

test('SSE parser preserves app-owned type and JSON payload', () => {
  const { parseEventBlock } = require('./app.js');
  assert.deepEqual(parseEventBlock('event: tool.finished\ndata: {"generation":2,"version":"v2","plugin_pid":44}\n'), {
    type: 'tool.finished', data: { generation: 2, version: 'v2', plugin_pid: 44 }
  });
});

test('assistant markdown becomes safe structured blocks and inline runs', () => {
  const { parseMarkdownBlocks, parseInline } = require('./app.js');
  assert.deepEqual(parseMarkdownBlocks('结果如下：\n\n- **输入**：` moon `\n- **输出**：`moon`\n\n```text\nraw <tag>\n```'), [
    { type: 'paragraph', text: '结果如下：' },
    { type: 'list', ordered: false, items: ['**输入**：` moon `', '**输出**：`moon`'] },
    { type: 'code', language: 'text', text: 'raw <tag>' }
  ]);
  assert.deepEqual(parseInline('**输入**：` moon `'), [
    { type: 'strong', text: '输入' },
    { type: 'text', text: '：' },
    { type: 'code', text: ' moon ' }
  ]);
});

test('tool summary preserves immutable execution identity', () => {
  const { toolSummary } = require('./app.js');
  assert.equal(toolSummary({ generation: 3, version: 'v1', plugin_pid: 91 }), 'generation 3 · v1 · PID 91');
  assert.equal(toolSummary({}), 'generation — · — · PID —');
});

test('tool copy names each tool and falls back without inventing one', () => {
  const { toolLabel } = require('./app.js');
  assert.deepEqual(toolLabel('luna_text_transform'), {
    noun: '文本转换', running: '正在转换文本…', finished: '文本转换完成', failed: '文本转换失败'
  });
  assert.deepEqual(toolLabel('luna_read_file'), {
    noun: '读取文件', running: '正在读取文件…', finished: '读取文件完成', failed: '读取文件失败'
  });
  assert.deepEqual(toolLabel(undefined), {
    noun: '工具调用', running: '正在调用工具…', finished: '工具调用完成', failed: '工具调用失败'
  });
  assert.deepEqual(toolLabel('luna_unknown'), toolLabel(undefined), 'an unknown tool must not borrow a known tool name');
});

test('tool activity copy is per tool while values remain exact', () => {
  const { toolActivityLabel, candidateLabel, reloadCopy } = require('./app.js');
  assert.equal(toolActivityLabel('luna_text_transform', 'running'), '正在转换文本…');
  assert.equal(toolActivityLabel('luna_text_transform', 'finished'), '文本转换完成');
  assert.equal(toolActivityLabel('luna_text_transform', 'failed'), '文本转换失败');
  assert.equal(toolActivityLabel('luna_read_file', 'running'), '正在读取文件…');
  assert.equal(toolActivityLabel('luna_read_file', 'finished'), '读取文件完成');
  assert.equal(toolActivityLabel('luna_read_file', 'failed'), '读取文件失败');
  assert.equal(toolActivityLabel('luna_read_file', 'unknown-state'), '读取文件');
  assert.equal(toolActivityLabel(undefined, 'running'), '正在调用工具…');
  assert.equal(candidateLabel('v1'), '稳定版本 v1');
  assert.equal(candidateLabel('v2'), '候选版本 v2');
  assert.equal(candidateLabel('broken'), '故障演练 broken');
  assert.deepEqual(reloadCopy('pending', 'v2'), { summary: '正在验证 v2…', technical: '' });
  assert.deepEqual(reloadCopy('success', 'v2'), { summary: 'v2 已启用。', technical: '' });
  assert.deepEqual(reloadCopy('failure', 'broken', 'handshake timeout'), {
    summary: '无法启用 broken，当前版本保持不变。', technical: 'handshake timeout'
  });
});

test('valueOrDash keeps exact values and marks missing ones', () => {
  const { valueOrDash } = require('./app.js');
  assert.equal(valueOrDash(0), '0');
  assert.equal(valueOrDash('v1'), 'v1');
  assert.equal(valueOrDash(12), '12');
  assert.equal(valueOrDash(undefined), '—');
  assert.equal(valueOrDash(null), '—');
  assert.equal(valueOrDash(''), '—');
});

test('plugin status copy distinguishes active, draining and failed records', () => {
  const { pluginStatusLabel } = require('./app.js');
  assert.equal(pluginStatusLabel('active'), '启用中');
  assert.equal(pluginStatusLabel('retiring'), '退役中');
  assert.equal(pluginStatusLabel('failed'), '不可用');
  assert.equal(pluginStatusLabel(undefined), '状态未知');
  assert.equal(pluginStatusLabel('something-else'), '状态未知');
});

test('plugin rows cover every tool and stay honest for empty and partial states', () => {
  const { pluginRows } = require('./app.js');
  assert.deepEqual(pluginRows([
    { tool: 'luna_text_transform', generation: 2, version: 'v2', plugin_pid: 91, candidate: 'v2', status: 'active', inflight: 0 },
    { tool: 'luna_read_file', generation: 2, version: 'v2', plugin_pid: 92, candidate: 'v2', status: 'active', inflight: 1 }
  ]), [
    { tool: 'luna_text_transform', label: '文本转换', status: '启用中', identity: 'generation 2 · v2 · PID 91' },
    { tool: 'luna_read_file', label: '读取文件', status: '启用中', identity: 'generation 2 · v2 · PID 92' }
  ]);

  // A tool mid-replacement reports both generations; both rows are shown.
  assert.deepEqual(pluginRows([
    { tool: 'luna_read_file', generation: 3, version: 'v2', plugin_pid: 93, candidate: 'v2', status: 'active', inflight: 0 },
    { tool: 'luna_read_file', generation: 2, version: 'v1', plugin_pid: 41, candidate: 'v1', status: 'retiring', inflight: 1 }
  ]), [
    { tool: 'luna_read_file', label: '读取文件', status: '启用中', identity: 'generation 3 · v2 · PID 93' },
    { tool: 'luna_read_file', label: '读取文件', status: '退役中', identity: 'generation 2 · v1 · PID 41' }
  ]);

  // No records, no payload, or a record without fields: never an invented tool.
  assert.deepEqual(pluginRows([]), []);
  assert.deepEqual(pluginRows(undefined), []);
  assert.deepEqual(pluginRows(null), []);
  assert.deepEqual(pluginRows('luna_read_file'), []);
  assert.deepEqual(pluginRows([{}]), [
    { tool: '—', label: '工具调用', status: '状态未知', identity: 'generation — · — · PID —' }
  ]);
  assert.deepEqual(pluginRows([null, { tool: 'luna_read_file', version: 'v1', status: 'failed' }]), [
    { tool: '—', label: '工具调用', status: '状态未知', identity: 'generation — · — · PID —' },
    { tool: 'luna_read_file', label: '读取文件', status: '不可用', identity: 'generation — · v1 · PID —' }
  ]);
});

test('Moonline markup is conversation-first with an accessible hidden runtime drawer', () => {
  const html = source('index.html');
  assert.match(html, /<title>Luna<\/title>/);
  assert.match(html, />Luna<\/span>/);
  assert.match(html, /本地会话 · 不保存记录/);
  assert.match(html, /有什么想一起看看？/);
  assert.match(html, /给 Luna 发消息…/);
  assert.match(html, />发送<\/button>/);
  assert.match(html, /id="runtime-toggle"[^>]*aria-expanded="false"[^>]*aria-controls="runtime-drawer"/);
  assert.match(html, /id="runtime-drawer"[^>]*hidden/);
  assert.match(html, /id="runtime-backdrop"[^>]*hidden/);
  assert.match(html, /id="runtime-close"[^>]*aria-label="关闭运行详情"/);
  assert.match(html, /id="transcript"[^>]*tabindex="0"/);
  assert.equal(/id="transcript"[^>]*aria-live/.test(html), false, 'streaming transcript must not announce every token');
  assert.match(html, /<summary>生命周期<\/summary>/);
  assert.match(html, /<summary>技术详情<\/summary>/);
  assert.match(html, /<section class="drawer-section" aria-labelledby="plugins-title">/);
  assert.match(html, /<h3 id="plugins-title">工具插件<\/h3>/);

  for (const id of ['model', 'provider', 'host-pid', 'busy', 'plugins', 'plugins-empty', 'candidate', 'reload', 'reload-status', 'events']) {
    assert.match(html, new RegExp(`id="${id}"`));
  }
  for (const value of ['v1', 'v2', 'broken']) {
    assert.match(html, new RegExp(`<option value="${value}"`));
  }
});

test('the plugin section is an empty list in markup and never a fixed tool', () => {
  const html = source('index.html');
  const js = source('app.js');
  assert.match(html, /<dl id="plugins" class="fact-list"><\/dl>/, 'the drawer list is filled from state, not from markup');
  assert.match(html, /id="plugins-empty"[^>]*hidden[^>]*>暂未读到工具插件。</);
  for (const id of ['plugin-version', 'plugin-generation', 'plugin-pid']) {
    assert.equal(html.includes(id), false, `stale single-plugin field ${id} must be gone`);
  }
  assert.equal(html.includes('文本转换器'), false, 'the drawer must not be titled after one tool');
  assert.equal(js.includes('state.active'), false, 'the removed single-active field must not be read');
  assert.equal(js.includes('plugin-version'), false, 'the removed single-plugin field must not be written');
  assert.match(js, /pluginRows\(state\.plugins\)/, 'the drawer renders the plugins array');
});

test('the primary surface omits console-era and fabricated content', () => {
  const html = source('index.html');
  for (const text of ['Core preview', 'LOCAL KERNEL', '真实模型', 'Run agent', 'Idle', 'Finished', 'luna_text_transform', 'Moonline']) {
    assert.equal(html.includes(text), false, `unexpected primary copy: ${text}`);
  }
  assert.equal(source('app.js').includes('Moonline'), false, 'internal codename must not reach the frontend script');
  assert.equal(source('style.css').includes('Moonline'), false, 'internal codename must not reach the stylesheet');
  assert.equal(/<article[^>]*class="[^"]*assistant/.test(html), false, 'empty state must not fabricate an assistant turn');
  assert.equal(/\b(?:src|href)=["']https?:\/\//.test(html), false, 'remote assets are not allowed');
});

test('styles implement fixed-shell Moonline tokens, type, motion and mobile sheet', () => {
  const css = source('style.css');
  for (const token of ['#100F0D', '#151411', '#1B1915', '#2C2A24', '#454139', '#F1EDE4', '#B7B0A3', '#8B8579', '#D8D4C8', '#E6E1D5', '#C7C2B7', '#171612', '#9FB59C', '#C4A978', '#C88982']) {
    assert.match(css.toUpperCase(), new RegExp(token.toUpperCase()), `missing token ${token}`);
  }
  assert.match(css, /height:\s*100dvh/);
  assert.match(css, /height:\s*52px/);
  assert.match(css, /max-width:\s*760px/);
  assert.match(css, /Noto Sans SC/);
  assert.match(css, /Noto Serif SC/);
  assert.match(css, /Iosevka Fixed/);
  assert.match(css, /\.assistant-body\s*>\s*p/);
  assert.match(css, /\.assistant-body\s+code/);
  assert.match(css, /\.assistant-body\s*>\s*pre/);
  assert.match(css, /@media\s*\(max-width:\s*600px\)/);
  assert.equal(/\.section-kicker|\.empty-kicker/.test(css), false, 'unused kicker styles must not remain');
  assert.equal(/\.turn\.assistant\s*\{[^}]*padding-right/.test(css), false, 'assistant turns must share one right edge with user turns');
  assert.match(css, /\.composer-hint[^}]*font-size:\s*11px/s);
  assert.match(css, /max-height:\s*92dvh/);
  assert.match(css, /prefers-reduced-motion:\s*reduce/);
  const reducedMotion = css.slice(css.indexOf('prefers-reduced-motion'));
  assert.equal(/transform:\s*none/.test(reducedMotion), false, 'reduced motion must not cancel the drawer transform');
  assert.match(css, /\.luna-mark[^}]*width:\s*12px/s);
  assert.equal(/gradient\s*\(/i.test(css), false, 'gradients are not allowed');
  assert.equal(/box-shadow\s*:/i.test(css), false, 'ambient shadows are not allowed');
  for (const match of css.matchAll(/border-radius:\s*([^;}]+)/gi)) {
    for (const radius of match[1].trim().split(/\s+/)) {
      assert.ok(['0', '4px', '8px', '12px'].includes(radius), `unsupported radius ${radius}`);
    }
  }
});

test('frontend uses safe DOM APIs and includes interaction contracts', () => {
  const js = source('app.js');
  assert.equal(js.includes('innerHTML'), false);
  assert.equal(/\beval\s*\(/.test(js), false);
  assert.equal(js.includes('new Function'), false);
  assert.match(js, /currentTurn\.terminal\s*=\s*true/);
  assert.match(js, /if \(!currentTurn \|\| !currentTurn\.terminal\) showRunFailure\(error\.message\)/);
  assert.match(js, /function resolveOpenTools\(\)/);
  assert.match(js, /工具在完成前中断。/);
  assert.match(js, /终止事件/);
  assert.match(js, /if \(currentTurn && !currentTurn\.terminal && !currentTurn\.failed\)/);
  assert.match(js, /'回应在完成前中断了。'/);
  assert.match(js, /isComposing/);
  assert.match(js, /event\.key === 'Escape'/);
  assert.match(js, /\.inert\s*=/);
  assert.match(js, /document\.querySelector\('\.app-shell'\)/);
  assert.match(js, /appShell\.inert = value/);
  assert.match(js, /scrollHeight\s*-\s*scrollTop/);
  assert.match(source('index.html'), /回到最新/);
  assert.match(js, /Luna 正在回应…/);
  assert.match(js, /Luna 没能完成这次回应。/);
  assert.match(js, /正在连接…/);
  assert.match(js, /运行详情暂不可用/);
  assert.match(js, /const retryMessage = currentTurn\.message/);
  assert.match(js, /submitMessage\(retryMessage\)/);
  assert.match(js, /replaceChildren/);
  assert.match(js, /document\.createElement/);
  assert.match(js, /function renderMarkdown/);
  assert.match(js, /currentTurn\.answer \+= text/);
  assert.match(js, /renderMarkdown\(currentTurn\.body, currentTurn\.answer\)/);
  assert.match(js, /'工具'/);
  assert.match(js, /\$\('plugins-empty'\)\.hidden = rows\.length > 0/);
});

test('the current session travels in the URL hash and only a well formed id is used', () => {
  const { parseSessionHash, sessionHash, isSessionID } = require('./app.js');
  assert.equal(parseSessionHash('#session=8f2a1c4d9e0b'), '8f2a1c4d9e0b');
  assert.equal(parseSessionHash('session=8f2a1c4d9e0b'), '8f2a1c4d9e0b');
  assert.equal(parseSessionHash('#session=8f2a1c4d9e0b&other=1'), '8f2a1c4d9e0b');
  assert.equal(parseSessionHash('#session='), '');
  assert.equal(parseSessionHash('#session=../../etc/passwd'), '');
  assert.equal(parseSessionHash('#session=8F2A1C4D'), '', 'uppercase is outside the store charset');
  assert.equal(parseSessionHash('#session=short'), '', 'below the length bound');
  assert.equal(parseSessionHash('#other=1'), '');
  assert.equal(parseSessionHash(''), '');
  assert.equal(parseSessionHash(undefined), '');

  assert.equal(sessionHash('8f2a1c4d9e0b'), '#session=8f2a1c4d9e0b');
  assert.equal(sessionHash('bad id'), '', 'a fragment is only written for a usable id');
  assert.equal(isSessionID('0123456789abcdef'), true);
  assert.equal(isSessionID('a'.repeat(64)), true);
  assert.equal(isSessionID('a'.repeat(65)), false);
  assert.equal(isSessionID('01234567'), true);
  assert.equal(isSessionID('0123456'), false);
  assert.equal(isSessionID(undefined), false);
});

test('session rows carry title, time and run count while the current one is marked', () => {
  const { sessionRows } = require('./app.js');
  assert.deepEqual(sessionRows({
    sessions: [
      { id: '8f2a1c4d9e0b', title: '把这段文字改短', updated_at: '2026-09-23T17:14:16.123456789+08:00', run_count: 3 },
      { id: 'a1b2c3d4e5f6', title: '', updated_at: '', run_count: 0 }
    ]
  }, '8f2a1c4d9e0b'), [
    { id: '8f2a1c4d9e0b', shortId: '8f2a1c4d', title: '把这段文字改短', time: '2026-09-23 17:14', runs: '3 次运行', current: true },
    { id: 'a1b2c3d4e5f6', shortId: 'a1b2c3d4', title: '未命名会话', time: '—', runs: '尚无运行', current: false }
  ]);

  // No list, an unreadable list or entries without an id: nothing is invented.
  assert.deepEqual(sessionRows({ sessions: [] }, ''), []);
  assert.deepEqual(sessionRows({ sessions: [] }), []);
  assert.deepEqual(sessionRows({}), []);
  assert.deepEqual(sessionRows(undefined), []);
  assert.deepEqual(sessionRows({ sessions: 'nope' }), []);
  assert.deepEqual(sessionRows({ sessions: [null, 'x', { id: '../../etc' }, { id: '8f2a1c4d9e0b' }] }, ''), [
    { id: '8f2a1c4d9e0b', shortId: '8f2a1c4d', title: '未命名会话', time: '—', runs: '运行次数未知', current: false }
  ], 'a row without a usable id cannot be switched to');

  // The server order (newest first) is kept; the browser does not re-sort it.
  const rows = sessionRows({
    sessions: [
      { id: 'aaaaaaaaaaaa', title: '先', updated_at: '2026-09-23T09:00:00+08:00', run_count: 2 },
      { id: 'bbbbbbbbbbbb', title: '后', updated_at: '2026-09-23T10:00:00+08:00', run_count: 1 }
    ]
  }, 'aaaaaaaaaaaa');
  assert.deepEqual(rows.map((row) => row.id), ['aaaaaaaaaaaa', 'bbbbbbbbbbbb']);
  assert.deepEqual(rows.map((row) => row.current), [true, false]);
});

test('a session replays in record order and tool calls attach to their answer', () => {
  const { replaySession } = require('./app.js');
  const replay = replaySession({
    id: '8f2a1c4d9e0b',
    title: '把这段文字改短',
    truncated: false,
    records: [
      { type: 'session', id: '8f2a1c4d9e0b', created_at: '2026-09-23T17:00:00+08:00', title: '把这段文字改短' },
      { type: 'message', run_id: 'r1', role: 'user', text: '把  moon  改短', at: '2026-09-23T17:00:01+08:00' },
      { type: 'tool_call', run_id: 'r1', name: 'luna_text_transform', arguments: '{"text":"  moon  "}', result: 'moon', error: '', at: '2026-09-23T17:00:02+08:00' },
      { type: 'message', run_id: 'r1', role: 'assistant', text: '结果如下：\n\n- `moon`', at: '2026-09-23T17:00:03+08:00' },
      { type: 'run', run_id: 'r1', started_at: '2026-09-23T17:00:00+08:00', ended_at: '2026-09-23T17:00:03+08:00', status: 'ok' }
    ]
  });
  assert.equal(replay.title, '把这段文字改短');
  assert.deepEqual(replay.notices, []);
  assert.deepEqual(replay.turns, [
    { role: 'user', text: '把  moon  改短' },
    {
      role: 'assistant',
      answer: '结果如下：\n\n- `moon`',
      tools: [{ name: 'luna_text_transform', arguments: '{"text":"  moon  "}', result: 'moon', error: '', failed: false }],
      status: 'ok',
      failed: false
    }
  ], 'the session header record carries no conversation');
});

test('replay stays honest for empty, partial, unknown and truncated sessions', () => {
  const { replaySession } = require('./app.js');
  assert.deepEqual(replaySession({ records: [] }), { title: '未命名会话', truncated: false, turns: [], notices: [] });
  assert.deepEqual(replaySession({}), { title: '未命名会话', truncated: false, turns: [], notices: [] });
  assert.deepEqual(replaySession(undefined), { title: '未命名会话', truncated: false, turns: [], notices: [] });
  assert.deepEqual(replaySession({ records: 'nope', title: 'x' }), { title: 'x', truncated: false, turns: [], notices: [] });

  // A torn tail is reported as such, never silently repaired.
  const truncated = replaySession({ records: [{ type: 'message', role: 'user', text: '好' }], truncated: true });
  assert.equal(truncated.truncated, true);
  assert.deepEqual(truncated.notices, ['这个会话的最后一行没有写完，已按可读的部分回放。']);
  assert.equal(truncated.turns.length, 1);
  assert.equal(replaySession({ records: [{ type: 'message', role: 'user', text: '好' }], truncated: false }).notices.length, 0);

  // An unknown type, an unknown role and an unreadable entry are counted, never guessed at.
  const unknown = replaySession({
    records: [
      { type: 'message', role: 'user', text: '好' },
      { type: 'message', role: 'system', text: 'x' },
      { type: 'tool_result' },
      null,
      'nope',
      {}
    ]
  });
  assert.deepEqual(unknown.notices, ['有 5 条记录无法识别，未回放。']);
  assert.deepEqual(unknown.turns, [{ role: 'user', text: '好' }]);

  // Missing fields are replayed as they are, not filled in.
  const partial = replaySession({
    records: [
      { type: 'message', role: 'user' },
      { type: 'tool_call' },
      { type: 'run', status: 'error' }
    ]
  });
  assert.deepEqual(partial.turns, [
    { role: 'user', text: '' },
    {
      role: 'assistant',
      answer: '',
      tools: [{ name: undefined, arguments: undefined, result: undefined, error: '', failed: false }],
      status: 'error',
      failed: true
    }
  ]);

  // A run that ended without leaving a turn still says so; an ok one stays metadata.
  assert.deepEqual(replaySession({
    records: [
      { type: 'message', role: 'user', text: '好' },
      { type: 'run', status: 'cancelled' },
      { type: 'run', status: 'ok' }
    ]
  }).turns, [
    { role: 'user', text: '好' },
    { role: 'note', text: '这次运行被取消' }
  ]);
});

test('a replayed tool call shows the frozen facts and no invented identity', () => {
  const { toolCallFacts, argumentsText, runStatusLabel, sessionTitle, sessionTime, runCountLabel } = require('./app.js');

  // The model's argument text is JSON; it is pretty printed for reading and
  // shown verbatim when it is not JSON at all.
  assert.equal(argumentsText('{"text":"  moon  "}'), '{\n  "text": "  moon  "\n}');
  assert.equal(argumentsText('not json'), 'not json');
  assert.equal(argumentsText(''), '—');
  assert.equal(argumentsText(undefined), '—');
  assert.equal(argumentsText({ text: 'moon' }), '—');

  assert.deepEqual(toolCallFacts({ name: 'luna_read_file', arguments: '{"path":"a.txt"}', result: 'raw <tag>', error: '' }), [
    { label: '工具', value: 'luna_read_file' },
    { label: '参数', value: '{\n  "path": "a.txt"\n}' },
    { label: '结果', value: 'raw <tag>' }
  ]);
  assert.deepEqual(toolCallFacts({ name: 'luna_text_transform', arguments: '{}', result: 'ignored', error: '工具执行失败' }), [
    { label: '工具', value: 'luna_text_transform' },
    { label: '参数', value: '{}' },
    { label: '错误', value: '工具执行失败' }
  ], 'a failed call reports the error instead of a result');
  assert.deepEqual(toolCallFacts({}), [
    { label: '工具', value: '—' },
    { label: '参数', value: '—' },
    { label: '结果', value: '—' }
  ]);
  assert.deepEqual(toolCallFacts(undefined), [
    { label: '工具', value: '—' },
    { label: '参数', value: '—' },
    { label: '结果', value: '—' }
  ]);

  // A record has no plugin identity field, so replay cannot show one.
  const labels = toolCallFacts({ name: 'luna_read_file', arguments: '{}', result: 'ok' }).map((fact) => fact.label);
  assert.equal(labels.includes('执行身份'), false);
  assert.equal(JSON.stringify(toolCallFacts({ name: 'luna_read_file', result: 'ok' })).includes('generation'), false);

  assert.equal(runStatusLabel('ok'), '这次运行已完成');
  assert.equal(runStatusLabel('error'), '这次运行失败了');
  assert.equal(runStatusLabel('cancelled'), '这次运行被取消');
  assert.equal(runStatusLabel('interrupted'), '这次运行中断了');
  assert.equal(runStatusLabel('something-else'), '这次运行的结果未知');
  assert.equal(runStatusLabel(undefined), '这次运行的结果未知');

  assert.equal(sessionTitle('  把文字改短  '), '把文字改短');
  assert.equal(sessionTitle(''), '未命名会话');
  assert.equal(sessionTitle(7), '未命名会话');

  assert.equal(sessionTime('2026-09-23T17:14:16Z'), '2026-09-23 17:14');
  assert.equal(sessionTime('2026-09-23 17:14'), '—');
  assert.equal(sessionTime(undefined), '—');

  assert.equal(runCountLabel(0), '尚无运行');
  assert.equal(runCountLabel(3), '3 次运行');
  assert.equal(runCountLabel('3'), '运行次数未知');
  assert.equal(runCountLabel(-1), '运行次数未知');
  assert.equal(runCountLabel(undefined), '运行次数未知');
});

test('a run carries the current session only when there is one', () => {
  const { runPayload } = require('./app.js');
  assert.deepEqual(runPayload('你好', ''), { message: '你好' });
  assert.equal('session_id' in runPayload('你好', ''), false, 'a new session sends no session_id at all');
  assert.deepEqual(runPayload('你好', '8f2a1c4d9e0b'), { message: '你好', session_id: '8f2a1c4d9e0b' });
  assert.equal('session_id' in runPayload('你好', 'not a session id'), false, 'a malformed id never reaches the server');
  assert.equal(runPayload('你好', undefined).message, '你好');
});

test('the drawer carries a 会话 section and the replay area without widening the transcript contract', () => {
  const html = source('index.html');
  assert.match(html, /<section class="drawer-section" aria-labelledby="sessions-title">/);
  assert.match(html, /<h3 id="sessions-title">会话<\/h3>/);
  assert.match(html, /id="session-new"[^>]*>新建会话<\/button>/);
  assert.match(html, /<ul id="session-list" class="session-list"><\/ul>/, 'the list is filled from the server, not from markup');
  assert.match(html, /id="sessions-empty"[^>]*hidden[^>]*>还没有历史会话。/);
  assert.match(html, /id="session-notices" class="session-notices" hidden/);
  assert.match(html, /id="current-session"/);
  assert.match(html, /id="session-status"[^>]*role="status"/);

  // The transcript keeps its own contract: labelled, keyboard scrollable and
  // never aria-live, because a replay must not be announced record by record.
  assert.match(html, /id="transcript"[^>]*tabindex="0"[^>]*aria-label="对话记录"/);
  assert.equal(/id="transcript"[^>]*aria-live/.test(html), false, 'the replay area must not announce every record');
  assert.match(html, /id="runtime-drawer"[^>]*role="dialog"[^>]*aria-modal="true"[^>]*hidden/);
  assert.match(html, /id="runtime-toggle"[^>]*aria-expanded="false"[^>]*aria-controls="runtime-drawer"/);

  assert.equal(/data-session-id/.test(html), false, 'no session is fabricated in markup');
  assert.equal(/\b(?:src|href)=["']https?:\/\//.test(html), false);
});

test('the front end keeps the session in the hash and reaches the DOM only through safe APIs', () => {
  const js = source('app.js');
  assert.equal(js.includes('localStorage'), false, 'the current session must not be kept in browser storage');
  assert.match(js, /location\.hash/);
  assert.match(js, /addEventListener\('hashchange'/);
  assert.match(js, /history\.replaceState/);
  assert.match(js, /runPayload\(message, currentSessionID\)/, 'a message carries the current session id');
  assert.match(js, /adoptSession\(data\.session_id\)/, 'a new session id arrives on run.started');
  assert.match(js, /\/api\/sessions\/\$\{id\}/);
  assert.match(js, /fetch\('\/api\/sessions'/);
  assert.match(js, /replaySession\(detail\)/);
  assert.match(js, /emptyState\.hidden = sessionNotices\.childElementCount > 0 \|\| replay\.turns\.length > 0/);
  assert.match(js, /sessionRowNode/);
  assert.match(js, /assistantReplayNode/);
  assert.match(js, /valueOrDash\(state\.current_session_id\)/);
  assert.equal(js.includes('innerHTML'), false);
  assert.equal(/\beval\s*\(/.test(js), false);
  assert.equal(js.includes('new Function'), false);
});

test('every session style the script builds a class for exists in the stylesheet', () => {
  const css = source('style.css');
  const selectors = ['.session-new', '.session-list', '.session-row', '.session-row.is-current', '.session-title', '.session-meta', '.session-current', '.session-empty', '.session-status', '.session-notice', '.turn-note', '.assistant-body.failed'];
  for (const selector of selectors) {
    assert.ok(
      [`${selector} {`, `${selector}:`, `${selector},`, `${selector}.`].some((form) => css.includes(form)),
      `missing style ${selector}`
    );
  }
  // The new rules are inside the same budget the existing stylesheet test
  // measures: no shadow, no gradient, and only the four allowed radii.
  const added = css.slice(css.indexOf('/* Session list, replay notices'));
  assert.equal(/box-shadow\s*:/i.test(added), false);
  assert.equal(/gradient\s*\(/i.test(added), false);
  for (const match of added.matchAll(/border-radius:\s*([^;}]+)/gi)) {
    for (const radius of match[1].trim().split(/\s+/)) {
      assert.ok(['0', '4px', '8px', '12px'].includes(radius), `unsupported radius ${radius}`);
    }
  }
});
