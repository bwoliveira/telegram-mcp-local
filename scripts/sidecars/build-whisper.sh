#!/usr/bin/env bash
# Build the pinned whisper.cpp CLI after verifying the source archive.
#
#   scripts/sidecars/build-whisper.sh <out-dir>
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=pins.sh
source "$SCRIPT_DIR/pins.sh"
# shellcheck source=verify-download.sh
source "$SCRIPT_DIR/verify-download.sh"

OUT="$(mkdir -p "$1" && cd "$1" && pwd)"
CROSS="${2:-}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
archive="$WORK/whisper.cpp-${WHISPER_VERSION}.tar.gz"
download_verified "$WHISPER_SOURCE_URL" "$WHISPER_SOURCE_SHA256" "$archive"
# Extract only after the complete archive has matched its pinned digest.
tar -tzf "$archive" | grep -Eq '(^/|(^|/)\.\.(/|$))' && {
  echo 'Unsafe path in whisper.cpp archive' >&2; exit 1;
}
tar -xzf "$archive" -C "$WORK" --no-same-owner
src="$WORK/whisper.cpp-${WHISPER_VERSION}"
[ -f "$src/LICENSE" ] || { echo 'whisper.cpp LICENSE missing' >&2; exit 1; }

flags=(
  -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF
  -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_SERVER=OFF -DWHISPER_SDL2=OFF -DWHISPER_CURL=OFF
  -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF
)
case "$(uname -s)" in
  Darwin)
    flags+=(-DGGML_METAL=ON -DGGML_METAL_EMBED_LIBRARY=ON -DCMAKE_OSX_ARCHITECTURES="arm64;x86_64" -DCMAKE_OSX_DEPLOYMENT_TARGET=11.0)
    ;;
  Linux)
    if [ -n "$CROSS" ]; then
      flags+=(-DCMAKE_SYSTEM_NAME=Linux -DCMAKE_SYSTEM_PROCESSOR=aarch64 \
        -DCMAKE_C_COMPILER="${CROSS}gcc" -DCMAKE_CXX_COMPILER="${CROSS}g++")
    elif [ "$(uname -m)" = x86_64 ]; then
      flags+=(-DGGML_AVX=ON -DGGML_AVX2=ON -DGGML_FMA=ON -DGGML_F16C=ON)
    fi
    flags+=(-DCMAKE_EXE_LINKER_FLAGS="-static-libgcc -static-libstdc++")
    ;;
esac
cmake -S "$src" -B "$WORK/build" "${flags[@]}" >/dev/null
cmake --build "$WORK/build" --config Release -j "$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)" --target whisper-cli >/dev/null
cp "$WORK/build/bin/whisper-cli" "$OUT/"
"${CROSS}strip" "$OUT/whisper-cli" 2>/dev/null || strip "$OUT/whisper-cli" 2>/dev/null || true
cp "$src/LICENSE" "$OUT/whisper.cpp-LICENSE.txt"
printf '%s  %s\n' "$WHISPER_SOURCE_SHA256" "$(basename "$archive")" > "$OUT/whisper.cpp-SOURCE.sha256"
printf 'version=%s\nurl=%s\nsha256=%s\n' \
  "$WHISPER_VERSION" "$WHISPER_SOURCE_URL" "$WHISPER_SOURCE_SHA256" > "$OUT/whisper.cpp-build-input.txt"
ls -l "$OUT/whisper-cli"
