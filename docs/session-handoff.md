# 会话交接

## 2026-11-03：SRV-001 第一版完成，待 Mac 验证

**做了什么**

- 照 deepfate-server 搭好骨架：`cmd/server`、`cmd/migrate`、`cmd/admin`，`internal/platform`、`internal/gateway`、`internal/account`、`internal/testdb`，`migrations/001_accounts.sql`，`api/openapi.json`。
- Go 1.27.3；依赖只有 pgx v5.12 和 Prometheus 客户端 v1.25。密码用标准库 PBKDF2-SHA256（60 万次）。
- 接口：`/livez`、`/readyz`；`/v1/auth/login`、`/refresh`、`/logout`、`/password`；`/v1/me`；`/v1/schools`、`/v1/schools/{school}/members`。

**与规划不同、实现时定下的**

- 会话从登录起算 30 天，刷新不顺延；高权限账号的桌面会话 12 小时。deepfate 是滑动续期。
- 刷新令牌复用即作废整个会话。deepfate 对同一次刷新的重试会原样补发，这里没做，客户端要在刷新成功后立即保存新令牌。
- `Idempotency-Key` 还没实现。建账号靠登录名唯一约束防重复，到作品提交时再做。
- 二次确认密码（高风险写操作）还没做，到有这类接口时再加。

**验证**

- Grok：`go build`、`go vet`、`gofmt`、`go test ./...` 通过。连库测试（`internal/gateway/accounts_test.go`）在 Grok 上**跳过**，未验证。
- Mac：待做。

**下一步**

- 交 Mac：`make check`，以及本地起服务后的 curl 冒烟。
- 然后 SRV-002 演示账号、SRV-003 课程导入。
