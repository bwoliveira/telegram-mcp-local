#!/usr/bin/env bash
# Builds Telegram MCP for Linux on the machine's own architecture: a .deb for
# Ubuntu and Debian, and an AppImage carrying WebKitGTK for Ubuntu 24.04,
# Debian 13 and newer glibc distributions. Needs Go, a C compiler, GTK/WebKit
# development packages, dpkg-deb, file, Python and squashfs-tools: run it in
# build/linux/Dockerfile (Ubuntu 24.04), the oldest supported distribution, or
# the AppImage ABI check below fails.
#
# VERSION   the version to stamp (default: git describe)
# OUT       output directory (default: build/bin); created if missing
#
# Both packages are inventoried from the bytes extracted back out of the final
# artifact: every file is mapped to a Go payload, project asset, pinned input,
# build tool or originating Debian package (scripts/native_inventory.py), and
# the build fails if anything is unmapped or a legal text is missing.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.0.0)}"
VERSION="${VERSION#v}"
case "$(uname -m)" in
  x86_64) arch=amd64 appimage_arch=x86_64 ;;
  aarch64|arm64) arch=arm64 appimage_arch=aarch64 ;;
  *) echo "unsupported machine $(uname -m)" >&2; exit 1 ;;
esac
repo="$PWD"
OUT="${OUT:-$repo/build/bin}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd -P)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
ldflags="-s -w -X main.version=$VERSION"

echo "==> building $VERSION for linux/$arch"
# GTK 3 and WebKitGTK 4.1 are in every supported Ubuntu (24.04 and later)
# and Debian (13 and later). The same binary is the app and, given a
# subcommand (serve, service, open, version...), the headless command line.
CGO_ENABLED=1 go build -tags "gtk3${TAGS:+,$TAGS}" -trimpath -ldflags "$ldflags" -o "$work/telegram-mcp" ./cmd/app
CGO_ENABLED=0 go build -tags "${TAGS:-}" -trimpath -ldflags "$ldflags" -o "$work/telegram-mcp-bridge" ./cmd/telegram-mcp-bridge
scripts/notices.sh "$work/telegram-mcp" "$work/telegram-mcp-bridge" > "$work/THIRD-PARTY-NOTICES.txt"
# The AppImage copy links with the RUNPATH linuxdeploy would otherwise patch in.
# patchelf moves PT_GNU_STACK ahead of the PT_LOAD segments, which makes Go's
# debug/buildinfo (go version -m) misread the binary as "not a Go executable".
mkdir -p "$work/appimage-bin"
CGO_ENABLED=1 go build -tags "gtk3${TAGS:+,$TAGS}" -trimpath \
  -ldflags "$ldflags -extldflags '-Wl,-rpath,\$ORIGIN/../lib'" -o "$work/appimage-bin/telegram-mcp" ./cmd/app
cp "$work/telegram-mcp-bridge" "$work/appimage-bin/telegram-mcp-bridge"
scripts/notices.sh "$work/appimage-bin/telegram-mcp" "$work/appimage-bin/telegram-mcp-bridge" \
  > "$work/appimage-bin/THIRD-PARTY-NOTICES.txt"

# ---- .deb -------------------------------------------------------------------
deb="$work/deb"
mkdir -p "$deb/DEBIAN" "$deb/opt/telegram-mcp" "$deb/usr/bin" \
  "$deb/usr/share/applications" "$deb/usr/share/icons/hicolor/512x512/apps" "$deb/usr/share/doc/telegram-mcp"
install -m 0755 "$work/telegram-mcp" "$work/telegram-mcp-bridge" "$deb/opt/telegram-mcp/"
ln -s /opt/telegram-mcp/telegram-mcp "$deb/usr/bin/telegram-mcp"
sed 's|^Exec=.*|Exec=/opt/telegram-mcp/telegram-mcp|' build/linux/telegram-mcp.desktop > "$deb/usr/share/applications/telegram-mcp.desktop"
install -m 0644 build/linux/telegram-mcp.png "$deb/usr/share/icons/hicolor/512x512/apps/telegram-mcp.png"
install -m 0644 "$work/THIRD-PARTY-NOTICES.txt" "$deb/usr/share/doc/telegram-mcp/copyright"
install -m 0644 "$work/THIRD-PARTY-NOTICES.txt" "$deb/usr/share/doc/telegram-mcp/THIRD-PARTY-NOTICES.txt"
size="$(du -sk "$deb" | cut -f1)"
cat > "$deb/DEBIAN/control" <<CONTROL
Package: telegram-mcp
Version: ${VERSION}
Architecture: ${arch}
Maintainer: Bruno Oliveira <https://github.com/bwoliveira>
Installed-Size: ${size}
Depends: libgtk-3-0, libwebkit2gtk-4.1-0
Section: net
Priority: optional
Homepage: https://github.com/bwoliveira/telegram-mcp-local
Description: Telegram for AI tools, on this computer
 Telegram MCP connects your Telegram to Claude and other AI tools through
 the Model Context Protocol. Everything runs on this computer.
