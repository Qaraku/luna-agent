// The smallest UI plugin: one line of text, no state.
//
// The host contract is mount(target, api) and unmount(target). This plugin only
// needs target, so it assumes nothing about api: that shape belongs to the host.
// Text goes in through textContent: nothing is parsed as markup and nothing is
// evaluated as code.

const CLASS_NAME = "ui-plugin-hello";

export function mount(target, api) {
  const line = document.createElement("p");
  line.className = CLASS_NAME;
  line.textContent = "hello from a UI plugin";
  target.appendChild(line);
}

export function unmount(target) {
  const line = target.querySelector("." + CLASS_NAME);
  if (line !== null) {
    line.remove();
  }
}
