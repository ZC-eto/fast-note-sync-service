# S4 窄修复记录

## 失败标准

- 相同 `operationId` 重放必须返回首次提交的完整 `SafeMutationResponse`，包括 `contentHash`。
- 递归文件夹重命名必须从已校验的旧根路径查询整棵资源树，且目标冲突时不产生部分提交。

## 允许范围

- `SyncOperation` 终态结果字段。
- `SafeMutationCoordinator` 的操作重放与递归树根路径选择。
- 对应定向测试与必要格式化。

## 必跑验证

```text
go test ./internal/service -run 'TestStrictVaultWriteGuard|TestSafeMutationCoordinator' -count=1
```

不得在本次窄修复中接入附件上传、扩展旧写入口或修改客户端行为。
