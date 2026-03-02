# Sentinel

基于 Go、Gin、Temporal、PostgreSQL、MinIO/S3 和 OpenAI API 的只读源码分析系统。
设计依据见 [提案](docs/PROPOSAL.md)。当前为项目骨架。

## 目录

```text
compose.yaml              本地 PostgreSQL、MinIO、Temporal
deploy/minio/             MinIO 和 mc 的固定版本源码构建
docs/DEVELOPMENT.md        中间件配置、初始化及启动说明
cmd/
  api/                    Gin HTTP API 入口
  agent-worker/           Agent Worker 入口
  source-worker/          Source Worker 入口
  relay-worker/           Relay Worker 入口
internal/
  app/                    依赖组装和进程生命周期
  api/                    Gin 路由、handler 和中间件
  agent/
    activity/             模型调用和工具执行注册位置
    workflow/             Agent / Child Workflow 实现位置
    state.go              会话输入、恢复及 Continue-As-New 状态
  agentctx/               Context head、CAS 和 compact 边界
  eventlog/               完整事件及索引接口
  source/
    activity/             源码同步及只读查询 Activity 实现位置
    workflow/             外部仓库同步 Workflow 实现位置
    git/                  原生 Git / bare 仓库适配位置
    sync.go               外部仓库和同步契约
    snapshot.go           快照身份、状态及版本索引接口
    reader.go             目录、文件、搜索和版本差异接口
    archive.go            bundle 清单及恢复接口
  relay/                  outbox、事件索引和重试实现位置
  llm/openai/             OpenAI SDK 客户端配置
  platform/
    config/               环境变量配置及校验
    temporal/             Temporal 连接、注册及 Worker 生命周期
  storage/
    postgres/             PostgreSQL 连接池
    s3/                   MinIO/S3 客户端
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

当前 Worker 仅验证 Temporal 连接并等待退出，尚未注册 Workflow/Activity，因此不轮询任务。
业务实现完成后，在 `internal/app/worker.go` 为各入口传入注册函数。
统一运行器负责启动、错误传播和停止 Worker。

## 源码和存储边界

- 从外部 Git 仓库同步 commit，以仓库 ID 和完整 commit SHA 标识不可变版本。
- 本地 bare 仓库是可重建缓存；MinIO/S3 保存基线和增量 Git bundle、manifest。
- PostgreSQL 保存快照索引、状态、归档依赖、CAS 和幂等记录。
- 模型仅通过快照 ID 查看版本、目录、文件、搜索及 diff；会话默认版本保持固定。
- Workflow 只传递状态和引用，源码正文、模型 I/O 与大工具结果保存在对象存储。

当前这些是领域类型与接口。Git 同步、归档、数据表迁移、源码读取、模型循环、Context compact、
持久化幂等和 outbox 的业务实现尚未加入。PG、S3、OpenAI 提供独立客户端工厂，尚未接入 API/Worker；
创建 OpenAI 客户端不会发起请求，后续使用时需明确配置 `OPENAI_API_KEY` 和 `OPENAI_MODEL`。

## 开发检查

```powershell
go test ./...
go vet ./...
go build ./cmd/...
```

依赖文件统一通过 `go mod init`、`go get`、`go mod tidy` 等 Go 命令管理。
Go 使用其默认 build/module 缓存目录。
