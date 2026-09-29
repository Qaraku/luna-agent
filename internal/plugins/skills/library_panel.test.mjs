import {test} from 'node:test';
import assert from 'node:assert/strict';
function node(tag){const n={tagName:tag,children:[],listeners:new Map(),attributes:new Map(),value:'',disabled:false,hidden:false,textContent:'',parentNode:null};n.append=(...xs)=>{for(const x of xs){x.parentNode=n;n.children.push(x)}};n.replaceChildren=(...xs)=>{n.children=[];n.append(...xs)};n.remove=()=>{if(n.parentNode)n.parentNode.children=n.parentNode.children.filter(x=>x!==n)};n.setAttribute=(k,v)=>n.attributes.set(k,String(v));n.getAttribute=k=>n.attributes.get(k)??null;n.addEventListener=(k,fn)=>{if(!n.listeners.has(k))n.listeners.set(k,[]);n.listeners.get(k).push(fn)};n.emit=k=>{for(const fn of n.listeners.get(k)||[])fn({preventDefault(){}})};n.focus=()=>{};return n}
function find(n,key,value){if(n.getAttribute?.('data-'+key)===value)return n;for(const x of n.children){const hit=find(x,key,value);if(hit)return hit;}return null}
const field=(n,k)=>find(n,'field',k),action=(n,k)=>find(n,'action',k);
const flush=async()=>{for(let i=0;i<8;i++)await new Promise(resolve=>setImmediate(resolve))};
async function harness(fn,{conflict=false,hold=false}={}){
 const prior={document:globalThis.document,fetch:globalThis.fetch};const calls=[];let release;
 const entry={name:'learned',description:'Workflow',revision:'r1',origin:{kind:'model',session_id:'aaaaaaaa'},at:'2026-09-30T00:00:00Z'};let definition={name:'learned',description:'Workflow',body:'old method',files:{'references/note.md':'reference'}};
 globalThis.document={head:node('head'),createElement:node};globalThis.fetch=async(url,options={})=>{calls.push({url,options});let payload={};let ok=true;
 if(url==='/api/skill-library'){if(hold)await new Promise(resolve=>release=resolve);payload={entries:[entry]};}
 else if(url==='/api/skills')payload={skills:[{name:'learned',enabled:true,managed:true,revision:entry.revision}]};
 else if(url.startsWith('/api/skill-library/detail'))payload={entry,definition};
 else if(url==='/api/skill-library/preview')payload={diff:'-old method\n+new method',truncated:false};
 else if(url==='/api/skill-library/save'){if(conflict){ok=false;payload={error:'revision conflict'}}else{const body=JSON.parse(options.body);definition=body.definition;entry.revision='r2';payload=entry;}}
 else if(url.startsWith('/api/skill-library/history'))payload={revisions:[{...entry,revision:'r0'},entry]};
 else if(url==='/api/skill-library/restore')payload={...entry,revision:'r2'};
 return {ok,status:ok?200:409,json:async()=>payload};};
 const module=await import('./library_panel.js');const target=node('div');module.mount(target,{});
 try{await flush();await fn({target,module,calls,release:()=>release?.()});}finally{module.unmount(target);globalThis.document=prior.document;globalThis.fetch=prior.fetch;}
}
test('技能库编辑先预览再保存，并保留来源与参考文件',async()=>{await harness(async({target,calls})=>{
 assert.match(field(target,'origin:learned').textContent,/模型.*aaaaaaaa/);action(target,'edit:learned').emit('click');await flush();field(target,'body').value='new method';action(target,'editor').emit('submit');await flush();assert.equal(calls.some(x=>x.url==='/api/skill-library/save'),false);action(target,'confirm').emit('click');await flush();const saved=calls.find(x=>x.url==='/api/skill-library/save');const body=JSON.parse(saved.options.body);assert.equal(body.definition.body,'new method');assert.equal(body.expected_revision,'r1');assert.equal(body.definition.files['references/note.md'],'reference');assert.equal('origin' in body,false);
})});
test('预览后继续编辑必须重新预览，保存失败不丢弃草稿',async()=>{await harness(async({target,calls})=>{
 action(target,'edit:learned').emit('click');await flush();field(target,'body').value='new method';action(target,'editor').emit('submit');await flush();field(target,'body').value='changed after preview';action(target,'confirm').emit('click');await flush();assert.equal(calls.some(x=>x.url==='/api/skill-library/save'),false);assert.match(field(target,'status').textContent,/重新预览/);
 action(target,'editor').emit('submit');await flush();action(target,'confirm').emit('click');await flush();assert.equal(field(target,'body').value,'changed after preview');assert.equal(action(target,'editor').hidden,false);assert.match(field(target,'status').textContent,/revision conflict/);
},{conflict:true})});
test('恢复技能历史也先展示差异，再提交明确版本',async()=>{await harness(async({target,calls})=>{
 action(target,'history:learned').emit('click');await flush();action(target,'restore:r0').emit('click');await flush();assert.equal(calls.some(x=>x.url==='/api/skill-library/restore'),false);action(target,'confirm').emit('click');await flush();const body=JSON.parse(calls.find(x=>x.url==='/api/skill-library/restore').options.body);assert.equal(body.revision,'r0');assert.equal(body.expected_revision,'r1');
})});
test('关闭技能库取消请求，不渲染迟到结果',async()=>{await harness(async({target,module,calls,release})=>{
 module.unmount(target);assert.equal(calls[0].options.signal.aborted,true);release();await flush();assert.equal(action(target,'edit:learned'),null);assert.equal(globalThis.document.head.children.length,0);
},{hold:true})});

test('技能库面板只使用文本 DOM 与宿主主题变量',async()=>{
 const fs=await import('node:fs/promises');const code=await fs.readFile(new URL('./library_panel.js',import.meta.url),'utf8');assert.equal(code.includes('innerHTML'),false);
 const css=await fs.readFile(new URL('./library_panel.css',import.meta.url),'utf8');const host=await fs.readFile(new URL('../../../web/style.css',import.meta.url),'utf8');for(const match of css.matchAll(/var\((--luna-[a-z0-9-]+)/g))assert.ok(host.includes(match[1]+':'),match[1]);
});
