# 控制 API

控制 API 用于创建和查询 Agent、管理源码仓库与同步任务。当前版本不提供认证或租户管理：
每个 `/v1` 请求都必须带 `X-Tenant-ID` 头，内容是调用方声明的非零 UUID。该头是管理入口的
显式输入，不是授权机制；API 默认只监听 `127.0.0.1:8080`，不要直接暴露到不可信网络。

API 需要 PostgreSQL、MinIO/S3、Temporal 和 `OPENAI_MODEL`。它不调用模型，因此不需要
`OPENAI_API_KEY`；Agent Worker 仍需该 key。启动和迁移步骤见 [本地开发环境](DEVELOPMENT.md)。

## 通用约定

- 请求和响应均为 `application/json`，请求体上限 1 MiB。
- 错误响应：

```json
{"error":"human-readable message","code":"machine_readable_code"}
```

- `/healthz` 只表示 HTTP 进程存活。
- 未实现认证、资源上传、事件/Context 查询、outbox 投递和 Web UI。

## Agent

### 创建根 Agent

```http
POST /v1/agents
X-Tenant-ID: 0194e229-8576-7f42-8f08-3496913847cf
Content-Type: application/json
```

```json
{
  "task": "分析认证模块并给出问题、证据和建议",
  "context_ref": "可选的同租户 verified 对象 UUID",
  "expected_output": "可选的结果格式说明",
  "resources": [
    {
      "binding_name": "auth_source",
      "resource_kind": "source_snapshot",
      "repository_id": "仓库 UUID",
      "source_snapshot_id": "快照 UUID"
    }
  ],
  "max_steps": 64,
  "max_child_depth": 3
}
```

`resources` 可为空。资源形状在 Agent Worker 校验，快照必须属于该租户且处于 `ready` 状态。
`max_steps` 和 `max_child_depth` 可选，使用服务端默认限制。

返回 `202 Accepted`：

```json
{
  "tenant_id": "...",
  "session_id": "...",
  "agent_id": "...",
  "workflow_id": "sentinel/.../agent/...",
  "status": "pending",
  "row_version": 0,
  "context_version": 0,
  "last_event_sequence": 0,
  "next_step_id": 1,
  "created_at": "..."
}
```

任务在调度 Workflow 前已持久化。如果响应为 `502 workflow_start_failed`，仍可用返回的
`agent_id` 调用 resume。

### 查询

```http
GET /v1/agents/{agent_id}
GET /v1/agents?limit=50
```

状态响应包含 `status`、`row_version`、`context_version`、`last_event_sequence`、`next_step_id`、
`result_ref`、`error_ref`、`created_at`、`started_at`、`finished_at` 和资源绑定。
`status` 可为 `pending`、`running`、`waiting`、`completed`、`failed`、`cancelled`。

### 读取结果

```http
GET /v1/agents/{agent_id}/result
```

完成时返回：

```json
{
  "agent_id": "...",
  "status": "completed",
  "result_ref": "stored_objects UUID",
  "summary": "...",
  "response_ref": "原始模型响应对象 UUID",
  "artifacts": [],
  "source_refs": [
    {"repository_id": "...", "source_snapshot_id": "..."}
  ]
}
```

失败或取消时返回 `error_ref` 和持久化的错误对象。仍在运行时返回
`409 agent_not_finished`。结果正文保存在对象存储，API 只做租户范围读取。

### 取消与恢复

```http
POST /v1/agents/{agent_id}/cancel
POST /v1/agents/{agent_id}/resume
```

- cancel 返回 `202 {"status":"cancelling"}`；Workflow 收到取消后写入终态。已完成、失败或已取消的
  Agent 返回 `409 agent_terminal`。Workflow 尚未启动时返回 `409 agent_workflow_not_started`。
- resume 连接或重新调度已持久化的根 Agent；不会替换任务、资源或限制。已完成时返回当前状态，
  失败/取消时返回 `409 agent_terminal`，其他情况返回 `202`。
- cancel/resume 只接受根 Agent；子 Agent 由父 Workflow 管理。

## 源码仓库

### 注册仓库

```http
POST /v1/source/repositories
X-Tenant-ID: ...
```

```json
{
  "url": "https://github.com/owner/repository",
  "tracked_refs": ["refs/heads/*", "refs/tags/*"],
  "credential_ref": "env:GITHUB_TOKEN",
  "object_format": "sha1"
}
```

- `url` 只接受规范化前的 GitHub HTTPS 地址；保存时会归一化为 `https://github.com/owner/repository.git`。
- `tracked_refs` 省略时跟踪所有分支和标签。
- `credential_ref` 只保存引用（当前支持 `env:变量名`）；token 由 Source Worker 环境解析。
- `object_format` 为 `sha1` 或 `sha256`，默认 `sha1`。

返回 `201` 和仓库记录，包含 `repository_id`、`archive_head_id`、`archive_version` 等字段。

```http
GET /v1/source/repositories
GET /v1/source/repositories/{repository_id}
```

### 触发同步

```http
POST /v1/source/repositories/{repository_id}/syncs
```

返回 `202`：

```json
{
  "tenant_id": "...",
  "repository_id": "...",
  "sync_id": "...",
  "workflow_id": "source/.../...",
  "status": "queued"
}
```

同一仓库同时只允许一个活动同步；已有活动同步时 `ClaimSync` 会按租约和 fencing 处理。
仓库不是 `active` 时返回 `409 repository_not_active`。

### 查询同步

```http
GET /v1/source/syncs/{sync_id}
```

```json
{
  "tenant_id": "...",
  "repository_id": "...",
  "sync_id": "...",
  "workflow_id": "...",
  "status": "succeeded",
  "result_ref": "...",
  "manifest_id": "...",
  "snapshot_count": 2,
  "snapshots": [
    {
      "source_snapshot_id": "...",
      "repository_id": "...",
      "commit_sha": "...",
      "tree_sha": "...",
      "storage_manifest_id": "...",
      "status": "ready",
      "created_at": "..."
    }
  ]
}
```

`status` 可为 `queued`、`running`、`archiving`、`publishing`、`succeeded`、`failed`、`cancelled`。
失败或取消时包含 `error_ref` 和持久化的错误对象。

```http
POST /v1/source/syncs/{sync_id}/resume
```

成功时返回当前状态；失败/取消返回 `409 sync_terminal`；其他情况重新连接或重新调度已持久化的
Workflow，返回 `202`。

### 列出就绪版本

```http
GET /v1/source/repositories/{repository_id}/versions?offset=0&limit=50
```

```json
{
  "repository_id": "...",
  "versions": [ ... ],
  "more": true,
  "offset": 0,
  "limit": 50,
  "next_offset": 50
}
```

只返回 `ready` 快照，按发布时间倒序。`limit` 最大 200。

## 状态码

| 状态 | 含义 |
| --- | --- |
| 200 | 查询成功，或 resume 的 Agent/Sync 已完成 |
| 201 | 仓库创建成功 |
| 202 | Agent、同步或 resume 已调度 |
| 400 | 缺少 `X-Tenant-ID`、UUID/参数/JSON 非法、资源不可用 |
| 404 | Agent、仓库或同步不存在（含跨租户） |
| 409 | 资源终态、Agent 未完成、Workflow 未启动、仓库非 active |
| 500 | 数据库或对象存储读取失败 |
| 502 | Temporal Workflow 调度/取消失败；实体已持久化时可用 resume 恢复 |

## 后续

认证、租户管理、资源/文档上传、事件与 Context 查询、cancel/resume 的子 Agent 控制、
outbox 通知投递和 OpenAPI 描述尚未实现。
