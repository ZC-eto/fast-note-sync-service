# Attention

本文件是 CodeStable 技能启动必读的项目注意事项入口。所有 CodeStable 子技能开始工作前必须读取它。

## 报告语言

CodeStable 所有落盘产出的正文使用简体中文。机器状态字段、代码标识符、命令和日志保持原始语言。

## 项目碎片知识

### 编译与构建

- 服务端使用 Go，最小构建命令为 `go build ./...`。

### 测试

- 核心测试入口：`go test ./internal/routers/websocket_router ./internal/service ./internal/dao`。
- 完整测试入口：`go test ./...`。

### 路径与目录约定

- 安全多端同步的跨仓库权威规格为 `docs/safe-multi-device-sync.zh-CN.md`。
- 协议、数据契约、迁移和破坏性同步语义必须与插件仓库协同修改。

### 环境变量与凭证

- 不把 Dokploy、数据库、令牌或备份凭证写入仓库和测试日志。

### 其他

- 删除、镜像和回滚不得首次在生产 Dokploy 或唯一 Vault 验证。
- `mtime` 不得作为安全同步的唯一冲突依据。
