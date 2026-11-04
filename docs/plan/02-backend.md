# 02 后端架构：照 deepfate-server 的骨架

**参照实现**：同目录的 `deepfate-server`（线上 heyfate.ai，提交 `c9e36da`）。它已经跑通了 Go 单服务 + PostgreSQL + Prometheus 指标 + 告警 + systemd 部署这一整套。本仓直接沿用它的目录、工具链和约定，只有业务模块不同。下文写「同 deepfate」的地方，实现时直接照它的文件改，不另起一套。

## 1. 技术栈

| 项 | 选择 | deepfate 对应 |
|---|---|---|
| 语言 | Go，**用建仓时的最新稳定版**，之后跟随小版本升级。11-03 最新稳定版是 1.27.3（go.dev），和 deepfate 一致 | Grok 上现装的是 1.24.9，建仓前先装好最新工具链 |
| HTTP | 标准库 `net/http`，`ServeMux` 路由模板（`GET /v1/labs/{id}`） | 同 |
| 数据库 | **PostgreSQL 18**（当前最新稳定大版本），驱动 `jackc/pgx/v5`（`pgxpool`）；手写 SQL，不用 ORM | deepfate 用的是 16，本仓不跟 |
| 迁移 | `migrations/*.sql` 嵌进程序，由 `cmd/migrate` 执行 | `migrations/migrate.go` |
| 指标 | `prometheus/client_golang` | 同，ADR-0024 |
| 契约 | `api/openapi.json`，加契约测试 | `api/contract.go`、`contract_test.go` |
| 日志 | `log/slog` JSON，每个请求一行 | 同 |

依赖只在确有需要时加。和 deepfate 一样，不预建接口工厂、通用工作流框架。

## 2. 仓库结构

```
AGENTS.md  CLAUDE.md  README.md  Makefile  Dockerfile  feature_list.json
api/                openapi.json、contract.go、contract_test.go
cmd/
  server/           HTTP API + 进程内后台任务；另开指标端口
  migrate/          执行迁移
  admin/            运维命令：建首个运营账号、导入课程内容、重置演示学校、对账、重放失败任务
  broker/           网页版每人容器的管理进程：唯一持有 Docker 权限的进程
  harness-check/    检查 feature_list.json、文档链接等仓库约定（同 deepfate）
internal/
  platform/         配置、ID、外部客户端
  gateway/          HTTP 层：路由、中间件（request_id、请求体上限、CORS、限流、认证、审计）、错误格式、指标
  account/          账号、密码、会话与刷新令牌、桌面端回环授权、角色与权限判断
  org/              学校、行政班
  content/          源课程、章节、实训、实训指引、发布版本、品类
  teaching/         教学班、成员、实训开放
  learning/         实训尝试、作品提交
  grading/          评分规则、AI 初评、评阅草稿、发布、申诉、成绩册
  modelgateway/     OpenAI 兼容的模型网关、模型令牌
  metering/         用量账本、预留与结算、预算
  catalog/          模型目录、渠道、单价
  jobs/             PostgreSQL 任务队列
  workspace/        每人云端 WorkSwarm：开容器、按登录身份转发、闲置回收（调用 broker）
  testdb/           测试用数据库辅助（同 deepfate）
migrations/         001_initial.sql …，migrate.go
deployments/
  compose.yaml      本地联合启动：postgres、migrate、server
  production/       deploy.sh、server.env.example、systemd 单元、compose.postgres.yaml、backup.sh
  monitoring/       config.alloy、rules.yaml、rules.test.yaml、看板 JSON、install.sh（上线后数据量起来再建）
scripts/            with-test-db.sh、smoke 脚本
docs/               index.md、architecture.md、decisions/、session-handoff.md、quality-document.md …
```

两处和 deepfate 不一样：

- deepfate 的 `internal/gateway` 是 HTTP 层，本仓同样这样用。所以模型网关改叫 `internal/modelgateway`，免得同名。
- deepfate 的分析运行时是独立的 Node 服务；本仓没有这一层，AI 初评由 `grading` 经后台任务直接调模型。

目录到实现相应功能时再建立。

**工作约定同 deepfate**：

