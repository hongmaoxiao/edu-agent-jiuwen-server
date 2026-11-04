# agent-api.hrtiger.cn 部署报告

2026-11-04，Mac Codex 执行；Grok 从独立网络复核公网。**验收 1–7 全部通过。未配置备份。**

## 一、部署了什么

| 项目 | 实际结果 |
|---|---|
| 代码 | main，`2ade9d02f5fdb55f83b2922f79aa515186bd1a96`；未改后端运行代码 |
| 主机 | `gpu-train`，UCloud，117.50.226.14，Linux amd64 |
| 构建 | 提交自带 Dockerfile 的 build 阶段；Go 1.27.3、CGO_ENABLED=0；三份静态 Linux 二进制 |
| 发布 | `/var/www/edu-agent-jiuwen-server/releases/2ade9d02f5fdb55f83b2922f79aa515186bd1a96`，`current` 指向该目录 |
| API | `edu-agent-server.service`，非 root 用户 `edu-agent`，开机自启，Restart=on-failure |
| 数据库 | 新建独立 `postgres:18.6` 容器、`edu_agent` 数据库和业务角色；卷 `edu-agent-api-postgres` 挂载 `/var/lib/postgresql`，实际 PGDATA 为 `/var/lib/postgresql/18/docker` |
| 端口 | API 127.0.0.1:4020；指标 127.0.0.1:4029；数据库 127.0.0.1:25432；部署前均空闲 |
| 运行配置 | production；网页来源 https://agent.hrtiger.cn；EDU_TRUST_PROXY=loopback；Cookie Domain 和最低客户端版本未设置 |
| 初始内容 | 两次迁移成功，第二次 3 条迁移记录及数据库结构完全不变；新建 2 个品类、1 门课、10 章、51 个实训、1 套规则 |
| 初始账号 | `ops`（运营）、`demo`（客户演示）；密码随机生成，仅通过标准输入传给 admin |
| 密码位置 | 服务器 `/etc/edu-agent-server/accounts.json`；数据库及应用环境分别为同目录 `postgres.env`、`server.env`；目录 root:root 0700，三个文件 root:root 0600 |

证书选择 **独立 Let's Encrypt 证书**。服务器已具备 certbot 镜像、注册账号和 HTTP 验证目录，公司通配符文件尚未提供，因此沿用已有机制。没有替换 agent.hrtiger.cn 或智教云的证书，没有改动它们的续期任务。

- 新证书：SAN `DNS:agent-api.hrtiger.cn`，签发者 Let's Encrypt YE2；有效期至 **2027-02-02 08:14:47 UTC**。
- 叶证书 SHA-256：`1e7d6aded8485050d4df9a3e20e7f0920a4ddf249215e62700fa7c6c021cdaaa`。
- 证书位于 `/etc/letsencrypt/live/agent-api.hrtiger.cn/`，未回传证书私钥。
- 新增 `edu-agent-api-certbot-renew.timer`，每日 04:15 和 16:15 后随机延迟最多 30 分钟检查；独立脚本仅续期 API 证书，与现有 agent 脚本共用锁。
- 首次容器网络访问 ACME 超时，切换宿主机网络后重试签发成功。首次续期演练遇到权威 DNS 的 CAA SERVFAIL；重试 `renew-certificate.sh --dry-run` **退出 0**。实际执行一次续期 systemd unit 也成功，当前有效证书按预期无需续签。相关证据在 `certificate-renewal.json`。

构建输入直接取自该提交的 Git 对象；归档 SHA-256 为 `090928f4ddcac78cda091d615d10b5f6bf5122e8a25ec7cfdb60237bf2750e6d`。实际二进制哈希、Go 构建信息、静态链接结果、配置哈希和初始化结果记录在 `server-deployment.json`。本次完成生产验收和部署脚本检查；未重复执行已验证源码的 `make check`。没有进行 Git 提交或推送。

## 二、验收 1–7

