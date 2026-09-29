import {test} from 'node:test';
import assert from 'node:assert/strict';

function node(tag){
 const n={tagName:tag,children:[],listeners:new Map(),attributes:new Map(),value:'',checked:false,disabled:false,hidden:false,textContent:'',parentNode:null};
 n.append=(...items)=>{for(const x of items){x.parentNode=n;n.children.push(x)}};
 n.replaceChildren=(...items)=>{for(const x of n.children)x.parentNode=null;n.children=[];n.append(...items)};
 n.remove=()=>{if(n.parentNode)n.parentNode.children=n.parentNode.children.filter(x=>x!==n);n.parentNode=null};
 n.setAttribute=(k,v)=>n.attributes.set(k,String(v));n.getAttribute=k=>n.attributes.get(k)??null;
 n.addEventListener=(k,fn)=>{if(!n.listeners.has(k))n.listeners.set(k,[]);n.listeners.get(k).push(fn)};
 n.emit=(k)=>{for(const fn of n.listeners.get(k)||[])fn({preventDefault(){}})};
 n.focus=()=>{};return n;
}
function find(root,predicate){if(predicate(root))return root;for(const c of root.children){const got=find(c,predicate);if(got)return got;}return null;}
const field=(root,id)=>find(root,n=>n.getAttribute?.('data-field')===id);
const action=(root,id)=>find(root,n=>n.getAttribute?.('data-action')===id);
const flush=async()=>{for(let i=0;i<8;i++)await new Promise(resolve=>setImmediate(resolve));};
const preset=(overrides={})=>({owner:'presets',id:'mine',title:'个人工作',revision:'r1',instructions:'旧方法',capabilities:null,tools:null,resources:{skills:[]},builtin:false,archived:false,...overrides});
async function harness(fn,{failSave=false,holdList=false}={}){
 const original={document:globalThis.document,fetch:globalThis.fetch};let release;const calls=[];const entries=[preset({id:'general',title:'通用助手',builtin:true}),preset()];
 globalThis.document={head:node('head'),createElement:node};
 globalThis.fetch=async(url,options={})=>{
  calls.push({url,options});let data={};let ok=true;
  if(url.startsWith('/api/presets?')){if(holdList)await new Promise(resolve=>release=resolve);data={presets:entries};}
  else if(url==='/api/state')data={capabilities:[{id:'skills',title:'技能',state:'enabled'},{id:'web',title:'联网',state:'registered'}]};
  else if(url==='/api/skills')data={skills:[{name:'alpha',description:'示例方法',enabled:true}]};
  else if(url==='/api/models')data={models:[{name:'one'},{name:'two'}]};
  else if(url==='/api/reasoning')data={levels:['none','high','xhigh','max']};
  else if(url==='/api/presets/save'){if(failSave){ok=false;data={error:'revision conflict'}}else{const body=JSON.parse(options.body);data={...body.definition,revision:'r2',builtin:false};entries[1]=data;}}
  else if(url.startsWith('/api/presets/history'))data={revisions:[preset({revision:'r0',instructions:'最早方法'}),preset()]};
  else if(url==='/api/presets/restore')data=preset({revision:'r2'});
  return {ok,status:ok?200:409,json:async()=>data};
 };
 const module=await import('./panel.js');const target=node('div');module.mount(target,{});
 try{await flush();await fn({target,module,calls,release:()=>release?.(),entries});}finally{module.unmount(target);globalThis.document=original.document;globalThis.fetch=original.fetch;}
}

test('预设面板编辑真实表单并提交当前修订，能力选择不授予权限',async()=>{
 await harness(async({target,calls})=>{
  action(target,'edit:mine').emit('click');field(target,'instructions').value='新方法';field(target,'inherit-capabilities').checked=false;field(target,'capability:web').checked=true;
  field(target,'reasoning').value='max';action(target,'editor').emit('submit');await flush();
  const saved=calls.find(x=>x.url==='/api/presets/save');assert.ok(saved);const body=JSON.parse(saved.options.body);
  assert.equal(body.expected_revision,'r1');assert.equal(body.definition.instructions,'新方法');assert.deepEqual(body.definition.capabilities,['web']);assert.deepEqual(body.definition.resources.skills,[]);assert.equal(body.definition.reasoning_effort,'max');assert.equal('permissions' in body.definition,false);
 });
});

test('内置预设只可复制，保存冲突保留正在编辑的内容',async()=>{
 await harness(async({target,calls})=>{
  assert.equal(action(target,'edit:general'),null);action(target,'copy:general').emit('click');assert.equal(field(target,'id').disabled,false);assert.notEqual(field(target,'id').value,'general');
  action(target,'edit:mine').emit('click');field(target,'instructions').value='不能丢弃的修改';action(target,'editor').emit('submit');await flush();
  assert.equal(field(target,'instructions').value,'不能丢弃的修改');assert.equal(action(target,'editor').hidden,false);assert.match(field(target,'status').textContent,/revision conflict/);
  assert.equal(calls.filter(x=>x.url==='/api/presets/save').length,1);
 },{failSave:true});
});

test('预设历史恢复提交旧内容版本和当前父版本',async()=>{
 await harness(async({target,calls})=>{
  action(target,'history:mine').emit('click');await flush();action(target,'restore:r0').emit('click');await flush();
  const saved=calls.find(x=>x.url==='/api/presets/restore');assert.deepEqual(JSON.parse(saved.options.body),{id:'mine',revision:'r0',expected_revision:'r1'});
 });
});

test('预设面板卸载取消请求，迟到结果不复活界面',async()=>{
 await harness(async({target,module,calls,release})=>{
  const pending=calls.find(x=>x.url.startsWith('/api/presets?'));module.unmount(target);assert.equal(pending.options.signal.aborted,true);release();await flush();assert.equal(action(target,'edit:mine'),null);assert.equal(globalThis.document.head.children.length,0);
 },{holdList:true});
});


test('预设样式只引用宿主定义的主题变量，不包含任意 HTML 注入',async()=>{
 const fs=await import('node:fs/promises');const css=await fs.readFile(new URL('./panel.css',import.meta.url),'utf8');const host=await fs.readFile(new URL('../../../web/style.css',import.meta.url),'utf8');
 for(const match of css.matchAll(/var\((--luna-[a-z0-9-]+)/g))assert.ok(host.includes(match[1]+':'),'宿主缺少 '+match[1]);
 const source=await fs.readFile(new URL('./panel.js',import.meta.url),'utf8');assert.equal(source.includes('innerHTML'),false);
});
