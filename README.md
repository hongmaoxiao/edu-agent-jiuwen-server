# edu-agent-jiuwen-server

基于 jiuwen 的 AI 教学实训平台的后端：账号与角色、学校、课程与实训、教学班、作品提交、AI 初评与教师评阅、模型网关与用量计量。客户端在同级目录的 `edu-agent-jiuwen` 仓库。

从 [总体规划](docs/plan/00-overview.md) 读起；工作约定见 [AGENTS.md](AGENTS.md)。

## 本地运行

需要 Go 1.27.3 和 PostgreSQL 18。

```sh
make check                                   # 全部检查；没有 EDU_TEST_DATABASE_URL 时用 Docker 临时起 postgres:18.6
export DATABASE_URL='postgres://…/edu?sslmode=disable'
go run ./cmd/migrate                         # 执行迁移
printf '%s\n' '<密码>' | go run ./cmd/admin create-operator ops 运营   # 首个运营账号，密码从标准输入读
go run ./cmd/admin import-content seed/fde-content.json                # 导入 FDE 课程，可以重复执行
printf '%s\n' '<密码>' | go run ./cmd/admin create-demo demo 客户演示   # 演示账号（三视角切换）
EDU_ENV=development go run ./cmd/server      # API 在 127.0.0.1:4020，指标在 127.0.0.1:4029
```

配置都来自环境变量，见 `internal/platform/config.go`。
