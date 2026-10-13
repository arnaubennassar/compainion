#!/usr/bin/env bash
# Send a Telegram notification. Usage: telegram-notify.sh "message"
# Credentials come from TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID env vars,
# or ~/.config/telegram-notify.env (chmod 600; never commit them).
set -euo pipefail

if [ -z "${TELEGRAM_BOT_TOKEN:-}" ] || [ -z "${TELEGRAM_CHAT_ID:-}" ]; then
  if [ -f "$HOME/.config/telegram-notify.env" ]; then
    # shellcheck disable=SC1091
    . "$HOME/.config/telegram-notify.env"
  fi
fi

: "${TELEGRAM_BOT_TOKEN:?TELEGRAM_BOT_TOKEN not set}"
: "${TELEGRAM_CHAT_ID:?TELEGRAM_CHAT_ID not set}"

curl -fsS -o /dev/null "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
  --data-urlencode "chat_id=${TELEGRAM_CHAT_ID}" \
  --data-urlencode "text=$1"
