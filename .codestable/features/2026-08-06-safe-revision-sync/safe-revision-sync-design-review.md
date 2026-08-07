---
doc_type: feature-design-review
feature: 2026-08-06-safe-revision-sync
status: passed
review_state: passed
review_reason: ""
reviewer_id: "local-only:/root"
reviewed: 2026-08-06
round: 3
---

# safe-revision-sync feature design 审查报告

## 1. Scope And Inputs

- Design: `.codestable/features/2026-08-06-safe-revision-sync/safe-revision-sync-design.md`
- Checklist: `.codestable/features/2026-08-06-safe-revision-sync/safe-revision-sync-checklist.yaml`
- Intent / brainstorm: none
- Roadmap: none
- Related docs: `.codestable/requirements/safe-multi-device-sync.md`、`docs/safe-multi-device-sync.zh-CN.md`、插件端 `docs/safe-multi-device-sync-client.zh-CN.md`
- Code facts checked: 服务端 Resource domain、WebSocket DTO/protobuf、DAO 写队列、迁移、REST/WebSocket 写入口；插件设置、pending 状态与远端删除入口

### Independent Review

- Status: local-only
- Detection: local-only
- Provider / agent: `/root/safe_sync_design_review`、`/root/safe_sync_design_review_retry`
- Raw output: 两个独立 reviewer 均在返回 findings 前因平台 `429 Too Many Requests` 终止；本轮 PostgreSQL 范围修订沿用 owner 已批准的 local-only lane，不再启动子代理
- Merge policy: owner 在获知 reviewer 因平台限流失败且当前无运行中 agent 后回复“继续吧”，作为 `ApproveLocalOnly`；本轮又明确选择 Dokploy 公用 PostgreSQL，主 agent 仅合并本地代码、配置和文档证据
- Gate effect: owner-approved local review lane

## 2. Design Summary

- Goal: 通过显式设置开关启用 Vault 级安全修订同步，阻止过期修改、删除和重命名静默覆盖新状态
- Key contracts: Resource/Vault Revision、稳定 Resource ID、幂等操作日志、严格 Vault、客户端 ACK 基准与恢复区
- Steps: 8 个，风险热点为跨仓库协议、SQLite 用户库导入、PostgreSQL 事务迁移、客户端删除保护和 Dokploy 升级
- Checks: 16 个；S1 已在 implementation 中完成，其余保持 `pending`
- Baseline / validation: checklist YAML 已通过解析；已核验 SQLite repository key 分库、PostgreSQL `user_<uid>` schema 归一路由、domain、protobuf、DAO 写队列、迁移、REST/WebSocket 写入口及插件设置、pending 和远端删除路径

## 3. Findings

### blocking

- [x] FDR-101 `design#2.1 Interface 设计检查 / #2.2 流程级约束` “同一数据库事务”没有可执行的 Unit of Work 与文件系统一致性协议。
  - Evidence: `internal/dao/dao.go:617` 的 `ExecuteWrite` 只提供按用户写队列，不开启 `gorm.Transaction`；Note/File 正文存储在数据库外，数据库回滚不能回滚物理文件。
  - Impact: Resource 状态、正文/附件、事件和幂等结果可能部分成功，ACK 前置条件无法成立。
  - Expected fix scope: 明确 per-user DB 的 `SafeSyncUnitOfWork`、物理内容 staging/原子替换/崩溃恢复，以及 ACK/广播只观察 committed 操作。

- [x] FDR-102 `design#2.1 Seam / #2.3 挂载点` 严格模式只描述 WebSocket seam，未覆盖 REST、MCP、Git sync、回收站恢复等既有写入口。
  - Evidence: `internal/routers/api_router/handler_note.go`、`handler_file.go`、`handler_folder.go` 均可直接调用现有 service 写资源；MCP 和 Git sync 也复用资源 service。
  - Impact: 资源会变化但 Resource/Vault Revision 与事件不变，安全客户端基准失真。
  - Expected fix scope: strict write guard 放在 service/domain 边界；本阶段仅新安全协议可写，无 safe mutation context 的逻辑写入口稳定 fail-closed。