- `feature_list.json` 是功能状态的唯一来源：`not_started`、`in_progress`、`blocked`、`passing`，同一时间只有一个 `in_progress`。
- 决策写成 `docs/decisions/NNNN-*.md`（ADR）。现在的 `docs/decisions.md` 建仓时拆成 0001 起的几条。
- 每次收尾更新 `docs/session-handoff.md`。
- `make check` 的内容：`harness-check`、带真实数据库的 `go test ./...`、部署脚本测试、`gofmt`、`git diff --check`。它全部通过，才算本地验证通过。

## 3. PostgreSQL

- **用最新稳定大版本，现在是 PostgreSQL 18**（11-03 最新是 18.6；19 还只有 RC1，正式发布前不用）。开发、测试、线上用同一个大版本，之后跟随小版本升级。大版本升级单独写 ADR，用 `pg_upgrade` 或导出再导入，先在备份上演练。智教云在没配数据库时会退回 SQLite，本仓不保留这条退路。
- **一个库，一个业务角色。** 线上数据库跑在独立容器里，只听 `127.0.0.1`；如果和 Go 服务在同一个 Compose 网络，就不映射端口。和智教云、agent.hrtiger.cn 现有的 postgres 容器分开，不共用实例。
- **迁移只向前。** `migrations/NNN_*.sql` 按文件名顺序执行，同 deepfate 的 `migrate.go`：
  - 整个过程在一个事务里，并持有 `pg_advisory_xact_lock`，两台机器同时发布也不会重复执行；
  - `schema_migrations` 记录文件的 sha256，已执行过的文件被改动时直接报错；
  - 回滚靠新的迁移文件，不写 down 脚本。
- **连接池**：`pgxpool` 上限做成配置项，池的状态作为指标暴露（见 §8）。
- **测试**：`scripts/with-test-db.sh` 临时起一个 PostgreSQL 18（`initdb` 到临时目录，跑完删除），也可以通过 `EDU_TEST_DATABASE_URL` 指定现成的库。不用 SQLite，也不用 mock 代替数据库。
- **备份**：同 deepfate 的 `backup.sh`。每天用 `pg_dump -Fc` 导出，保留 14 天；成功后写一个 textfile 指标，记录最近成功时间和大小，供备份过期告警使用。每个阶段上线前都要做一次恢复演练。
- **扩展**：首版不装扩展。P5 做知识库时再评估 pgvector，到时用对应 18 的 pgvector 镜像，单独写 ADR。
- **监控角色**：建一个只有 `pg_monitor` 权限的只读角色，给 postgres exporter 用。

## 4. 接口约定

- 前缀 `/v1`。JSON 字段用 `snake_case`，和现有插件接口一致，前端改动最小。
- 列表接口统一用游标分页：`?cursor=&limit=`。
- 写接口要求带 `Idempotency-Key`。服务端记录键和结果，重复请求返回第一次的结果。它取代智教云的各种「写入回执」表。
- 并发修改用行版本：请求带 `If-Match: <revision>`，冲突返回 409。不再有全局 revision。
- 错误统一为 `{ "error": { "code", "message", "details" } }`。`code` 是稳定的大写错误码，同时作为指标标签；`message` 用中文，可以直接展示。
- 范围放在路径里，例如 `/v1/teaching-spaces/{id}/submissions`，不放请求头。
- 客户端每次请求都带 `X-Client-Version`。服务端配置最低支持版本，低于它返回 426，前端提示升级。桌面客户端更新慢，这一条从 P1 就要有。
- 健康检查同 deepfate：`/livez` 只看进程存活，`/readyz` 检查数据库连通和迁移版本。外部拨测打 `/readyz`。

## 5. 认证与权限

**令牌**

| 令牌 | 有效期 | 用途 | 存放位置 |
|---|---|---|---|
| 访问令牌 | 15 分钟 | 调 `/v1` 接口 | 前端内存 |
| 刷新令牌 | 30 天，每次刷新轮换 | 换新的访问令牌 | 桌面版：Electron `safeStorage`；网页版：API 域名下的 HttpOnly cookie |
| 模型令牌 | 跟随登录会话，可单独吊销 | 只能调 `/v1/model-gateway/*` | 写进智能体的模型配置 |

