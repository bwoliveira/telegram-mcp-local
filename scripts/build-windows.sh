#!/usr/bin/env bash
# Builds Telegram-MCP-Setup.exe: the app and the Telegram bridge for
# Windows (amd64), in a per-user NSIS installer. Runs on Linux, macOS or the
# Windows runner's bash; needs Go and makensis (apt install nsis, brew install
# makensis).
#
# VERSION   the version to stamp (default: git describe)
# Python 3.9+ is required for notices and CycloneDX inventories.
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=sidecars/pins.sh
source scripts/sidecars/pins.sh
# shellcheck source=sidecars/verify-download.sh
source scripts/sidecars/verify-download.sh

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.0.0)}"
VERSION="${VERSION#v}"
# Windows wants four numbers; anything after them (a commit, -rc1) is dropped.
num="$(printf '%s' "$VERSION" | sed -E 's/^([0-9]+(\.[0-9]+){0,3}).*/\1/')"
[[ "$num" =~ ^[0-9] ]] || num=0.0.0
VERSION4="$(printf '%s.0.0.0' "$num" | cut -d. -f1-4)"
OUT="build/bin"
work="$(mktemp -d)"
trap 'rm -rf "$work" cmd/app/rsrc_windows_*.syso cmd/telegram-mcp-bridge/rsrc_windows_*.syso' EXIT
stage="$work/stage"
mkdir -p "$stage" "$OUT"

winres() {
  go run github.com/tc-hib/go-winres@v0.3.3 make --in winres.json \
    --out "$1/rsrc" --arch amd64 --product-version "$VERSION4" --file-version "$VERSION4"
}
echo "==> building $VERSION ($VERSION4)"
(cd build/windows && winres ../../cmd/app && winres ../../cmd/telegram-mcp-bridge)
ldflags="-s -w -X main.version=$VERSION"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags "${TAGS:-}" -trimpath -ldflags "$ldflags -H windowsgui" -o "$stage/telegram-mcp.exe" ./cmd/app
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags "${TAGS:-}" -trimpath -ldflags "$ldflags" -o "$stage/telegram-mcp-bridge.exe" ./cmd/telegram-mcp-bridge
# Generate release notices from BOTH payloads, not a source-level module list.
scripts/notices.sh "$stage/telegram-mcp.exe" "$stage/telegram-mcp-bridge.exe" > "$stage/THIRD-PARTY-NOTICES.txt"

webview="$work/MicrosoftEdgeWebview2Setup.exe"
download_verified "$WEBVIEW2_BOOTSTRAPPER_URL" "$WEBVIEW2_BOOTSTRAPPER_SHA256" "$webview"
# SHA-256 is mandatory everywhere. Verify Authenticode as a second signal when
# the build host has a suitable verifier; the pinned hash remains authoritative.
if command -v powershell.exe >/dev/null 2>&1 && { command -v cygpath >/dev/null 2>&1 || command -v wslpath >/dev/null 2>&1; }; then
  if command -v cygpath >/dev/null 2>&1; then
    webview_win="$(cygpath -w "$webview")"
  else
    webview_win="$(wslpath -w "$webview")"
  fi
  WEBVIEW2_FILE="$webview_win" WSLENV="${WSLENV:+$WSLENV:}WEBVIEW2_FILE" \
    powershell.exe -NoProfile -NonInteractive -Command \
    '$s = Get-AuthenticodeSignature -LiteralPath $env:WEBVIEW2_FILE; if ($s.Status -ne "Valid" -or $s.SignerCertificate.Subject -notmatch "(^|, )CN=Microsoft Corporation(,|$)") { Write-Error "Invalid WebView2 Authenticode signature: $($s.Status) $($s.SignerCertificate.Subject)"; exit 1 }'
elif command -v osslsigncode >/dev/null 2>&1; then
  signature_report="$work/webview2-signature.txt"
  # Linux trust stores often omit Microsoft's code-signing roots. Pin the leaf
  # signer certificate and require the exact Microsoft subject even when full
  # chain validation is unavailable; the executable itself already matched its
  # mandatory SHA-256. Windows performs full Authenticode chain validation.
  osslsigncode verify -index 0 -ignore-timestamp -ignore-cdp -ignore-crl \
    -require-leaf-hash "sha256:$WEBVIEW2_SIGNER_CERT_SHA256" \
    -in "$webview" >"$signature_report" 2>&1 || true
  grep -Fq 'Leaf hash match: ok' "$signature_report" || {
    cat "$signature_report" >&2; echo 'WebView2 signing certificate hash mismatch' >&2; exit 1;
  }
  grep -Eq 'Subject:.*O=Microsoft Corporation/CN=Microsoft Corporation' "$signature_report" || {
    cat "$signature_report" >&2; echo 'WebView2 signer is not Microsoft Corporation' >&2; exit 1;
  }
else
  echo 'warning: no Authenticode verifier; WebView2 is protected by its pinned SHA-256 only' >&2
fi
cp build/windows/installer.nsi build/windows/icon.ico "$work/"
makensis -V2 -DVERSION="$VERSION" -DVERSION4="$VERSION4" -DSRC="$stage" -DOUT="$PWD/$OUT/Telegram-MCP-Setup.exe" "$work/installer.nsi"
# Inventory only payloads extracted back from the exact final NSIS bytes.
command -v 7z >/dev/null || { echo '7z is required to inventory the final installer' >&2; exit 1; }
installer_extract="$work/installer-extracted"
mkdir -p "$installer_extract"
7z x -y -o"$installer_extract" "$OUT/Telegram-MCP-Setup.exe" >/dev/null
extracted_webview="$(find "$installer_extract" -type f -name MicrosoftEdgeWebview2Setup.exe -print -quit)"
[ -n "$extracted_webview" ] || { echo 'Final installer is missing WebView2 bootstrapper' >&2; exit 1; }
python3 scripts/build-inventory.py --artifact-only \
  --binary "$installer_extract/telegram-mcp.exe" \
  --binary "$installer_extract/telegram-mcp-bridge.exe" \
  --component "$extracted_webview" --artifact "$OUT/Telegram-MCP-Setup.exe" --output-dir "$OUT"
python3 scripts/build-inventory.py --artifact-only --check \
  --binary "$installer_extract/telegram-mcp.exe" \
  --binary "$installer_extract/telegram-mcp-bridge.exe" \
  --component "$extracted_webview" --artifact "$OUT/Telegram-MCP-Setup.exe" --output-dir "$OUT"
ls -l "$OUT/Telegram-MCP-Setup.exe" "$OUT/Telegram-MCP-Setup.exe.cdx.json" \
  "$OUT/Telegram-MCP-Setup.exe.THIRD-PARTY-NOTICES.txt"
