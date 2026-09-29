const instances = new WeakMap();
export function mount(target, api) {
  unmount(target);
  const doc=target.ownerDocument;
  const style=doc.createElement('link');style.rel='stylesheet';style.href='/api/runtime-widgets/widget.css';
  const body=doc.createElement('section');body.className='runtime-generated';
  target.append(style,body);instances.set(target,{body,doc});update(target,api.data);
}
export function update(target,data) {
  const state=instances.get(target);if(!state)return;
  const {body,doc}=state;body.replaceChildren();
  const make=(tag,text)=>{const node=doc.createElement(tag);if(text!==undefined)node.textContent=String(text);return node;};
  const origin=make('p','模型提供 · 非系统计量');origin.className='luna-muted';body.append(origin);
  if(!data)return;
  if(typeof data.text==='string')body.append(make('p',data.text));
  if(data.kind==='progress'){
    const progress=make('progress');progress.max=100;
    if(typeof data.progress==='number'&&Number.isFinite(data.progress)){progress.value=data.progress;progress.setAttribute('aria-label','模型报告进度 '+data.progress+'%');body.append(make('p',data.progress+'%'));}
    else progress.setAttribute('aria-label','进度尚未报告');
    body.append(progress);
  }
  if(data.kind==='metrics'&&Array.isArray(data.metrics)){
    const list=make('dl');for(const row of data.metrics.slice(0,8))list.append(make('dt',row.label),make('dd',row.value));body.append(list);
  }
}
export function unmount(target){instances.delete(target);target.replaceChildren();}
