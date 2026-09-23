// A UI plugin with state: a counter button and a ticker.
//
// The point of this example is teardown. mount() adds an event listener and
// starts an interval; unmount() clears both and removes the elements, so
// mounting twice into the same container cannot leave a second listener or a
// second timer behind.
//
// Per-mount state lives in a WeakMap keyed by the target element, so the same
// plugin can be mounted into two containers without sharing one counter.
//
// The host contract is mount(target, api) and unmount(target); this plugin only
// needs target, so it assumes nothing about api. Text goes in through
// textContent: nothing is parsed as markup and nothing is evaluated as code.

const mounts = new WeakMap();

export function mount(target, api) {
  if (mounts.has(target)) {
    unmount(target);
  }

  const value = document.createElement("span");
  value.className = "ui-plugin-counter-value";
  value.textContent = "0";

  const ticks = document.createElement("span");
  ticks.className = "ui-plugin-counter-ticks";
  ticks.textContent = "0s";

  const button = document.createElement("button");
  button.type = "button";
  button.className = "ui-plugin-counter-button";
  button.textContent = "count";

  const entry = { count: 0, seconds: 0, value, ticks, button, timer: 0 };
  entry.onClick = function onClick() {
    entry.count += 1;
    value.textContent = String(entry.count);
  };
  button.addEventListener("click", entry.onClick);

  // The side effect that only unmount can end.
  entry.timer = window.setInterval(function onTick() {
    entry.seconds += 1;
    ticks.textContent = entry.seconds + "s";
  }, 1000);

  mounts.set(target, entry);
  target.append(value, button, ticks);
}

export function unmount(target) {
  const entry = mounts.get(target);
  if (entry === undefined) {
    return;
  }
  mounts.delete(target);
  window.clearInterval(entry.timer);
  entry.button.removeEventListener("click", entry.onClick);
  entry.value.remove();
  entry.button.remove();
  entry.ticks.remove();
}