| 编号 | 实际执行的检查 | 结果 |
|---|---|---|
| 1 | Mac、服务器 `python3 deployments/production/verify.py`；Grok `curl --noproxy '*' --resolve agent-api.hrtiger.cn:443:117.50.226.14 https://agent-api.hrtiger.cn/livez` 和 `/readyz`；`openssl s_client -connect 117.50.226.14:443 -servername agent-api.hrtiger.cn -verify_hostname agent-api.hrtiger.cn -verify_return_error` | `/livez` **200 `{"status":"alive"}`**；`/readyz` **200 `{"status":"ready"}`**；TLS 链和域名验证成功，openssl verify return code 0。HTTP `/`、`/livez` 为 301，保留路径跳至 HTTPS。 |
| 2 | `OPTIONS /v1/auth/login`，Origin 为 `https://agent.hrtiger.cn`，请求头为 Authorization、X-Client-Version、Content-Type；另测 evil.example | 允许来源返回 204，Allow-Origin 精确匹配，Allow-Credentials=true，Allow-Headers 包含所需头。evil.example 返回 204，**不带任何 Access-Control 放行头**。 |
| 3 | 服务器 `python3 /var/www/edu-agent-jiuwen-server/deployments/production/verify.py` 从 root 私有文件读取密码；POST 登录、刷新和退出，响应令牌只留在内存中 | `ops` 的 web 登录 200；edu_refresh 带 **HttpOnly、Secure、SameSite=Lax、Path=/v1/auth，无 Domain**。带 Cookie 和正确 Origin 刷新 200，访问令牌与刷新 Cookie 都轮换；无 Origin 刷新 401。`demo` 也全部通过。测试会话已退出。 |
| 4 | Grok 公网 GET `/metrics`、`/metrics/`；对公网 IP 的 4020、4029、25432 执行两轮 8 秒 socket 探测及 curl；数据库另执行 `pg_isready -h 117.50.226.14 -p 25432 -t 5`；服务器 `ss -lnt` 和 Docker PortBindings | 两个指标路径均 **404**；三个端口从外网均超时，pg_isready 退出 2、no response。服务器确认三个端口均仅绑定 127.0.0.1。 |
| 5 | 服务器读取 `http://127.0.0.1:4029/metrics`，并用 curl 复核 | **200，有 Prometheus 输出及 edu_http 指标**；初次记录 18406 字节。 |
| 6 | Mac 对比原站点部署前后状态、跳转、页面哈希和证书指纹；Grok 独立复核；比较 `/etc/nginx` 与部署前快照的文件哈希；检查原容器启动时间 | agent 仍 **302 到原 `/login`**，智教云 **200**；两站页面哈希、证书指纹均不变。nginx 仅新增 **conf.d/agent-api.conf**，原文件变化 0、删除 0。原 agent 和智教云容器均未重启，已有健康检查仍 healthy。 |
| 7 | `systemctl restart edu-agent-server.service`，再用带重试的 curl 检查 `/readyz`；`systemctl show`、`is-enabled` | 重启后恢复为 **200 ready**，ActiveState=active、SubState=running；用户 edu-agent，Restart=on-failure；API、续期 timer 和 Docker 均已启用。 |

公网端口复核由 Grok 通过独立的洛杉矶出口完成，固定连接真实 IP 并绕过代理。它还验证了相同端口可连接 portquiz.net、目标主机 80/443 可连接，以及黑洞地址和关闭端口不会被假接通。Mac 的透明代理会先接受任意端口的 TCP 连接，因此没有拿 Mac 的纯 connect() 结果作为公网暴露依据。完整命令、时间戳和结果见 `Grok 报告` 与 `JSON`。这是一个外网出口的复核；服务器监听和 Docker 绑定的证据同时支持端口隔离结论。

原站点前后状态与新证书信息见 `mac-public-state.json`。原证书指纹：

- agent.hrtiger.cn：`2c8fc38dba006286d0e94458c49c2a0f5720315d9fcb30061f95f26489d1111b`。
- practise.hrtiger.cn：`5d07e8d15ee8549a1e312a0d7ce450c707ef7394198ab298a1e3b9857c46bdeb`。

`nginx -t` 通过并返回 0；它同时报告现有 `ai-router-demo.conf:18` 和 `ai.conf:42` 的监听选项重定义警告。两份原文件哈希均未变化，本次未修改这些站点。验收脚本最初把 livez 误按纯文本断言，已按固定提交的 JSON 契约修正为 `{"status":"alive"}` 后通过；没有改线上接口。

## 三、服务器改动与回滚

新增内容：

- 系统用户、组 `edu-agent`；`/etc/edu-agent-server/` 下三个私有文件。
- `/var/www/edu-agent-jiuwen-server/` 下发布目录、current 链接和本目录的公开部署配置。
- 构建镜像 `edu-agent-api-build:2ade9d0`，拉取的 `postgres:18.6` 镜像；新 Compose 网络 `edu-agent-api_default`、容器 `edu-agent-api-postgres-1`、命名卷 `edu-agent-api-postgres`。
- `/etc/systemd/system/edu-agent-server.service`、API 证书续期 service/timer 及开机启用链接。
- `/etc/nginx/conf.d/agent-api.conf`；nginx 每次变更前均测试，通过后热加载。
- API 独立证书及其 certbot 续期配置；共用现有 HTTP 验证目录和 certbot 账户。
- root 私有操作记录与 nginx 快照 `/var/backups/edu-agent-api/20261104-initial/`。**未配置数据库自动备份或恢复演练。**

没有部署、重建或修改 agent 的前端、登录页、容器及智教云，没有连接它们的数据库，没有修改 DNS、安全组或防火墙规则，没有删卷或删除业务数据。

首次上线前没有旧版 Go API，撤销时只停用这次新增内容，保留数据库和所有数据：

```bash
systemctl disable --now edu-agent-server.service edu-agent-api-certbot-renew.timer
mv /etc/nginx/conf.d/agent-api.conf \
  /var/backups/edu-agent-api/20261104-initial/agent-api.conf.disabled
nginx -t && systemctl reload nginx
docker compose -f /var/www/edu-agent-jiuwen-server/deployments/production/compose.postgres.yaml stop postgres
```

不恢复整个 nginx 快照，避免覆盖后续他人修改；不删除数据库卷、密码文件、证书或发布目录。后续版本回滚是将 current 指回兼容当前数据库结构的已保留 release，重启 Go 服务并检查 readyz；迁移只向前。

完整实际步骤及配置用途见 [README.md](README.md)。本目录可整体收进服务端仓库 `deployments/production/`；只包含公开配置、占位模板、操作脚本和无密钥证据。

证据文件（`evidence/`：服务器状态、证书续期、公网端口复核）不进仓库，原件在 Grok `/workspace/box-dev/workspaces/agent-peer/reports/agent-api-production-20261104-ac5d7782/files/deployments/production/evidence/`。
