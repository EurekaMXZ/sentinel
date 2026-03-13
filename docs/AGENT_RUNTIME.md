# Agent 运行说明

Agent Worker 已实现任务读取、模型请求、工具调用、独立子 Agent、Context 检查点和 Continue-As-New。
根 Agent 与子 Agent 使用同一个 `sentinel.AgentSessionWorkflow`，运行无需绑定仓库或快照。
本次实现沿用 schema 版本 2，不需要新增迁移。

## 启动

先按 [本地开发环境](DEVELOPMENT.md) 启动 PostgreSQL、MinIO、Temporal，执行 `minio-init` 和 `go run ./cmd/migrate up`。
在 Agent Worker 和 CLI 各自的终端设置环境变量：

```powershell
$env:POSTGRES_DSN = "postgres://sentinel:sentinel-dev-password@127.0.0.1:5432/sentinel?sslmode=disable"
$env:S3_ENDPOINT = "127.0.0.1:9000"
$env:S3_BUCKET = "sentinel"
$env:S3_ACCESS_KEY = "sentinel-dev"
$env:S3_SECRET_KEY = "sentinel-dev-secret"
$env:S3_USE_TLS = "false"
$env:TEMPORAL_ADDRESS = "127.0.0.1:7233"
$env:TEMPORAL_NAMESPACE = "default"
$env:OPENAI_API_KEY = "填入 API key"
$env:OPENAI_MODEL = "填入可访问的模型 ID"
$env:OPENAI_REQUEST_TIMEOUT = "2m"
```

默认使用 SDK 的 OpenAI 服务地址；自定义地址通过 `OPENAI_BASE_URL` 配置，须支持 Responses API 和 function calling。
当前请求还包含 `reasoning.encrypted_content`，用于在本地保存并恢复推理上下文。
程序不自动加载 `.env`，也不会自动创建 bucket 或修改表结构。

启动 Worker：

```powershell
go run ./cmd/agent-worker
```

Worker 会检查模型配置、S3 bucket 和数据库连接，然后轮询 `sentinel-agent` task queue。
可用 `TEMPORAL_AGENT_TASK_QUEUE` 修改队列；CLI 与 Worker 应使用同一队列及 namespace。

在另一个已配置相同环境变量的终端创建任务：

```powershell
go run ./cmd/agent -tenant "0194e229-8576-7f42-8f08-3496913847cf" -task "解释如何把一项分析任务拆分为独立子任务" -wait
```

`-tenant` 是调用方明确提供的租户 UUID；当前 CLI 属于管理入口。未来 HTTP API 应从认证上下文取得租户身份。
CLI 先输出 `tenant_id`、`session_id`、`agent_id`、`workflow_id`；加 `-wait` 后再输出 `result_ref` 和结果摘要。
省略 `-wait` 会在调度后退出。CLI 的 Ctrl+C 只停止本地等待，已调度的 Workflow 会继续运行。
需要取消任务时可在 Temporal UI 对相应 Workflow 发起取消。

较长任务可用 `-task-file path` 读取 UTF-8 文件。`-resources path` 接受资源关联 JSON 数组：

```json
[
  {
    "binding_name": "document",
    "resource_kind": "object",
    "object_id": "已校验且属于该租户的 stored_objects UUID"
  }
]
```

也支持 `source_repository` 与 `source_snapshot` 资源，创建时检查租户及资源状态。
关联资源只向模型提供标识；当前尚无资源读取工具，因此不会自动把其正文加入模型请求。
需要直接注入已有文本时，`-context UUID` 引用对象存储中的 `{"text":"..."}` 初始 Context 文档。
该对象须已登记为同租户的 verified 对象；当前未提供上传文档的业务 API。

## 执行与子 Agent

每步依次执行模型请求、工具派发和检查点提交。模型返回文本时保存结构化结果并完成；返回 function call 时保存各个工具结果，
将完整模型输出及对应 `function_call_output` 加入 Context，再请求模型。一个响应中的多个工具按返回顺序处理。

当前注册的模型工具为 `call_child_agent`，参数包括 `task`、可选的 `context_ref`、`resources` 和 `expected_output`。
服务端检查工具白名单、深度及资源范围，保存独立子任务和资源关联，再由 Workflow 启动 Child Workflow。
子 Agent 不隐式继承父任务、完整消息历史或资源集合。父 Agent 通过工具结果接收子 Agent 的结构化结果及结果引用。
子任务失败会形成工具错误结果，父 Agent 可以据此继续；父 Workflow 取消时会向子 Workflow 传播取消。

