# Sentinel

基于 Go、Gin、Temporal、PostgreSQL、MinIO/S3 和 OpenAI API 的只读源码分析系统。
设计依据见 [提案](docs/PROPOSAL.md)。已实现 Agent 模型循环、独立子 Agent、持久化恢复，以及 GitHub 源码拉取和增量归档。

## 目录

```text
compose.yaml              本地 PostgreSQL、MinIO、Temporal
deploy/minio/             MinIO 和 mc 的固定版本源码构建
docs/DEVELOPMENT.md        中间件配置、初始化及启动说明
docs/SCHEMA.md             数据表、约束及发布事务约定
docs/AGENT_RUNTIME.md      Agent 启动、SDK 调用与恢复边界
docs/SOURCE_SYNC.md        GitHub 仓库导入、增量同步及恢复
cmd/
  api/                    Gin HTTP API 入口
  agent/                  创建、等待或重新连接根 Agent 的 CLI
  migrate/                数据库迁移入口（up / status / down）
  source-sync/            创建、等待或重新连接源码同步任务的 CLI
  agent-worker/           Agent Worker 入口
  source-worker/          Source Worker 入口
  relay-worker/           Relay Worker 入口
internal/
  app/                    依赖组装和进程生命周期
  api/                    Gin 路由、handler 和中间件
  agent/
    activity/             模型请求、工具派发、Context 和检查点
    workflow/             通用 Agent / Child Workflow
    state.go              会话输入、恢复及 Continue-As-New 状态
    resource.go           可选资源类型和资源读取接口
  agentctx/               Context head、CAS 和 compact 边界
  eventlog/               完整事件及索引接口
  source/
    activity/             源码同步、归档恢复及发布
    workflow/             外部仓库同步 Workflow
    git/                  原生 Git / bare 仓库和 bundle 适配
    sync.go               外部仓库和同步契约
    snapshot.go           快照身份、状态及版本索引接口
    reader.go             目录、文件、搜索和版本差异接口
    archive.go            bundle 清单及恢复接口
  relay/                  outbox、事件索引和重试实现位置
  llm/openai/             官方 OpenAI SDK Responses 适配
  payload/                租户隔离的不可变正文存储接口
  platform/
    config/               环境变量配置及校验
    temporal/             Temporal 连接、注册及 Worker 生命周期
  storage/
    postgres/             迁移、Agent / 源码事务、执行记录和对象索引
    s3/                   MinIO/S3 客户端、压缩正文和流式 bundle 存储
```

## 启动中间件

需要 Docker Engine（Linux containers）和 Docker Compose V2。在项目根目录执行：

```powershell
docker compose --env-file .env.example up -d --build --wait postgres minio temporal
docker compose --env-file .env.example run --build --rm minio-init
```

PostgreSQL、MinIO 和 Temporal 均使用命名卷持久化。初始化任务创建私有 `sentinel` bucket。
Temporal UI 为 `http://127.0.0.1:8233`，MinIO Console 为 `http://127.0.0.1:9001`。
MinIO 使用固定源码版本构建，首次启动需要下载和编译依赖。
完整配置、默认凭据、环境变量和停止方式见 [本地开发环境](docs/DEVELOPMENT.md)。

## 初始化数据库

中间件启动后，显式执行迁移：

```powershell
$env:POSTGRES_DSN = "postgres://sentinel:sentinel-dev-password@127.0.0.1:5432/sentinel?sslmode=disable"
go run ./cmd/migrate up
go run ./cmd/migrate status
```

首个迁移建立 17 张业务表，覆盖源码归档、Agent / Context、执行记录、对象索引、幂等和 outbox。
第二个迁移解除 Agent 对单一仓库 / 快照的绑定，新增两张可选资源关联表，当前共 19 张业务表。
迁移采用 Goose，嵌入程序、按版本执行、使用事务和 PostgreSQL advisory lock；API / Worker 不自动迁移。
表结构和事务边界见 [Schema](docs/SCHEMA.md)，回滚及集成测试见 [本地开发环境](docs/DEVELOPMENT.md)。

## 启动 API

