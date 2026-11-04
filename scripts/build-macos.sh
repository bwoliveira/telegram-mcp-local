#!/usr/bin/env bash
# Build a universal Telegram MCP.app and signed, notarized, stapled DMG.
# Run from a committed release snapshot with VERSION=X.Y.Z. For local tests,
# --unsigned explicitly selects ad-hoc app signatures and skips notarization.
# SIGN_IDENTITY must name a Developer ID Application identity; credentials stay
# in the existing notarytool keychain profile (default: telegram-mcp-notary).
set -euo pipefail
cd "$(dirname "$0")/.."

unsigned=false
case "${1:-}" in
  --unsigned) unsigned=true; shift ;;
  --help|-h)
    echo 'Usage: VERSION=X.Y.Z SIGN_IDENTITY="Developer ID Application: ..." scripts/build-macos.sh [--unsigned]'
    exit 0 ;;
esac
[ "$#" -eq 0 ] || { echo "Unexpected argument: $1" >&2; exit 2; }
VERSION="${VERSION:?Set VERSION explicitly; release builds must not use git-describe labels}"
[[ "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
  echo 'VERSION must be an exact X.Y.Z version' >&2; exit 2;
}
[ "$(uname -s)" = Darwin ] || { echo 'macOS is required' >&2; exit 1; }
# A release always contains both architectures. Do not allow inherited local
# build options or paid overlays to silently change the reviewed source tree.
[ -z "${ARCHS:-}" ] || [ "$ARCHS" = 'arm64 amd64' ] || {
  echo 'macOS packages require ARCHS="arm64 amd64"' >&2; exit 2;
}
[ -z "${TAGS:-}" ] && [ -z "${PRO_DIR:-}" ] || {
  echo 'Custom TAGS and PRO_DIR overlays are not allowed in macOS packages' >&2; exit 2;
}
if ! "$unsigned"; then
  case "${SIGN_IDENTITY:-}" in
    'Developer ID Application: '*) ;;
    *) echo 'SIGN_IDENTITY must be a Developer ID Application identity (or use --unsigned for a local test)' >&2; exit 1 ;;
  esac
  # Check before compiling; a missing identity must never fall back to ad hoc.
  security find-identity -v -p codesigning | grep -F -- "\"$SIGN_IDENTITY\"" >/dev/null || {
    echo 'The requested Developer ID Application identity is unavailable' >&2; exit 1;
  }
fi
for tool in go clang lipo codesign uv xcrun hdiutil plutil; do
  command -v "$tool" >/dev/null || { echo "Missing build tool: $tool" >&2; exit 1; }
done
NOTARY_PROFILE="${NOTARY_PROFILE:-telegram-mcp-notary}"
if ! "$unsigned"; then
  xcrun notarytool history --keychain-profile "$NOTARY_PROFILE" --output-format json >/dev/null
fi
OUT="build/bin"
APP="$OUT/Telegram MCP.app"
export MACOSX_DEPLOYMENT_TARGET=11.0
export CGO_CFLAGS="-mmacosx-version-min=11.0" CGO_LDFLAGS="-mmacosx-version-min=11.0"
# Do not embed the path of the private source checkout or VCS/dirty labels.
export GOFLAGS= GOWORK=off
ldflags="-s -w -X main.version=$VERSION"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
apps=() bridges=()
for arch in arm64 amd64; do
  cc_arch="$arch"; [ "$arch" = amd64 ] && cc_arch=x86_64
  echo "==> building for $arch"
  CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" CC="clang -arch $cc_arch" \
    go build -buildvcs=false -trimpath -ldflags "$ldflags" -o "$work/app-$arch" ./cmd/app
  CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" \
    go build -buildvcs=false -trimpath -ldflags "$ldflags" -o "$work/bridge-$arch" ./cmd/telegram-mcp-bridge
  apps+=("$work/app-$arch"); bridges+=("$work/bridge-$arch")
done

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
lipo -create -output "$APP/Contents/MacOS/telegram-mcp" "${apps[@]}"
lipo -create -output "$APP/Contents/MacOS/telegram-mcp-bridge" "${bridges[@]}"
for binary in telegram-mcp telegram-mcp-bridge; do
  # Verify separately: current Xcode lipo rejects a multi-arch verification
  # list despite advertising that syntax in its help output.
  lipo -verify_arch arm64 "$APP/Contents/MacOS/$binary"
  lipo -verify_arch x86_64 "$APP/Contents/MacOS/$binary"
  lipo -info "$APP/Contents/MacOS/$binary"
