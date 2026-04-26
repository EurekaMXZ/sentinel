# Sentinel Web

Sentinel 的 Next.js 前端。当前提供：

- `/`：项目概览和最近分析
- `/projects`、`/projects/[projectId]`：项目创建、编辑、仓库绑定和分析记录
- `/repositories`、`/repositories/[repositoryId]`：GitHub 仓库注册、配置、refs、同步历史、快照
- `/new`：从项目 → 仓库 → 已就绪 commit 快照创建分析
- `/tasks/[agentId]`：查看某个分析任务的命令行式执行日志和最终分析报告，支持 SSE 回放、实时追加、搜索和取消/恢复

## 技术栈

- Next.js 16（App Router）+ React 19
- HeroUI v3（`@heroui/react` + `@heroui/styles`）
- Tailwind CSS v4
- pnpm（`packageManager` 已写入 `package.json`）

## 启动

先启动 Sentinel API（默认 `127.0.0.1:8080`）和 Agent / Source Worker，然后：

```bash
corepack enable pnpm        # 如果 pnpm 不在 PATH
pnpm install
cp .env.example .env.local
pnpm dev
```

Windows 下如果 `corepack enable` 因 Node 安装目录权限失败，可改用
`corepack prepare pnpm@latest --activate`，并用 `corepack pnpm ...` 代替 `pnpm ...`。

打开 `http://localhost:3000`。

`.env.local`：

```dotenv
SENTINEL_API_URL=http://127.0.0.1:8080
NEXT_PUBLIC_TENANT_ID=0194e229-8576-7f42-8f08-3496913847cf
```

- `SENTINEL_API_URL` 只在服务端使用；`next.config.ts` 将同源 `/v1/*` 代理到该地址，避免 CORS。
- `NEXT_PUBLIC_TENANT_ID` 是当前唯一租户。API 还没有认证，所有请求通过 `X-Tenant-ID` 或 SSE 的 `tenant_id` 查询参数传入。

## 界面布局

- 桌面端为固定左侧栏 + workspace：左侧栏包含概览、项目、仓库、新建分析和最近分析
- 移动端左侧栏收起为顶部栏和左侧 Drawer
- 图标统一使用 `lucide-react`，不使用 Unicode 字符或 emoji 充当图标
- 任务详情通过右侧 Drawer 打开，主 workspace 只保留执行日志和最终报告

## 页面行为

### 项目和仓库

- 项目承载仓库绑定和分析记录；同一仓库可以绑定到多个项目
- 仓库在租户内只注册一次，默认跟踪所有分支和标签
- 仓库详情支持修改 tracked refs、凭据引用和状态，手动同步，查看 refs、同步历史和最近快照
- 只允许 `active` 仓库发起同步或创建分析；归档项目不能创建新分析
- 分析目标固定为选中的 `source_snapshot`，后续分支更新不会改变已有分析

### 新建分析

1. 选择项目
2. 选择项目已绑定的 `active` 仓库
3. 从该仓库的 ready 快照中选择一个 commit
4. 提交任务，服务端创建根 Agent 并写入项目分析记录
5. 跳转 `/tasks/[agentId]`

创建分析不会隐式注册仓库或触发同步；仓库必须先在仓库页注册并同步出 ready 快照。

### 执行日志

- SSE 走 `app/stream/agents/[agentId]/route.ts` 专用 Route Handler，直接转发上游 `ReadableStream`；它故意不放在 `/v1` 下，避免被 `/v1/*` rewrite 拦截并缓冲。浏览器请求 `/stream/agents/{id}`。
- 通过 `fetch` + `ReadableStream` 解析 SSE；首次加载完整回放，断线使用 `last_event_id` 增量续传
- 执行日志采用命令行样式：`[step N]` 标记步骤，`$ tool {...}` 显示工具调用及参数，`> text` 显示模型中间输出；每行一个条目
- `tool.started` 携带 `call_id` 和完整参数，`tool.completed` 携带状态，前端用 `call_id` 将结果与调用配对
- `model.text` 在模型文本完成时发送一次；中间步骤文本按纯单行展示，不再逐 token 更新
- 只有完成状态下的终态步骤文本会作为“分析报告”用 Streamdown 渲染 Markdown；其他内容保持纯文本
- 旧历史中的 `model.delta` 仍兼容；原始事件不单独展示，可通过导出 JSONL 查看
- 收到终态事件后关闭流；最终结果仍可通过 `GET /v1/agents/{id}/result` 读取
- Redis 不可用时降级为轮询；事件超过 `REDIS_STREAM_TTL` 时提示历史已过期

## 检查

```bash
pnpm build
pnpm lint
pnpm typecheck
```

## 已知边界

- 当前没有认证或租户管理；私有仓库通过 `credential_ref` 引用 Source Worker 环境变量
- 当前只支持从已就绪快照选择 commit；PR 目标解析和 GitHub Actions 回调尚未接入
- 只展示根 Agent 的事件，子 Agent 事件流尚未聚合
- 事件历史依赖 Redis Stream，超过 TTL 后只能通过 API 读取最终结果
- 工具事件包含工具名、`call_id` 和完整参数；工具结果正文仍保存在对象存储中，不在事件流里下发
