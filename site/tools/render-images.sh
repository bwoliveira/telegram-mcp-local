#!/usr/bin/env bash
# Renders the social images (one per language) and the apple-touch icon from
# the HTML that `go run ./site/gen -og-html` writes, using headless Chromium.
# Run it after changing the brand, qualifier, og.tagline or og.notice strings,
# then commit the PNGs. Offline: the HTML loads only the local font file.
#
#   site/tools/render-images.sh            # needs Go and chromium (or CHROMIUM=...)
set -euo pipefail
cd "$(dirname "$0")/../.."

CHROMIUM="${CHROMIUM:-$(command -v chromium || command -v chromium-browser || command -v google-chrome)}"
[ -n "$CHROMIUM" ] || { echo "chromium not found; set CHROMIUM=/path/to/chromium" >&2; exit 1; }

work="$(mktemp -d "${TMPDIR:-/tmp}/tgmcp-og.XXXXXX")"
trap 'rm -rf "$work"' EXIT
go run ./site/gen -src site/src -og-html "$work"

shot() { # html png width height
  "$CHROMIUM" --headless=new --no-sandbox --disable-gpu --hide-scrollbars \
    --force-device-scale-factor=1 --allow-file-access-from-files \
    --window-size="$3,$4" --screenshot="$2" "file://$1" >/dev/null 2>&1
  [ -s "$2" ] || { echo "failed to render $2" >&2; exit 1; }
}

mkdir -p site/src/static/assets/og
for lang in pt-BR en es; do
  shot "$work/og-$lang.html" "site/src/static/assets/og/og-$lang.png" 1200 630
done
shot "$work/apple-touch-icon.html" site/src/static/apple-touch-icon.png 180 180
ls -l site/src/static/assets/og/*.png site/src/static/apple-touch-icon.png
