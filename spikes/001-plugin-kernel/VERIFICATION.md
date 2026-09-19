# 独立验证：Luna Plugin Lab

## 结论

插件机制 Demo 已通过独立复审、真实 API 验证、隔离 Chromium 交互测试及当前 Hermes Desktop 预览读取。它不是完整 Agent，未调用模型或读取 `~/.secrets/`。

## 实际证据

### 测试与构建

在本目录独立执行以下命令，整体退出码为 0：

```sh
go test -race ./...
go vet ./...
go build -o .runtime/luna-verified ./cmd/luna
node --check web/app.js
node --test web/app.test.cjs
git diff --check
```

Go httpapi/kernel 测试通过，无 race 报告；Node 测试 3 项通过。最终源码中无 Go 源文件或模块文件比已验证 binary 更新。

独立代码审查最初发现 startup watcher 在 ready/取消同时就绪时可能误杀新插件；实现修复为先 stop/join 再 cancel，并增加回归测试。修复后的定向复审通过，无剩余发现。

### 真实 API 检查

9 项独立 API 检查全部通过，脚本结果保存在 `/home/j/.hermes/cache/luna-demo-verification/results/api-evidence.json`。

- 宿主 PID 始终为 `272095`，启动时间保持不变。
- v1 generation 3 / PID `281827` 返回 `moon light`。
- 在 v1 的 3000ms 调用仍未完成时，v2 generation 4 / PID `282303` 已发布。
- 此时状态同时包含旧代 retiring/inflight=1 和新代 active。
- 旧调用最后仍返回 v1 generation 3；随后旧 PID 在 `/proc` 中消失。
- 新调用返回 `Luna · MOON LIGHT`。
- broken 候选真实握手失败，旧 active generation/PID 不变，仍返回 `Luna · STILL ALIVE`。
- 连续替换后旧进程全部退出；同版本重新编译也创建新 generation/PID。
- foreign Origin、null Origin、foreign Host 均被拒绝。

### 真实浏览器与可见预览

使用临时 Playwright 环境驱动隔离的系统 Chromium，不操作用户共享浏览器：

- 通过页面按钮调用 v1，得到 `hello luna`（generation 10，PID `289620`）。
- 通过页面按钮加载 v2 并再次调用，得到 `Luna · HELLO LUNA`（generation 11，PID `290044`）。
- 页面 Host PID 保持 `272095`；轮询不覆盖文本输入。
- broken 重载错误在页面可见，随后原插件仍可调用。
- 390px 窄屏无横向溢出，页面无 JavaScript pageerror。
- 1440px 桌面截图可读，无阻碍试用的遮挡。

UI 证据：`/home/j/.hermes/cache/luna-demo-verification/results/ui-evidence.json`；截图同目录 `desktop.png`、`mobile.png`。

已通过 desktop_preview 打开实际应用，并读取确认：标题 `Luna · Plugin Lab`，`已连接 · 实时状态`，Host PID `272095`，工具与插件控制项存在。可见窗口只做打开和读取，未代用户点击。

## 试用与运行状态

本次交付 URL：`http://127.0.0.1:36487/`。这是本次进程的端口，不是永久配置。

父任务保留的示例进程：PID `272095`，命令 `./.runtime/luna-verified -addr 127.0.0.1:0`，Hermes 后台进程句柄 `proc_34233970bec8`。它仅监听 loopback，供用户试用。页面关掉不等于服务停止；以后需要关闭时仅操作这个已核实的 demo 进程，不使用泛化 pkill。

重新运行的方式见 README。没有提交或推送 Git。

## 明确边界

- 手动点击“重新编译并加载”，没有自动文件监听。
- 进程外工具插件，不含运行时 UI 插件，也不证明 Go 内核可进程内热替换。
- 不含 Eino、模型调用、会话持久化和插件状态迁移。
- 单独的真实客户端断连场景未做端到端验证；不可把核心 pin/drain 测试描述为已经覆盖该场景。
- 现有隔离/绑定是本机可信源码 Demo 的边界，不是生产多用户安全沙箱。
