# 001 · Go plugin kernel

机制 Demo · 未接入模型。Go host 使用 HashiCorp go-plugin v1.8.0 / net/rpc 调用真实独立进程，不使用 Go 的动态库 plugin 包。

## 运行

要求 PATH 中有 Go（模块要求 Go 1.24+），首次允许下载依赖。必须从本 spike 目录启动，web 文件在运行时读取，没有 embed 或 Node 构建依赖。

```sh
cd spikes/001-plugin-kernel   # from the repository root
go mod download
go build -o .runtime/luna ./cmd/luna
./.runtime/luna -addr 127.0.0.1:0
```

启动默认构建 v1，绑定后打印 `LISTEN_URL=http://127.0.0.1:<实际端口>`。打开该精确地址；Host 校验不接受替换成 localhost。固定端口可用 `./.runtime/luna -addr 127.0.0.1:8787`。Ctrl-C 或 SIGTERM 关闭 host 与它拥有的插件；不留后台服务。

## 手动演示

1. v1 对 `  hello Luna  ` 返回 `hello Luna`。
2. 先切换 v2 一次、再切回 v1，预热编译缓存。发起 3000ms 慢调用，立即重新编译并加载 v2。新 active 的 PID / generation 改变，host PID 不变；旧 v1 显示 retiring/inflight，完成后返回其原来的 v1 generation，然后旧进程退出。
3. 新 v2 对 `  hello Luna  ` 返回 `Luna · HELLO LUNA`。
4. 选择 broken：真实编译的故障进程拒绝 handshake，reload 返回错误；当前 v2 不改变，仍可调用。
5. 修改 `plugins/v2/main.go` 中 `"Luna · "` 为其他前缀，保存后选择 v2 并点击重新编译并加载。每次 reload 都执行真实 `go build`，不是切换前端计数器。不要改变 Metadata 的固定 version/protocol 契约。

冷缓存编译可能超过慢调用的 3 秒，演示并行 drain 前先预热候选。reload 是显式手动操作，没有文件监听器。

## HTTP 契约

- GET `/healthz` → `{ok:true}`。
- GET `/api/state` → `{host_pid,started_at,active:{generation,version,plugin_pid,candidate,status,inflight},plugins:[...],events:[{time,type,message}],demo:true}`。没有 active 时 active 为 null；plugins 仅保留活跃/正在退出的进程，事件最多 100 条。
- POST `/api/invoke` JSON `{text,delay_ms}` → `{result,generation,version,plugin_pid}`。
- POST `/api/reload` JSON `{candidate:"v1"|"v2"|"broken"}` → 更新 state，失败 `{error}`。
- `/`, `/app.js`, `/style.css` 只服务 web/ 下这三个文件；其他路径 404。

单次 body 最多 32768 字节，text 最多 16384 字节，delay_ms 范围 0..3000；未知字段/多 JSON 对象拒绝。mutation 只接受 POST。绑定只接受字面量 loopback IP；Host 必须等于真实绑定地址；Origin 如存在必须精确同源（包括拒绝 null）。无跨域许可，跨站 Fetch Metadata 拒绝。它是本机演示防护，不是鉴权服务。

## 生命周期与边界

构建最长 60 秒；握手、Dispense、Metadata 共用 5 秒 watchdog。watchdog 成功路径先停止并 join 再 cancel，避免迟到取消杀掉新插件。reload 使用单飞互斥，并发请求立即失败，不排队；构建期间调用和 state 不被 reload 锁阻塞。

每次接受调用在锁内固定 generation 并加引用。HTTP 客户端断开不释放引用，等待真实 RPC 返回。RPC 超过 5 秒则标记失败、撤下 active 并终止该拥有的进程，等 RPC 实际返回后才减引用；同进程其他调用也可能失败，需要手动重新加载恢复。空闲 retiring generation 被终止、等待回收并删除临时二进制。输出使用真实插件 Metadata/PID，元数据需匹配候选/version/protocol/实际子 PID。

插件使用 PATH/HOME/TMPDIR 最小环境，并设置 go-plugin `SkipHostEnv: true`，没有继承模型密钥。构建也使用最小环境；但本机 Go 的正常模块缓存与配置仍适用。子进程隔离不是安全沙箱，插件作为当前用户仍有文件/网络权限，只运行受信任的本地源码。没有任意代码上传端点。

不证明：完整 Agent、LLM/Eino、任务或会话持久化、状态迁移、自动源码监听、React HMR、运行时 UI 插件或生产级隔离。状态仅保存在当前 host 内存中；事件不记录用户输入内容。

## 验证

```sh
go test -race ./... -count=1 -v
go vet ./...
go build -o .runtime/luna ./cmd/luna
node --check web/app.js
```

真实集成测试覆盖 v1、v2 热替换、3000ms 旧 generation pin/drain/进程退出、broken 回滚、重复替换、并发 state/invoke/reload、Metadata 与 Invoke 挂起终止。HTTP 测试覆盖方法、Host、Origin、body、输入限制、loopback 绑定与 HTML 文件服务；HTML 服务测试使用临时本地 fixture，不是浏览器 UI 测试。

参见 [IMPLEMENTATION_REPORT.md](IMPLEMENTATION_REPORT.md) 的实际执行记录。

Verdict: PARTIAL — 后端机制集成测试通过；本后端交付不声明浏览器交互已验证，也不代表完整 Agent 已实现。
