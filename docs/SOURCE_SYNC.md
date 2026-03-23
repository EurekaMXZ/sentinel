# GitHub 源码同步

源码通过外部 GitHub 仓库的 commit 导入。Source Worker 使用本机原生 Git 拉取配置范围内的完整历史，
首次生成基线 bundle，之后只上传新增对象的增量 bundle。S3 保存归档和 manifest，PostgreSQL 保存
同步任务、ref 观测、归档依赖和 `ready` 快照。Agent 的运行独立于同步任务。

## 启动

先按 [本地开发环境](DEVELOPMENT.md) 启动 PostgreSQL、MinIO、Temporal，创建 bucket 并执行迁移。
Source Worker 和 CLI 都直接读取进程环境变量，不会自动加载 `.env`，也不需要 OpenAI 配置。
两者需要访问相同的数据库、bucket、Temporal namespace 和 Source task queue。

在各自的 PowerShell 终端中设置：

```powershell
$env:POSTGRES_DSN = "postgres://sentinel:sentinel-dev-password@127.0.0.1:5432/sentinel?sslmode=disable"
$env:S3_ENDPOINT = "127.0.0.1:9000"
$env:S3_BUCKET = "sentinel"
$env:S3_ACCESS_KEY = "sentinel-dev"
$env:S3_SECRET_KEY = "sentinel-dev-secret"
$env:S3_USE_TLS = "false"
$env:TEMPORAL_ADDRESS = "127.0.0.1:7233"
$env:TEMPORAL_NAMESPACE = "default"
$env:TEMPORAL_SOURCE_TASK_QUEUE = "sentinel-source"
$env:GIT_BINARY = "git"
$env:SOURCE_CACHE_DIR = "var/git"
$env:SOURCE_MAX_BUNDLE_BYTES = "536870912"
```

安装 Git 并确保 `git --version` 可用，然后启动 Worker：

```powershell
go run ./cmd/source-worker
```

在另一个已配置的终端导入仓库：

```powershell
go run ./cmd/source-sync -tenant "0194e229-8576-7f42-8f08-3496913847cf" -url "https://github.com/octocat/Hello-World" -wait
```

`-tenant` 是必填的非零 UUID；仓库、同步任务、manifest 和快照 ID 由程序生成 UUIDv7。
CLI 是管理员入口，租户由调用者明确传入；尚未提供业务 HTTP API。

默认跟踪 `refs/heads/*` 和 `refs/tags/*`，为这些 ref 可达的所有 commit 建立快照。
也可以在新建仓库时指定范围，例如只跟踪主分支及发布标签：

```powershell
go run ./cmd/source-sync -tenant "0194e229-8576-7f42-8f08-3496913847cf" -url "https://github.com/OWNER/REPO" -refs "refs/heads/main,refs/tags/v*" -wait
```

当前只接受 `https://github.com/OWNER/REPO` 或带 `.git` 的 URL，禁止内嵌凭据、查询参数和自定义端口。
不跟踪 GitHub pull request 隐藏 ref，不拉取子模块仓库或 Git LFS 实体；LFS 指针和子模块 gitlink 随 Git 对象保存。

## 后续同步和恢复等待

CLI 在启动 Temporal Workflow 前，先持久化任务并输出第一行 JSON：

```json
{"repository_id":"...","sync_id":"...","tenant_id":"...","workflow_id":"source/.../..."}
```

省略 `-wait` 时，成功调度后即退出；指定 `-wait` 则继续输出结果引用、manifest ID、本次发布快照数量和快照列表。
快照包含 commit SHA、tree SHA 和归档 manifest ID。已就绪的旧版本不会重复输出；空仓库的结果没有 manifest。
CLI 中断等待不会取消后台 Workflow。

后续同步使用首次输出的仓库 ID；每次创建新的同步任务，复用已保存的仓库配置：

```powershell
go run ./cmd/source-sync -tenant "0194e229-8576-7f42-8f08-3496913847cf" -repository "仓库 UUID" -wait
```

重新连接已有任务使用同步 ID；它不会创建新任务，也不会重复发布已完成的结果：

```powershell
go run ./cmd/source-sync -tenant "0194e229-8576-7f42-8f08-3496913847cf" -resume "同步 UUID" -wait
```

Temporal 启动响应丢失时也使用 `-resume`。失败或取消的任务不能重新启动，应使用 `-repository` 创建新任务。
`-url` 每次会登记新的仓库 ID；后续同步请使用已有的仓库 ID。

## 私有仓库