工具扩展位于 `internal/agent/activity.Tool`。新增工具必须注册 SDK 工具定义及处理函数，并由服务端加入任务白名单。
处理函数应只读，或根据传入的稳定 `OperationID` 实现幂等。当前未注册命令执行、源码修改或尚未实现的源码查询工具。

## SDK 与持久化

上游调用使用已有的官方 `github.com/openai/openai-go/v3` SDK 的 `Responses.New`。
鉴权、HTTP、请求序列化及响应解析由 SDK 完成；没有另写 HTTP 请求客户端。

- 每次请求显式设置 `store=false`，不使用 `previous_response_id`，从本地 Context 组装完整输入。
- 请求在发送前不可变保存；重试通过 SDK 的原始参数接口重用同一份请求 JSON。
- 响应保存 SDK 提供的原始 JSON，完整保留工具参数、call ID、未知字段和加密推理内容。
- S3 正文按租户和内容摘要组织，gzip 压缩；读取时校验大小和 SHA-256，限制解压后的字节数。
- PostgreSQL 保存对象索引、模型 / 工具执行记录、资源关联、幂等状态、租约和 fencing token。
- 检查点在一个数据库事务中更新 Context head、连续事件索引、步骤状态、完成结果和 outbox，按 head 版本做 CAS。

Temporal History 只接收任务、Context、参数及结果的引用和必要元数据，不包含提示词、模型正文或完整工具结果。
默认每 16 个已提交步骤进行 Continue-As-New，也响应 Temporal 的建议；切换前没有未完成子任务，逻辑步骤编号延续。

## 重试与恢复

SDK 自带重试关闭，由 Activity 与数据库中的操作状态共同管理。已经保存的模型响应和工具结果优先复用。
并发模型调用通过租约和 fencing token 排他认领，过期且没有保存结果的调用不会被自动再次发送。

| 上游结果 | 处理 |
| --- | --- |
| 明确返回 429 | 恢复为 prepared，由 Activity 退避重试同一请求 |
| 明确拒绝的其他 4xx | 失败；408、409 单独按不确定处理 |
| 网络中断、超时、408 / 409、5xx | 标为 uncertain，停止自动重发 |
| 已保存响应后 Activity 重试 | 直接读取已保存响应 |

`X-Client-Request-Id` 仅用于关联排查，不代表上游保证只计费或执行一次。
`uncertain` 需要人工核对上游记录；当前没有自动核对、清除状态或重新执行该请求的入口。

调度响应丢失、CLI 退出或 Worker 重启后，可使用已输出的 Agent ID：

```powershell
go run ./cmd/agent -tenant "0194e229-8576-7f42-8f08-3496913847cf" -resume "已有根 Agent UUID" -wait
```

`-resume` 连接现有运行，或为尚未成功调度的持久化任务重试启动；已完成时直接读取保存的结果。
它不替换任务、资源或限制，也不重新启动已失败 / 已取消的 Agent。子 Agent 由父 Workflow 调度。
Worker 正常退出或暂时不可用时，Temporal 保留历史并在 Worker 恢复后继续派发任务。

## 当前限制与检查

默认限制按每个 Agent 计算：最多 64 次模型步骤、每次最多 16 个工具调用、子 Agent 深度最多 3、
完整请求最多 4 MiB、模型输出最多 8192 tokens。`-max-steps`、`-max-child-depth` 可在创建根任务时调整；
深度为 0 时不向模型提供子 Agent 工具。`OPENAI_REQUEST_TIMEOUT` 最大为 30 分钟。

当前每步保存完整 Context Snapshot；尚未实现 delta、token 估算和自动 compact，超过请求字节预算会明确失败。
Git 同步、源码读取工具、Source Worker、outbox 消费及业务 HTTP API 仍待实现。
结构化结果中的源码证据及产物数组目前为空，不会生成未经工具验证的源码位置。

```powershell
go test ./...
go vet ./...
go build ./cmd/...
```

单元测试使用 Temporal 测试环境和本地 SDK 测试服务，不请求真实模型。设置具有 `CREATEDB` 权限的测试 DSN 后运行数据库集成测试：

```powershell
$env:SENTINEL_TEST_POSTGRES_DSN = "postgres://sentinel:sentinel-dev-password@127.0.0.1:5432/sentinel?sslmode=disable"
go test -tags=integration -count=1 ./internal/storage/postgres
```

集成测试为每例创建并清理独立数据库，覆盖父子 Agent、多个工具、检查点恢复、429 重试、结果复用、
租约 fencing、不确定请求、取消状态和资源范围。S3 适配测试另行验证 SDK 签名、内容去重、租户隔离及损坏 / 超限拒绝。