done
sed "s/@VERSION@/$VERSION/g" build/darwin/Info.plist > "$APP/Contents/Info.plist"
plutil -lint "$APP/Contents/Info.plist"
cp build/darwin/icon.icns "$APP/Contents/Resources/icon.icns"
scripts/notices.sh "${apps[@]}" "${bridges[@]}" > "$APP/Contents/Resources/THIRD-PARTY-NOTICES.txt"

# Inside out: seal both executables before the containing bundle. Never use
# --deep to sign (it can conceal an incorrectly signed nested executable).
if "$unsigned"; then
  echo '==> UNSIGNED TEST ONLY: ad-hoc app signatures, no notarization'
  sign=(codesign --force --sign -)
else
  sign=(codesign --force --options runtime --timestamp --sign "$SIGN_IDENTITY")
fi
"${sign[@]}" "$APP/Contents/MacOS/telegram-mcp-bridge"
"${sign[@]}" "$APP/Contents/MacOS/telegram-mcp"
"${sign[@]}" "$APP"
codesign --verify --deep --strict --verbose=2 "$APP"
if ! "$unsigned"; then
  for target in "$APP/Contents/MacOS/telegram-mcp-bridge" "$APP"; do
    codesign --verify --strict --all-architectures \
      -R '=anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.13] exists' "$target"
    codesign --display --verbose=4 "$target"
  done
fi

echo '==> making the .dmg'
DMG="$OUT/Telegram-MCP.dmg"
"$unsigned" && DMG="$OUT/Telegram-MCP-unsigned.dmg"
rm -f "$DMG"
uv tool run dmgbuild==1.6.7 -s build/darwin/dmg-settings.py -D "app=$APP" 'Telegram MCP' "$DMG"
hdiutil verify "$DMG"
if ! "$unsigned"; then
  codesign --force --timestamp --sign "$SIGN_IDENTITY" "$DMG"
  codesign --verify --strict --verbose=2 "$DMG"
  echo '==> notarizing with the existing keychain profile'
  xcrun notarytool submit "$DMG" --keychain-profile "$NOTARY_PROFILE" \
    --wait --output-format json > "$OUT/notarization.json"
  cat "$OUT/notarization.json"
  [ "$(plutil -extract status raw -o - "$OUT/notarization.json")" = Accepted ] || {
    echo 'Notarization was not accepted; no release artifact is ready' >&2; exit 1;
  }
  xcrun stapler staple "$DMG"
  xcrun stapler validate "$DMG"
  spctl --assess --type open --context context:primary-signature --verbose=2 "$DMG"
fi
# Re-open the final signed/stapled (or explicitly unsigned test) image and build
# the legal inventory from the payload bytes actually carried by that DMG.
mount="$work/dmg-mount"
mkdir -p "$mount"
hdiutil attach -readonly -nobrowse -mountpoint "$mount" "$DMG" >/dev/null
mounted_app="$(find "$mount" -maxdepth 1 -type d -name '*.app' -print -quit)"
[ -n "$mounted_app" ] || { hdiutil detach "$mount" >/dev/null; echo 'Final DMG contains no app bundle' >&2; exit 1; }
# `go version -m` inspects only one slice of a universal Mach-O. Derive both
# architecture slices from the mounted final payload and inventory all four;
# also hash the exact universal files as packaged components.
thin=()
for binary in telegram-mcp telegram-mcp-bridge; do
  universal="$mounted_app/Contents/MacOS/$binary"
  for arch in arm64 x86_64; do
    slice="$work/${binary}-${arch}"
    lipo -thin "$arch" "$universal" -output "$slice"
    thin+=(--binary "$slice")
  done
done
python3 scripts/build-inventory.py --artifact-only \
  "${thin[@]}" \
  --component "$mounted_app/Contents/MacOS/telegram-mcp" \
  --component "$mounted_app/Contents/MacOS/telegram-mcp-bridge" \
  --artifact "$DMG" --output-dir "$OUT"
python3 scripts/build-inventory.py --artifact-only --check \
  "${thin[@]}" \
  --component "$mounted_app/Contents/MacOS/telegram-mcp" \
  --component "$mounted_app/Contents/MacOS/telegram-mcp-bridge" \
  --artifact "$DMG" --output-dir "$OUT"
hdiutil detach "$mount" >/dev/null
shasum -a 256 "$DMG"
echo 'Private build artifacts only; nothing was uploaded or published as a release.'