- 服务端只存令牌的哈希。改密码、停用账号会吊销该账号的全部会话和模型令牌。会话和刷新令牌的轮换照 deepfate 的 ADR-0005。
- 访问令牌不交给本机 Python 进程，也不写进任何文件。
- 桌面版的令牌保护有上限，见 [客户端接入](04-client-integration.md) §4 的威胁说明。

**登录方式**

- P1：账号密码登录。学生和教师由运营或学校管理员开通，首次登录强制改密码。
- 桌面版可选浏览器授权：客户端打开系统浏览器登录，再经本机回环地址取回授权码（PKCE）。deepfate 的 ADR-0007 和 `internal/gateway/device.go`、`auth.go` 有现成实现。11-03 已定：P1 先做账号密码，这一种后加。
- P5：学校 SSO、华为账号。账号表从第一天就留 `identity_providers`，后加不用改表。

**演示账号与视角切换**

- `accounts.is_demo` 标记演示账号。演示学校在 `schools.is_demo` 上标记，固定绑定三个演示身份（运营、教师、学生）。
- 会话上多一个字段 `acting_account_id`。`POST /v1/session/perspective` 切换视角：服务端校验发起者是演示账号、目标是它的演示身份，然后更新会话，返回新的角色和默认页面。访问令牌随之重签，旧令牌立即失效。
- 之后所有权限判断都按 `acting_account_id` 进行，所以演示身份能做什么、不能做什么，和真实账号走同一套规则，不另写一套演示逻辑。
- 演示运营的「全平台」查询会额外加上 `schools.is_demo = true` 的条件；全局设置的写接口对演示会话一律返回 403。
- 网页版里，演示账号只有一个 WorkSwarm 容器，三个视角共用。

**桌面版的高权限账号**（11-03 用户确认运营、学校管理员可以用桌面版）

- 桌面版上，智能体和客户端是同一个系统用户，保存在本机的令牌保护不住（见客户端接入 §4）。所以高风险写操作要**二次确认密码**：建账号、停用账号、重置密码、改预算、改模型和单价、批量发布成绩。确认后 5 分钟内有效。
- 有运营或学校管理员角色的账号，桌面版刷新令牌缩短到 12 小时。

**权限**

每个请求先从会话得到 `account_id`（演示会话取 `acting_account_id`），再在业务模块里判断能否操作目标数据。身份永远来自会话，不信任请求体里的身份字段。规则集中在 `internal/account/authz.go`：

- 运营：全平台。
- 学校管理员：本校的账号、教学班、预算。
- 教师：自己任教的教学班；有建课权限的可以建、改自己的源课程。
- 学生：自己选的教学班；只能读自己的作品和成绩，只能看已开放的实训。

智教云 698 个测试里，权限相关的用例要优先改写成 Go 测试，这是最容易迁出漏洞的地方。

## 6. 学习与评测闭环（P2 核心）

把现在 `platform/backend/app/agent_client_api.py`、`agent_client_demo.py` 的行为搬过来，同时去掉演示捷径：

1. `GET /v1/me/catalog`：本人可见的课程、章节和实训，含指引和交付物，字段和现在的插件 catalog 一致。
2. `POST /v1/labs/{id}/attempts`：开始一次实训，返回 `attempt_id`。客户端在本机建目录，并把 `attempt_id` 记进 `.zhijiao/lab.json`。
3. `POST /v1/attempts/{id}/submissions`：提交本机采集的材料，包括最终回答、文件、执行步骤、学员指令、指引步骤和核查回答、用量。材料大小限制和现在相同。
4. 服务端取该实训最新的已确认评分规则，排一个 AI 初评任务，立即返回 `queued`。没有规则时返回 `no_rubric`，材料照样保存。
5. `GET /v1/submissions/{id}`：轮询评测结果。分项、理由、改进建议、「一次性完成」标记和封顶规则照搬现有实现。
6. 教师发布成绩之前，学生看到的结果都标注「AI 初评」。

现在的评测是在请求里同步调模型、等结果。改成任务队列以后，前端改为轮询。

## 7. 模型网关与计量

