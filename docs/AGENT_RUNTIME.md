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
$env:OPENAI_CONTEXT_WINDOW_TOKENS = "128000"
$env:OPENAI_CONTEXT_SAFETY_TOKENS = "8192"
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
关联资源只向模型提供标识；需要时由模型显式调用源码工具读取，不会自动把正文加入模型请求。
需要直接注入已有文本时，`-context UUID` 引用对象存储中的 `{"text":"..."}` 初始 Context 文档。
该对象须已登记为同租户的 verified 对象；当前未提供上传文档的业务 API。

## 执行与子 Agent

每步依次执行模型请求、工具派发和检查点提交。模型返回文本时保存结构化结果并完成；返回 function call 时保存各个工具结果，
将完整模型输出及对应 `function_call_output` 加入 Context，再请求模型。一个响应中的多个工具按返回顺序处理。

当前注册的模型工具分为两类：

- `call_child_agent`：参数包括 `task`、可选的 `context_ref`、`resources` 和 `expected_output`。
  服务端检查工具白名单、深度及资源范围，保存独立子任务和资源关联，再由 Workflow 启动 Child Workflow。
- `source_list_versions`、`source_list_files`、`source_read_file`、`source_search`、`source_diff`：
  Agent Worker 校验参数和资源范围后，Workflow 通过 `sentinel.source.read` Activity 将请求派发到
  `TEMPORAL_SOURCE_TASK_QUEUE`，由 Source Worker 在恢复的 bare 仓库上执行只读查询。

子 Agent 不隐式继承父任务、完整消息历史或资源集合。父 Agent 通过工具结果接收子 Agent 的结构化结果及结果引用。
子任务失败会形成工具错误结果，父 Agent 可以据此继续；父 Workflow 取消时会向子 Workflow 传播取消。

工具扩展位于 `internal/agent/activity.Tool`。`Source` 为 true 的工具只在 Agent Worker 做参数、权限和
快照状态校验，实际执行由 Source Worker 的 Activity 承担。本地处理函数应只读，或根据传入的稳定
`OperationID` 实现幂等。当前未注册命令执行或源码修改工具。

## 源码读取工具

| 工具 | 主要参数 | 结果 |
| --- | --- | --- |
| `source_list_versions` | `repository_id`、`cursor`、`limit` | 仓库内已就绪快照及 `next_cursor` |
| `source_list_files` | `source_snapshot_id`、`path`、`cursor`、`limit` | 单层目录项或文件项 |
| `source_read_file` | `source_snapshot_id`、`path`、`start_line`、`end_line` | 行范围内容、实际行号和 `truncated` |
| `source_search` | `source_snapshot_id`、`query`、`path`、`cursor`、`limit` | 字面量匹配行及位置 |
| `source_diff` | `base_snapshot_id`、`target_snapshot_id`、`path`、`cursor`、`limit` | 同一仓库两个快照的 unified patch |

- Agent Worker 用 `LoadSourceSnapshot` 解析快照所属仓库和 ready 状态，并用 Agent 的
  `agent_resource_bindings` 校验访问范围；Source Worker 会再次校验租户、仓库和快照状态。
- `source_list_versions` 需要仓库绑定或同仓库快照绑定；其余工具需要快照绑定，或所在仓库的仓库绑定。
- 游标是不透明字符串，绑定工具、租户、快照/查询、路径和分页大小；跨查询复用会被拒绝。
- 单页结果受条数、行数和字节数限制；`next_cursor` 表示还有下一页。完整结果作为
  `source-tool-result` 对象存入 S3；进入 Context 的工具结果包含受限数据和指向该对象的
  `result_ref`。
- `tool_executions` 记录仓库、快照或 diff 两端版本；`event_index` 和 `context_source_refs`
  保存实际使用的证据。重试复用已保存的 tool result，不重新选择版本。
- Source Worker 每次查询从当前归档 head 恢复 bare 仓库；缓存恢复失败会重试，查询本身不产生新版本。

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

## Context Compact

每次 `Initialize` 和 `Checkpoint` 都会估算下一次模型请求的 token 数。超过预算时，
Workflow 在下一次模型调用前执行 `sentinel.agent.compact`：

```text
估算输入 tokens + MaxOutputTokens + ContextSafetyTokens > OPENAI_CONTEXT_WINDOW_TOKENS
```

- 常规路径使用上一步响应返回的真实 `input_tokens`，只按字节估算该步新增的消息和工具结果；
  恢复时没有 usage，则按完整请求字节估算。
- Compact 使用同一次普通 Responses API 调用，不启用工具，要求模型返回严格 JSON 摘要：
  任务要求、约束、决策、当前状态、未决项、错误、子 Agent 结果、产物和源码引用。
- 摘要渲染为一条 user message，成为新 Context Snapshot 的唯一内容；原始事件、模型请求和响应全部保留。
- 服务端保留 instructions、工具策略、资源绑定和权限，模型输出不能扩大它们；源码引用须属于
  Agent 的资源委派范围或当前已有证据，产物引用须是已校验的 `tool-result` 对象。
- 新 Context 记录覆盖全部已产生事件（`from_event_sequence=1`、`through_event_sequence=last_event_sequence`），
  `agent_head.context_version` 和 `row_version` 各递增一次，`next_step_id` 与事件游标不变。
- Compact 使用 `agent_id+source_row_version` 派生的稳定 operation ID、租约和 fencing token。
  已保存的响应可以复用；head 在发布前变化时旧候选标记为 `stale`，Activity 重试时按最新 head 新建操作。
  429 恢复为 `pending` 重试；其他明确 4xx 或非法摘要失败；网络、超时、408/409/5xx 标为 `uncertain`，
  不自动重发。
- 摘要类别各最多 64 条、单条最多 8 KiB、总计最多 64 KiB；Compact 输出最多 4096 tokens。
  压缩后仍超过预算时明确失败，不发布新 Context。

`OPENAI_CONTEXT_WINDOW_TOKENS`、`OPENAI_CONTEXT_SAFETY_TOKENS` 随任务保存为执行限制。
不支持 JSON Schema 结构化输出的兼容端点可设置 `OPENAI_COMPACT_STRUCTURED_OUTPUT=false`，
服务端仍会严格校验摘要。

## 当前限制与检查

默认限制按每个 Agent 计算：最多 64 次模型步骤、每次最多 16 个工具调用、子 Agent 深度最多 3、
完整请求最多 4 MiB、模型输出最多 8192 tokens。`-max-steps`、`-max-child-depth` 可在创建根任务时调整；
深度为 0 时不向模型提供子 Agent 工具。`OPENAI_REQUEST_TIMEOUT` 最大为 30 分钟。

当前每步保存完整 Context Snapshot，尚未实现 delta；token 估算为保守近似，不使用模型专用 tokenizer。
控制 API 已提供 Agent 创建、状态、结果、cancel 和 resume；outbox 消费仍待实现。
源码工具只在实际调用后生成证据；无源码工具调用的任务保持空的 `source_refs`。
Source Worker 当前每次查询都会从归档恢复临时 bare 仓库，尚未共享持久读缓存。

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
