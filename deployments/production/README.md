# agent-api.hrtiger.cn 生产部署

目标主机是 UCloud `gpu-train`（117.50.226.14，Linux amd64），与 agent.hrtiger.cn 同机。这里只新增 Go API、独立 PostgreSQL 和 nginx 站点。首次上线版本是 `2ade9d02f5fdb55f83b2922f79aa515186bd1a96`，实际验收见 [deployment-report.md](deployment-report.md)。

**未配置备份。** 每日数据库备份、异机备份和恢复演练留待有真实用户数据之前补齐。下述 nginx 配置备份不等于数据库备份。

## 文件和状态

| 用途 | 服务器路径或名称 |
|---|---|
| 发布根目录 | `/var/www/edu-agent-jiuwen-server` |
| 运行版本 | `current` → `releases/2ade9d02f5fdb55f83b2922f79aa515186bd1a96` |
| Go 服务 | `edu-agent-server.service`，用户 `edu-agent` |
| API / 指标 | `127.0.0.1:4020` / `127.0.0.1:4029` |
| 数据库 Compose | `deployments/production/compose.postgres.yaml`，项目 `edu-agent-api` |
| 数据库容器 / 卷 | `edu-agent-api-postgres-1` / `edu-agent-api-postgres` |
| 数据库端口 | `127.0.0.1:25432` |
| 服务环境 / 数据库环境 | `/etc/edu-agent-server/server.env` / `postgres.env` |
| 初始账号密码 | `/etc/edu-agent-server/accounts.json`，包含 `ops` 和 `demo` |
| 私有配置权限 | `/etc/edu-agent-server` 为 root:root 0700，以上三个文件为 root:root 0600 |
| nginx | `/etc/nginx/conf.d/agent-api.conf` |
| TLS | `/etc/letsencrypt/live/agent-api.hrtiger.cn/` |
| 独立续期任务 | `edu-agent-api-certbot-renew.timer`，每日两次 |
| 上线前配置快照及操作日志 | `/var/backups/edu-agent-api/20261104-initial/`，root 私有 |

配置模板只有占位符；不要把实际 env、账号文件、证书私钥或整个 nginx 快照回传到仓库。Go 业务角色 `edu_agent` 拥有新库，无超级用户、建角色或建库权限。数据库管理员密码与业务密码分开生成。

## 首次上线的实际步骤

以下服务器命令以 root 执行。堡垒机连接使用项目 `platform/deploy/practise-target.json` 已有配置；保持严格主机密钥校验，在菜单按 IP 选择 `117.50.226.14`，进入后核对 `hostname` 为 `gpu-train`。SSH 主机密钥校验失败时停止，不绕过。

1. **核对版本与基线。** Mac 新建独立服务端 checkout，确认 main HEAD，保留客户端工作区和所有已有修改。部署前从 Mac 记录 agent 的 302 登录跳转、智教云的 200 页面及两个证书指纹。在服务器确认 4020、4029、25432 均未监听，并核对现有容器。

   ```bash
   git rev-parse HEAD
   # 2ade9d02f5fdb55f83b2922f79aa515186bd1a96
   hostname
   uname -m
   ss -lnt '( sport = :4020 or sport = :4029 or sport = :25432 )'
   docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
   ```

2. **保存现有配置。** 在添加新站点前执行；不要把备份放进 nginx 自动包含的 conf.d 目录。

   ```bash
   umask 077
   mkdir -p /var/backups/edu-agent-api/20261104-initial
   cp -a /etc/nginx /var/backups/edu-agent-api/20261104-initial/nginx
   find /etc/nginx -type f -exec sha256sum {} + | sort \
     > /var/backups/edu-agent-api/20261104-initial/nginx-before.sha256
   docker ps --format '{{.Names}} {{.ID}} {{.Status}}' \
     > /var/backups/edu-agent-api/20261104-initial/containers-before.txt
   ss -lnt > /var/backups/edu-agent-api/20261104-initial/listeners-before.txt
   ```

3. **传输固定提交的构建输入。** Mac 从 Git 对象归档，未纳入未提交的部署文件或本机配置。通过已有堡垒机连接用分块 Base64 传输到发布根目录，在服务器解码后核对 SHA-256，再解压到 `releases/<完整提交>`。本次构建输入归档为 183687 字节，SHA-256 为 `090928f4ddcac78cda091d615d10b5f6bf5122e8a25ec7cfdb60237bf2750e6d`。部署配置另包传输，只包含本目录公开文件。

   ```bash
   # Mac，服务端 checkout
   git archive --format=tar.gz --output=/tmp/edu-agent-server-2ade9d0-build.tar.gz \
     2ade9d02f5fdb55f83b2922f79aa515186bd1a96 \
     cmd internal migrations seed go.mod go.sum Dockerfile

   # gpu-train，设置一次后在后续同一 shell 使用
   release=/var/www/edu-agent-jiuwen-server/releases/2ade9d02f5fdb55f83b2922f79aa515186bd1a96
   production=/var/www/edu-agent-jiuwen-server/deployments/production
   mkdir -p "$release"
   tar -xzf /var/www/edu-agent-jiuwen-server/build-source.tar.gz -C "$release"
   chmod -R a+rX "$release"
   ```

