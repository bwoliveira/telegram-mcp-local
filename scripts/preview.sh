#!/usr/bin/env bash
# Serves the panel with the in-process fake engine and its synthetic Telegram
# fixtures. No account, external CLI, or manually seeded database is needed.
#
#   scripts/preview.sh
#   PREVIEW_DIR=/path/to/isolated/preview PREVIEW_PORT=47890 scripts/preview.sh
# The fake starts authenticated; it never connects to Telegram.
set -euo pipefail
cd "$(dirname "$0")/.."
DIR="${PREVIEW_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/telegram-mcp-preview.XXXXXX")}"
mkdir -p "$DIR"
DIR="$(cd "$DIR" && pwd)"
mkdir -p "$DIR/data" "$DIR/home" "$DIR/config" "$DIR/cache" "$DIR/runtime" "$DIR/xdg-data"
chmod 700 "$DIR/runtime"
go build -o "$DIR/telegram-mcp" ./cmd/telegram-mcp
# Never reuse a real daemon's token for the synthetic preview. Keep its
# random bearer token in the isolated folder, readable only by this user.
TOKEN="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
TOKEN_FILE="$(mktemp "$DIR/token.XXXXXX")"
printf '%s\n' "$TOKEN" > "$TOKEN_FILE"
mv -f "$TOKEN_FILE" "$DIR/token"
echo "fake-engine preview in $DIR (no Telegram connection)"
echo "panel bearer token: $DIR/token"
HOME="$DIR/home" XDG_CONFIG_HOME="$DIR/config" XDG_DATA_HOME="$DIR/xdg-data" \
  XDG_CACHE_HOME="$DIR/cache" XDG_RUNTIME_DIR="$DIR/runtime" \
  TELEGRAM_MCP_ENGINE=fake TELEGRAM_MCP_DATA="$DIR/data" \
  TELEGRAM_MCP_TOKEN="$TOKEN" \
  TELEGRAM_MCP_PORT="${PREVIEW_PORT:-47890}" exec "$DIR/telegram-mcp" serve
