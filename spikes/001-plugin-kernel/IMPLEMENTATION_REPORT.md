# 后端实现完成报告

## 结果

真实 Go 子进程插件机制已运行并通过集成测试。使用固定 `github.com/hashicorp/go-plugin v1.8.0`、net/rpc transport；独立构建 v1/v2/broken；默认 v1。提供精确约定的 state/invoke/reload/healthz API，与运行时 web 文件服务，无 embed 依赖。

实现 generation 引用固定、并发热替换、旧进程 drain/回收、失败替换保持 active、单飞 reload（并发返回错误）、60 秒构建上限、5 秒完整启动 watchdog、5 秒 RPC 超时终止。HTTP 断开不提前减引用，超时时先终止拥有的插件并等实际 RPC 返回。SIGINT/SIGTERM 清理拥有的插件。

实现过程中修复启动 watcher 的退出竞争：先 stop + join，再 cancel；在 publish 前检查取消。回归测试覆盖 1000 次 stop-before-cancel 和 watchdog deadline。

## 实际执行记录

### RED / GREEN

- `go test ./internal/kernel -run TestRealV1 -v` 首次失败：`undefined: Kernel / New / Input`；实现后 PASS，真实 host PID 117114、plugin PID 117509，result `hello Luna`。
- `go test ./internal/kernel -run TestHotReloadDrainRollback -v` 首次失败：缺失 `k.Reload`；加入替换/drain 后完整 kernel suite PASS。
- `go test ./internal/httpapi -v` 首次失败：缺失 `New / Listen`；实现 HTTP 后 PASS。
- `go test ./internal/kernel -run TestStartupWatch -v` 首次失败：缺失 `watchStartup`；加入可 join helper 后完整 race suite PASS。

以上 RED 是接口尚不存在的真实编译失败，不是运行时断言失败；不将其描述为完整的 assertion-level RED。超时集成测试作为补充回归添加，在现有超时实现上通过。

### 最终完整验证

执行 `go test -race ./... -count=1 -v`：退出 0，无 race 报告。

- httpapi package PASS（2.658s）：方法 405、foreign/null Origin 403、错误 Host 403、超大 body 413、输入/候选/多对象/未知字段 400、health/state、HTML fixture 服务、loopback-only 绑定。
- kernel package PASS（29.340s）。热替换实测 host PID 245338 保持；旧 v1 PID 245813 → 新 v2 PID 246804；3000ms 旧调用在 v2 publish 后仍返回 v1 generation，旧进程退出；broken 失败后 v2 保持可调用。
- 重复替换和并发 state/invoke/reload 通过，每次确认退休 PID 已不存在，最终只有一个插件记录。
- `TestRealMetadataTimeout`：真实握手成功但 Metadata 挂起的临时插件 6.8267s 后拒绝（含构建），PID 251341 已退出。
- `TestRealInvokeTimeout`：真实 RPC 挂起，5.0098s 后返回错误，PID 253267 已退出，active 已撤下且插件记录已清理。
- `go vet ./...` → VET_OK。
- `go build -o .runtime/luna ./cmd/luna` → BUILD_OK。
- `node --check web/app.js` → JS_SYNTAX_OK。只读取并检查前端工作者交付文件，未改动 web/。
- `git diff --check` → 退出 0；没有提交。

### 实际 CLI / HTTP smoke

自己启动已构建 binary 并读取真实输出：

```text
LISTEN_URL=http://127.0.0.1:39537
HTTP_SMOKE_OK {"result":"smoke","generation":1,"version":"v1","plugin_pid":285336} host_pid 285124 html_bytes 5214
HOST_EXIT 0
PLUGIN_EXIT_CONFIRMED 285336
```

确认 `/healthz`、`/api/state`、`/api/invoke`、实际 `/` HTML、app.js、style.css 可服务。发送 SIGTERM，host 正常退出，并确认插件 PID 不存在。没有遗留自己的 demo server。不声明浏览器交互/视觉验证；由父任务独立验证 UI。

## 文件

后端创建：根 README.md、.gitignore；本 spike 的 go.mod/go.sum、README.md、本报告；`cmd/luna/main.go`；`internal/protocol/protocol.go`；`internal/kernel/{kernel.go,deadline.go,kernel_test.go,deadline_test.go,timeout_integration_test.go}`；`internal/httpapi/{http.go,http_test.go}`；`plugins/{v1,v2,broken}/main.go`。`.runtime/luna` 是真实已构建且 git 忽略的产物。web/ 由前端工作者负责，未写入。

## 启动命令

```sh
cd /home/j/probe/luna-agent/spikes/001-plugin-kernel
./.runtime/luna -addr 127.0.0.1:0
```

重建：`go build -o .runtime/luna ./cmd/luna`。必须使用打印出的精确 loopback 地址访问，Host 不接受 localhost 别名。

## 明确限制

- 只证明受控本机的手动热替换；没有文件自动监听、运行时 UI 插件、状态迁移、模型、Eino 或任务持久化。
- 子进程不是安全沙箱；源码必须可信。最小环境不等于文件系统/网络隔离。
- 挂起插件被强制终止时，其同进程调用会共同失败；active 为空后需手动重新加载。不是透明重试/恢复系统。
- 初次构建可能比 3000ms 慢调用更慢；演示 drain 前先预热 v2，再切回 v1。
- 真实网络客户端断开的单独集成测试未添加；处理器明确不使用请求 context 取消 Invoke，核心 pin/drain 和 RPC 挂起终止已有真实集成验证。
- Go race 检测作用于宿主/测试；测试中构建的插件为普通 Go build，不是 -race 插件 binary。
- 编译错误摘要最多返回 2000 字符；只是受控演示，不是通用构建服务。

可复用教训：go-plugin StartTimeout 只限握手，不限 Dispense/Metadata；为完整 startup 提供外层 watchdog。watchdog 成功路径必须 join 后取消，不能让 ready 和 ctx.Done 同时可读。RPC 超时必须终止拥有的插件/等待真实返回再释放 generation 引用，不能仅因 HTTP 断开宣称任务取消。

Verdict: PARTIAL（后端真实机制与构建验证通过；浏览器交互不在本报告验证范围）。
