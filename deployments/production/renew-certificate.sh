#!/usr/bin/env bash
set -euo pipefail

# Share the existing agent renewal lock without editing its script or timer.
exec 9>/run/edu-agent-certbot.lock
flock -w 300 9
docker run --rm --network host --name edu-agent-api-certbot-renew \
  -v /etc/letsencrypt:/etc/letsencrypt \
  -v /var/lib/letsencrypt:/var/lib/letsencrypt \
  -v /var/log/letsencrypt:/var/log/letsencrypt \
  -v /var/www/practise-acme:/var/www/practise-acme \
  certbot/certbot:v5.8.0 renew --cert-name agent-api.hrtiger.cn --quiet "$@"
nginx -t
systemctl reload nginx
