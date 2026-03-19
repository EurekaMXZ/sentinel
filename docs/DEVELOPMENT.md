# 本地开发环境

在项目根目录执行以下命令。需要 Docker Engine（Linux containers）和 Docker Compose V2；
Windows 可使用 Docker Desktop。API 和三个 Worker 仍通过本机 Go 启动。

## 中间件

| 服务 | 本机地址 | 用途 |
| --- | --- | --- |
| PostgreSQL 17 | `127.0.0.1:5432` | 业务元数据、快照索引、CAS、幂等记录 |
| MinIO S3 | `127.0.0.1:9000` | 源码 bundle、manifest、Context 和事件正文 |
| MinIO Console | `http://127.0.0.1:9001` | 查看 bucket 和对象 |
| Temporal gRPC | `127.0.0.1:7233` | Workflow 和 Activity 调度 |
| Temporal UI | `http://127.0.0.1:8233` | 查看 Workflow、重试和执行历史 |

全部发布端口仅绑定 `127.0.0.1`。当前架构不需要额外启动 Redis、Kafka 或 Elasticsearch。

Temporal 使用官方 CLI 镜像的 `server start-dev`，自动创建 `default` namespace 并启动 UI。
SQLite 数据库保存在命名卷中，容器重建后仍可恢复。这与保存业务元数据的 PostgreSQL 是两套独立存储。
本配置用于本地开发；生产部署应使用独立的 Temporal 服务或 Temporal Cloud。

MinIO 和 `mc` 从 Dockerfile 中固定的官方源码标签构建，避免依赖已无法拉取的旧预编译镜像。
首次构建需要访问 GitHub、Go 模块代理和 Alpine 软件源，耗时会比拉取镜像长。
两项服务共享 Docker 构建缓存，不会修改本机的 `go.mod`、`go.sum` 或 Go 缓存配置。

## 启动

直接使用仓库中的开发配置：

```powershell
docker compose --env-file .env.example config --quiet
docker compose --env-file .env.example up -d --build --wait postgres minio temporal
docker compose --env-file .env.example run --build --rm minio-init
```

第一条命令检查配置；第二条等待三个长期服务的健康检查通过；第三条创建默认 `sentinel` bucket，
重复运行会保留已有 bucket 和对象。bucket 默认私有，不设置匿名访问权限。
初始化返回非零退出码时应先检查错误，修复后重新运行该命令。

也可以在项目根目录创建 `.env` 并填写本地配置，然后省略所有命令中的 `--env-file .env.example`。
`.env` 已被 Git 忽略；`.env.example` 内的凭据只用于本地开发。

执行 `docker compose --env-file .env.example up -d --build` 会启动所有服务，并在 MinIO 健康后自动运行
`minio-init`。它成功后退出是正常行为。使用 `--wait` 时按上面的方式分开启动长期服务和初始化任务。

默认连接信息：

| 配置 | 值 |
| --- | --- |
| PostgreSQL database / user | `sentinel` / `sentinel` |
| PostgreSQL password | `sentinel-dev-password` |
| MinIO access key | `sentinel-dev` |
| MinIO secret key | `sentinel-dev-secret` |
| S3 bucket | `sentinel` |
| Temporal namespace | `default` |

`minio-init` 只创建 bucket；PostgreSQL 镜像创建业务数据库和用户，业务表由下面的迁移命令建立。

## 数据库迁移

迁移通过独立 Go 入口执行，只需要 PostgreSQL。只启动数据库时可以执行：

```powershell
docker compose --env-file .env.example up -d --wait postgres
$env:POSTGRES_DSN = "postgres://sentinel:sentinel-dev-password@127.0.0.1:5432/sentinel?sslmode=disable"
go run ./cmd/migrate up
go run ./cmd/migrate status
```

`up` 执行所有待应用版本，重复执行不会重新建表。`status` 显示每个内嵌迁移的状态，
首次执行时会初始化 `sentinel_schema_version` 版本表，但不创建业务表。
默认命令超时为 5 分钟；需要调整时使用 `go run ./cmd/migrate -timeout 10m up`。
迁移期间使用 PostgreSQL 会话级 advisory lock，最多等待锁 60 秒，每个 SQL 迁移在事务中执行。
API 和 Worker 启动不会隐式更改 schema，应在发布应用前单独执行 `up`。

回滚最近一个版本：

```powershell
go run ./cmd/migrate down
```

当前最新版本为 2，共 19 张业务表。版本 2 的 `down` 会将可兼容的数据恢复为版本 1 的单仓库结构，
要求每个会话和 Agent 只有一个名为 `default_source` 的快照资源，并且关联的仓库、工具、事件和
Context 证据符合旧结构。无资源、多资源或跨仓库数据无法回退时，迁移会在事务中报错，保留版本 2。

继续回滚首个版本会删除全部 17 张业务表及其数据，只应对确认可以丢弃这些数据的库执行。
对象存储和 Temporal 数据不随数据库回滚删除。后续变更增加新的编号迁移，不修改已发布版本。
数据表和需要由业务事务保证的规则见 [SCHEMA.md](SCHEMA.md)。

数据库集成测试需要一个可用的 PostgreSQL 17 测试服务，连接用户须有 `CREATEDB` 权限：

```powershell
$env:SENTINEL_TEST_POSTGRES_DSN = "postgres://sentinel:sentinel-dev-password@127.0.0.1:5432/sentinel?sslmode=disable"
go test -tags=integration -count=1 -v ./internal/storage/postgres
```