4. **构建三份静态二进制。** 使用该提交自带的 Dockerfile 的 build 阶段（Go 1.27.3，CGO_ENABLED=0）。仅运行新建的构建镜像，不重建或重启任何已有项目。

   ```bash
   docker build --target build -t edu-agent-api-build:2ade9d0 "$release"
   docker pull postgres:18.6
   mkdir -p "$release/bin"
   chmod 755 /var/www/edu-agent-jiuwen-server /var/www/edu-agent-jiuwen-server/releases "$release" "$release/bin"
   docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
     -v "$release/bin:/release" --entrypoint sh edu-agent-api-build:2ade9d0 \
     -c 'cp /out/* /release/'
   chmod 755 "$release/bin/"*
   ```

5. **创建服务用户和私有配置，启动独立数据库。** `initialize-secrets.py` 仅允许 root 执行，使用随机密码，拒绝覆盖已有文件。初始化脚本只在新卷第一次启动时建业务角色及新库；不得复用已有服务的数据库或卷。

   ```bash
   getent passwd edu-agent || useradd --system --user-group \
     --home-dir /nonexistent --shell /usr/sbin/nologin edu-agent
   python3 "$production/initialize-secrets.py"
   docker compose --progress plain -f "$production/compose.postgres.yaml" up -d --wait
   ```

6. **迁移两次、导入课程、建账号。** 该脚本以 `edu-agent` 身份执行后端工具，密码只通过标准输入传给 admin，不出现在命令行或输出里。第二次迁移比较迁移记录（含时间戳）和结构导出的 SHA-256。重复执行会保留已有账号及密码，不重置它们。

   ```bash
   python3 "$production/initialize-data.py"
   ```

7. **安装 systemd 服务。** 首次安装建立 current 链接；更新发布时记录旧链接，再切到新版本。数据库容器随已启用的 Docker 服务自启动。

   ```bash
   ln -s releases/2ade9d02f5fdb55f83b2922f79aa515186bd1a96 \
     /var/www/edu-agent-jiuwen-server/current
   install -m 644 "$production/edu-agent-server.service" /etc/systemd/system/
   systemd-analyze verify /etc/systemd/system/edu-agent-server.service
   systemctl daemon-reload
   systemctl enable --now edu-agent-server.service
   curl --fail --silent --show-error --retry 10 --retry-delay 1 --retry-connrefused \
     http://127.0.0.1:4020/readyz
   ```

8. **新增 HTTP 验证站点并签证书。** 先确认新站点文件不存在，安装临时 HTTP 配置，测试通过才 reload。首次普通容器网络请求 ACME 超时，宿主机访问正常，重试与新续期脚本采用 `--network host`。使用现有 certbot 镜像、ACME 验证目录和注册账号，新增独立证书，不替换 agent 的证书。与现有 agent 续期任务共用锁，避免并发操作同一证书目录。

   ```bash
   test ! -e /etc/nginx/conf.d/agent-api.conf
   install -m 644 "$production/agent-api.http.conf" /etc/nginx/conf.d/agent-api.conf
   nginx -t && systemctl reload nginx
   flock -w 300 /run/edu-agent-certbot.lock docker run --rm --network host \
     --name edu-agent-api-certbot-issue \
     -v /etc/letsencrypt:/etc/letsencrypt \
     -v /var/lib/letsencrypt:/var/lib/letsencrypt \
     -v /var/log/letsencrypt:/var/log/letsencrypt \
     -v /var/www/practise-acme:/var/www/practise-acme \
     certbot/certbot:v5.8.0 certonly --webroot -w /var/www/practise-acme \
     --cert-name agent-api.hrtiger.cn -d agent-api.hrtiger.cn --non-interactive --agree-tos
   ```

9. **启用 HTTPS 和独立续期任务。** HTTPS 配置只反代 4020；4029 不进入 nginx，`/metrics` 和其子路径明确返回 404。`X-Real-IP` 由 nginx 覆盖，配合 `EDU_TRUST_PROXY=loopback`，限流读取真实来源地址。

   ```bash
   install -m 644 "$production/agent-api.nginx.conf" /etc/nginx/conf.d/agent-api.conf
   nginx -t && systemctl reload nginx
   install -m 644 "$production/edu-agent-api-certbot-renew.service" \
     "$production/edu-agent-api-certbot-renew.timer" /etc/systemd/system/
   systemctl daemon-reload
   systemctl enable --now edu-agent-api-certbot-renew.timer
   "$production/renew-certificate.sh" --dry-run
   ```

10. **验收并保存无密钥证据。** 服务器运行完整验收会读取 root 私有账号文件，仅输出断言结果；Mac / Grok 使用 `--public-only`。本次还在 Mac 检查公网 4020、4029、25432 连接失败，对比原站点 HTTP 行为、页面哈希、证书指纹及容器身份。

    ```bash
    python3 "$production/verify.py"
    curl --fail --silent http://127.0.0.1:4029/metrics | head
    systemctl restart edu-agent-server.service
    curl --fail --silent --show-error --retry 10 --retry-delay 1 --retry-connrefused \
      https://agent-api.hrtiger.cn/readyz
    ```

