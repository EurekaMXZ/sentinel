# 云端 Coding Agent 提案

## 1. 目标

构建一个基于 Go、Temporal、Multi-Worker 和 OpenAI API 的云端 Coding Agent。当前阶段面向只读源码分析。

源码版本来自外部 Git 仓库的 commit。系统负责增量同步和持久化，并向模型提供各个版本的只读源码快照。源码变更由外部开发流程产生。

Agent 接收任务、可选的初始 Context、工具配置和零到多个资源引用。资源可以是仓库、不可变源码快照，或对象存储中的文档和产物。Agent 运行本身不要求绑定仓库或源码版本，随后由模型自行决定是否：

- 查看源码版本、目录和文件；
- 搜索指定版本的源码，或比较两个版本的差异；
- 调用子 agent 分解分析任务；
- 直接返回结果。

当前阶段模型使用只读源码工具和子 agent 工具，不修改源码，也不执行命令、测试或构建。父子 agent 各自维护任务、Context、事件历史和资源集合；可以共享快照、选择不同仓库和版本，也可以不使用源码资源。

Temporal 只负责保证 Agent 流程和仓库同步任务可恢复。系统不实现固定的代码审查流水线，核心是通用 Agent Runtime。

## 2. 总体架构

```text
Client / API
    |
    +-- SourceSyncWorkflow
    |       +-- Source Worker ----------> 外部 Git 仓库
    |               +-- 本地 bare 仓库缓存
    |               +-- 基线 / 增量 bundle + manifest --> MinIO / S3
    |               +-- 仓库 / 快照 / 归档索引 ---------> PostgreSQL
    |
    +-- AgentSessionWorkflow
            |
            +-- Model Activity ----------------------> OpenAI API
            |
            +-- Tool Dispatcher
            |       +-- call_child_agent ------------> Child Agent Workflow
            |       +-- source_* --------------------> Source Worker
            |
            +-- Context / Event Store ---------------> MinIO / S3
            +-- Metadata / CAS / Idempotency --------> PostgreSQL
            +-- Workflow history / retry ------------> Temporal
```

每个 Agent 都是一个独立的 `AgentSessionWorkflow`。子 agent 也是同一种 Workflow，只是由父 agent 的工具调用启动。

`agent_sessions` 表示用户任务会话，`agents` 表示其中的 root 和 child 执行。它们只关联租户和任务，不持有必填的仓库或快照字段。`session_resource_bindings` 保存会话提供的资源，`agent_resource_bindings` 保存每个 Agent 显式选择的资源，两者都允许为空。

资源关联在创建会话或 Agent 时持久化，作为后续恢复的输入。子 agent 不隐式继承父 agent 的完整 Context 或资源集合；调用时显式传入所需部分。资源引用描述任务上下文，不授予访问权限；工具配置和资源访问范围由服务端校验。

仓库同步由 API、定时任务或 webhook 触发，独立于模型工具循环。模型查询已发布的快照；新的同步结果不会自动替换已有的快照引用。

只有选择源码快照资源或调用源码工具时，才校验快照属于目标租户、仓库且状态为 `ready`。分支名和标签只用于发现版本，实际读取解析到不可变的 commit。一次读取或 diff 的版本记录在该工具操作中，重试复用已保存的参数，不随上游分支变化。

## 3. Agent 执行循环

```text
读取任务、Context、工具配置和可选资源引用
    -> 判断是否需要 compact
    -> 调用模型并传入该 Agent 获准使用的 tools
    -> 模型返回文本或 function call
    -> 文本：结束本轮
    -> function call：执行工具并保存 tool_result
    -> 将 tool_result 放回 Context
    -> 再次调用模型
```

模型是否读取源码、比较版本、创建子 agent，以及先调用哪个工具，都由模型的 function call 决定。Workflow 不预设固定步骤。

一次模型工具循环中的事件顺序：

