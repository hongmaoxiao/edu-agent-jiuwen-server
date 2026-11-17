#!/bin/sh
# 在本机跑 AI 漫剧服务端，给本地演示用（说明见 docs/comic-studio.md「本地演示」）。
#   - 数据库：设了 DATABASE_URL 就用它；否则用 Docker 起一个 postgres:18.6（容器 edu-comic-pg，数据卷 edu-comic-pg，只听 127.0.0.1）。
#   - 密钥：放在 ${EDU_COMIC_KEYS:-~/.edu-comic-keys}/ 下的 minimax、ark、deepseek 三个文件，每个文件只写密钥本身；缺哪个，哪家模型就显示「暂不可用」。
#   - 媒体和程序：${EDU_COMIC_LOCAL:-~/.edu-comic-local}/。
#   - 第一次运行会建演示账号 demo（密码取 EDU_DEMO_PASSWORD，默认 demo-password-1），给运营、学员、教师三个视角各加 1000 创作点。
set -eu
cd "$(dirname "$0")/.."
KEYS=${EDU_COMIC_KEYS:-$HOME/.edu-comic-keys}
DATA=${EDU_COMIC_LOCAL:-$HOME/.edu-comic-local}
PORT=${EDU_COMIC_PORT:-4120}
WEB=${EDU_COMIC_WEB_PORT:-4173}

for tool in go ffmpeg ffprobe; do
  command -v "$tool" >/dev/null 2>&1 || { echo "缺少 $tool。macOS：brew install go ffmpeg" >&2; exit 1; }
done
mkdir -p "$DATA/bin" "$DATA/media" "$KEYS"

if [ -z "${DATABASE_URL:-}" ]; then
  command -v docker >/dev/null 2>&1 || { echo "没有 Docker：请装 Docker，或者把 DATABASE_URL 指向一个 PostgreSQL 16+ 的空库" >&2; exit 1; }
  if ! docker container inspect edu-comic-pg >/dev/null 2>&1; then
    docker run -d --name edu-comic-pg -e POSTGRES_USER=edu -e POSTGRES_DB=edu -e POSTGRES_HOST_AUTH_METHOD=trust \
      -p 127.0.0.1:55440:5432 -v edu-comic-pg:/var/lib/postgresql "${EDU_COMIC_PG_IMAGE:-postgres:18.6}" >/dev/null
  fi
  docker start edu-comic-pg >/dev/null
  tries=0
  until docker exec edu-comic-pg pg_isready -U edu -d edu -h 127.0.0.1 >/dev/null 2>&1; do
    tries=$((tries + 1))
    [ "$tries" -le 60 ] || { echo "PostgreSQL 容器没有就绪" >&2; exit 1; }
    sleep 1
  done
  DATABASE_URL="postgres://edu@127.0.0.1:55440/edu?sslmode=disable"
fi
export DATABASE_URL

echo "构建服务端…"
CGO_ENABLED=0 go build -o "$DATA/bin/" ./cmd/server ./cmd/migrate ./cmd/admin
"$DATA/bin/migrate"
if [ ! -f "$DATA/.demo-ready" ]; then
  printf '%s\n' "${EDU_DEMO_PASSWORD:-demo-password-1}" | "$DATA/bin/admin" create-demo demo 演示账号 || true
  # 登录 demo 后按视角用不同的人设账号（默认运营视角），漫剧项目和创作点跟着人设走。
  for persona in demo.operator demo.student demo.teacher; do "$DATA/bin/admin" comic-grant "$persona" 1000 本地演示额度; done
  touch "$DATA/.demo-ready"
fi

export EDU_ENV=development EDU_LISTEN="127.0.0.1:$PORT" EDU_METRICS_LISTEN=
export EDU_WEB_ORIGINS="http://127.0.0.1:$WEB,http://localhost:$WEB"
export EDU_COMIC_MEDIA_DIR="$DATA/media" EDU_COMIC_SIMULATE=${EDU_COMIC_SIMULATE:-1} EDU_COMIC_INITIAL_CREDITS=${EDU_COMIC_INITIAL_CREDITS:-300}
for pair in minimax:EDU_COMIC_MINIMAX_KEY_FILE ark:EDU_COMIC_ARK_KEY_FILE deepseek:EDU_COMIC_TEXT_KEY_FILE; do
  file="$KEYS/${pair%%:*}"
  if [ -s "$file" ]; then
    export "${pair#*:}=$file"
    echo "密钥：${pair%%:*} 已配置"
  else
    echo "密钥：${pair%%:*} 未配置（${file}），这家模型显示「暂不可用」"
  fi
done
# 烧录字幕用的中文字体；找不到就改成内嵌字幕轨。
for font in "${EDU_COMIC_SUBTITLE_FONT:-}" /System/Library/Fonts/PingFang.ttc "/System/Library/Fonts/STHeiti Medium.ttc" "/System/Library/Fonts/Hiragino Sans GB.ttc" \
  /usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc; do
  if [ -n "$font" ] && [ -f "$font" ]; then
    export EDU_COMIC_SUBTITLE_FONT="$font"
    break
  fi
done
echo "平台服务：http://127.0.0.1:$PORT    网页：在客户端仓库 client/jiuwenswarm/channels/web/frontend 下运行 npm run studio:local，再打开 http://127.0.0.1:$WEB"
exec "$DATA/bin/server"
