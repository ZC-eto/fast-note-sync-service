---
doc_type: feature-review
feature: authoritative-mirror
status: passed
summary: "权威覆盖、设备角色、自维护更新链路通过双仓库代码审查与完整自动化闸门"
reviewer_id: "local-only:/root"
reviewed: 2026-08-08
---

# 安全权威覆盖代码审查

## 范围

- 服务端：设备角色表与租约、WebSocket/protobuf、STRICT 写入约束、PostgreSQL 迁移、自维护更新与 GHCR Release。
- 插件端：差异计划、目标恢复包、两个覆盖方向、最近一次回滚、角色设置、问号帮助、GitHub-only 更新。
- 部署边界：现有 `Fast Note Sync Safe` Compose 原地升级，继续使用共享 PostgreSQL 和现有持久卷；旧 Compose 不变。

## 已修复发现

- blocking：远端覆盖本地在执行前未重新校验本地目标，可能覆盖预览后产生的新编辑。现已对两个方向统一重检本地清单。
- blocking：远端回滚新增嵌套目录时删除顺序不稳定。现已按子项到父目录排序并补纯函数回归测试。
- blocking：Markdown fallback 哈希与安全写入哈希不一致，大附件客户端三段采样与服务端两段采样不一致。现已统一并增加中文笔记、附件中段变化验证。
- important：覆盖中断留下 `APPLYING` 恢复记录时无法回滚。现已允许恢复，并禁止在已失效计划上重复点击执行。
- important：bootstrap 分页失败时客户端可能遗留服务端 session。现已在分页前持有 session，并验证失败会发送 cancel。
- important：示例 Compose 和多语言部署文档仍引用上游 Docker Hub 镜像。现已统一为 `ghcr.io/zc-eto/fast-note-sync-service`。

## 更新源审计

- 运行时更新、Issue、Release、网页仓库入口和安装脚本均指向 `ZC-eto`。
- 未发现 `github.com/haierkeys`、上游 CNB 或 Gitee 的运行时外链。
- 保留的 `github.com/haierkeys/...` 仅为 Go module/import/ldflags；保留 HaierKeys 署名、Apache-2.0、Ko-fi 和历史旧部署证据。

## 自动化证据

- 插件：Node `24.14.0`、pnpm `11.1.2`；`pnpm test`、`pnpm lint`、`pnpm lint:css`、`pnpm build` 全部通过。
- 服务端：Go `1.26.5`；`go test ./...`、`go build ./...`、`go vet ./...` 全部通过。
- 两仓库 `git diff --check` 无空白错误，仅报告 Windows 工作区的 LF/CRLF 提示。
- 修改过的前端 `.js` 与对应 `.gz`、`.br` 解压内容逐字节一致。

## Acceptance 补充证据

- GitHub `3.6.2` / `2.5.0` Release 已发布；GHCR `3.6.2` 可匿名读取，digest 与 Dokploy 部署记录一致。
- Dokploy 新项目已升级到 `3.6.2`，继续使用共享 PostgreSQL；健康检查和版本更新地址通过，旧项目未改动。
- Windows 已安装 `2.5.0`，真实 Obsidian 设置页和问号帮助已验证；两个覆盖方向及对应回滚通过隔离 `SafeMirrorManager` 集成测试。
- Android 本轮不安装；Release ZIP 保持可供后续手工导入。

## Verdict

- Status: passed
- Next: release / deploy / acceptance
