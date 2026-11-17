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
| `internal/comic/compose.go` | ffmpeg 合成：按轨道排视频（裁剪、定格、空档补黑）、五类声音轨混音、对白期间压低音乐、响度调整和测量、内嵌或烧录字幕、H.264/AAC MP4 + SRT |
| `internal/comic/sim.go` | 模拟供应商：`EDU_COMIC_SIMULATE=1` 时出现，生成真实的小文件，不扣点 |
| `internal/gateway/comic.go` | `/v1/comic/*` 路由 |
| `migrations/007_comic.sql` | `comic_projects`、`comic_tasks`、`comic_media`、`comic_ledger` |
| `migrations/008_comic_request_id.sql` | 提交去重用的 `comic_tasks.request_id` |
| `scripts/comic-local.sh` | 本地演示：起数据库、迁移、建演示账号、读本机密钥文件、启动服务 |

## 规则

- **项目内容归客户端**：剧本、资产、分镜、时间轴、采用关系是一份 JSON。服务端只校验大小（≤ 2 MB）和版本。任务和媒体由后台写进各自的表，不改文档，所以不会和学员的编辑互相覆盖。
- **解析结果不直接写进项目**：parse/storyboard 的结果放在任务的 `result` 里，学员确认后才由客户端写进文档。
- **扣点**：提交时按预估扣，失败全额退，图片部分失败按张退。模拟模型不扣点。
- **取消**（用户 2026-11-17 定）：
  - 还在排队、没发给模型的任务，取消后全额退点。
  - 已经发给模型的任务（状态 running）也能取消，但模型费用已经产生，不退点。取消只是不再等结果，错误码是 `CANCELLED_AFTER_SEND`。
  - 后台只把仍在排队的任务发给模型，所以领取后、发出前被取消的任务不会再发出去。
  - 取消时按读到的状态精确匹配：读到排队、这期间刚好被发出的，按已发出处理，不退。
- **结果待核对**（用户 2026-11-17 定）：
  - 提交时连接都没建立（含代理连不上），请求肯定没发出，按失败全额退。
  - 请求发出后超时或断开、回复没收完整、2xx 回复看不懂：不知道模型有没有收到。任务进 `unknown`（结果待核对），错误码 `RESULT_UNKNOWN`，点数冻结。
  - 待核对期间，同一项目同一 target 不能再提交，也不能取消。
  - 运营到供应商后台查，确实收到了用 `edu-agent-admin comic-settle <任务号> charge [备注]`（不退点），没收到用 `refund`（全额退）。任务变成 `failed`，错误码是 `SETTLED_CHARGED` 或 `SETTLED_REFUNDED`。
  - 已经拿到供应商任务号的任务，轮询时网络出错不算失败，30 秒后接着查；两小时还没结果才按失败处理。
  - 服务重启导致同步任务中断，是平台原因，照旧全额退。
  - 模拟模型提示词里写「【模拟未知】」可以演示这个状态。
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

## 本地演示

`scripts/comic-local.sh` 在本机起一个完整的漫剧服务端，网页用客户端仓库的 `npm run studio:local`。完整步骤见客户端仓库 `docs/ai-comic-studio/local-demo.md`。

- 需要：Go、ffmpeg 和 ffprobe、Docker（用来起 PostgreSQL 18）。也可以不用 Docker：把 `DATABASE_URL` 指向一个空的 PostgreSQL 16 或更新版本的库。
- 密钥：`~/.edu-comic-keys/` 下放 `minimax`、`ark`、`deepseek` 三个文件，每个文件只写密钥本身，权限 600。缺哪个，哪家模型就显示「暂不可用」。模拟模型一直都在。
- 第一次运行时：建演示账号 `demo`，密码默认 `demo-password-1`，可以用 `EDU_DEMO_PASSWORD` 改。运营、学员、教师三个视角各加 1000 创作点。
- 数据和媒体放在 `~/.edu-comic-local/`。PostgreSQL 容器叫 `edu-comic-pg`，数据卷同名。重跑脚本不会清掉数据。

