---
doc_type: feature-design-review
feature: authoritative-mirror
status: passed
review_state: passed
review_reason: ""
reviewer_id: "local-only:/root"
reviewed: 2026-08-08
round: 1
---

# 安全权威覆盖设计审查

## 审查方式

- Design: `.codestable/features/2026-08-08-authoritative-mirror/authoritative-mirror-design.md`
- Checklist: `.codestable/features/2026-08-08-authoritative-mirror/authoritative-mirror-checklist.yaml`
- 权威规格: `docs/safe-multi-device-sync.zh-CN.md`
- 跨仓库客户端入口: `obsidian-fast-note-sync/docs/safe-multi-device-sync-client.zh-CN.md`
- 独立审查状态: local-only。前一阶段独立 reviewer 因平台 429 失败后，owner 已明确要求不再使用子代理并继续实现，本 feature 沿用该 owner-approved lane。

## 结论

设计范围可实现，且与既有安全修订同步契约兼容：权威覆盖复用固定 bootstrap 快照和 STRICT mutation，不把旧完整同步改造成破坏性镜像；三种设备角色通过服务端租约约束写入；更新和发布源固定到 `ZC-eto` fork。

实现必须保持以下 gate：

- 预览不写资源，计划 10 分钟失效。
- 执行前重新校验本地清单，远端由 bootstrap 修订固定。
- 目标原像完整写入恢复包后才提交 bootstrap。
- 删除或类型替换达到 50 项或目标清单 10% 时要求输入确认词。
- 覆盖与回滚后重新读取两端清单并校验类型、大小和内容哈希。
- 远端镜像端不可写，本地发布端租约阻止其他设备写入。
- 旧服务、旧域名、旧卷和共享 PostgreSQL 数据不得被替换或删除。

## 风险与处理

- 高风险数据语义：通过差异预览、双侧漂移校验、目标恢复包和最终哈希校验收敛。
- 嵌套目录：删除和回滚按子项到父目录排序，避免父目录递归删除后重复操作子项。
- 进程中断：`APPLYING` 恢复记录允许在重启后回滚。
- 哈希兼容：Markdown 使用 UTF-16 字符哈希，大附件使用首/中/尾三段采样，客户端和服务端保持一致。
- 旧设置冲突：设备角色显式管理只读同步；仅远端镜像端锁定并关闭离线删除上传。

## Verdict

- Status: passed
- Next: implementation / code review