## P2：每人一个 WorkSwarm 容器（还没上线）

服务端多一个 broker 进程，客户端那边 agent.hrtiger.cn 换成按登录身份转发，步骤见客户端仓库 `deploy/README.md` 的「每人一个容器」。两边要一起上线：先上本节，验证通过再切 agent.hrtiger.cn。

| 用途 | 服务器路径或名称 |
|---|---|
| broker | `edu-agent-broker.service`，用户 `edu-agent-broker`、组 `edu-agent`，附加组 `docker` |
| broker socket | `/run/edu-agent-broker/broker.sock`（0660，服务端 `edu-agent` 同组可连） |
| broker 配置 | `/etc/edu-agent-server/broker.env`（root 0600），模板 [broker.env.example](broker.env.example) |
| 模型网关令牌 | `/etc/edu-agent-server/gateway-token`（root 0600），systemd `LoadCredential` 交给 broker |
| nginx 内部校验 | 服务端 `127.0.0.1:4021`，只给 agent.hrtiger.cn 的 `auth_request` 用，不经 agent-api 站点 |
| 各人容器 | 名字 `edu-ws-<账号哈希>`，端口 `127.0.0.1:57000–57999` 一人一个，网络在 `10.231.0.0/16` 里各 /28，标签 `edu.owner=edu-agent-ws-v1` |

步骤（root）：

1. 构建并放进新的 `releases/<提交>`：`bin/edu-agent-server`、`bin/edu-agent-broker`、`bin/edu-agent-migrate`、`bin/edu-agent-admin`（`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`）。
2. 建用户：`useradd --system --no-create-home --shell /usr/sbin/nologin --gid edu-agent edu-agent-broker`。
3. 写 `/etc/edu-agent-server/broker.env`（照模板，镜像写客户端这次构建的标签）；把客户端 `deploy/secrets.env` 里 `AGENT_GATEWAY_DEMO_TOKEN` 的值写进 `/etc/edu-agent-server/gateway-token`，不打印。`server.env` 按 [server.env.example](server.env.example) 加 `EDU_WORKSPACE_*` 和 `EDU_INTERNAL_LISTEN`。
4. 迁移（004 教学班、005 容器），再 `edu-agent-admin demo-reset demo` 给已有的演示账号建演示社会班。
5. 原站点登录页的账号保留为演示账号：用户名和平台演示账号相同（应是 `demo`，不是就先停下来问）；把 Mac `/Users/xhm/project/edu-agent-jiuwen/deploy/access.env` 里的密码经标准输入交给 `edu-agent-admin set-password <用户名>`，不出现在命令行、日志和回复里。
6. 装 `edu-agent-broker.service`、更新 `edu-agent-server.service`，`systemctl daemon-reload && systemctl enable --now edu-agent-broker && systemctl restart edu-agent-server`。
7. 检查：`/readyz` 200；socket 存在且属组 `edu-agent`；`ss -ltn` 里 4021 只在 127.0.0.1；agent-api 站点访问 `/internal/workspace/auth` 是 404。

运行中要知道的：

- 课程服务容器重建后会从各人网络里掉出去，broker 每 30 秒把它接回来。
- 容器被系统杀掉（如内存不够）时，数据库里还记着「就绪」，这个人会看到连不上，要等闲置回收（默认 30 分钟）或运营停用再启用账号。这一条还没自动处理。
- 所有容器暂时共用同一个模型网关令牌，用量记在 `browser-demo` 名下，按人分开计量还没做。
- 回滚：停 `edu-agent-broker`，`server.env` 去掉 `EDU_WORKSPACE_BROKER` 后重启服务端，再按客户端仓库的步骤换回旧的 agent.conf。各人容器用 `docker ps -aq --filter label=edu.owner=edu-agent-ws-v1` 找到后删掉，卷保留。

## 回滚

首次上线前没有旧版 Go 服务。若需撤销这次新增服务，停用 `edu-agent-server.service` 和 API 续期 timer，把 **仅有的新文件** `/etc/nginx/conf.d/agent-api.conf` 移到 root 私有快照目录外的停用文件，`nginx -t` 通过后 reload。需要时用此 Compose 文件的 `stop postgres` 停止新数据库；保留容器、卷、密码、证书、发布目录和全部数据，不执行 `down -v` 或删除卷。旧站点无需替换配置或重启容器。

```bash
systemctl disable --now edu-agent-server.service edu-agent-api-certbot-renew.timer
mv /etc/nginx/conf.d/agent-api.conf /var/backups/edu-agent-api/20261104-initial/agent-api.conf.disabled
nginx -t && systemctl reload nginx
docker compose -f /var/www/edu-agent-jiuwen-server/deployments/production/compose.postgres.yaml stop postgres
```

以后发布回滚只把 `current` 指向已保留且兼容当前数据库结构的旧 release，再重启服务并检查 `/readyz`。迁移只向前，不能通过回滚二进制撤销数据库结构。不要批量恢复整个 nginx 快照，以免覆盖他人之后新增的配置。