照搬 `agent_gateway.py` 已经验证过的做法，并把 web-deploy-plan 阶段 0 发现的问题一起修掉：

- `POST /v1/model-gateway/chat/completions`，OpenAI 兼容，支持流式和工具调用。
- 模型令牌按「账号 + 课程」放行和计量，不按实训。一个智能体会话里可能做多个实训，网关分不出来。
- **每次调用先预留、后结算**：预留额度 = 请求大小估算 + 输出上限；结束后按实际用量结算，失败的退回预留。账本是 `usage_ledger` 一张表，教学预算、学校额度、Token 计量都读它。这修掉了智教云「三个额度模块各算各的、都不读网关计量」的问题。
- 渠道密钥只在服务端环境变量里。页面只能启停模型、改单价，不能输入或查看密钥。
- 每人每日上限、并发上限做成配置项。正式使用前要按实测用量调回去（内测期临时调到 1 亿 token/天，见部署记录）。

## 8. Prometheus 指标

做法同 deepfate 的 ADR-0024，指标名前缀用 `edu_`。

**暴露**

- `cmd/server` 在独立端口 `EDU_METRICS_LISTEN`（默认 `127.0.0.1:4029`，设为空则关闭）提供 `/metrics`，不挂在对外处理器上。所以它不经 nginx，主端口访问 `/metrics` 返回 404。
- 端口要避开 deepfate 的 4019。部署时检查端口没有被同机其他项目占用。

**标签规则（同 deepfate）**

- 只用路由模板、状态码、稳定错误码、后台任务名、模型名、用途。
- **不放账号、学校、教学班、课程的 id，也不放 IP、路径参数和请求内容**。一是隐私，二是这些 id 会让序列数无限增长。
- 按学校、教学班、学生看用量，是运营总览和 Token 计量页的事，数据来自 PostgreSQL 的 `usage_ledger`，不来自 Prometheus。**Prometheus 管服务健康，业务报表查数据库**，两者不混用。
- 标签叫 `task`，不叫 `job`。`job` 是采集端加的标签，同名会被改写成 `exported_job`。

**指标清单**

| 指标 | 内容 |
|---|---|
| `edu_http_requests_total{route,status,error}` | 全部请求。没匹配到路由的记 `unmatched`，被限流挡下的记 `rate_limited` |
| `edu_http_request_duration_seconds{route}` | 非流式请求耗时。模型网关的流式响应不计入，否则 p95 只反映流的时长 |
| `edu_http_requests_in_flight` | 正在处理的请求，含流式响应 |
| `edu_db_pool_*` | 连接池：使用中、空闲、上限、取连接次数、等待次数与等待时长（同 deepfate `RegisterDBMetrics`） |
| `edu_task_last_success_timestamp_seconds{task}`、`edu_task_started_timestamp_seconds{task}`、`edu_task_failures_total{task,kind}` | 后台任务心跳。`task` 取值：`grading_worker`、`job_lease_reaper`、`usage_rollup`、`session_purge` |
| `edu_jobs_queued{kind}`、`edu_jobs_oldest_age_seconds{kind}` | 队列积压，以及最老一条任务等了多久 |
| `edu_jobs_finished_total{kind,result}` | 任务结束数，`result` 为 `succeeded`、`retried`、`failed` |
| `edu_logins_total{result}` | 登录结果：成功、密码错误、被限流、账号停用 |
| `edu_submissions_total{result}` | 作品提交：已排队、无规则、被拒（及原因码） |
| `edu_evaluations_total{status}`、`edu_evaluation_duration_seconds` | AI 初评结果和耗时（从排队到出结果） |
| `edu_model_gateway_requests_total{model,purpose,status}` | 模型调用，`purpose` 为 `agent`、`grading` |
| `edu_model_gateway_upstream_duration_seconds{model}` | 上游模型首字节耗时 |
| `edu_model_tokens_total{model,purpose,direction}` | 输入、输出 token 数，用于看总量趋势，不用于计费 |
| `edu_model_gateway_reservations_open` | 已预留、尚未结算的调用数。持续上涨说明结算漏了 |
| `go_*`、`process_*`、`go_build_info` | 运行时、内存、启动时间、版本 |