```text
model_request
model_response
assistant_tool_call
tool_execution
tool_result
```

源码同步和快照发布由 Source Worker 完成，不在模型工具调用后生成新版本。

## 4. Function Call 接口

### 4.1 子 agent

父 agent 使用模型可见的工具调用子 agent：

```json
{
  "name": "call_child_agent",
  "arguments": {
    "task": "分析认证模块并给出问题、证据和建议",
    "context_ref": "s3://.../context-input.json.zst",
    "resources": [
      {
        "binding_name": "auth_source",
        "resource_kind": "source_snapshot",
        "repository_id": "repo-1",
        "source_snapshot_id": "snap-42"
      }
    ],
    "expected_output": "问题、证据、建议和 artifact 引用"
  }
}
```

`resources` 可省略或为空，也可以包含多个仓库、多个版本或文档对象。服务端校验并保存子任务和资源关联，再启动 Child Workflow；子 agent 的工具集由服务端配置。

Tool Handler 的实现：

```text
模型产生 call_child_agent
    -> Agent Workflow 启动 Child Workflow
    -> Child Workflow 读取自己的任务、Context 和资源，运行独立的模型和工具循环
    -> Child Workflow 返回结构化结果
    -> 父 Workflow 保存 child tool_result
    -> 继续调用父 agent 模型
```

父子 agent 的语义通信只通过 `call_child_agent` 的输入和返回结果完成。Temporal Signal 只用于取消、暂停、心跳和内部生命周期控制，不作为主要消息通道。

父 agent 只将子 agent 的最终结果写入自己的 Context：

```json
{
  "child_agent_id": "child-17",
  "summary": "...",
  "decisions": [],
  "artifacts": [],
  "source_refs": [
    {"repository_id": "repo-1", "source_snapshot_id": "snap-42"}
  ],
  "unresolved_items": []
}
```

父子 agent 读取同一个快照时无需创建各自的源码工作区。子 agent 的完整事件历史保存在自己的事件日志中。结果里的 `source_refs` 可为空或包含多个版本；涉及源码的结论必须保留对应的仓库、快照标识和源码位置。

### 4.2 只读源码工具

模型可见的源码工具如下：

| 工具 | 主要参数 | 用途 |
|---|---|---|
| `source_list_versions` | repository_id、cursor、limit | 查看仓库已就绪的源码版本 |
| `source_list_files` | source_snapshot_id、path、cursor、limit | 查看指定快照的目录和文件 |
| `source_read_file` | source_snapshot_id、path、start_line、end_line | 按路径和行范围读取文件 |
| `source_search` | source_snapshot_id、query、path、cursor、limit | 搜索指定快照中的源码 |
| `source_diff` | base_snapshot_id、target_snapshot_id、path、cursor、limit | 比较同一仓库的两个快照 |

读取类工具显式指定快照，结果携带快照标识、commit SHA 和源码位置。分页游标绑定查询条件和快照，避免多次读取混入不同版本。同一个 Agent 可以在不同调用中访问多个获准的仓库和版本；单次 `source_diff` 的两个快照仍须属于同一仓库。

工具派发前保存解析后的不可变版本参数和请求摘要。`tool_executions`、相关事件和结果记录实际使用的仓库、快照或 diff 两端版本；不含源码的工具调用可以完全没有这些字段。重试复用该次操作的参数和结果，不从 Agent 状态推断某个默认版本。

例如：

```json
{
  "name": "source_read_file",
  "arguments": {
    "source_snapshot_id": "snap-42",
    "path": "internal/auth/token.go",
    "start_line": 40,
    "end_line": 42
  }
}
```

Tool Handler 将调用转发给 Source Worker。模型可见的结果示例：

```json
{
  "repository_id": "repo-1",
  "source_snapshot_id": "snap-42",
  "commit_sha": "<full-commit-sha>",
  "path": "internal/auth/token.go",
  "start_line": 40,
  "end_line": 42,
  "content": "func TokenExpired(now, expiresAt time.Time) bool {\n    return !now.Before(expiresAt)\n}\n",
  "truncated": false,
  "result_ref": "s3://.../result.json.zst"
}
```