## 提交去重和清洁版

- 提交任务可以带 `request_id`，最长 64 字符：
  - 同一账号、同一 `request_id` 再提交，返回原来那个任务，状态码同样是 201，不再扣点。
  - 客户端每次确认生成时新建一个 uuid。网络断开重发时复用同一个。
- 合成的 `compose.clean=true` 且有字幕时，另出一份无字幕的清洁版：
  - 同一条时间线、同一路声音，不收费。
  - 存为单独的媒体，target 是 `project/export/clean`，meta 里 `clean: true`。
  - 任务结果里的 `clean_media` 是它的媒体 id。

## 多轨合成（V2 第 3 步）

`compose` 任务的输入有两种格式，内部都转成轨道再渲染：

- **多轨**（客户端现在只发这个）：`tracks: { video, audio, subtitles, duck }`。
  - 视频片段：`media`、`start`（成片里的起点）、`in`/`out`（素材区间）、`hold`（末帧定格）、`volume`（原生声）、`fade_in`/`fade_out`。
  - 视频片段按 `start` 排序，不能重叠，重叠返回 400 `CLIP_OVERLAP`；片段之间的空档补黑画面和静音。
  - 声音片段多一个 `role`：`dialogue`、`narration`、`music`、`ambience`、`sfx`；`loop` 循环到成片结尾。超出成片结尾的截掉。
  - `subtitles` 是成片时间的字幕条，≤ 200 字。
- **旧格式**：`clips` 首尾相接，`lines` 变成对白片段和字幕，`bgm` 变成循环的音乐片段（结尾淡出 1.5 秒），`transition: fade` 变成 0.3 秒淡入淡出。

规则：

- 上限：视频 120 段、声音 300 段、字幕 300 条。
- 校验：视频至少 0.2 秒、声音至少 0.05 秒；定格 ≤ 10 秒；淡入淡出 ≤ 10 秒；音量 0–2。
- 循环：只有 `music`、`ambience` 可以 `loop`。视频和对白、旁白、音效带 `loop` 直接拒绝（400），不能悄悄循环素材来填缺口。
- 压低：`duck`（-40…0 dB）作用在音乐轨，在对白、旁白片段的时间区间里按 `volume` 的 `enable` 表达式压低。区间是确定的，测试能直接量；没有起落斜坡。
- 响度：`loudness`（-24…-10 LUFS）填了就用 `loudnorm` 调到目标；不管填没填，成片都再测一次。
- 混音最后过 `alimiter` 防削波。
- 成片说明 `manifest` 有这些字段：`duration_ms`、`clips`（视频段数）、`lines`（对白和旁白段数）、`audio_clips`、`subtitle_count`、`bgm`、`duck_db`、`loudness_target`、`loudness_lufs`、`true_peak_db`。静音成片测不出响度时，不写后两个字段。
- 轨道的静音、独听由客户端处理：静音轨不发，独听只影响浏览器预览。服务端不知道这两个状态。

## 部署

见 `deployments/production/server.env.example` 末尾的 `EDU_COMIC_*`，以及 `edu-agent-server.service` 的 `StateDirectory`、`agent-api.nginx.conf` 的 `/v1/comic/`。主机需要 `ffmpeg` 和 `ffprobe`，以及 libx264、libmp3lame。

## 验证

- `go test ./internal/comic/` 不需要数据库：用假的 HTTP 服务核对 MiniMax 和方舟的请求格式、错误说明，以及模拟解析。
- `internal/gateway/comic_test.go` 需要 PostgreSQL 和 ffmpeg，用模拟模型走完整链：解析 → 部分失败的生图 → 配音 → 首帧生视频 → 合成 MP4（Range 下载、签名防篡改）→ 多轨合成（空档、定格、旁白、字幕、压低，成片说明里有响度），再验证扣点和退点、参数随模型变化、余额不足 402、版本冲突 409。