CONTROL
dpkg_deb="$OUT/telegram-mcp_${VERSION}_${arch}.deb"
dpkg-deb --root-owner-group --build "$deb" "$dpkg_deb" >/dev/null
# Inventory the bytes extracted back out of the final package, not the pre-package
# build directory. The sidecars live beside the artifact and enter checksums.txt.
deb_extract="$work/deb-extracted"
dpkg-deb -x "$dpkg_deb" "$deb_extract"
# The DEB embeds no native code beyond Go; the scan still covers every file.
for mode in generate --check; do
  python3 scripts/build-inventory.py --artifact-only ${mode#generate} \
    --binary "$deb_extract/opt/telegram-mcp/telegram-mcp" \
    --binary "$deb_extract/opt/telegram-mcp/telegram-mcp-bridge" \
    --native-root "$deb_extract" --native-profile linux-deb --native-arch "$arch" \
    --artifact "$dpkg_deb" --output-dir "$OUT"
done

# ---- AppImage ---------------------------------------------------------------
# The way Wails builds one: linuxdeploy with its GTK plugin, plus WebKit's
# helper processes, which no library dependency pulls in.
appdir="$work/Telegram-MCP.AppDir"
mkdir -p "$appdir/usr/bin"
# Preserve the visual asset until the branding step, but use the new icon ID.
install -m 0644 build/linux/telegram-mcp.png "$appdir/telegram-mcp.png"
install -m 0755 "$work/appimage-bin/telegram-mcp" "$work/appimage-bin/telegram-mcp-bridge" "$appdir/usr/bin/"
install -m 0644 "$work/appimage-bin/THIRD-PARTY-NOTICES.txt" "$appdir/usr/bin/"
for f in WebKitWebProcess WebKitNetworkProcess libwebkit2gtkinjectedbundle.so; do
  src="$(find /usr/lib -name "$f" -path '*webkit2gtk-4.1*' 2>/dev/null | head -1)"
  [ -n "$src" ] || { echo "WebKitGTK file $f not found" >&2; exit 1; }
  mkdir -p "$appdir$(dirname "$src")"
  cp "$src" "$appdir$src"
done
tools="$work/tools"
mkdir -p "$tools"
curl -fsSL -o "$tools/linuxdeploy" "https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-${appimage_arch}.AppImage"
curl -fsSL -o "$appdir/AppRun" "https://github.com/AppImage/AppImageKit/releases/download/continuous/AppRun-${appimage_arch}"
# appimagetool would otherwise download an unpinned runtime at pack time.
curl -fsSL -o "$tools/runtime" "https://github.com/AppImage/type2-runtime/releases/download/continuous/runtime-${appimage_arch}"
cp "$(go env GOMODCACHE)/github.com/wailsapp/wails/v3@$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v3)/internal/commands/linuxdeploy-plugin-gtk.sh" "$tools/"
# Continuous release URLs are mutable: fail closed if upstream replaces assets.
case "$appimage_arch" in
  x86_64)
    deploy_sha=df908b19f1ed1cdafa79508d415aa12d6c56ce4d56d4149aa8c09286718706e5
    apprun_sha=f30140a43a0a59e46db21bdefdf749b9e9f2c6946e92afabbacf98b8ae73fb4f
    runtime_sha=617aa77314d57c8b8e4fae9c3f0b35af7b30d5529cb69e98f64f92510ed98a46 ;;
  aarch64)
    deploy_sha=f38e4d7cebf20d8073ec4e00e0a167ac146cd8585fe2f8fe82df27d74f2bd8e1
    apprun_sha=072f17c0895a85c490282fe5395c5007e5fc75da727e553b3b8fb680feb11578
    runtime_sha=9a72a744d221576c9347ec074243acc5c3c7568896b12714e0e82e9f0bf47900 ;;
esac
# The same pins are recorded, with provenance and legal texts, in
# scripts/linux-native/pinned-inputs.json; the native inventory re-verifies them.
printf '%s  %s\n' "$deploy_sha" "$tools/linuxdeploy" "$apprun_sha" "$appdir/AppRun" \
  "$runtime_sha" "$tools/runtime" | sha256sum -c -
