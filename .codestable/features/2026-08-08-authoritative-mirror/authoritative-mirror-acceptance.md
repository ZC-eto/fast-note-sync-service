---
doc_type: feature-acceptance
feature: authoritative-mirror
status: pending
audit_state: completed
audit_reason: ""
auditor_id: "local-only:/root"
acceptance_authorization_ref: ""
accepted: ""
round: 1
---

# 安全权威覆盖与自维护发布验收报告

> 阶段：Standard acceptance-inline
> 验收日期：2026-08-08
> 关联方案：`.codestable/features/2026-08-08-authoritative-mirror/authoritative-mirror-design.md`

## 1. 接口契约核对

- [x] `SafeMirrorManager.prepare/apply/rollbackLatest` 分离预览、执行和恢复；执行必须持有仍有效的活动计划。
- [x] `createSafeMirrorPlan` 使用 `LOCAL_TO_REMOTE` / `REMOTE_TO_LOCAL` 生成 CREATE、UPDATE、DELETE、REPLACE 清单和高风险阈值。
- [x] `SafeMirrorRecoveryStore` 在插件私有目录保存 30 天目标原像，`APPLYING`、`COMPLETED`、`FAILED` 均可恢复。
- [x] `SyncRole` 只允许 `bidirectional`、`local-publisher`、`remote-mirror`，并由角色统一管理旧只读设置。
- [x] 服务端 STRICT mutation、设备租约、operationId 和资源修订契约与插件 protobuf 一致。

## 2. 行为与决策核对

- [x] 预览只读取两端清单，不写任何资源；集成测试在 `prepare` 后确认目标未变化。
- [x] 覆盖前先完整保存目标端原像，再提交 bootstrap；保存、提交或最终 hash 校验失败时记录 `FAILED`。
- [x] 执行前重检本地清单；远端由 bootstrap 修订固定；计划 10 分钟后拒绝执行。
- [x] 远端镜像端禁止覆盖远端；本地发布端租约由服务端约束其他设备写入。
- [x] `.obsidian` 配置目录仍由原配置同步管理，不进入权威覆盖。
- [x] 旧“完整同步”和按时间合并没有被改造成权威镜像；权威覆盖保持独立入口。
- [x] 挂载点反查覆盖插件 `setting.tsx`、`main.ts`、`safe_mirror_*`、`safe_sync_role.ts`、i18n、modal 和测试，以及服务端 protobuf、service、DAO、upgrade、task、frontend 和发布流程。
- [x] 拔除推演：移除设置入口、manager/recovery/plan、角色协议与服务端 STRICT 契约后没有隐藏的替代覆盖路径；旧同步仍可按原默认行为工作。

## 3. 验收场景核对

验证证据来源：accept-inline verification。

| ID | 来源 | 核心性 | 命令或动作 | 结果 |
| --- | --- | --- | --- | --- |
| S1 | `REQ-MIRROR-001` / `REQ-BACKUP-001` | 核心 | 隔离执行本地覆盖远端及远端整批回滚 | 通过 |
| S2 | `REQ-MIRROR-002` / `REQ-BACKUP-001` | 核心 | 隔离执行远端覆盖本地、附件覆盖及本地整批回滚 | 通过 |
| S3 | `REQ-MIRROR-001` / `REQ-OBS-001` | 核心 | 预览后修改本地、使计划过期 | 均被拒绝，未提交 bootstrap |
| S4 | `REQ-ROLE-001` | 核心 | 远端镜像端执行本地覆盖远端 | 被拒绝，未写远端 |
| S5 | `REQ-TEST-001` | 核心 | 插件 `pnpm test`、lint、CSS lint、build；服务端 test、build、vet | 全部通过 |
| S6 | 设置 UI | 用户可见 | Obsidian 1.13.4 打开设置、点击问号 | 入口、角色、覆盖、回滚和说明均可见 |
| S7 | 发布部署 | 核心 | GitHub Release、GHCR manifest、Dokploy health/version、Windows 文件 hash | 全部一致 |