测试创建随机命名的独立数据库，验证并发迁移、重复执行、旧数据资源回填、兼容回滚与不兼容回滚保护、
无源码资源的父子 Agent、多仓库 / 多版本资源、关联约束、模型请求恢复、工具结果、租约 fencing、CAS、outbox 行锁，
以及 Git 基线 / 增量恢复、强推后版本保留、源码发布重试和损坏归档拦截，
结束后删除这个测试库。连接 URI 中指定的已有数据库只用于建立管理连接，不应用业务迁移。
普通 `go test ./...` 不需要数据库。

## 启动 Go 进程

Compose 的环境文件仅用于容器配置插值，不会把变量注入宿主机终端。
Go 程序直接读取进程环境变量，不自动加载 `.env`。使用默认本地中间件时，在对应终端设置：

```powershell
$env:POSTGRES_DSN = "postgres://sentinel:sentinel-dev-password@127.0.0.1:5432/sentinel?sslmode=disable"
$env:S3_ENDPOINT = "127.0.0.1:9000"
$env:S3_BUCKET = "sentinel"
$env:S3_ACCESS_KEY = "sentinel-dev"
$env:S3_SECRET_KEY = "sentinel-dev-secret"
$env:S3_USE_TLS = "false"
$env:TEMPORAL_ADDRESS = "127.0.0.1:7233"
$env:TEMPORAL_NAMESPACE = "default"
$env:GIN_MODE = "release"
go run ./cmd/api
```

Agent Worker 和 Agent CLI 还需要 OpenAI 配置：

```powershell
$env:OPENAI_API_KEY = "填入 API key"
$env:OPENAI_MODEL = "填入可访问的模型 ID"
$env:OPENAI_CONTEXT_WINDOW_TOKENS = "128000"
$env:OPENAI_CONTEXT_SAFETY_TOKENS = "8192"
```

`OPENAI_CONTEXT_WINDOW_TOKENS` 和 `OPENAI_CONTEXT_SAFETY_TOKENS` 定义自动 Context compact 的预算；
`OPENAI_COMPACT_STRUCTURED_OUTPUT=false` 可关闭 Compact 请求的 JSON Schema 输出。
未配置或无法访问模型时 API、Source Worker 和 Relay Worker 仍可独立启动。

`S3_ENDPOINT` 不带 `http://` 前缀。更改 `.env` 的数据库或 MinIO 凭据时，也应同步修改终端环境变量；
数据库用户名、密码或库名包含 URI 特殊字符时，DSN 中需要 URL 编码。

分别在独立终端启动需要的 Worker：

```powershell
go run ./cmd/agent-worker
go run ./cmd/source-worker
go run ./cmd/relay-worker
```

每个终端需要各自设置所需环境变量。Agent Worker 已接入 PG、S3 和官方 OpenAI SDK，
注册通用 Agent Workflow 和模型、工具、检查点等 Activity。Source Worker 已接入原生 Git、PG 和 S3，
注册源码同步及失败清理 Activity；它需要安装 Git，并可通过 `GIT_BINARY` 指定可执行文件。
Relay Worker 仍只验证 Temporal 连接并等待退出。
API 的 `/healthz` 仅表示 HTTP 进程存活；业务 HTTP API 尚未加入。

模型接口使用外部服务。启动 Agent Worker 和 Agent CLI 的终端还须设置 `OPENAI_API_KEY`、`OPENAI_MODEL`；
需要自定义 Responses API 地址时设置 `OPENAI_BASE_URL`，无需为它增加 Compose 服务。
创建任务、等待结果及恢复已有 Agent 的命令见 [AGENT_RUNTIME.md](AGENT_RUNTIME.md)。
源码导入、后续同步和私有 GitHub 仓库凭据配置见 [SOURCE_SYNC.md](SOURCE_SYNC.md)。

若以后把 Go 进程放进同一个 Compose 网络，连接地址改用容器服务名：

| 配置 | 容器内地址 |
| --- | --- |
| PostgreSQL DSN host | `postgres:5432` |
| `S3_ENDPOINT` | `minio:9000` |
| `TEMPORAL_ADDRESS` | `temporal:7233` |

## 检查和停止

```powershell
docker compose --env-file .env.example ps -a
docker compose --env-file .env.example logs --tail 100 postgres minio temporal
docker compose --env-file .env.example exec temporal temporal operator cluster health
docker compose --env-file .env.example exec postgres psql -U sentinel -d sentinel -c "select 1;"
Invoke-WebRequest http://127.0.0.1:9000/minio/health/ready
```

访问上表中的 MinIO Console 和 Temporal UI 地址即可查看管理界面。
API 启动后可用 `Invoke-RestMethod http://127.0.0.1:8080/healthz` 验证 HTTP 进程。

```powershell
docker compose --env-file .env.example down
```

普通 `down` 保留 `postgres-data`、`minio-data`、`temporal-data` 三个命名卷。
不要为日常停止添加 `--volumes`，该选项会删除上述持久数据。
PostgreSQL 的 `POSTGRES_*` 初始化变量只对空数据目录生效；已有数据库修改密码应通过 SQL 完成，
仅修改 `.env` 不会改变已有数据库用户的密码。

启动失败时先查看对应服务日志。端口冲突时，调整 `compose.yaml` 的宿主机端口并同步修改
Go 环境变量；容器内部连接仍使用服务名和原来的容器端口。

## 参考

- [Temporal CLI 开发服务](https://docs.temporal.io/cli/setup-cli)
- [Compose 启动顺序和健康检查](https://docs.docker.com/compose/how-tos/startup-order/)
- [MinIO 固定源码版本](https://github.com/minio/minio/tree/RELEASE.2025-10-15T17-29-55Z)
- [MinIO Client 固定源码版本](https://github.com/minio/mc/tree/RELEASE.2025-08-13T08-35-41Z)