- [x] FDR-103 `design#2.2 激活流程 / AC-03 / AC-13` bootstrap 没有稳定的服务端状态机、快照一致性或本地不一致处理。
  - Evidence: design 未定义分页期间写入冻结、cursor/manifest、完成 CAS、超时取消和 local hash mismatch 规则。
  - Impact: 激活期间写入可能漏同步，或把不同内容错误标成已确认基准。
  - Expected fix scope: 定义 `off -> bootstrapping -> strict`、过期 session、写入冻结、manifest hash、完成 CAS/取消/超时和 mismatch fail-closed。

- [x] FDR-104 `design#2.1 协议示例 / S3-S5` mutation 契约不足以覆盖创建和附件分片上传。
  - Evidence: 新资源尚无 `resourceId/baseRevision`；现有文件上传使用 session/chunks，design 未定义 operationId 与 session/commit 的绑定。
  - Impact: 新建资源与断线附件上传会产生不兼容的临时语义，AC-04/08 不可完整证明。
  - Expected fix scope: 定义创建的 absent/0 前置条件与服务端分配 ID；定义附件 start/chunk/commit、请求指纹、hash 校验和幂等 ACK。

- [x] FDR-105 `design#0 Resource ID / #2.1 SyncResourceMetadata / #2.2 重命名` Resource 当前路径、旧路径墓碑和文件夹递归变更关系不明确。
  - Evidence: design 同时把 metadata 定义为“当前路径状态”，又称旧/新路径状态各自共享同一 ID/Revision；文件夹移动未定义后代 revision/event。
  - Impact: 唯一约束、增量事件、baseline 重映射与递归原子性无法确定。
  - Expected fix scope: 分离唯一 Resource 当前状态与 `SyncPathTombstone`；每个受影响 Resource 只增一次 revision，递归操作以同一 transactionId 产生逐资源事件。

- [x] FDR-109 `design#前置依赖 / #2.1 迁移 / S2/S8` 原设计假设 Note/File/Folder/Vault 与修订表位于同一 per-user 数据库，但默认 SQLite 实际按 repository key 分散到多个文件。
  - Evidence: `internal/dao/note_repository.go` 使用 `user_<uid>`，File/Folder/Vault 分别使用 `user_file_<uid>`、`user_folder_<uid>`、`user_vault_<uid>`；`internal/dao/dao.go#GetOrCreateDB` 在 SQLite 下按完整 key 生成不同文件，而 PostgreSQL `extractUserSchema` 统一映射到 `user_<uid>` schema。
  - Impact: SQLite 下无法让真实资源写入与 Resource/Vault Revision、事件和操作终态处于同一事务，继续实现会违反 FDR-101 的原子契约。
  - Expected fix scope: 第一阶段仅在 PostgreSQL 用户库且 SQLite 导入校验完成后宣告 capability；提供可重入导入、校验和回滚，不实现 SQLite 跨文件伪事务。

### important

- [x] FDR-106 `design#操作日志 / ClientRevisionBaseline` 未定义超过 30 天的 pending、存储命名空间和移动端 localStorage 丢失恢复。
  - Evidence: 新 baseline/pending 只写“持久化”，当前 `FileHashManager` 已有文件镜像用于移动端恢复。
  - Impact: 长期离线或 WebView 存储回收后可能错误重放或绑定错误 Vault。

- [x] FDR-107 `design#关键决策 6 / S6` toggle 的关闭语义容易误导，且 checklist 漏掉 `bootstrapping`、`strict-vault-local-disabled`。
  - Evidence: 本地关闭只暂停、服务端保持 strict；S6 exit signal 未列全部状态。
  - Impact: 用户可能误以为关闭会恢复旧模式，移动端验收也不完整。

- [x] FDR-108 `docs/safe-multi-device-sync-client.zh-CN.md#插件端必须交付的能力` 未区分当前第一阶段与未来 Mirror/设备角色。
  - Evidence: 客户端入口文档把权威覆盖、角色和事务报告都列为“必须交付”，与当前 design 明确不做冲突。
  - Impact: 实现与验收范围不可稳定追踪。

### nit