文件、搜索和 diff 结果按行数、条数及字节数限制输出，并支持继续读取。完整工具结果归档到 MinIO；进入 Context 的内容保持在预算内。

Activity 向 Workflow 返回结果引用和必要元数据，模型请求组装 Activity 再按引用读取内容。源码正文和大工具结果不进入 Temporal History。

## 5. 消息和上下文存储

### 5.1 存储职责

```text
MinIO / S3
  events/{tenant}/{agent}/segment-*.ndjson.zst
  contexts/{tenant}/{agent}/snapshot-*.json.zst
  contexts/{tenant}/{agent}/delta-*.ndjson.zst
  llm/{agent}/{step}/request.json.zst
  llm/{agent}/{step}/response.json.zst
  tools/{agent}/{operation}/result.json.zst
  git/{tenant}/{repository}/bundles/base-*.bundle
  git/{tenant}/{repository}/bundles/increment-*.bundle
  git/{tenant}/{repository}/manifests/{manifest_id}.json

PostgreSQL
  agent_sessions / agents
  session_resource_bindings / agent_resource_bindings
  event_index
  agent_head
  context_records / context_source_refs
  compaction_jobs
  idempotency_records
  llm_executions
  tool_executions
  source_repositories
  source_sync_jobs
  source_snapshots
  source_storage_manifests
  outbox

Temporal
  Workflow 状态、重试、Timer、Signal、Child Workflow 和引用
```

消息正文、模型输入输出、源码正文和大工具结果不放 PostgreSQL。PostgreSQL 保存索引、游标、状态、幂等记录、归档依赖和 CAS 版本。

MinIO/S3 中的 Git 归档与 PostgreSQL 中的版本索引共同构成源码持久化来源。本地 bare 仓库作为可重建缓存。

### 5.2 全量事件

MinIO 保存不可变的完整事件；PostgreSQL 保存事件索引：

```json
{
  "event_id": "evt-123",
  "tenant_id": "tenant-1",
  "agent_id": "agent-1",
  "sequence": 1042,
  "type": "tool_result",
  "payload_ref": "s3://.../result.json.zst",
  "repository_id": "repo-1",
  "source_snapshot_id": "snap-42",
  "created_at": "..."
}
```

源码相关事件记录对应的仓库和版本；跨版本 diff 的事件和结果同时记录 `base_snapshot_id` 和 `target_snapshot_id`，以便重放和核对证据。普通模型或子 agent 生命周期事件可以不含源码字段。

```sql
agent_head(
  tenant_id,
  agent_id,
  row_version,
  context_version,
  last_event_sequence,
  context_event_sequence,
  snapshot_record_id,
  next_step_id
)

idempotency_records(
  operation_id,
  status,
  result_ref,
  created_at
)
```

`snapshot_record_id` 指向 Agent Context Snapshot。源码版本属于可选资源和证据引用，不存入通用 head；`context_source_refs` 记录 Context 实际保留的零到多个源码版本。

## 6. Context 和 Compact

Context 不是当前会话的全部历史，而是当前模型真正需要的状态：

```json
{
  "context_version": 42,
  "base_event_seq": 1200,
  "task_requirements": [],
  "constraints": [],
  "decisions": [],
  "current_state": [],
  "open_items": [],
  "errors": [],
  "child_agent_results": [],
  "tool_artifact_refs": [],
  "resource_refs": [
    {"binding_name": "auth_source", "resource_kind": "source_snapshot", "repository_id": "repo-1", "source_snapshot_id": "snap-42"}
  ],
  "source_refs": [
    {"repository_id": "repo-1", "source_snapshot_id": "snap-42"}
  ],
  "recent_messages": []
}
```