UI 证据：`authoritative-mirror-settings.png`、`authoritative-mirror-help.png`。人工窄视口桌面缩放截图不代表 Android 原生布局，已从验收产物移除。

没有 failed / blocked 场景。生产唯一 Vault 未执行首次破坏性覆盖；核心覆盖逻辑已由隔离运行测试替代，真实 Windows 设置和普通同步另有运行证据。

## 4. 术语一致性

- “安全多端同步”“权威覆盖”“本地发布端”“远端镜像端”“恢复包”在中文设置、客户端文档和服务端权威规格中一致。
- 代码使用 `safeRevisionSyncEnabled`、`SafeMirrorManager`、`LOCAL_TO_REMOTE`、`REMOTE_TO_LOCAL`、`SyncRole`，没有混用旧完整同步语义。
- 运行时更新 URL 未命中上游 GitHub、CNB 或 Gitee；Go module/import 与作者署名保留不属于更新链路。

## 5. 领域影响盘点

- 新术语和稳定流程约束已进入权威规格与客户端入口文档；仓库没有独立 `requirements/CONTEXT.md` 或 ADR 目录，本次不另建重复术语文档。
- PostgreSQL per-user schema、bootstrap 固定清单、STRICT mutation 和恢复包是当前功能的结构性约束，已在权威规格中持续维护。
- 不需要额外 `cs-domain` 回写；后续若把 Android 后台恢复或 `.obsidian` 权威镜像纳入范围，应单独设计和审查。

## 6. Requirement 回写

本 feature 未挂接独立 CodeStable requirement。用户已通过 `user-confirmed-2026-08-08` 批准设计，跨仓库权威需求由 `docs/safe-multi-device-sync.zh-CN.md` 管理；本轮已将最终版本、测试、部署和 Windows 安装结果回写该文档，不自由创建第二份 requirement。

## 7. Roadmap 回写

方案没有 `roadmap` / `roadmap_item`，属于非 roadmap 起头，跳过。

## 8. attention.md 候选盘点

没有新的通用候选。Node 最低版本已由 `package.json` 声明；共享 PostgreSQL、Dokploy Compose 和生产 Vault 禁止首测属于本 feature 的部署文档与现有 attention 约束。

## 9. 遗留

- Android 插件安装和真实双设备 smoke 按用户要求延后，不属于当前 Windows 交付阻塞项。
- 首次在生产 Vault 开启安全同步或执行权威覆盖仍应由用户在已备份前提下主动确认，插件不会自动执行。
- 首版权威覆盖不处理 `.obsidian`，不自动选举多个本地发布端，也不使用 `mtime` 自动解决并发冲突。

## 10. 最终审计

- 验证证据来源：accept-inline verification；Standard lane 无独立 QA 报告。
- Evidence sources：方案、checklist、review、两个 UI 截图、GitHub Release 元数据、GHCR manifest、Dokploy `/api/health` 与 `/api/version`、Windows 安装文件 hash。
- 聚合命令：Node `24.14.0` 下 `pnpm test`、`pnpm lint`、`pnpm lint:css`、`pnpm build`；`go test ./...`、`go build ./...`、`go vet ./...`，退出码均为 0。
- 场景复核：re-verified 10 / trust-prior-verify 2；后者仅为本轮未重新打开的 UI 截图，核心覆盖路径已重新运行。
- 交付物复核：代码、设置、protobuf、数据库迁移、Release、镜像、Dokploy、Windows 插件和双仓库文档均存在。
- 完整工作区复核：验收产物和本轮测试/文档变更均纳入判断；人工窄视口截图与临时 Obsidian 环境不进入提交。
- diff 清洁度：无新增 debug、TODO、FIXME、注释代码或上游运行时更新链接。
- 知识沉淀出口：用户指南变化已归并到两份安全同步文档；无额外 attention、learning、decide 或 libdoc 候选。
- 结论：技术验收通过，等待用户终审确认。
