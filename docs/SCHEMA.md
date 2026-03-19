# PostgreSQL 基础 Schema

依据 [提案](PROPOSAL.md) 第 5 至 10 节。初始版本为
`internal/storage/postgres/migrations/00001_initial_schema.sql`，面向 PostgreSQL 17。
初始迁移建立 17 张业务表；`00002_decouple_agent_resources.sql` 解除 Agent 的单仓库绑定，
新增两张可选资源关联表，当前共 19 张业务表。Goose 另外维护 `sentinel_schema_version`。
执行方法见 [本地开发环境](DEVELOPMENT.md#数据库迁移)。

## 表及职责

| 模块 | 表 | 职责与主要唯一键 |
| --- | --- | --- |
| 源码 | `source_repositories` | 上游地址、凭据引用、跟踪范围、Git 对象格式和归档 head / CAS 版本 |
| 源码 | `source_sync_jobs` | 同步任务、配置快照和状态；租户内 operation / workflow 唯一，每仓库同时最多一个活动同步 |
| 源码 | `source_ref_observations` | 每次同步的 ref 目标、peel 后的 commit 和删除标记；租户 + sync + ref 唯一 |
| 源码 | `source_storage_manifests` | 基线 / 增量包、不可变清单对象和恢复依赖；租户 + 仓库 + operation + archive_attempt 唯一 |
| 源码 | `source_snapshots` | commit / tree 和恢复清单；租户 + 仓库 + 完整 commit SHA 唯一 |
| Agent | `agent_sessions` | 用户任务、会话状态和错误引用；不要求源码资源 |
| Agent | `agents` | 父子关系、任务、Workflow 和结果引用；每会话一个 root，每父工具执行最多一个 child |
| 资源 | `session_resource_bindings` | 会话输入的零到多个资源；租户 + session + binding_name 为主键 |
| 资源 | `agent_resource_bindings` | 每个 Agent 显式选择的零到多个资源；租户 + agent + binding_name 为主键 |
| Context | `agent_head` | 当前 Context、事件游标、步骤游标和整行 CAS 版本 |
| Context | `context_records` | 已发布的 Context snapshot / delta 及覆盖事件范围；租户 + agent + context_version 唯一 |
| Context | `context_source_refs` | Context 保留的零到多个源码版本、证据和 diff 两端版本；每条 Context 对每仓库最多一个可选默认版本 |
| 事件 | `event_index` | 事件顺序、对象段及记录序号；租户 + agent + sequence、租户 + segment + ordinal 分别唯一 |
| 执行 | `llm_executions` | 普通模型循环的请求、响应、状态和 token 计数；租户 + agent + step 唯一 |
| 执行 | `tool_executions` | 模型工具调用、参数和结果引用；租户 + agent + step + tool_call_id 唯一 |
| 执行 | `compaction_jobs` | compact 输入 head 版本、请求 / 响应、候选快照和发布结果；租户 + agent + source_row_version 唯一 |
| 公共 | `stored_objects` | 对象位置、SHA-256、大小、编码和状态；bucket + object_key 唯一 |
| 公共 | `idempotency_records` | 稳定操作键、请求摘要、结果和执行租约；租户 + operation_id 为主键 |
| 公共 | `outbox` | 事务内写入的待投递通知、租约和投递状态；租户 + dedupe_key 唯一 |

## 可选资源与子 Agent

`agent_sessions`、`agents`、`agent_head`、`context_records`、`llm_executions` 和 `compaction_jobs`
按租户、会话或 Agent 关联，不保存必填的仓库或源码快照。无源码任务可以建立完整的执行和恢复记录。

两张资源表都使用 `binding_name` 作为输入别名，通过 `resource_kind` 约束引用形状：

| resource_kind | 必填引用 | 必须为空的引用 |
| --- | --- | --- |
| `source_repository` | `repository_id` | `source_snapshot_id`、`object_id` |
| `source_snapshot` | `repository_id`、`source_snapshot_id` | `object_id` |
| `object` | `object_id` | `repository_id`、`source_snapshot_id` |

每个引用都有租户外键，快照同时校验所属仓库；CHECK 约束避免空字段跳过复合外键校验。
同一 Agent 可以关联多个仓库、同仓库的多个版本和文档对象。资源关联在创建时持久化，
恢复时按 tenant / agent 读取；compact 不修改这些输入。

Session 资源表示用户提供的输入，每个 Agent 的实际输入另行明确登记。子 Agent 仍属于父 Agent 的
租户和会话，但任务、Context、工具配置和资源集合独立，不隐式继承父 Agent 或 Session 的资源。
资源关联用于描述上下文，不代替授权；后续 API / Activity 须校验工具访问范围，源码快照还须处于 ready 状态。

第二个迁移在事务内锁定运行时表，将旧会话和 Agent 的默认快照分别回填为 `default_source` 资源，
保留原仓库、快照和创建时间，然后移除通用记录的强制绑定。该别名不构成新 Runtime 的必选默认值。
回滚只支持每个 Session / Agent 恰有一个名为 `default_source` 的快照资源，且 Agent 与其会话、
Context 证据、工具和事件均可归属于同一仓库的数据；不满足时整次回滚报错并保留版本 2。

## 通用约定

实体主键使用 `uuid`，由 Go 的 API / Activity 生成 UUIDv7 后传入；数据库不设置随机 UUID 默认值，
也不依赖 PostgreSQL 18 的 UUIDv7 函数。关系表使用自然复合键。外部 `tool_call_id`、
Temporal Workflow / Run ID 和派生 `operation_id` 使用 `text`。

每表带 `tenant_id`，外键同时绑定租户；具体源码资源和证据再绑定 `repository_id`，防止串租户或串仓库。
这些约束保证关联一致性，尚未启用 RLS，业务查询仍须显式限定租户并执行授权校验。
外键不级联删除历史或归档。除非明确清理引用，不允许删除被使用的快照、对象或清单。

时间字段为 `timestamptz`。`updated_at` 只有插入默认值，后续更新由执行该操作的 SQL 显式设置，
没有更新时间触发器。各表使用各自的状态 CHECK；状态迁移规则由后续仓储方法的条件 UPDATE 实现。

PostgreSQL 不存消息、源码、Context 或模型 I/O 正文。正文统一通过 `stored_objects` 引用，
SHA-256 以 32 字节 `bytea` 保存，编码可为 `identity`、`gzip`、`zstd`；`kind` 是可扩展的对象用途标识。
`uploading` 可暂缺摘要和大小，`verified` 必须具备摘要、大小和验证时间。
同一个 bucket / key 不允许插入第二份记录；重试应读取原记录并比较请求及校验值，不能覆盖不同内容。

Git commit / tree / ref OID 使用完整小写十六进制字符串，支持 40 位 SHA-1 和 64 位 SHA-256。
Source Worker 在首次获取时确认 `object_format`，并验证全部 OID（包括 prerequisite 数组）与仓库格式一致。
ref 观测不强制引用已收录的 commit，允许保存同步范围外的目标和 annotated tag 的对象 OID。

## 源码发布和保留

`base` 清单没有 parent 或 prerequisite；`increment` 必须关联同仓库 parent。
归档恢复链与 Git commit 父子关系分开管理。`archive_attempt` 区分 head 竞争后重新计算的候选归档，
新的候选使用新清单 ID 和新对象 key，不能替换先前上传的对象。

数据库保证引用存在、属于同一租户 / 仓库，以及 `ready` 具备清单和就绪时间。
它不会自行访问 S3 或验证 Git 对象。后续 Source Worker 必须实现以下发布事务：

1. 上传 bundle 和 manifest，核验对象大小、校验值、prerequisite、恢复链及目标 commit 完整性。
2. 只接受已发布 parent，保持已发布清单的对象与依赖不可变，防止形成恢复环。
3. 在同一 PostgreSQL 事务中发布清单、登记 ready 快照，并通过预期 `archive_version` 更新仓库 head。
4. 同事务完成同步状态、幂等结果和 outbox。CAS 未更新到行时回滚，基于最新 head 重新计算候选。

同步无新增对象时可复用已发布清单。`source_ref_observations` 保存分支移动和删除，
上游 ref 删除不能删除已保留的源码版本。

归档回收必须从当前归档 head、资源关联、Context / 工具 / 事件 / 结果引用和运行中任务保留的快照出发，
计算 parent / prerequisite 的传递依赖。
生成覆盖保留版本的新基线并验证、迁移引用后，才能回收旧归档。本版本不提供自动 GC。

## Context、事件和执行恢复

`agent_head.row_version` 用于整行 CAS。仅推进事件或步骤游标也要递增它；
`context_version` 只在发布新 Context snapshot / delta 时递增，两者不能混用。
`last_event_sequence` 是已持久化事件游标，`context_event_sequence` 是当前 Context 已覆盖的事件游标，
后者不能超过前者。`next_step_id` 从 1 开始，跨 Temporal Continue-As-New 延续。

初始 head 可以是 Context 版本 0、空 Context 指针、覆盖事件 0。
建立首个 Context snapshot 后，`snapshot_record_id` 指向已发布的 snapshot 记录；
读取时结合它和之后的 delta 恢复到 `context_version`。`from_event_sequence` / `through_event_sequence`
是闭区间，初始空 Context 使用 0 / 0，实际事件编号从 1 开始。

外键保证 head 和 delta 的基线属于同一 Agent。发布仓储还须检查基线记录的 `kind = snapshot`、
版本顺序、事件覆盖连续性，以及 head 指向的 snapshot 不晚于当前 Context 版本。
Context 可以不引用源码，也可以保留多个仓库和版本。`default` 仅是 Context 内某个仓库的可选提示，
唯一索引保证每条 Context 对每仓库最多一条，不是 Agent 的全局版本绑定。
发布仓储须在事务中检查对象内保留结论的证据与 `context_source_refs` 一致。

`tool_executions` 和 `event_index` 的 `repository_id` 可以为空，普通模型或子 Agent 调用无需源码字段。
一旦记录源码快照或 diff 两端版本，就必须提供仓库，复合外键校验实际版本所属仓库。
同一 Agent 的不同操作可以访问不同仓库；单次 `source_diff` 的两个版本必须同时出现且属于同一仓库。
工具派发前保存解析后的快照参数，重试复用该参数；资源选择和证据都不会被上游分支移动隐式替换。

事件正文先写入不可变 NDJSON 对象段。索引的 `record_ordinal` 是解压后段内从 0 开始的记录号，
不是压缩文件的字节偏移。事件索引、Context 记录、head CAS、执行 / 幂等完成及 outbox
在同一业务事务提交；CAS 冲突时不能留下未发布的 Context 记录。
Agent Workflow 保持单逻辑事件序列写入方，Activity 重试复用稳定事件 ID / sequence，且比较已有内容。

compact 候选保存在 `compaction_jobs`；保存模型响应后可复用。只有源 `row_version` CAS 成功，
才新增 Context 记录、源码引用并推进 head。失败的候选标记 `stale`，最新 head 对应新的逻辑 compact 操作。
compact 不删除完整历史事件。

Compact 发布新增一条 `kind='snapshot'` 的 Context 记录，覆盖从事件 1 到当前 `last_event_sequence`；
它只推进 `agent_head.context_version` 和 `row_version`，`last_event_sequence`、`context_event_sequence`
与 `next_step_id` 保持不变，也不写入新的 `event_index` 行。`compaction_jobs` 从 `response_saved`
变为 `published`；发布前 head 已变化的候选标记为 `stale`，不发布新 Context。

`response_saved` 表示响应已归档；`uncertain` 表示远端可能已经执行但本地没有可确认结果。
幂等键、请求摘要和已保存响应用于重放恢复，不能据此声称外部模型调用 exactly once。

## 幂等和 outbox

同一租户内 `operation_id` 在重试和 Continue-As-New 后保持不变。同键但不同 `request_hash` 应返回冲突，
不能简单 `ON CONFLICT DO NOTHING` 后继续执行。`request_hash` 是规范化输入的 SHA-256，
源码工具的输入须包含已解析的具体快照参数。
幂等记录保留期必须覆盖任务的重试和恢复周期。

执行租约使用 `lease_owner`、`lease_until` 和单调增长的 `fencing_token`。获取或接管租约时递增 token，
续约和提交结果都要匹配 owner / token；过期执行者不能提交。租约状态及时间配对由 CHECK 约束。

Relay 通过 `FOR UPDATE SKIP LOCKED` 批量领取可用 outbox 行，短事务内设置处理状态、租约及 token，
提交后发送通知，再用 owner / token 条件确认；失败或过期租约进入重试 / dead。
`pending` 的 `(available_at, outbox_id)` 和 `processing` 的 `(lease_until, outbox_id)` 分别有部分索引，
投递语义为至少一次，接收方按稳定键去重。当前版本只建立 schema，尚未实现 Relay 投递循环。