`resource_refs` 是已持久化任务输入的引用，`source_refs` 是当前结论依赖的源码证据。两者都可以为空，也可以包含多个仓库或版本；Context 的自身快照与源码快照分别管理。

### 6.1 Compact 触发

在每次模型调用前计算：

```text
context_tokens + new_message_tokens + reserved_output_tokens
    > model_context_limit - safety_margin
```

也可以在 delta 或工具输出超过阈值、即将 Continue-As-New 时触发。

### 6.2 Compact 实现

Compact 使用普通模型调用，不使用 OpenAI remote compact 接口：

```text
读取当前 Context + 未合并的 delta
    -> 加入 compact system prompt
    -> 发送给模型
    -> 模型返回结构化关键点
    -> 校验返回结果及保留结论依赖的资源和证据引用
    -> 用结果生成新的 Context Snapshot
    -> CAS 更新 agent_head
    -> 丢弃旧 Context 指针，但保留全部原始事件
```

Compact 的模型输出示例：

```json
{
  "task_requirements": ["分析 token 刷新逻辑"],
  "constraints": ["只读源码", "结论标明版本和文件位置"],
  "decisions": ["比较 snap-41 与 snap-42 的过期时间判断"],
  "current_state": ["已定位过期边界判断与测试覆盖差异"],
  "open_items": ["确认调用方对过期时间相等时的约定"],
  "errors": [],
  "child_agent_results": [],
  "tool_artifact_refs": ["s3://.../source-diff-result.json.zst"],
  "source_refs": [
    {"repository_id": "repo-1", "source_snapshot_id": "snap-41"},
    {"repository_id": "repo-1", "source_snapshot_id": "snap-42"}
  ],
  "recent_messages": []
}
```

Compact 只保留关键点、状态、源码位置和 artifact 引用。完整源码保存在 Git 对象归档中，完整事件、模型响应和工具结果保存在 MinIO。

服务端保留既有的任务和资源关联，并校验保留结论依赖的仓库、快照、源码位置和 artifact 引用。Compact 不创建、替换或扩大 Agent 的资源集合；模型输出不能改写工具配置或访问权限。无源码任务无需生成 `source_refs`。

`compaction_jobs` 保存 `compaction_id`、`source_row_version`、`source_context_version`、`source_event_sequence`、request/response 引用和目标 Context Snapshot。重试时复用已经保存的模型响应和 snapshot；按源 `row_version` 做 CAS，冲突时将候选标记为 stale，并为最新 head 建立新的 compact 操作。

## 7. Git 增量存储和只读源码快照

### 7.1 存储方案

采用原生 Git 对象、MinIO/S3 Git bundle 归档和 PostgreSQL 快照索引。

每个 commit 指向完整的源码树，模型按版本看到完整源码快照。相同内容复用 Git 对象，pack 对对象提供压缩和 delta 存储。逻辑上的完整快照不要求每次复制整份源码。

| 组件 | 职责 |
|---|---|
| 外部 Git 仓库 | 提供 commit、分支和标签，作为版本导入来源 |
| MinIO/S3 | 持久保存基线 bundle、增量 bundle 和不可变归档清单 |
| PostgreSQL | 保存仓库、同步任务、快照、commit 和归档依赖索引 |
| Source Worker | 同步、归档、恢复缓存和处理只读查询 |
| 本地 bare 仓库 | 缓存 Git 对象和 refs，可从持久化归档重建 |

### 7.2 版本同步和发布

同步范围由仓库配置指定，包括需要跟踪的 refs 和历史范围。

```text
首次同步外部 Git 仓库
    -> 获取目标 refs 及范围内快照所需的 Git 对象
    -> 建立本地 bare 仓库缓存
    -> 生成自包含的 base.bundle
    -> 上传 bundle 和 manifest，校验完整性
    -> PostgreSQL 事务记录归档索引和 source_snapshots
    -> 发布 ready 快照

后续同步
    -> git fetch 获取新的 refs 和所需对象
    -> 基于已持久化对象生成增量 bundle
    -> 上传 bundle 和 manifest，校验前置依赖与完整性
    -> PostgreSQL 事务登记新快照，并以 CAS 更新归档 head
    -> 发布 ready 快照
```