**采集、存储和看板**

- 同 deepfate：主机上装 Grafana Alloy，抓三类目标：服务指标端口、主机资源（unix exporter）、PostgreSQL（postgres exporter，用只读的 `pg_monitor` 角色）。另外通过 textfile 收集备份新鲜度。
- 指标送到哪里（11-03 已定）：先实测，再二选一。deepfate 推到 Grafana Cloud 免费版（新加坡）。本系统的主机在 UCloud，要先实测能否稳定连到 Grafana Cloud；连不上就在主机上自建 Prometheus 加 Grafana。服务端代码两种情况完全一样，区别只在 `config.alloy` 的 `remote_write` 地址。
- 看板放在 `deployments/monitoring/`，以 JSON 提交进仓库，分区参考 deepfate 的「Fate 服务总览」：概览、请求与延迟、错误、业务（登录、提交、AI 初评、模型网关）、运行时与数据库、主机、后台任务与备份。

**告警**（`deployments/monitoring/rules.yaml`，用 `promtool test rules` 跑单元测试，测试数据全部合成）

- 可用性：收不到指标、服务掉线、30 分钟内重启 3 次以上、外部拨测 `/readyz` 失败、证书 14 天内到期。
- 质量：SLO 定为已匹配路由的非 5xx 占比 99.5% / 30 天。按多窗口燃烧率告警：1 小时与 5 分钟都超 14.4 倍为严重，6 小时与 30 分钟都超 6 倍为警告；窗口内请求少于 20 个不判。另有 p95 超 1 秒的告警。
- 业务：AI 初评失败率偏高；评测队列最老任务超过 10 分钟；模型上游错误率偏高；预留长时间不结算。
- 资源：进程内存接近上限、连接池用满、磁盘或内存不足、PostgreSQL 掉线或连接数过高。
- 定时任务：任一后台任务超时没成功；备份 26 小时没成功。

**分两步做**（11-03 用户：等上线后数据量起来再加，先集中精力做别的）：

- **现在做**：只做服务端埋点，就是 `/metrics` 端口、HTTP 指标和连接池指标，这部分直接照 deepfate 的 `metrics.go` 改，成本很低。业务指标随模块顺手加，标签规则从一开始就遵守。
- **上线后数据量起来再做**：Alloy 采集、指标存储（Grafana Cloud 还是自建）、看板、告警规则和 `promtool` 测试，整套 `deployments/monitoring/`。上文的告警清单是到时候的方案，不在 P1–P4 的完成标准里。

## 9. 后台任务

PostgreSQL 表 `jobs` 加 `SELECT … FOR UPDATE SKIP LOCKED`，不引入消息队列：

- 任务类型：AI 初评、批量评阅、成绩发布、批量导入账号、用量汇总。
- 每个任务有尝试次数、下次运行时间、租约到期时间。进程崩溃后，租约到期的任务会被别的 worker 接走。
- 模型调用失败按指数退避重试；超过次数标记失败，在「评阅运行」页可以手动重试。
- 每一轮都更新 `edu_task_*` 心跳，队列积压通过 `edu_jobs_*` 暴露。

## 10. 网页版每人一个 WorkSwarm 容器（P2）

照 web-deploy-plan 的设计，用 Go 重写，去掉旧的 Jupyter、code-server、Coze 环境：

- **broker 单独一个进程**（`cmd/broker`）。它是唯一能操作 Docker 的进程，只提供开、停、查三个动作，经本机 Unix socket 接受 server 的请求，并校验调用方。server 本身不持有 Docker 权限。
- **容器**：每个账号一个，有自己的数据卷（实训目录、会话），沿用现在 client 容器的安全设置：只读根文件系统、去掉全部 capability、`no-new-privileges`、内存和进程数上限。出网只允许访问模型网关和本系统 API。
- **转发**：nginx 对 agent.hrtiger.cn 的每个请求做 `auth_request`，交给 server 的内部接口校验登录会话，返回这个人容器的地址，nginx 再转发过去。容器端口不对外。
- **模型令牌**：开容器时写进该容器的智能体配置，所以每个人的用量天然分开。
- **闲置回收**：一段时间没有连接就停容器，数据卷保留，下次登录再起。
- **容量**：开工前先在 gpu-train 上实测单个容器的内存和 CPU 占用，再定同时在线人数的上限，以及超过上限时的排队提示。

