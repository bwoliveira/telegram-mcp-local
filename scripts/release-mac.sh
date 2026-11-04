#!/usr/bin/env bash
# Build the macOS release from a clean, committed snapshot; never publish it.
#   SIGN_IDENTITY="Developer ID Application: ..." scripts/release-mac.sh X.Y.Z
#   scripts/release-mac.sh 0.0.0 --ref FULL_COMMIT --output DIR --unsigned
# Signing credentials use the existing telegram-mcp-notary keychain profile.
set -euo pipefail
cd "$(dirname "$0")/.."

unsigned=false
args=()
for arg in "$@"; do
  case "$arg" in
    --unsigned) unsigned=true ;;
    --help|-h)
      echo 'Usage: scripts/release-mac.sh X.Y.Z [--ref FULL_COMMIT --output DIR] [--unsigned]'
      exit 0 ;;
    *) args+=("$arg") ;;
  esac
done
[ -n "${args[*]:-}" ] || { echo 'A release version is required' >&2; exit 2; }
[ "$(uname -s)" = Darwin ] || { echo 'Run release-mac.sh on macOS' >&2; exit 1; }
# Shared preflight pins the private origin, tag (or explicit 0.0.0 test commit),
# and exports only tracked source. It owns temporary snapshot cleanup.
source scripts/release-common.sh
release_prepare "${args[@]}"
release_embed_public_key
release_validate_installer_key
if "$unsigned" && [ "$VERSION" != 0.0.0 ]; then
  echo '--unsigned is restricted to version 0.0.0 test builds' >&2
  exit 2
fi

source scripts/release-provenance.sh
if "$unsigned"; then
  (cd "$src" && VERSION="$VERSION" scripts/build-macos.sh --unsigned) 2>&1 | tee "$OUT/mac-build.log"
else
  (cd "$src" && VERSION="$VERSION" scripts/build-macos.sh) 2>&1 | tee "$OUT/mac-build.log"
fi
artifact=Telegram-MCP.dmg
mode=signed
"$unsigned" && { artifact=Telegram-MCP-unsigned.dmg; mode=unsigned; }
# Recheck the final stapled bytes, recording the native trust gates. A failed
# gate never produces the manifest or copies a distributable DMG to the output.
if ! "$unsigned"; then
  dmg="$src/build/bin/$artifact"
  hdiutil verify "$dmg" > "$OUT/mac-image-verify.log" 2>&1
  codesign --verify --strict --verbose=2 \
    -R '=anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.13] exists' \
    "$dmg" > "$OUT/mac-signature.log" 2>&1
  codesign --display --verbose=4 "$dmg" >> "$OUT/mac-signature.log" 2>&1
  xcrun stapler validate "$dmg" > "$OUT/mac-stapler.log" 2>&1
  spctl --assess --type open --context context:primary-signature --verbose=2 \
    "$dmg" > "$OUT/mac-gatekeeper.log" 2>&1
  cp "$src/build/bin/notarization.json" "$OUT/notarization.json"
fi
cp "$src/build/bin/$artifact" "$OUT/$artifact"
cp "$src/build/bin/$artifact.cdx.json" "$src/build/bin/$artifact.THIRD-PARTY-NOTICES.txt" "$OUT/"
release_mac_provenance create "$OUT" "$mode"
release_checksums "$OUT"
echo "==> $OUT/$artifact"
echo 'Private build artifacts only; no release was uploaded or published.'