chmod +x "$tools/linuxdeploy" "$tools/linuxdeploy-plugin-gtk.sh" "$appdir/AppRun"
python3 scripts/extract-appimage.py "$tools/linuxdeploy" "$tools/linuxdeploy-root"
(
  cd "$tools"
  export DEPLOY_GTK_VERSION=3 NO_STRIP=1 PATH="$tools:$PATH"
  deploy() {
    "$tools/linuxdeploy-root/AppRun" --appdir "$appdir" --executable "$appdir/usr/bin/telegram-mcp" \
      --desktop-file "$repo/build/linux/telegram-mcp.desktop" --icon-file "$appdir/telegram-mcp.png" \
      "$@" >>"$work/linuxdeploy.log" 2>&1 || { tail -30 "$work/linuxdeploy.log" >&2; exit 1; }
  }
  deploy --plugin gtk
  # WebKitGTK starts its helper processes from a path compiled into it,
  # /usr/lib/<arch>/webkit2gtk-4.1. Rewritten as ././/lib/…, the same length,
  # it is relative to the folder AppRun runs the app from, $APPDIR/usr,
  # where the helpers were copied.
  find "$appdir/usr/lib" -name 'libwebkit2gtk-4.1.so*' -type f -exec sed -i 's|/usr/lib/|././/lib/|g' {} +
  # Drop host extras the app never loads: GTK settings modules of the build
  # host's desktop, GObject-introspection typelibs (no language bindings run),
  # the TIFF pixbuf loader (libtiff pulls in GPL-2+ libjbig) and GSettings
  # schemas of unrelated packages. Their now-unreferenced libraries go too.
  python3 "$repo/scripts/prune-appdir.py" "$appdir" --arch "$arch" \
    --remove usr/lib/gtk-3.0/modules --remove usr/lib/girepository-1.0 \
    --remove usr/lib/gdk-pixbuf-2.0/2.10.0/loaders/libpixbufloader-tiff.so \
    --remove usr/lib/libpixbufloader-tiff.so \
    --schema-owner '(libgtk-3-common|gsettings-desktop-schemas|libglib2\.0-.*|libgio-2\.0-dev)' \
    >>"$work/linuxdeploy.log"
  # appimagetool adds these when absent; add them first so the embedded native
  # inventory describes the packed bytes exactly.
  # linuxdeploy may leave the root entry as a symlink into usr/share/applications;
  # make it a regular copy so only the AppImage-root file gains the key.
  if [ -L "$appdir/telegram-mcp.desktop" ]; then
    cp --remove-destination "$(readlink -f "$appdir/telegram-mcp.desktop")" "$appdir/telegram-mcp.desktop"
  fi
  grep -q '^X-AppImage-Version=' "$appdir/telegram-mcp.desktop" ||
    printf 'X-AppImage-Version=%s\n' "$VERSION" >> "$appdir/telegram-mcp.desktop"
  [ -L "$appdir/.DirIcon" ] || ln -s telegram-mcp.png "$appdir/.DirIcon"
  python3 "$repo/scripts/build-inventory.py" --native-embed --native-root "$appdir" \
    --native-profile linux-appimage --native-arch "$arch" \
    --native-patchelf "$tools/linuxdeploy-root/usr/bin/patchelf" \
    --binary "$appdir/usr/bin/telegram-mcp" --binary "$appdir/usr/bin/telegram-mcp-bridge"
  appimage="$OUT/Telegram-MCP-${appimage_arch}.AppImage"
  rm -f "$appimage"
  ARCH="$appimage_arch" VERSION="$VERSION" \
    "$tools/linuxdeploy-root/plugins/linuxdeploy-plugin-appimage/appimagetool-prefix/AppRun" \
    --no-appstream --runtime-file "$tools/runtime" "$appdir" "$appimage" >>"$work/linuxdeploy.log" 2>&1 ||
    { tail -30 "$work/linuxdeploy.log" >&2; exit 1; }
  appimage_extract="$work/appimage-extracted"
  python3 "$repo/scripts/extract-appimage.py" "$appimage" "$appimage_extract"
  # Every symbol a bundled ELF needs from a library left to the system must
  # exist in the oldest supported distribution, this build root.
  python3 "$repo/scripts/check-appimage-abi.py" "$appimage_extract" --arch "$arch" \
    --baseline-root / --baseline-os ubuntu:24.04
  # Regenerated from the extracted final artifact; the embedded copies must match.
  for mode in generate --check; do
    python3 "$repo/scripts/build-inventory.py" --artifact-only ${mode#generate} \
      --binary "$appimage_extract/usr/bin/telegram-mcp" \
      --binary "$appimage_extract/usr/bin/telegram-mcp-bridge" \
      --native-root "$appimage_extract" --native-profile linux-appimage --native-arch "$arch" \
      --native-patchelf "$tools/linuxdeploy-root/usr/bin/patchelf" \
      --artifact "$appimage" --output-dir "$OUT"
  done
)
ls -l "$OUT"/telegram-mcp_*_"$arch".deb "$OUT/Telegram-MCP-${appimage_arch}.AppImage" \
  "$OUT"/telegram-mcp_*_"$arch".deb.cdx.json "$OUT/Telegram-MCP-${appimage_arch}.AppImage.cdx.json"
