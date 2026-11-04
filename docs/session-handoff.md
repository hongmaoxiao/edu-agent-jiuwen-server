# 会话交接

## 2026-11-03：SRV-003 课程导入完成，Mac 验证通过；P1 后端完成

**做了什么**

- `migrations/003_content.sql`：categories、courses、chapters、labs（含 `guide_steps`）、rubrics、rubric_versions。
- `scripts/export_fde_content.py` → `seed/fde-content.json`（导出自客户端 `78686ae`）。
- `internal/content`：`Decode` 严格校验导入文件，`Import` 可重复执行，保留本地修改。
- `cmd/admin import-content <file>`，输出 JSON 报告。
- 规则见 `docs/plan/03-data-migration.md` §4。

**验证**

- Grok：`go build`、`go vet`、`gofmt`、`go test ./...` 通过；`TestSeedIsComplete`、`TestDecodeRejectsBrokenExports` 在 Grok 上实际跑过；连库的 `TestImportIsRepeatableAndKeepsLocalEdits` 在 Grok 上跳过，未验证。
- Mac（Codex 任务 16766ff1，提交 9fc5178）：`make check` 退出码 0，19 个用例全部 PASS、没有 SKIP；临时库导入两次，第一次全部新建，第二次全部 unchanged；库里 51 个实训、255 个指引步骤、1 套 100 分规则。报告在 Mac `~/Documents/Codex/agent-peer-workspace/edu-server-9fc5178/verification/report.md`。

**下一步**

- P1 后端（SRV-001～003）全部通过。接下来是客户端：登录页、按角色显示侧栏、二级栏、视角切换器，等 Claude Design 的设计稿。
- 还没做：本仓的部署脚本和备份（`deployments/production/`），上线前要做；`Idempotency-Key` 和高风险操作二次确认密码，等有对应接口时再做。

## 2026-11-03：SRV-002 演示账号完成，Mac 验证通过

**做了什么**

- `migrations/002_demo.sql`：账号和学校加 `demo_account_id`，新表 `demo_setups`、`demo_personas`，会话加 `acting_account_id`，审计加 `acting_account_id`。
- `POST /v1/session/perspective`；`/v1/me` 在演示会话里多一个 `demo` 字段（当前视角、可选视角、演示账号名）。
- `cmd/admin create-demo <login> <名称>`（密码从标准输入读）、`cmd/admin demo-reset <login>`。
- 规则见 `docs/plan/02-backend.md` §5「演示账号与视角切换」。

**验证**

- Grok：`go build`、`go vet`、`gofmt`、`go test ./...` 通过；`demo_test.go` 3 个连库用例在 Grok 上跳过，未验证。
- Mac（Codex 任务 2336c37b，提交 bc5edbe）：`make check` 退出码 0，gateway 14 个用例全部 PASS、没有 SKIP；curl 冒烟 31 项断言全部通过。报告在 Mac `~/Documents/Codex/agent-peer-workspace/edu-server-bc5edbe/REPORT.md`。

## 2026-11-03：SRV-001 完成，Mac 验证通过

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
- Mac（Codex 任务 79090165，提交 fe986e8）：源码核对一致；`make check` 退出码 0，gateway 11 个用例全部 PASS、没有 SKIP，含 7 个连库用例；curl 冒烟（迁移两次、建运营、登录、建学校、开管理员、首次改密码、开教师和学生、刷新轮换、指标端口）全部通过。PostgreSQL 是 Docker 临时容器 `postgres:18.6`。报告在 Mac `~/Documents/Codex/agent-peer-workspace/edu-server-fe986e8/REPORT.md`。

**下一步**

- SRV-002 演示账号与视角切换（进行中），然后 SRV-003 课程导入。