在 **Source Worker 所在终端**设置有仓库读取权限的 GitHub token，再启动 Worker：

```powershell
$env:GITHUB_TOKEN = "你的 token"
go run ./cmd/source-worker
```

CLI 只提交凭据引用：

```powershell
go run ./cmd/source-sync -tenant "0194e229-8576-7f42-8f08-3496913847cf" -url "https://github.com/OWNER/PRIVATE_REPO" -credential-ref "env:GITHUB_TOKEN" -wait
```

Worker 从环境变量解析 token，经 Git 子进程环境中的 HTTPS header 配置传递。
token 不进入 URL、命令行参数、仓库配置文件、数据库正文或 Temporal History。
多个 Worker 消费同一个队列时，需要配置相同的凭据引用。当前凭据提供器只支持 `env:变量名`。

## 归档与事务

每次尝试创建独立的临时 bare 仓库，先按依赖顺序恢复已发布 bundle，校验 SHA-256 和 Git prerequisites，
再读取 GitHub ref 广告并按当时的完整 OID 拉取。已导入对象用内部 retained ref 保留，因此强推或删分支
不会删除旧版本。同步完成后清理临时目录；Worker 异常退出可能留下临时目录，它们不参与后续发布。
`SOURCE_CACHE_DIR` 属于源码工作区，与 Go 的 build / module cache 无关。

新 bundle 上传后会重新读取校验，manifest 和同步候选结果也先持久化。
发布事务同时更新归档 head、快照、ref 观测、幂等结果和 `source.synced` outbox。
同一仓库通过租约和递增 fencing token 串行发布，归档 head 还需通过版本检查。
旧 Worker 即使仍在运行也无法覆盖新结果；发布前失败不会产生新的 `ready` 快照。
归档已保存而发布失败时，重试可以复用候选结果；若其他同步已推进 head，则从最新归档重新构建。

没有新增对象时复用当前 manifest，仍记录 ref 变化与同步结果，不产生空增量归档。
所有已发布快照始终指向覆盖其对象的归档链，不能单独删除链上的基线或增量文件。
当前尚未实现归档链合并、定时同步、webhook 或对象垃圾回收。

## 只读源码查询

Source Worker 注册 `sentinel.source.read` Activity，供 Agent Workflow 派发源码工具：

| 工具 | 作用 |
| --- | --- |
| `source_list_versions` | 列出仓库已就绪快照 |
| `source_list_files` | 列出快照中某个目录层级的条目 |
| `source_read_file` | 按行范围读取文本文件 |
| `source_search` | 按字面量搜索匹配行 |
| `source_diff` | 比较同一仓库的两个快照 |

每次查询先确认租户、仓库和快照状态，再从当前归档 head 恢复临时 bare 仓库并校验目标 commit。
查询使用原生 Git 对象，不创建源码工作副本；结果按条数、行数和字节数分页，游标绑定查询条件。
完整结果写入对象存储（`source-tool-result`），Activity 只向 Workflow 返回结果引用和状态。
查询失败分为可恢复的工具错误（路径不存在、二进制文件、快照不可用等）和需要重试的基础设施错误。
读取和 diff 都不产生新的快照或归档。当前每次查询独立恢复归档，尚未加入共享的持久读缓存。

单次 Activity 最长 20 分钟，网络和存储失败在 24 小时的调度窗口内退避重试；Workflow 失败或取消后记录终态。
当前单仓库上限为 10,000 个跟踪 ref、100,000 个保留 commit、10,000 层归档链；JSON 正文另受 16 MiB 限制。
单个 bundle 默认最多 512 MiB，通过 `SOURCE_MAX_BUNDLE_BYTES` 调整，最大 5 GiB。
此配置在上传及恢复时校验文件大小，Git 拉取仍需要足够的本地磁盘空间。

## 验证

```powershell
go test ./...
go vet ./...
go build ./cmd/...
$env:SENTINEL_TEST_POSTGRES_DSN = "postgres://sentinel:sentinel-dev-password@127.0.0.1:5432/sentinel?sslmode=disable"
go test -tags=integration -count=1 ./internal/storage/postgres
$env:SENTINEL_TEST_GITHUB = "1"
go test ./internal/source/git -run TestGitHubSmoke -count=1 -v
```

普通测试使用本地临时 Git 仓库和模拟 S3；数据库集成测试创建独立测试库，覆盖真实 Git 和 PostgreSQL 的发布链路。
GitHub smoke test 需要网络，验证公开仓库的真实拉取，不使用个人凭据。
