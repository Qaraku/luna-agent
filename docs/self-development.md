# 受控的自我开发

Luna 可以在用户授权下参与修改自己的代码，但“提出改进”和“采用改进”是两件事。个人方法优先通过技能学习、预设和插件包形成可恢复修订；需要改程序核心时，使用独立源码副本与候选补丁，不能在后台覆盖运行实例或自行修改权限。

## 创建固定提交的独立副本

需要本地 Git；实际编译和测试仍需项目要求的 Go/Node 等开发工具。命令不下载仓库或依赖，不调用模型：

```sh
COMMIT=$(git -C /绝对路径/luna-source rev-parse HEAD)
luna dev create -repo /绝对路径/luna-source -commit "$COMMIT" -dest /绝对路径/luna-development
```

目标必须是原仓库之外的全新目录。它包含：

- `source/`：从指定提交复制的正式应用源码和一个新建、独立的 Git 仓库；仅在此副本创建初始基线提交，不修改原仓库分支或历史。
- `baseline/`：同一份固定源码基线，供内容与权限位比较。
- `workspace.json`：完整来源提交、原仓库位置、文件清单和摘要。

通过 Git 对象读取已提交源码，不使用未提交工作树，不复制原仓库的 Git 配置、objects 硬链接、hooks、凭据、会话、记忆或生成目录。`spikes/` 历史实验不在正式应用开发副本内。发现所选提交中有被排除的私人/生成文件时明确拒绝，不悄悄漏掉被跟踪内容。

**只把 `source/` 绑定为 Luna 工作区**，将基线和来源记录留在模型可写范围之外。模型沿用已启用的文件写入/终端能力和会话审批；这个副本不赋予任何新权限。摘要不是签名，也不是防御宿主 Full access 用户的权限边界。

## 开发与试运行

模型可以在 `source/` 中读取代码、提出修改、写文件和运行被允许的开发命令。仍须遵守预算、隔离、写入范围与联网审批；环境缺依赖时如实说明，不宣称验证通过。

试运行必须使用另一份私人数据，例如由用户在可信终端中执行：

```sh
cd /绝对路径/luna-development/source
LUNA_HOME=/绝对路径/luna-development/test-home go run ./cmd/luna -addr 127.0.0.1:0
```

这条命令是手动试运行示例，`dev create` 不会替你执行它。不要复用日常实例的 `LUNA_HOME` 或自定义会话/状态目录；不要复制日常凭据进行无人值守验证。独立数据空间首次启动没有提供方凭据。开发检查可使用项目现有假模型与单元测试。

## 检查与导出候选

```sh
luna dev status -workspace /绝对路径/luna-development
luna dev export -workspace /绝对路径/luna-development -dest /绝对路径/luna-proposal
```

`status` 比较实际文件内容和可执行位，不相信候选的 Git 索引或提交历史。它列出修改/删除、新增未选文件和排除路径。副本中的新 Git 提交不会改变受控基线；要换基线，应从新的完整提交重新创建副本。

新文件必须逐个明确加入，否则导出会拒绝，避免悄悄生成缺文件的补丁：

```sh
luna dev export -workspace /绝对路径/luna-development -dest /绝对路径/luna-proposal \
  -include internal/example/new.go -include internal/example/new_test.go
luna dev inspect -bundle /绝对路径/luna-proposal
```

生成 `candidate.patch` 和 `candidate.json`，记录来源提交、基线摘要、变更文件及补丁摘要。支持普通文本、二进制、删除、新文件与可执行位变更；即便新文件被候选 `.gitignore` 忽略，明确选入后也会进入补丁。文件在导出期间变化、基线损坏、路径越界、内部符号链接或目标已存在时拒绝。

补丁生成发生在新的临时 Git 目录：不执行候选 `.git` 的 hooks、fsmonitor、clean filters、textconv 或外部 diff；原始字节不受候选属性中的换行/编码转换影响。候选代码不会因“导出补丁”而自动运行。

已排除的路径不能用 `-include` 绕过，包括常见凭据名、会话/记忆文件、`.env`、密钥扩展名、`.git`、`.runtime`、`.evidence`、`.spec`、`node_modules` 和历史实验。文件名过滤不是秘密检测器：正文仍须用户审阅，不能据此宣称补丁绝无私人内容。

上限：10000 个源码文件、扫描 20000 个条目、每文件 16 MiB、源码总量 128 MiB、补丁 32 MiB、元数据 4 MiB；命令有五分钟协作截止。未通过检查的候选不会发布为成功输出，已有副本/补丁目录不会被覆盖。

## 用户决定采用

`dev inspect` 只核对摘要和元数据，**不是代码审查、测试通过或来源签名**。没有 `dev apply`，也没有浏览器按钮自动覆盖原仓库。

由你在另外一份可信、干净且基线匹配的开发 checkout 中审阅并采用：

```sh
# 先核对 HEAD 与 candidate.json 的 source_commit，并检查工作树干净。
git -C /可信开发副本 status --short
git -C /可信开发副本 rev-parse HEAD
git -C /可信开发副本 apply --check /绝对路径/luna-proposal/candidate.patch
# 审阅补丁及其权限、数据兼容影响后，才明确采用：
git -C /可信开发副本 apply /绝对路径/luna-proposal/candidate.patch
```

随后按项目规则运行相关检查、审阅并提交；采用补丁不等于更新运行实例。通过 [程序分发流程](distribution.md) 创建新程序包，先备份私人数据，再由用户手动切换。拒绝候选时不需要对原程序做任何回退，因为它从未被这套命令改动。
