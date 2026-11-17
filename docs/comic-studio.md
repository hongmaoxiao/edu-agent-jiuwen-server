# AI 漫剧制作工作台（服务端）

写于 2026-11-17。客户端在 `edu-agent-jiuwen` 的 `extensions/edu_course/frontend/studio/`，接口约定见客户端仓 `docs/ai-comic-studio/api.md`，设计说明见同目录 `design-brief.md`。

## 为什么放在 Go 服务里

- **密钥**：MiniMax、火山方舟、DeepSeek 的密钥只在这台服务器上。学员容器里的智能体能读到容器的环境变量，并把命令输出上传到教师评阅页，所以密钥绝不能进容器。
- **任务时长**：视频生成要几分钟。容器闲置会被回收，学员也可能关掉页面，所以任务由服务端后台协程跑。
- **结果保存**：模型给的结果链接大约 24 小时就失效，生成成功后立刻下载到 `EDU_COMIC_MEDIA_DIR`。

## 结构

| 文件 | 内容 |
|---|---|
| `internal/comic/catalog.go` | 模型目录：能力（时长、分辨率、画幅、原生声音、参考图上限）、单价、可用性；参数校验；预置音色、画风 |
| `internal/comic/service.go` | 项目（JSON 文档 + 版本号乐观锁）、媒体（签名链接）、创作点账本 |
| `internal/comic/tasks.go` | 提交（扣点）、列表、取消；后台协程：领任务（租约 + SKIP LOCKED）、按供应商限并发、异步轮询、完成或失败退点 |
| `internal/comic/minimax.go` | H3 视频（`/v2/video_generation`）、H3-Image（`/v1/images/generations`、`/edits`）、语音（`/v1/t2a_v2`）、音色设计（`/v1/voice_design`） |
| `internal/comic/ark.go` | Seedance 2.0 / Fast（`/api/v3/contents/generations/tasks`）、Seedream 5.5（`/api/v3/images/generations`） |
| `internal/comic/text.go` | 剧本解析与分镜：OpenAI 兼容接口（默认 DeepSeek），输出 JSON，服务端再规整 |
| `internal/comic/compose.go` | ffmpeg 合成：裁剪、统一画幅、淡入淡出、原声/对白/配乐混音、内嵌或烧录字幕、H.264/AAC MP4 + SRT |
| `internal/comic/sim.go` | 模拟供应商：`EDU_COMIC_SIMULATE=1` 时出现，生成真实的小文件，不扣点 |
| `internal/gateway/comic.go` | `/v1/comic/*` 路由 |
| `migrations/007_comic.sql` | `comic_projects`、`comic_tasks`、`comic_media`、`comic_ledger` |

## 规则

- **项目内容归客户端**：剧本、资产、分镜、时间轴、采用关系是一份 JSON。服务端只校验大小（≤ 2 MB）和版本。任务和媒体由后台写进各自的表，不改文档，所以不会和学员的编辑互相覆盖。
- **解析结果不直接写进项目**：parse/storyboard 的结果放在任务的 `result` 里，学员确认后才由客户端写进文档。
- **扣点**：提交时按预估扣，失败或取消全额退，图片部分失败按张退。模拟模型不扣点。
- **初始额度**：账号第一次打开漫剧时，送 `EDU_COMIC_INITIAL_CREDITS` 点。运营加点用 `POST /v1/comic/credits`。
- **服务重启**：
  - 已经交给模型的异步视频任务，重启后按 `provider_task_id` 继续查。
  - 同步任务做到一半时重启，结果拿不回来，按失败处理，全额退点。
- **超时**：异步任务两小时还没出结果，按失败退点。
- **并发**：每个供应商最多 3 个任务同时在跑（方舟个人账号的 Seedance 并发上限是 3），文本 4 个，合成 1 个。
- **媒体链接**：形如 `/v1/comic/media/{id}?e=&s=`，HMAC 签名，12 小时有效。签名钥匙在媒体目录的 `.url-key`。`<img>`、`<video>` 不带令牌也能加载。

## 单价（创作点，1 点约 0.1 元）

按 2026-11 的官方价目表取整，改价只改 `catalog.go`：

- H3：768P 每秒 5 点，2K 每秒 8 点。
- H3 Highspeed：480P 每秒 3 点，768P 每秒 4 点。
- Seedance 2.0：480p 每秒 4 点，720p 每秒 6 点，1080p 每秒 10 点。
- Seedance 2.0 Fast：480p 每秒 3 点，720p 每秒 4 点。
- H3-Image：1K 每张 1 点，2K 每张 2 点，4K 每张 4 点。
- Seedream 5.5：每张 3 点。
- 配音：每 100 字 1 点。
- 音色设计：99 点，因为模型方在首次使用时收 9.9 元。
- 剧本解析：2 点。
- 合成：0 点。

Seedance 的价格是按官方 token 计价估算的，拿到真实账单后要再核对。

## 部署

见 `deployments/production/server.env.example` 末尾的 `EDU_COMIC_*`，以及 `edu-agent-server.service` 的 `StateDirectory`、`agent-api.nginx.conf` 的 `/v1/comic/`。主机需要 `ffmpeg` 和 `ffprobe`，以及 libx264、libmp3lame。

## 验证

- `go test ./internal/comic/` 不需要数据库：用假的 HTTP 服务核对 MiniMax 和方舟的请求格式、错误说明，以及模拟解析。
- `internal/gateway/comic_test.go` 需要 PostgreSQL 和 ffmpeg，用模拟模型走完整链：解析 → 部分失败的生图 → 配音 → 首帧生视频 → 合成 MP4（Range 下载、签名防篡改），再验证扣点和退点、参数随模型变化、余额不足 402、版本冲突 409。
