# edu-agent-jiuwen-server 工作约定

基于 jiuwen 的 AI 教学实训平台的 Go 后端。客户端在同级目录 `edu-agent-jiuwen`。做法参照同级目录 `deepfate-server`。

## 开工

1. 读 [文档地图](docs/index.md) 和 [最新交接](docs/session-handoff.md)。
2. `git status --short --branch`，保留已有修改。
3. 读 [功能清单](feature_list.json)，确认用户当前授权的范围。一次推进一个功能，标为唯一的 `in_progress`。
4. 读目标功能对应的规划和决策：[规划](docs/plan/00-overview.md)、[决策记录](docs/decisions.md)。

## 本仓职责与边界

- 账号与角色、学校、课程与实训、教学班、作品与 AI 初评、教师评阅、模型网关与计量。前端页面不在本仓。
- 身份只来自已验证的会话，不信任请求体里的身份字段。权限判断集中在 `internal/account/authz.go`。
- 对外契约只有 `api/openapi.json`。改路由必须同时改它，`TestRoutesMatchOpenAPI` 会检查。
- 密码、令牌、模型密钥、作品内容不进日志、Git 和测试快照；测试只用合成数据。
- 指标标签不放账号、学校等 id。按学校、学生看的用量查数据库。
- 迁移只向前；已执行过的 SQL 文件不能改，改结构就加新文件。

## 验证

- **Grok**（开发）：`go build ./...`、`go vet ./...`、`go test ./...`。没有 PostgreSQL，连库测试会跳过，**这不算通过**。
- **Mac**（调试验证，经 agent_peer 交给 Codex）：`make check`，用 Docker 临时起 PostgreSQL 18 跑全部测试；联调和体验验证也在 Mac。
- 交给 Mac 的必须是已提交的版本，Mac 核对提交哈希后再跑。报告分别记录：`make check`、联调或端到端、体验检查。

## 状态

`feature_list.json` 是功能状态的唯一来源：`not_started`、`in_progress`、`blocked`、`passing`。`passing` 必须有 Mac 上实际通过的证据。不删失败的测试，不降低验收标准。

## 收尾

更新 `docs/session-handoff.md`：做了什么、验证结果（分 Grok、Mac 记）、没做的和下一步。提交前按用户的审查流程处理。推送、部署要用户明确同意。
