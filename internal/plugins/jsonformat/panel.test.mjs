import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
const source = await readFile(new URL('./panel.js', import.meta.url), 'utf8');
const panel = await import('data:text/javascript;base64,' + Buffer.from(source).toString('base64'));
class Node {
 constructor(tag) { this.tag=tag; this.children=[]; this.events={}; this.attributes={}; this.textContent=''; this.value=''; }
 append(...nodes) { this.children.push(...nodes); }
 replaceChildren(...nodes) { this.children=[...nodes]; }
 setAttribute(name,value) { this.attributes[name]=value; }
 addEventListener(name,fn) { this.events[name]=fn; }
}
function find(root,match) { if(match(root))return root;for(const child of root.children){const got=find(child,match);if(got)return got;}return null; }
test('面板使用后端结果、保留大整数并展示真实执行元数据', async () => {
 const oldDocument=globalThis.document,oldFetch=globalThis.fetch;
 globalThis.document={ createElement:tag=>new Node(tag) };
 const calls=[];
 globalThis.fetch=async (path,options)=>{calls.push({path,options});return {ok:true,json:async()=>({text:'{"id":900719925474099312345}',version:'local',generation:7,plugin_pid:123})};};
 try {
  const target=new Node('div');panel.mount(target);
  find(target,n=>n.textContent==='填入示例').events.click();
  await find(target,n=>n.textContent==='处理 JSON').events.click();
  assert.equal(calls[0].path,'/api/json-format/format');
  assert.ok(JSON.parse(calls[0].options.body).text.includes('900719925474099312345'));
  assert.equal(find(target,n=>n.className==='json-format-output').value,'{"id":900719925474099312345}');
  assert.match(find(target,n=>n.className==='json-format-status').textContent,/代次 7.*PID 123/);
  panel.unmount(target);assert.equal(target.children.length,0);
 } finally {globalThis.document=oldDocument;globalThis.fetch=oldFetch;}
});
test('面板卸载取消请求，迟到响应不会重新写入容器', async () => {
 const oldDocument=globalThis.document,oldFetch=globalThis.fetch;
 globalThis.document={createElement:tag=>new Node(tag)};
 let resolve,signal;
 globalThis.fetch=(_path,options)=>{signal=options.signal;return new Promise(r=>{resolve=r;});};
 try {
  const target=new Node('div');panel.mount(target);
  const pending=find(target,n=>n.textContent==='处理 JSON').events.click();
  panel.unmount(target);assert.equal(signal.aborted,true);
  resolve({ok:true,json:async()=>({text:'late',version:'x',generation:1,plugin_pid:1})});
  await pending;assert.equal(target.children.length,0);
 } finally {globalThis.document=oldDocument;globalThis.fetch=oldFetch;}
});
