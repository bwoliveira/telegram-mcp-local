#!/usr/bin/env bash
# Private artifact builder on Linux (Docker), never a publisher.
# scripts/release.sh X.Y.Z [--mac-artifact /path/mac-output-bundle]
# scripts/release.sh 0.0.0 --ref FULL_COMMIT --output DIR
# Build the signed universal DMG separately with scripts/release-mac.sh.
# Import its entire output bundle (Telegram-MCP.dmg, mac-build.json and evidence).
set -euo pipefail
cd "$(dirname "$0")/.."
args=() mac_artifact=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --mac-artifact)
      [ "$#" -ge 2 ] || { echo 'Missing --mac-artifact value' >&2; exit 2; }
      mac_artifact="$2"; shift 2 ;;
    --help|-h)
      echo 'Usage: scripts/release.sh X.Y.Z [--ref FULL_COMMIT --output DIR] [--mac-artifact PATH]'
      exit 0 ;;
    *) args+=("$1"); shift ;;
  esac
done
if [ -n "$mac_artifact" ]; then
  [ -d "$mac_artifact" ] || {
    echo '--mac-artifact requires a release-mac.sh output bundle, not a bare DMG' >&2; exit 2;
  }
  mac_artifact="$(cd "$mac_artifact" && pwd -P)"
fi
source scripts/release-common.sh
source scripts/release-provenance.sh
release_prepare "${args[@]}"
release_embed_public_key
release_validate_installer_key
if [ -n "$mac_artifact" ]; then
  # Require the Mac build to use the same updater trust anchor as this release,
  # then validate and stage immutable, digest-bound bytes.
  verified_mac="$release_work/verified-mac"
  release_verify_signed_bundle "$mac_artifact" "$verified_mac"
  release_mac_provenance import "$verified_mac" "$OUT"
  # Digests prove the inventory bytes are the signed ones, not that they describe
  # the DMG. Semantically validate the exact imported bytes against the selected
  # snapshot's credits/license before any platform build; on refusal, leave no
  # imported Mac file in the combined output.
  if ! python3 "$src/scripts/validate-inventory.py" --artifact "$OUT/Telegram-MCP.dmg" \
      --profile macos-dmg --preamble "$src/THIRD_PARTY_NOTICES.md" \
      --project-license "$src/LICENSE"; then
    rm -f -- "$OUT/Telegram-MCP.dmg" "$OUT/Telegram-MCP.dmg.cdx.json" \
      "$OUT/Telegram-MCP.dmg.THIRD-PARTY-NOTICES.txt" "$OUT/mac-build.json" \
      "$OUT/notarization.json" "$OUT"/mac-*.log
    echo 'Imported Mac DMG SBOM/notices refused; removed imported Mac files' >&2
    exit 1
  fi
fi
command -v docker >/dev/null
command -v go >/dev/null
network="${RELEASE_DOCKER_NETWORK:-bridge}"
case "$network" in bridge|host) ;; *) echo 'RELEASE_DOCKER_NETWORK must be bridge or host' >&2; exit 2 ;; esac
cd "$src"
# Build platform-native GTK/WebKit binaries; ARM requires registered binfmt/QEMU
# on an amd64 host. No privileged build containers or signing credentials needed.
for arch in amd64 arm64; do
  echo "==> Linux $arch"
  docker build --network "$network" --platform "linux/$arch" -t "telegram-mcp-linux-build:$arch" build/linux
  docker run --rm --network "$network" --platform "linux/$arch" -v "$src:/src" -w /src \
    -v telegram-mcp-release-modules:/root/go/pkg/mod \
    -v "telegram-mcp-release-cache-$arch:/root/.cache/go-build" \
    -v "$release_work/release-go:/release-go:ro" -e TELEGRAM_MCP_MINISIGN_PUBLIC_KEY \
    -e VERSION -e TMPDIR=/build-tmp "telegram-mcp-linux-build:$arch" \
    bash -c 'mkdir -p "$TMPDIR"; export TELEGRAM_MCP_RELEASE_GO_REAL="$(command -v go)"; export PATH="/release-go:$PATH"; scripts/build-linux.sh'
done

echo '==> Windows'
VERSION="$VERSION" scripts/build-windows.sh
cp build/bin/Telegram-MCP-Setup.exe* build/bin/Telegram-MCP-*.AppImage* \
  build/bin/telegram-mcp_"$VERSION"_*.deb* "$OUT/"

echo '==> command line (all five targets)'
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/amd64; do
  os="${target%/*}" arch="${target#*/}"
  bin=telegram-mcp; [ "$os" = windows ] && bin=telegram-mcp.exe
  cli="$release_work/cli-$os-$arch"
  mkdir -p "$cli"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$cli/$bin" ./cmd/telegram-mcp
  scripts/notices.sh "$cli/$bin" > "$cli/THIRD-PARTY-NOTICES.txt"
  if [ "$os" = windows ]; then
    artifact="$OUT/telegram-mcp_${os}_${arch}.zip"
    (cd "$cli" && zip -q "$artifact" "$bin" THIRD-PARTY-NOTICES.txt)
    extracted="$release_work/cli-extracted-$os-$arch"
    mkdir -p "$extracted"
    python3 - "$artifact" "$extracted" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1]) as archive:
    archive.extractall(sys.argv[2])
PY
  else
    artifact="$OUT/telegram-mcp_${os}_${arch}.tar.gz"
    tar -C "$cli" -czf "$artifact" "$bin" THIRD-PARTY-NOTICES.txt
    extracted="$release_work/cli-extracted-$os-$arch"
    mkdir -p "$extracted"
    tar -xzf "$artifact" -C "$extracted"
  fi
  python3 scripts/build-inventory.py --artifact-only \
    --binary "$extracted/$bin" --artifact "$artifact" --output-dir "$OUT"
  python3 scripts/build-inventory.py --artifact-only --check \
    --binary "$extracted/$bin" --artifact "$artifact" --output-dir "$OUT"
done
if [ -z "$mac_artifact" ]; then
  echo 'macOS DMG not imported: run release-mac.sh on macOS and import its output bundle before distribution.'
fi
release_checksums "$OUT"
echo "==> $OUT"
echo 'Private build artifacts only; nothing was uploaded or published.'
