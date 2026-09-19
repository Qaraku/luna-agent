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

test('tool activity and reload copy are friendly while values remain exact', () => {
  const { toolActivityLabel, candidateLabel, reloadCopy } = require('./app.js');
  assert.equal(toolActivityLabel('running'), '正在转换文本…');
  assert.equal(toolActivityLabel('finished'), '文本转换完成');
  assert.equal(toolActivityLabel('failed'), '文本转换失败');
  assert.equal(candidateLabel('v1'), '稳定版本 v1');
  assert.equal(candidateLabel('v2'), '候选版本 v2');
  assert.equal(candidateLabel('broken'), '故障演练 broken');
  assert.deepEqual(reloadCopy('pending', 'v2'), { summary: '正在验证 v2…', technical: '' });
  assert.deepEqual(reloadCopy('success', 'v2'), { summary: 'v2 已启用。', technical: '' });
  assert.deepEqual(reloadCopy('failure', 'broken', 'handshake timeout'), {
    summary: '无法启用 broken，当前版本保持不变。', technical: 'handshake timeout'
  });
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

  for (const id of ['model', 'provider', 'host-pid', 'busy', 'plugin-version', 'plugin-generation', 'plugin-pid', 'candidate', 'reload', 'reload-status', 'events']) {
    assert.match(html, new RegExp(`id="${id}"`));
  }
  for (const value of ['v1', 'v2', 'broken']) {
    assert.match(html, new RegExp(`<option value="${value}"`));
  }
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
});
