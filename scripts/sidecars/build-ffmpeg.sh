#!/usr/bin/env bash
# Build a pinned minimal LGPL FFmpeg and retain complete compliance material.
#
#   scripts/sidecars/build-ffmpeg.sh <out-dir> [cross-prefix]
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=pins.sh
source "$SCRIPT_DIR/pins.sh"
# shellcheck source=verify-download.sh
source "$SCRIPT_DIR/verify-download.sh"

OUT="$(mkdir -p "$1" && cd "$1" && pwd)"
CROSS="${2:-}"
cross_compile=false
[ -n "$CROSS" ] && cross_compile=true
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
archive="$WORK/ffmpeg-${FFMPEG_VERSION}.tar.xz"
signature="$archive.asc"
download_verified "$FFMPEG_SOURCE_URL" "$FFMPEG_SOURCE_SHA256" "$archive"
download_verified "$FFMPEG_SIGNATURE_URL" "$FFMPEG_SIGNATURE_SHA256" "$signature"

command -v gpg >/dev/null || { echo 'gpg is required to verify FFmpeg releases' >&2; exit 1; }
gnupg="$WORK/gnupg"
mkdir -m 700 "$gnupg"
key="$SCRIPT_DIR/keys/ffmpeg-release.asc"
fingerprint="$(gpg --batch --with-colons --show-keys --fingerprint "$key" | awk -F: '$1 == "fpr" { print $10; exit }')"
[ "$fingerprint" = "$FFMPEG_SIGNING_FINGERPRINT" ] || {
  echo "Unexpected FFmpeg key fingerprint: $fingerprint" >&2; exit 1;
}
gpg --batch --homedir "$gnupg" --import "$key" >/dev/null 2>&1
status="$WORK/gpg.status"
gpg --batch --homedir "$gnupg" --status-fd 1 --verify "$signature" "$archive" >"$status" 2>"$WORK/gpg.log"
grep -Fq "[GNUPG:] VALIDSIG $FFMPEG_SIGNING_FINGERPRINT " "$status" || {
  cat "$WORK/gpg.log" >&2; echo 'FFmpeg signature fingerprint did not match' >&2; exit 1;
}

tar -tJf "$archive" | grep -Eq '(^/|(^|/)\.\.(/|$))' && {
  echo 'Unsafe path in FFmpeg archive' >&2; exit 1;
}
tar -xJf "$archive" -C "$WORK" --no-same-owner
cd "$WORK/ffmpeg-${FFMPEG_VERSION}"

flags=(
  --disable-everything --disable-autodetect --disable-doc --disable-debug
  --disable-network --disable-ffplay --disable-ffprobe --enable-small
  --enable-static --disable-shared --disable-x86asm
  --disable-avdevice --disable-swscale --disable-postproc
  --enable-protocol=file --enable-protocol=pipe
  --enable-demuxer=ogg,mov,mp3,wav,matroska
  --enable-parser=opus,vorbis,aac,mpegaudio
  --enable-decoder=opus,vorbis,aac,mp3float,mp3,pcm_s16le,pcm_f32le
  --enable-muxer=wav --enable-encoder=pcm_s16le
  --enable-filter=aresample,aformat,anull,abuffer,abuffersink
)
case "$(uname -s)" in
  Darwin)
    arch="${ARCH:-$(uname -m)}"
    export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-11.0}"
    flags+=(--arch="$arch" --cc="clang -arch $arch" --extra-cflags="-mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET" --extra-ldflags="-mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET")
    if [ "$arch" != "$(uname -m)" ]; then
      cross_compile=true
      flags+=(--enable-cross-compile --target-os=darwin)
    fi
    ;;
esac
if [ -n "$CROSS" ]; then
  case "$CROSS" in
    x86_64-w64-mingw32-)
      flags+=(--enable-cross-compile --cross-prefix="$CROSS" --target-os=mingw32 --arch=x86_64 --extra-ldflags=-static)
      ;;
    aarch64-linux-gnu-)
      flags+=(--enable-cross-compile --cross-prefix="$CROSS" --target-os=linux --arch=aarch64 --extra-ldflags=-static)
      ;;
    *) echo "Unsupported cross compiler prefix: $CROSS" >&2; exit 2 ;;
  esac
elif [ "$(uname -s)" = Linux ]; then
  flags+=(--extra-ldflags=-static)
fi
# GPL/nonfree switches are forbidden, including future accidental additions.
printf '%s\n' "${flags[@]}" > "$WORK/requested-configure-flags.txt"
if grep -Eq '^--enable-(gpl|nonfree)(=|$)' "$WORK/requested-configure-flags.txt"; then
  echo 'GPL or nonfree FFmpeg configuration is forbidden' >&2; exit 1
fi
./configure --prefix="$WORK/prefix" "${flags[@]}" >"$WORK/configure.log" 2>&1 || { tail -20 "$WORK/configure.log" >&2; exit 1; }
make -j"$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)" >"$WORK/make.log" 2>&1 || { tail -20 "$WORK/make.log" >&2; exit 1; }
bin=ffmpeg
[ "$CROSS" = x86_64-w64-mingw32- ] && bin=ffmpeg.exe
"${CROSS}strip" "$bin" 2>/dev/null || strip "$bin" 2>/dev/null || true
# Verify FFmpeg's generated configuration, not only our requested arguments.
# This works for native and cross builds without executing the target binary.
license_config="$WORK/ffmpeg-license-config.txt"
for setting in GPL NONFREE; do
  grep -Eq "^#define CONFIG_${setting} 0$" config.h || {
    echo "FFmpeg CONFIG_${setting} is not proven disabled" >&2; exit 1;
  }
  grep -E "^#define CONFIG_${setting} 0$" config.h >> "$license_config"
done
if ! "$cross_compile"; then
  ./ffmpeg -buildconf > "$WORK/ffmpeg-buildconf.txt" 2>&1
  if grep -Eq -- '--enable-(gpl|nonfree)(=|[[:space:]]|$)' "$WORK/ffmpeg-buildconf.txt"; then
    echo 'Built FFmpeg reports forbidden GPL or nonfree configuration' >&2; exit 1
  fi
fi
cp "$bin" "$OUT/"
cp "$archive" "$OUT/ffmpeg-${FFMPEG_VERSION}-source.tar.xz"
cp "$signature" "$OUT/ffmpeg-${FFMPEG_VERSION}-source.tar.xz.asc"
cp COPYING.LGPLv2.1 "$OUT/FFmpeg-LGPL-2.1.txt"
cp COPYING.LGPLv3 "$OUT/FFmpeg-LGPL-3.txt"
cp "$WORK/requested-configure-flags.txt" "$OUT/ffmpeg-build-config.txt"
cp "$license_config" "$OUT/ffmpeg-license-config.txt"
printf 'version=%s\nurl=%s\nsha256=%s\nsignature_url=%s\nsigning_fingerprint=%s\n' \
  "$FFMPEG_VERSION" "$FFMPEG_SOURCE_URL" "$FFMPEG_SOURCE_SHA256" \
  "$FFMPEG_SIGNATURE_URL" "$FFMPEG_SIGNING_FINGERPRINT" > "$OUT/ffmpeg-build-input.txt"
ls -l "$OUT/$bin"