以同步批次生成增量 bundle，一批可以包含多个 commit。已同步的 commit 通过唯一约束去重；没有新增对象时可复用已有归档。源码只在外部仓库更新后导入，模型读取不会产生新版本。

只有对象归档完成、恢复依赖有效且数据库事务提交成功，快照才进入 `ready` 状态并对模型可见。上传成功而事务失败时，重试复用已上传归档，避免发布缺少对象的快照。

同一仓库的同步归档串行执行，并对归档 head 使用 CAS。发生竞争时基于最新已持久化清单重新计算增量。

### 7.3 快照和归档元数据

```sql
source_snapshots(
  snapshot_id,
  repository_id,
  commit_sha,
  tree_sha,
  storage_manifest_id,
  status,
  created_at
)

source_storage_manifests(
  manifest_id,
  repository_id,
  parent_manifest_id,
  bundle_kind,
  bundle_ref,
  bundle_checksum,
  prerequisite_commit_shas,
  operation_id,
  status,
  created_at
)
```

`(repository_id, commit_sha)` 唯一标识源码版本，`snapshot_id` 是模型和 Agent 使用的稳定标识。分支和标签可能移动，其观测结果作为同步元数据保存。

`storage_manifest_id` 指向能够恢复该快照的归档清单。清单保存 bundle 引用、校验值和前置依赖；`parent_manifest_id` 表示归档恢复链，与 Git commit 的父提交关系分别管理。Git 的分支和 merge 历史保持原样。

`source_repositories` 保存仓库配置和当前归档 head；`source_sync_jobs` 保存稳定的同步标识、operation_id、同步状态和结果引用。

### 7.4 快照读取

Source Worker 直接根据目标 commit 查询 bare 仓库中的 tree/blob 对象。目录和文件读取使用 `git ls-tree`、`git cat-file` 等原生能力，搜索和 diff 同样绑定具体 commit。

查询从已经就绪的本地对象缓存读取；缓存缺失时先恢复所需归档。模型通过只读工具按需获取目录、文件片段、搜索结果和版本差异，无需为每个版本或 Agent 创建工作副本。

所有源码证据保留 repository、snapshot、commit、path 和行范围。跨版本结果同时保留两个版本标识。

### 7.5 缓存恢复和版本保留

```text
source_snapshot_id
    -> 读取快照与 storage_manifest_id
    -> 解析基线和所需增量包的依赖顺序
    -> 下载、校验并导入本地 bare 仓库
    -> 校验目标 commit 及对象依赖完整性
    -> 恢复指定版本的只读查询
```

为保留的快照维护内部 refs，并将它们纳入归档范围。上游跟踪 refs 与快照保留 refs 分开管理，确保上游分支删除或强制更新后，已保留快照仍可读取。

可以定期生成覆盖保留快照的新基线，缩短增量恢复链。保留范围包括会话和 Agent 的资源关联，以及 Context、工具执行、事件和结果中仍使用的版本。旧归档只有在不再被任何保留快照或恢复清单依赖时才能回收。

### 7.6 源码模块目录

```text
cmd/source-worker/main.go

internal/source/
  sync.go          # 外部 Git 仓库同步
  snapshot.go      # 快照索引与状态
  reader.go        # 目录、文件、搜索和 diff
  archive.go       # 基线、增量归档与恢复
  git/             # 原生 Git 操作封装
  activity/        # Temporal Activities
```

Source Worker 通过 Go 封装原生 Git 操作，存储适配和依赖组装沿用公共基础设施模块。

## 8. 宕机恢复和幂等

所有可重试操作都有稳定的 `operation_id`：

