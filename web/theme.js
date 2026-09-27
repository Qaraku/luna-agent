// 首帧主题。它必须在样式表之前同步执行，所以是 head 里的一个阻塞脚本，
// 而不是内联脚本：服务的 CSP 是 default-src 'self'，内联脚本会被拒绝，
// 那样主题要到 app.js（defer）才生效，用户会先看到错的主题再跳一下。
(() => {
  let preference = 'system';
  try {
    const saved = window.localStorage.getItem('luna.theme');
    if (saved === 'light' || saved === 'dark') preference = saved;
  } catch (_) {}
  document.documentElement.dataset.theme = preference === 'system'
    ? (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
    : preference;
})();
