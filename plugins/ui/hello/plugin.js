// 最小的界面插件：一行文字，没有状态。
//
// 宿主契约是 mount(target, api) 与 unmount(target)。本示例只用 target，
// 因此不假设 api 的形状——那是宿主自己的约定。文字通过 textContent 写入：
// 不解析标记，也不执行代码。样式沿用宿主公开的公共类，插件不必自带配色。

const CLASS_NAME = "ui-plugin-hello";

export function mount(target, api) {
  const line = document.createElement("p");
  line.className = CLASS_NAME + " luna-muted";
  line.textContent = "界面插件在浏览器里运行，只从宿主拿到契约版本和一个日志回调。";
  target.appendChild(line);
}

export function unmount(target) {
  const line = target.querySelector("." + CLASS_NAME);
  if (line !== null) {
    line.remove();
  }
}
