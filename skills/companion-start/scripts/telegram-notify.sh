#!/usr/bin/env bash
# Send a Telegram notification. Usage: telegram-notify.sh "message"
# Credentials come from TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID env vars,
# or ~/.config/telegram-notify.env (chmod 600; never commit them).
set -euo pipefail

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "telegram-notify: usage: telegram-notify.sh \"<message>\" (need exactly one non-empty message)" >&2
  exit 64
fi
MSG="$1"

if [ -z "${TELEGRAM_BOT_TOKEN:-}" ] || [ -z "${TELEGRAM_CHAT_ID:-}" ]; then
  if [ -f "$HOME/.config/telegram-notify.env" ]; then
    # shellcheck disable=SC1091
    . "$HOME/.config/telegram-notify.env"
  fi
fi

if [ -z "${TELEGRAM_BOT_TOKEN:-}" ] || [ -z "${TELEGRAM_CHAT_ID:-}" ]; then
  echo "telegram-notify: TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID not set (neither in env nor in ~/.config/telegram-notify.env)" >&2
  exit 78
fi

API="https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage"
RESP=""
HTTP_CODE=""
BODY=""
if ! RESP="$(curl -sS -w '\n%{http_code}' "$API" \
    --data-urlencode "chat_id=${TELEGRAM_CHAT_ID}" \
    --data-urlencode "text=${MSG}" 2>&1)"; then
  echo "telegram-notify: curl to api.telegram.org failed: ${RESP}" >&2
  exit 1
fi
HTTP_CODE="${RESP##*$'\n'}"
BODY="${RESP%$'\n'*}"
if [ "$HTTP_CODE" != "200" ] || ! printf '%s' "$BODY" | grep -q '"ok":true'; then
  echo "telegram-notify: delivery failed (http ${HTTP_CODE}): ${BODY}" >&2
  exit 1
fi

printf '%s\n' "$BODY"