none

### suggestion

none

### learning

- 当前 repository 写队列提供同用户串行化，但不等同于数据库事务；安全修订需要新的 transaction seam。

### praise

- 设计已将默认关闭、strict fail-closed、恢复区和不在生产唯一 Vault 首测列为明确边界。

### Round 2 Closure

- FDR-101：design 已增加 per-user `SafeSyncUnitOfWork`、`SafeContentStager`、`PREPARED` 恢复与 COMMITTED 可见性。
- FDR-102：strict guard 已下沉 service/domain，并列出旧 WebSocket、REST、MCP、Git sync、回收站恢复与 allowlist。
- FDR-103：已定义 10 分钟 bootstrap session、`OFF -> BOOTSTRAPPING -> STRICT`、写入冻结、manifest/CAS、取消/超时和 mismatch。
- FDR-104：已定义创建 absent/0 语义、服务端分配 ID、附件 start/chunk/commit 和稳定错误。
- FDR-105：已分离 Resource 当前状态与 PathTombstone，并定义递归操作逐资源 revision/event + transactionId。
- FDR-106：已定义 server/user/vault 命名空间、插件私有文件双写、终态 30 天与过期 pending fail-closed。
- FDR-107：已补全全部设置状态，并明确关闭只暂停、不解除 strict。
- FDR-108：客户端入口文档已区分当前第一阶段与后续 Mirror/设备角色。
- FDR-109：用户已选择 Dokploy 公用 PostgreSQL；design/checklist 已增加 PostgreSQL capability gate、SQLite 用户库 dry-run/可重入导入、校验失败不切换、备份回滚和 AC-21。

## 4. User Review Focus

- 用户已拍板：复用 Dokploy 公用 PostgreSQL；本地关闭只暂停，服务端 strict 不降级；首阶段不提供全局解除 strict。
- implement 需要重点遵守：先完成 SQLite 用户库导入校验和 PostgreSQL UoW/staging，再接 strict guard、WebSocket 与客户端状态机；SQLite 永不宣告安全修订 capability。
- code review / QA / acceptance 需要重点复核：导入保留主键与可重入性、配置切换回滚、崩溃恢复、绕过入口、bootstrap CAS、附件 commit 和递归文件夹事件。

## 5. Evidence Confidence Ledger

| Check | Verdict | Evidence Class | Basis | Follow-up |
| --- | --- | --- | --- | --- |
| Acceptance Coverage Matrix | pass | E/C | design AC-01..AC-21 已映射 S2..S8，补入 PostgreSQL gate、SQLite import、bootstrap、commit、crash、ingress、recursive | implementation/acceptance 取证 |
| DoD Contract | pass | E | design/checklist 已列 UoW/staging/guard、核心命令与 required artifacts | none |
| Steps and checks traceability | pass | E | S2/S7/S8 exit signal 与 C11..C16 覆盖新增契约；S1 已完成且增量未改写其证据 | none |
| Roadmap contract compliance | pass | E | 本 feature 无 roadmap owner | none |
| Module interface design | pass | C | 现有 DAO/物理存储事实对应 UoW/guard/stager seam | code review 复核实现未绕过 |
| Validation and artifacts | pass | E/C | AC-18..AC-21 与 checklist 明确 crash/ingress/recursive/migration evidence | implementation 执行 |

Summary: E=4, C=2, H=0, H-only core checks=none。

## 6. Residual Risk

- 本轮 PostgreSQL 实质修订仍缺少独立 reviewer 的第二视角；沿用 owner 已批准的 local-only lane。实现后的 code review 不得因此降低事实核验强度。
- Dokploy PostgreSQL 的实际内部地址、数据库名、权限、数据规模和持久卷尚未核验；S8 远程变更前必须按 `dokploy-deploy` preflight 确认，导入先在备份和独立实例 dry-run。

## 7. Verdict

- Status: passed
- Next: 用户已确认 PostgreSQL 范围变更，design 保持 approved，恢复 implementation S2。

## 8. Focused Closure

none（本轮为 PostgreSQL 实质契约修订后的完整 local-only 复审，不属于 focused closure）。