当前模块名为 `sentinel`，使用 Go 1.27.3 初始化和验证。API 可独立启动：

```powershell
go run ./cmd/api
```

在另一个终端验证：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/healthz
```

返回：

```json
{"service":"sentinel-api","status":"ok"}
```

`/healthz` 只表示 HTTP 进程存活。当前尚未暴露业务 API，也没有声明数据库或任务执行就绪。
API 带有 JSON 错误响应、请求日志、panic recovery、HTTP 超时和优雅退出。

程序直接读取进程环境变量。[.env.example](.env.example) 是配置清单，不会自动加载。
例如在 PowerShell 中修改监听地址：

```powershell
$env:HTTP_ADDR = "127.0.0.1:9090"
$env:GIN_MODE = "release"
go run ./cmd/api
```

## Worker 入口

先准备可访问的 Temporal Server，再分别启动需要的进程：

```powershell
go run ./cmd/agent-worker
go run ./cmd/source-worker
go run ./cmd/relay-worker
```

这些命令应分别在独立终端运行。默认连接 `127.0.0.1:7233`、namespace `default`，
分别使用 `sentinel-agent`、`sentinel-source`、`sentinel-relay` task queue。

Agent / Source Worker 已注册各自的 Workflow / Activity，会轮询任务。启动前需要执行数据库迁移、创建 S3 bucket，
并配置 PostgreSQL、S3 和 Temporal。Agent Worker 还需 OpenAI 配置；Source Worker 需要本机 Git。
Relay Worker 仍只验证 Temporal 连接并等待退出。
统一运行器负责启动、错误传播和停止 Worker。

配置好相同环境变量后，可在另一个终端创建任务：

```powershell
go run ./cmd/agent -tenant "0194e229-8576-7f42-8f08-3496913847cf" -task "说明只读源码分析可以怎样拆分任务" -wait
```

CLI 先输出持久化的会话、Agent 和 Workflow ID，`-wait` 再输出结果引用和摘要。
完整环境配置、子 Agent 机制和恢复方法见 [Agent 运行说明](docs/AGENT_RUNTIME.md)。

Source Worker 启动后，在配置相同中间件的另一个终端导入公开 GitHub 仓库：

```powershell
go run ./cmd/source-sync -tenant "0194e229-8576-7f42-8f08-3496913847cf" -url "https://github.com/octocat/Hello-World" -wait
```

CLI 输出仓库、同步和 Workflow ID；`-wait` 等待归档发布并输出本次新增的快照。
后续用 `-repository` 同步同一仓库，用 `-resume` 重新连接已有任务。
私有仓库凭据、跟踪分支和增量恢复说明见 [源码同步](docs/SOURCE_SYNC.md)。

## 源码和存储边界

- 从外部 Git 仓库同步 commit，以仓库 ID 和完整 commit SHA 标识不可变版本。
- 本地 bare 仓库是可重建缓存；MinIO/S3 保存基线和增量 Git bundle、manifest。
- PostgreSQL 保存快照索引、状态、归档依赖、CAS 和幂等记录。
- Agent 和子 Agent 各自选择零到多个仓库、快照或文档资源，运行不要求绑定源码。
- 模型通过明确的快照 ID 查看目录、文件、搜索及 diff；每次源码调用保存实际版本以便重试和核对证据。
- Workflow 只传递状态和引用，源码正文、模型 I/O 与大工具结果保存在对象存储。

模型请求通过已有的官方 `openai-go/v3` SDK 发往 Responses API。请求、原始响应、工具结果和 Context
保存在 S3，PostgreSQL 保存引用、幂等状态、租约、检查点及 outbox。Temporal History 只携带引用和游标。
当前工具集提供 `call_child_agent`；GitHub 同步和归档已实现，源码读取工具、Context compact、outbox 消费和业务 API
尚未实现。资源绑定不会自动读取资源正文。创建 SDK 客户端不会发起模型请求。

## 开发检查

```powershell
go test ./...
go vet ./...
go build ./cmd/...
```

依赖文件统一通过 `go mod init`、`go get`、`go mod tidy` 等 Go 命令管理。
Go 使用其默认 build/module 缓存目录。
