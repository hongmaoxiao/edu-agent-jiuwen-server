# 会话交接

## 2026-11-04：Go 后端上线 agent-api.hrtiger.cn

- 用户授权，由 Mac 上的 Codex 部署（任务 ac5d7782），版本 `2ade9d0`：
  - 和 agent.hrtiger.cn 同一台机器（gpu-train）；
  - 数据库是独立的 PostgreSQL 18.6，Docker，只监听 127.0.0.1:25432；
  - Go 服务由 systemd 运行，用户 edu-agent，非 root；
  - 证书单独用 Let's Encrypt 签，到期 2027-02-02，自动续期已演练。
- 线上已有数据：FDE 课程，运营账号 `ops`，演示账号 `demo`。密码只在服务器 `/etc/edu-agent-server/accounts.json`（root 0600）。
- 验收 1–7 全部通过。Grok 独立复核了以下几项：
  - `/livez`、`/readyz` 返回 200，证书匹配；
  - 跨域只放行 https://agent.hrtiger.cn，其他来源不放行；
  - 公网访问 `/metrics` 返回 404；
  - 错误密码返回 401；
  - agent.hrtiger.cn 仍是 302 跳转到 /login，没有变化。
- 配置和步骤见 `deployments/production/`。与交代不同的一处：打开了 `EDU_ALLOW_LOOPBACK_ORIGINS=true`，作用是给桌面版的本机回环来源放行跨域，不带 Cookie。
- **未配置数据库备份**，在有真实用户数据之前必须补上。
- 前端还没切：用户定的顺序是后端验证通过后再切前端。切前端要合并客户端分支 `feat/edu-platform-shell`，用 `VITE_EDU_API_BASE=https://agent-api.hrtiger.cn` 构建并部署，每一步都要用户确认。

## 2026-11-04：P1 客户端外壳，Mac 第三轮全部通过

**做了什么**（客户端仓库 worktree `edu-agent-jiuwen-platform`，分支 `feat/edu-platform-shell`，从 main 78686ae 切出，未推送、未合并、未部署）

- `984075b`：
  - 新插件 `extensions/edu_platform`；
  - 宿主插件插槽：多页面、侧栏图标、nav filter、rail footer、app gate；
  - 登录、首次改密、角色入口、二级栏、账号菜单、演示视角切换。
- `f3c5c0d`：
  - 修登录后落点被宿主路由覆盖；
  - 运营默认带建课权限，不再因此出现「教学」入口。
- `2ea52a7`：文档。交接与设计差异写在客户端 `docs/platform-shell.md`。
- 设计稿交接在本仓 `docs/plan/design/`（a0203c5）。
- 只有构建时设了 `VITE_EDU_API_BASE` 才生效；不设时和 main 一样。

- 设计稿逐字节核对：出稿会话用 Artifact 工具从画布下载 14 个文件，`cmp` 全部一致，记在 `docs/plan/design/README.md` 文末。本会话按该表逐个比对本地文件的字节数和 sha256，14/14 一致。

**验证**

- Mac 第一轮（Codex 任务 f5bf6416，验证 984075b）：
  - 自动化和构建都通过；
  - 端到端 1、7–11 通过，2–6 不通过（落点、运营多出「教学」）；
  - 报告：Mac `~/Documents/Codex/agent-peer-workspace/edu-platform-shell/verification/report.md`。
- Mac 复测（任务 f170a655，验证 f3c5c0d）结论是**有条件通过**：
  - 宿主模型配置引导完成后，2–6 全部通过；
  - 全新客户端数据目录第一次启动时，引导会把部分账号切回「任务」；
  - 打开已有任务链接的场景没有现成任务，跳过未测；
  - 报告：`verification-f3c5c0d/report.md`。
- Mac 上 Go 服务要 `CGO_ENABLED=0` 才能编译。本机 SDK 的 `.tbd` 有 `arm64e.x1` 链接错误，不是代码问题。

- Mac 第三轮（任务 8c49d97d，验证客户端 ee49f7e 和后端 fef996b）全部通过：
  - 连库测试 19 PASS、0 SKIP、0 FAIL；
  - 全新数据目录下，五个账号首次进入一次就落对页面，全程没有出现模型配置引导；
  - admin1 用 `roles` 数组建成「教师 + 学校管理员」，两个入口都在；
  - 学生任务的深链接停在该任务，没被跳走；
  - 报告：Mac `verification-ee49f7e/report.md`。

**遗留**

- 模型配置引导抢落点：客户端 `scripts/run-client.sh` 和 `deploy/client-entrypoint.sh` 现在都会关掉 `setup_guide.enabled`。模型本来就由平台配置，引导没有用处。Mac 第三轮已验证。
- 多角色账号：`POST /v1/schools/{id}/members` 的请求体从 `role` 改成 `roles` 数组，可以一次建「教师 + 学校管理员」；任命学校管理员仍然只有运营能做。Mac 第三轮已验证。

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