## 11. 部署

同 deepfate 的 ADR-0008 和 `deployments/production/`：

- 用 `CGO_ENABLED=0` 编出单个静态二进制，`deploy.sh` 只发布已提交的代码：编译 → 上传到 `releases/<提交>` → 执行迁移 → 切换 `current` 链接 → 重启 systemd 服务 → 检查 `/readyz`。保留最近 5 个版本，回滚就是把 `current` 指回上一个版本。迁移只向前，所以回滚的版本必须能兼容新表结构。
- 配置在 `/etc/edu-agent-server/server.env`（root 0600，由 systemd 读入）；模板 `server.env.example` 提交进仓库，不含真实值。
- PostgreSQL 18 跑在独立容器里（`compose.postgres.yaml`，镜像固定到小版本，如 `postgres:18.6`），数据卷单独命名，只听 `127.0.0.1`。注意 18 的官方镜像把数据目录改成了 `/var/lib/postgresql/18/docker`，卷要挂在 `/var/lib/postgresql`，照抄 16 的写法会把数据写在卷外面。备份用的 `pg_dump` 版本不能低于服务端。
- nginx 只反代 API 端口，指标端口不对外。
- 部署位置（11-03 用户确认）：和 agent.hrtiger.cn 同一台机器 `gpu-train`，单独部署，域名 `api.agent.hrtiger.cn`。这个域名**还没有解析**，所以 P1、P2 先在本地调试，上线时再配域名和证书。
- `gpu-train` 只有 Mac 端 Codex 能登录，所以发布由 Codex 执行 `deploy.sh`。Grok 负责准备已提交的版本和验收清单。
- **开发与调试分工**（11-03 用户确认）：
  - Grok 上的 Claude Code 写代码，跑不依赖服务端环境的检查：`go build`、`go vet`、`gofmt`、不连数据库的单元测试。
  - Mac 上的 Codex 负责调试和体验验证：带真实 PostgreSQL 的 `make check`、本地联合启动、broker 和每人容器、jiuwen 客户端的浏览器验收、`promtool test rules`。
  - 两边通过 agent_peer 直接沟通，不用用户转述。Grok 交给 Mac 的是**已提交的版本**：用 `git bundle` 增量包分段传输，Mac 核对提交哈希后再跑，报告里写明验证的是哪个提交。
  - Grok 上只有 PostgreSQL 客户端，没有服务端，也没有 Docker，所以不在 Grok 上补装这些，以 Mac 的结果为准。
  - Mac 环境（11-03 Codex 只读核对）：
    - 已经可用：Docker Desktop 27.5.1；Chrome 156 和 Python Playwright 1.64.1。deepfate 的 `make check` 11-03 在这台 Mac 上通过。
    - 开工前要补：Go 从 1.27.1 升到 1.27.3；装 PostgreSQL 18 服务端（`brew install postgresql@18`）。现有的 16.15 是 deepfate 在用，保留不动。`promtool` 等到做告警时再装。
    - 注意：`with-test-db.sh` 里写明 `postgresql@18` 的路径，不要依赖 `PATH` 里的 `initdb`、`pg_ctl`。本机 5432、15432 已经被 Docker 里的数据库占用，本仓的联调端口要另选。

## 12. 测试

- 单元测试和数据库测试一律用合成数据，数据库测试通过 `with-test-db.sh` 连真实 PostgreSQL。
- 迁移智教云某个模块之前，先列出它的测试用例，标出哪些规则保留、哪些随功能删除。迁移完成的标准是：保留的规则在 Go 里都有测试。
- 契约测试：每个已注册路由都在 `api/openapi.json` 里，响应结构和文档一致。
- 端到端测试：用 jiuwen 客户端走一遍「登录 → 去实训 → 提交 → 初评 → 教师发布 → 学生看到成绩」，再走一遍演示账号的三视角切换。在 Mac 上跑，报告里分开记：`make check`、端到端、体验检查（截图）。
