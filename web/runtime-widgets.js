// 通用运行时组件宿主：只持久化展示偏好，不保存会话数据或执行权限。
(function (root, factory) {
  const api = factory();
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.LunaRuntimeWidgets = api;
})(globalThis, function () {
  'use strict';
  const STORAGE_KEY = 'luna.widgets.v1';
  const idOK = value => typeof value === 'string' && /^[a-z][a-z0-9_.:-]{0,191}$/.test(value);
  const bounded = (value, fallback, min, max) => typeof value === 'number' && Number.isFinite(value) ? Math.max(min, Math.min(max, value)) : fallback;

  function normalizeLayout(value = {}) {
    value = value && typeof value === 'object' ? value : {};
    return {
      visible: value.visible === true,
      placement: ['left', 'right', 'bottom', 'float'].includes(value.placement) ? value.placement : 'right',
      x: bounded(value.x, .8, 0, 1), y: bounded(value.y, .2, 0, 1),
      width: bounded(value.width, 300, 240, 420),
      userHidden: value.userHidden === true, userMoved: value.userMoved === true,
      userVisibility: Object.prototype.hasOwnProperty.call(value, 'userVisibility') ? value.userVisibility === true : value.userHidden === true || value.visible === true
    };
  }

  function createRegistry({ storage, onError = () => {} } = {}) {
    const records = new Map(), listeners = new Set();
    const preferences = Object.create(null);
    const report = error => { try { onError(error); } catch (_) {} };
    try {
      const saved = JSON.parse(storage?.getItem(STORAGE_KEY) || '{}');
      if (saved && !Array.isArray(saved) && typeof saved === 'object') {
        for (const [id, layout] of Object.entries(saved).slice(0, 128)) {
          if (idOK(id)) preferences[id] = normalizeLayout(layout);
        }
      }
    } catch (_) { /* 损坏或不可用的浏览器偏好不能阻断对话。 */ }
    const need = id => { const record = records.get(id); if (!record) throw Error('未注册的运行组件：' + id); return record; };
    const view = record => ({ ...record, layout: { ...record.layout } });
    function changed(id, kind, save = false) {
      if (save) {
        delete preferences[id];
        preferences[id] = { ...need(id).layout };
        for (const old of Object.keys(preferences).slice(0, Math.max(0, Object.keys(preferences).length - 128))) delete preferences[old];
        try { storage?.setItem(STORAGE_KEY, JSON.stringify(preferences)); } catch (_) {}
      }
      for (const listener of listeners) { try { listener(id, kind); } catch (error) { report(error); } }
    }
    return {
      register(definition) {
        if (!definition || !idOK(definition.id) || typeof definition.title !== 'string' || !definition.title.trim() || definition.title.length > 80) throw Error('运行组件声明无效');
        const { id } = definition;
        if (records.has(id)) throw Error('运行组件标识冲突：' + id);
        if (records.size >= 32) throw Error('最多同时注册 32 个运行组件');
        records.set(id, { id, title: definition.title, definition: { ...definition }, layout: normalizeLayout(preferences[id]), data: undefined });
        changed(id, 'register');
        // 旧注册返回的清理函数不能误删后来使用同一 id 的新实例。
        const owned = records.get(id);
        return () => { if (records.get(id) === owned) { records.delete(id); changed(id, 'remove'); } };
      },
      get(id) { return records.has(id) ? view(records.get(id)) : undefined; },
      list() { return [...records.values()].map(view); },
      subscribe(listener) { listeners.add(listener); return () => listeners.delete(listener); },
      update(id, data) { need(id).data = data; changed(id, 'data'); },
      resetData() { for (const record of records.values()) { record.data = undefined; changed(record.id, 'data'); } },
      show(id, source = 'user') {
        const record = need(id);
        if (source !== 'user' && record.layout.userVisibility) return false;
        record.layout.visible = true;
        if (source === 'user') {record.layout.userHidden = false;record.layout.userVisibility = true;}
        changed(id, 'layout', source === 'user');
        return true;
      },
      hide(id, source = 'user') {
        const record = need(id);
        if (source !== 'user' && record.layout.userVisibility) return false;
        record.layout.visible = false;
        if (source === 'user') {record.layout.userHidden = true;record.layout.userVisibility = true;}
        changed(id, 'layout', source === 'user');
        return true;
      },
      move(id, position, source = 'user') {
        const record = need(id);
        if (source !== 'user' && record.layout.userMoved) return false;
        const next = normalizeLayout({ ...record.layout, ...position });
        // 位置请求不能顺带恢复显隐或清除用户控制标记。
        for (const key of ['placement', 'x', 'y', 'width']) record.layout[key] = next[key];
        if (source === 'user') record.layout.userMoved = true;
        changed(id, 'layout', source === 'user');
        return true;
      }
    };
  }

  function createHost({ document, window, root, menu, storage, getBounds, onError = () => {} }) {
    const registry = createRegistry({ storage, onError }), nodes = new Map(), zones = new Map();
    let drag = null;
    const observer=typeof window.ResizeObserver==='function'?new window.ResizeObserver(()=>resize()):null;
    function element(tag, className, text) {
      const node = document.createElement(tag);
      if (className) node.className = className;
      if (text !== undefined) node.textContent = text;
      return node;
    }
    for (const place of ['left', 'right', 'bottom', 'float']) {
      const zone = element('div', 'runtime-zone runtime-zone-' + place);
      root.append(zone); zones.set(place, zone);
    }
    function report(error) { try { onError(error); } catch (_) {} }
    function viewport() {
      const available = getBounds?.();
      return { width: available?.width || window.innerWidth || 1024, height: available?.height || window.innerHeight || 768 };
    }
    function position(record, item) {
      const layout = record.layout;
      item.node.dataset.placement = layout.placement;
      item.node.style.width = layout.width + 'px';
      item.node.style.maxHeight = Math.max(40, Math.min(420, viewport().height - 24)) + 'px';
      const zone = zones.get(layout.placement);
      const available=viewport();
      const bottom=Math.max(12,(window.innerHeight||768)-available.height+12);
      for(const [name,lane] of zones){if(name!=='float'){lane.style.bottom=bottom+'px';lane.style.maxHeight=Math.max(40,available.height-82)+'px';}}
      if (item.node.parentElement !== zone) zone.append(item.node);
      if (layout.placement === 'float') {
        const bounds = viewport(), rect = item.node.getBoundingClientRect();
        const width = Math.min(bounds.width - 24, rect.width || layout.width);
        const height = Math.min(bounds.height - 24, rect.height || 160);
        item.node.style.left = 12 + layout.x * Math.max(0, bounds.width - width - 24) + 'px';
        item.node.style.top = 12 + layout.y * Math.max(0, bounds.height - height - 24) + 'px';
      } else {
        item.node.style.removeProperty('left'); item.node.style.removeProperty('top');
      }
      item.placement.value = layout.placement;
      item.width.value = String(layout.width);
    }
    function remove(id) {
      const item = nodes.get(id);
      if (!item) return;
      if (drag?.id === id) drag = null;
      try { item.instance?.unmount?.(); } catch (error) { report(error); }
      finally { observer?.unobserve(item.node);item.node.remove(); item.toggle.remove(); nodes.delete(id); }
    }
    function mount(record) {
      const node = element('section', 'runtime-widget');
      node.id = 'widget-' + record.id;
      node.setAttribute('aria-label', record.title);
      const header = element('header', 'runtime-widget-header');
      const handle = element('button', 'runtime-widget-handle', record.title);
      handle.type = 'button';
      handle.setAttribute('aria-label', '移动' + record.title + '；也可用方向键调整位置');
      const hide = element('button', 'runtime-widget-hide', '×');
      hide.id = 'widget-hide-' + record.id; hide.type = 'button';
      hide.setAttribute('aria-label', '隐藏' + record.title);
      const settings = element('details', 'runtime-widget-options');
      const summary = element('summary', '', '⋯'); summary.setAttribute('aria-label', record.title + '的布局');
      const placement = element('select'); placement.setAttribute('aria-label', record.title + '的位置');
      for (const [value, title] of [['right', '右侧停靠'], ['left', '左侧停靠'], ['bottom', '底部停靠'], ['float', '自由放置']]) {
        const option = element('option', '', title); option.value = value; placement.append(option);
      }
      const width = element('select'); width.setAttribute('aria-label', record.title + '的宽度');
      for (const size of [240, 300, 360, 420]) { const option = element('option', '', size + ' px'); option.value = String(size); width.append(option); }
      settings.append(summary, placement, width);
      header.append(handle, settings, hide);
      const body = element('div', 'runtime-widget-body');
      node.append(header, body);
      const toggle = element('button', 'composer-menu-action');
      toggle.id = 'widget-toggle-' + record.id; toggle.type = 'button'; menu.append(toggle);
      const item = { node, body, toggle, placement, width, instance: null };
      nodes.set(record.id, item);
      observer?.observe(node);
      hide.addEventListener('click', () => { registry.hide(record.id); document.getElementById('composer-add')?.focus(); });
      toggle.addEventListener('click', () => registry.get(record.id).layout.visible ? registry.hide(record.id) : registry.show(record.id));
      placement.addEventListener('change', () => registry.move(record.id, { placement: placement.value }));
      width.addEventListener('change', () => registry.move(record.id, { width: Number(width.value) }));
      handle.addEventListener('keydown', event => {
        const deltas = { ArrowLeft: [-.05, 0], ArrowRight: [.05, 0], ArrowUp: [0, -.05], ArrowDown: [0, .05] };
        if (!deltas[event.key]) return;
        event.preventDefault();
        const layout = registry.get(record.id).layout, [dx, dy] = deltas[event.key];
        registry.move(record.id, { placement: 'float', x: layout.x + dx, y: layout.y + dy });
      });
      handle.addEventListener('pointerdown', event => {
        if (event.button !== undefined && event.button !== 0) return;
        const rect = node.getBoundingClientRect();
        drag = { id: record.id, pointerID: event.pointerId, offsetX: event.clientX - rect.left, offsetY: event.clientY - rect.top };
        handle.setPointerCapture?.(event.pointerId);
        event.preventDefault();
      });
      zones.get(record.layout.placement).append(node);
      try { item.instance = record.definition.mount?.(body) || null; }
      catch (error) { body.replaceChildren(element('p', '', '组件无法加载')); report(error); }
      return item;
    }
    function sync(id, kind) {
      if (kind === 'remove') { remove(id); return; }
      const record = registry.get(id);
      if (!record) return;
      const item = nodes.get(id) || mount(record);
      item.node.hidden = !record.layout.visible;
      item.toggle.textContent = (record.layout.visible ? '隐藏' : '显示') + record.title;
      item.toggle.setAttribute('aria-pressed', String(record.layout.visible));
      if (kind === 'register' || kind === 'data') {
        try { item.instance?.update?.(record.data); } catch (error) { report(error); }
      }
      position(record, item);
    }
    const unsubscribe = registry.subscribe(sync);
    function move(event) {
      if (!drag || drag.pointerID !== event.pointerId || !nodes.has(drag.id)) return;
      const item = nodes.get(drag.id), rect = item.node.getBoundingClientRect(), bounds = viewport();
      registry.move(drag.id, {
        placement: 'float',
        x: (event.clientX - drag.offsetX - 12) / Math.max(1, bounds.width - (rect.width || 300) - 24),
        y: (event.clientY - drag.offsetY - 12) / Math.max(1, bounds.height - (rect.height || 160) - 24)
      });
    }
    function finish(event) { if (drag?.pointerID === event.pointerId) drag = null; }
    function resize() { for (const record of registry.list()) position(record, nodes.get(record.id)); }
    document.addEventListener('pointermove', move);
    document.addEventListener('pointerup', finish);
    document.addEventListener('pointercancel', finish);
    window.addEventListener('resize', resize);
    return {
      ...registry,
      reflow: resize,
      destroy() {
        unsubscribe(); drag = null;observer?.disconnect();
        for (const id of [...nodes.keys()]) remove(id);
        document.removeEventListener('pointermove', move);
        document.removeEventListener('pointerup', finish);
        document.removeEventListener('pointercancel', finish);
        window.removeEventListener('resize', resize);
        root.replaceChildren();
      }
    };
  }
  return { createRegistry, createHost, normalizeLayout };
});
