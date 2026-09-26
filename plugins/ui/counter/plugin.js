// 带状态的界面插件：一个计数按钮和一个计时器。
//
// 这个示例的重点是卸载：mount() 注册监听器并启动计时器，unmount() 把两者一起清理，
// 因此重复挂载同一容器不会留下第二个监听器或第二个定时器。
//
// 每次挂载的状态放在以容器元素为键的 WeakMap 里，同一个插件挂到两个容器时互不共享计数。
//
// 宿主契约是 mount(target, api) 与 unmount(target)；本示例只用 target，因此不假设 api 的形状。
// 文字一律通过 textContent 写入：不解析标记，也不执行代码。
// 外观只引用宿主公开的 --luna-* 语义变量和公共 .luna-* 控件类，不写死颜色，
// 所以明暗主题切换时这个插件跟着一起变，不需要重新挂载。

const mounts = new WeakMap();

export function mount(target, api) {
  if (mounts.has(target)) {
    unmount(target);
  }

  const stack = document.createElement("div");
  stack.className = "luna-stack";

  const row = document.createElement("div");
  row.className = "luna-row";

  const value = document.createElement("span");
  value.className = "ui-plugin-counter-value luna-metric";
  value.textContent = "0";

  const button = document.createElement("button");
  button.type = "button";
  button.className = "ui-plugin-counter-button luna-button";
  button.textContent = "点一下";

  row.append(value, button);

  const status = document.createElement("p");
  status.className = "luna-status";
  const dot = document.createElement("span");
  dot.className = "luna-status-dot";
  dot.setAttribute("aria-hidden", "true");
  const ticks = document.createElement("span");
  ticks.className = "ui-plugin-counter-ticks";
  ticks.textContent = "0 秒";
  status.append(dot, ticks);

  stack.append(row, status);

  const entry = { count: 0, seconds: 0, value, ticks, button, stack, timer: 0 };
  entry.onClick = function onClick() {
    entry.count += 1;
    value.textContent = String(entry.count);
  };
  button.addEventListener("click", entry.onClick);

  // 只有 unmount 能结束的副作用。
  entry.timer = window.setInterval(function onTick() {
    entry.seconds += 1;
    ticks.textContent = entry.seconds + " 秒";
  }, 1000);

  mounts.set(target, entry);
  target.append(stack);
}

export function unmount(target) {
  const entry = mounts.get(target);
  if (entry === undefined) {
    return;
  }
  mounts.delete(target);
  window.clearInterval(entry.timer);
  entry.button.removeEventListener("click", entry.onClick);
  entry.stack.remove();
}