```text
Agent 工具操作：agent_id + step_id + tool_call_id + operation_type
仓库同步操作：repository_id + sync_id + operation_type
```

操作标识在重试和 Continue-As-New 后保持不变。一次模型返回的多个工具调用使用各自的 `tool_call_id` 区分。

Worker 宕机后：

```text
Temporal 重放 Workflow，恢复已记录的执行状态
    -> 通过 Activity 读取 agent_head
    -> 读取最新 Context Snapshot 和后续 delta
    -> 按 tenant_id / agent_id 读取已持久化的可选资源关联
    -> 检查未完成 operation_id
    -> 已有结果：直接使用
    -> 没有结果：读取该操作已保存的参数，必要时恢复 Git 缓存，再重试工具或子 agent 调用
    -> 继续模型循环
```

Temporal Workflow 只保存引用和执行状态；数据库、对象存储、模型和 Git I/O 通过 Activity 完成。

```go
type AgentRuntimeState struct {
    TenantID            string
    SessionID           string
    AgentID             string
    ContextVersion      int64
    LastEventSequence   int64
    PendingOperationID  string
    NextStepID          int64
    StepCount           int
}
```

OpenAI request、response、function call 和 function result 在调用前后都持久化。重试前先查 `idempotency_records`，复用已保存的模型响应和工具结果；子 Workflow 使用由稳定操作标识派生的 Workflow ID。

Source Worker 重试同步时复用已经校验的 bundle 和 manifest。Agent 恢复既有源码操作时使用该操作已保存的具体版本，独立于此时上游分支的最新状态。后续新工具调用可以显式选择其他获准的 ready 快照，不改变先前操作和证据的版本。

## 9. Temporal Continue-As-New

Context Compact 和 Temporal History Compact 是两件事。

Temporal History 过大时执行 Continue-As-New，只携带：

```go
type ContinueState struct {
    TenantID            string
    SessionID           string
    AgentID             string
    ContextVersion      int64
    LastEventSequence   int64
    PendingOperationID  string
    NextStepID          int64
}
```

新 Run 根据 `agent_head`、Context Snapshot、delta 和该 Agent 的资源关联继续执行。未完成工具仍使用已保存的版本参数，已有源码证据保留原引用。`NextStepID` 延续逻辑步骤编号，避免新 Run 重用已有操作标识。

## 10. Worker 划分

| Worker | 职责 |
|---|---|
| Agent Worker | Agent Workflow、模型调用、tool loop、Context 和子 agent |
| Source Worker | 外部 Git 同步、增量归档、快照发布、缓存恢复和只读源码工具 |
| Relay Worker | 事件索引、outbox、失败重试和通知 |

所有 Worker 必须支持 Activity retry、超时、取消和 `operation_id` 幂等。

当前阶段的源码能力统一由 Source Worker 承担，源码工具仅开放只读查询。子 agent 工具由 Agent Worker 调度；无源码资源的任务也可运行模型循环。GitHub 等 Git 托管平台作为上游版本来源接入。

## 11. 核心职责边界

```text
模型             = 决定如何读取、搜索和比较源码，以及是否调用子 agent
Agent Workflow   = 驱动通用模型循环、保存状态、恢复执行和管理子 agent
Tool Dispatcher  = 校验工具与资源访问、保存具体调用参数，并路由到 Worker 或 Child Workflow
Source Worker    = 同步外部版本、增量归档、恢复缓存并提供只读源码查询
Child Workflow   = 使用自己的任务、Context、工具配置和可选资源运行独立模型循环
MinIO / S3       = 全量事件、Context、工具结果、模型 I/O 和 Git 归档
PostgreSQL       = 索引、状态、幂等、CAS、可选资源关联、源码快照和归档依赖
Git              = 不可变源码版本、tree/blob 对象及 pack 存储
Temporal         = Workflow 执行、重试和恢复
外部 Git 仓库    = 源码版本导入来源
```
