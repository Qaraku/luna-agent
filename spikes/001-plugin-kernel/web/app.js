(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const display = value => value === undefined || value === null ? '—' : String(value);
  const node = (tag, text, className) => {
    const element = document.createElement(tag);
    if (text !== undefined) element.textContent = display(text);
    if (className) element.className = className;
    return element;
  };
  let lastEvents = '';
  let requestEpoch = 0;

  async function request(url, body) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), body ? 90000 : 6000);
    try {
      const response = await fetch(url, {
        method: body ? 'POST' : 'GET', cache: 'no-store', signal: controller.signal,
        ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {})
      });
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
      return data;
    } catch (error) {
      if (error.name === 'AbortError') throw new Error('等待响应超时；服务端操作可能仍在继续，请查看状态。');
      throw error;
    } finally { clearTimeout(timer); }
  }

  function renderState(state) {
    $('host-pid').textContent = display(state.host_pid);
    $('plugin-pid').textContent = display(state.active?.plugin_pid);
    $('active-version').textContent = display(state.active?.version);
    $('generation').textContent = state.active ? `g${state.active.generation}` : '—';
    $('active-candidate').textContent = display(state.active?.candidate);
    const retiring = (state.plugins || []).filter(plugin => plugin.status === 'retiring');
    $('retiring').replaceChildren(...(retiring.length ? retiring.map(plugin => {
      const row = node('li');
      row.append(node('code', `g${plugin.generation} · ${plugin.version} · PID ${plugin.plugin_pid}`), node('span', `${plugin.inflight} 个调用进行中`, 'muted'));
      return row;
    }) : [node('li', '暂无待退出的旧代。', 'muted')]));
    const events = (state.events || []).slice().sort((a, b) => Date.parse(a.time) - Date.parse(b.time)).slice(-80);
    const signature = JSON.stringify(events);
    if (signature !== lastEvents) {
      lastEvents = signature;
      $('events').replaceChildren(...(events.length ? events.map(event => {
        const row = node('li', undefined, 'event-row');
        const date = new Date(event.time);
        const time = node('time', Number.isNaN(date.getTime()) ? event.time : date.toLocaleTimeString('zh-CN', { hour12: false }));
        time.dateTime = event.time;
        row.append(time, node('code', event.type), node('span', event.message));
        return row;
      }) : [node('li', '后端尚未返回生命周期事件。', 'empty')]));
    }
  }

  async function refresh() {
    const epoch = ++requestEpoch;
    try {
      const state = await request('/api/state');
      if (epoch !== requestEpoch) return;
      renderState(state);
      $('status').textContent = '已连接 · 实时状态';
      $('status').dataset.state = 'online';
      $('connection-error').hidden = true;
    } catch (error) {
      if (epoch !== requestEpoch) return;
      $('status').textContent = '连接异常 · 状态可能过期';
      $('status').dataset.state = 'offline';
      $('connection-error').textContent = `无法刷新状态：${error.message} 正在自动重试。`;
      $('connection-error').hidden = false;
    }
  }
  async function poll() {
    await refresh();
    setTimeout(poll, 750);
  }
  let invokeBusy = false;
  let reloadBusy = false;
  let outputStarted = false;
  $('invoke-form').addEventListener('submit', async event => {
    event.preventDefault();
    if (invokeBusy) return;
    const text = $('tool-input').value;
    if (!text.trim()) { $('invoke-feedback').textContent = '请先输入要处理的文本。'; return; }
    invokeBusy = true;
    $('invoke-btn').disabled = true;
    $('invoke-btn').textContent = '调用中…';
    $('invoke-feedback').textContent = '正在等待插件返回；此时仍可切换或重载插件。';
    if (!outputStarted) { $('output').replaceChildren(); outputStarted = true; }
    const entry = node('li', undefined, 'result-entry');
    const meta = node('div', '调用中 · 使用代次以返回结果为准', 'result-meta');
    const result = node('pre', '等待后端结果…');
    entry.append(meta, result);
    $('output').prepend(entry);
    while ($('output').childElementCount > 20) $('output').lastElementChild.remove();
    try {
      const data = await request('/api/invoke', { text, delay_ms: Number($('delay-ms').value) });
      meta.textContent = `g${data.generation} · ${data.version} · PID ${data.plugin_pid}`;
      result.textContent = display(data.result);
      $('invoke-feedback').textContent = '调用完成。结果标注的是本次调用实际使用的插件代次。';
    } catch (error) {
      meta.textContent = '调用未取得成功响应';
      result.textContent = error.message;
      entry.className = 'result-entry failed';
      $('invoke-feedback').textContent = '调用异常；未收到结果不代表服务端任务已取消。';
    } finally {
      invokeBusy = false;
      $('invoke-btn').disabled = false;
      $('invoke-btn').textContent = '调用工具';
      void refresh();
    }
  });
  $('reload-form').addEventListener('submit', async event => {
    event.preventDefault();
    if (reloadBusy) return;
    reloadBusy = true;
    const candidate = $('candidate').value;
    $('reload-btn').disabled = true;
    $('candidate').disabled = true;
    $('reload-btn').textContent = '编译并加载中…';
    $('error').hidden = true;
    $('reload-feedback').textContent = '等待新插件就绪；工具调用仍然可用。';
    try {
      await request('/api/reload', { candidate });
      $('reload-feedback').textContent = '加载成功，正在读取当前插件状态。';
    } catch (error) {
      $('error').textContent = `重载未成功：${error.message} 新候选失败时不会替换原插件；当前代次请以刷新后的状态为准。`;
      $('error').hidden = false;
      $('reload-feedback').textContent = '可继续调用工具，或选择其他候选重试。';
    } finally {
      reloadBusy = false;
      $('reload-btn').disabled = false;
      $('candidate').disabled = false;
      $('reload-btn').textContent = '重新编译并加载';
      void refresh();
    }
  });
  poll();
})();
