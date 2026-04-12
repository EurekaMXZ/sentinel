# Sentinel Web

Sentinel 的 Next.js 前端。当前提供两个页面：

- `/`：输入 GitHub 仓库地址，完成「注册仓库 → 同步源码 → 选择最新就绪快照 → 创建 Agent」
- `/tasks/[agentId]`：查看某个分析任务的完整事件日志，支持 SSE 回放、实时追加、原始事件表、取消/恢复和最终结果

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

## 页面行为

### 新建分析

1. 校验 GitHub HTTPS 仓库地址
2. 复用同租户下已注册的同 URL 仓库，否则创建
3. 触发同步并轮询 `GET /v1/source/syncs/{id}`
4. 通过 `GET /v1/source/repositories/{id}/versions?limit=1` 取最新 ready 快照
5. 创建根 Agent，资源绑定为 `source_snapshot`
6. 跳转事件日志

同步中刷新页面后，流程状态会从 localStorage 恢复，可继续等待或恢复未启动的 Workflow。

### 事件日志

- 通过 `fetch` + `ReadableStream` 解析 SSE；首次加载完整回放，断线使用 `last_event_id` 增量续传
- 按 `step_id` 聚合模型输出和工具调用；`model.text` 是完整 Markdown 段落，由 Streamdown 渲染，不再逐 token 更新
- `model.arguments` 在模型完成时携带完整工具参数；旧历史中的 `model.delta` 仍兼容
- 「时间线」和「原始事件」双视图，支持类型筛选、搜索、导出 JSONL
- 收到终态事件后关闭流并读取 `GET /v1/agents/{id}/result`
- Redis 不可用时降级为轮询；事件超过 `REDIS_STREAM_TTL` 时提示历史已过期

## 检查

```bash
pnpm build
pnpm lint
pnpm typecheck
```

## 已知边界

- 当前没有认证、租户管理或私有仓库凭据输入
- 只展示根 Agent 的事件，子 Agent 事件流尚未聚合
- 事件历史依赖 Redis Stream，超过 TTL 后只能查看最终结果
- 工具事件只包含工具名与状态，参数和结果正文仍在对象存储中
