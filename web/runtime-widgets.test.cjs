const test = require('node:test');
const assert = require('node:assert/strict');
const { createRegistry, normalizeLayout } = require('./runtime-widgets.js');
const memory = () => { const values = new Map(); return { getItem: k => values.get(k), setItem: (k, v) => values.set(k, v) }; };

test('widget 注册是通用机制，拒绝重复标识并限制元数据', () => {
 const r = createRegistry();
 r.register({ id: 'usage', title: '用量' });
 r.register({ id: 'paint:progress', title: '绘画状态' });
 assert.equal(r.list().length, 2);
 assert.equal(r.get('usage').layout.visible, false);
 assert.throws(() => r.register({ id: 'usage', title: '重复' }));
 assert.throws(() => r.register({ id: '../bad', title: '坏标识' }));
 assert.throws(() => r.update('missing', {}));
});

test('布局与隐藏可恢复，运行数据不持久化，用户选择优先于模型建议', () => {
 const storage = memory();
 const r = createRegistry({ storage });
 r.register({ id: 'usage', title: '用量' });
 r.show('usage');
 r.move('usage', { placement: 'float', x: .3, y: .4, width: 340 });
 r.update('usage', { text: 'secret runtime content' });
 r.hide('usage');
 assert.equal(r.show('usage', 'model'), false);
 assert.equal(r.move('usage', { placement: 'left' }, 'model'), false);
 assert.doesNotMatch(storage.getItem('luna.widgets.v1'), /secret runtime content/);
 const restored = createRegistry({ storage });
 restored.register({ id: 'usage', title: '用量' });
 assert.equal(restored.get('usage').layout.visible, false);
 assert.equal(restored.get('usage').layout.placement, 'float');
 assert.equal(restored.get('usage').layout.x, .3);
 assert.equal(restored.get('usage').data, undefined);
 assert.equal(restored.show('usage'), true);
 restored.resetData();
 assert.equal(restored.get('usage').data, undefined);
});

test('widget 数据替换和撤除发出通知，不让一项监听器故障阻断其他组件', () => {
 const errors = [];
 const r = createRegistry({ onError: e => errors.push(e.message) });
 const events = [];
 r.subscribe(() => { throw Error('listener'); });
 r.subscribe((id, kind) => events.push([id, kind]));
 const dispose = r.register({ id: 'paint', title: '画图' });
 r.update('paint', { progress: 10 });
 r.update('paint', { progress: 20 });
 assert.equal(r.get('paint').data.progress, 20);
 dispose(); dispose();
 assert.equal(r.list().length, 0);
 assert.deepEqual(events.map(e => e[1]), ['register', 'data', 'data', 'remove']);
 assert.equal(errors.length, 4);
});

test('损坏偏好、存储不可用与越界布局均不阻断对话', () => {
 for (const value of ['{bad', '[]', 'null', '{"usage":{"x":-20,"y":1e100,"width":900,"placement":"unknown"}}']) {
  const r = createRegistry({ storage: { getItem: () => value, setItem: () => { throw Error('quota'); } } });
  r.register({ id: 'usage', title: '用量' });
  r.show('usage');
  assert.equal(r.get('usage').layout.placement, 'right');
 }
 const layout = normalizeLayout({ placement: 'float', x: -1, y: 20, width: Infinity });
 assert.equal(layout.x, 0);
 assert.equal(layout.y, 1);
 assert.equal(layout.width, 300);
});


test('模型不能覆盖用户明确的显示选择，位置变更也不改变可见性所有权',()=>{
 const registry=createRegistry();registry.register({id:'model:job',title:'任务'});
 registry.show('model:job','model');registry.hide('model:job');
 assert.equal(registry.show('model:job','model'),false);
 registry.show('model:job');assert.equal(registry.hide('model:job','model'),false);
 assert.equal(registry.get('model:job').layout.visible,true);
});
