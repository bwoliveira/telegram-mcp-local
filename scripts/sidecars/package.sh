#!/usr/bin/env bash
# Package the verified whisper.cpp and FFmpeg sidecars with licenses, exact
# FFmpeg corresponding source, build configuration and CycloneDX SBOM.
#
#   scripts/sidecars/package.sh darwin universal <built-dir> <out-dir>
#   scripts/sidecars/package.sh linux amd64 <built-dir> <out-dir>
#   scripts/sidecars/package.sh windows amd64 <ffmpeg-dir> <out-dir> [cuda]
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=pins.sh
source "$SCRIPT_DIR/pins.sh"
# shellcheck source=verify-download.sh
source "$SCRIPT_DIR/verify-download.sh"

os="$1" arch="$2" in="$(cd "$3" && pwd)" out="$(mkdir -p "$4" && cd "$4" && pwd)" variant="${5:-}"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
pkg="$stage/pkg"
mkdir -p "$pkg"
name="transcription_${os}_${arch}${variant:+_$variant}"

copy_ffmpeg_compliance() {
  local required=(
    "ffmpeg-build-config.txt" "ffmpeg-build-input.txt" "ffmpeg-license-config.txt"
    "FFmpeg-LGPL-2.1.txt" "FFmpeg-LGPL-3.txt"
    "ffmpeg-${FFMPEG_VERSION}-source.tar.xz"
    "ffmpeg-${FFMPEG_VERSION}-source.tar.xz.asc"
  )
  local file
  for file in "${required[@]}"; do
    [ -f "$in/$file" ] || { echo "Missing FFmpeg compliance file: $in/$file" >&2; exit 1; }
    cp "$in/$file" "$pkg/"
  done
  if grep -Eq '^--enable-(gpl|nonfree)(=|$)' "$pkg/ffmpeg-build-config.txt"; then
    echo 'Refusing to package GPL or nonfree FFmpeg configuration' >&2; exit 1
  fi
  grep -qx '#define CONFIG_GPL 0' "$pkg/ffmpeg-license-config.txt" || {
    echo 'Missing proof that FFmpeg CONFIG_GPL is disabled' >&2; exit 1;
  }
  grep -qx '#define CONFIG_NONFREE 0' "$pkg/ffmpeg-license-config.txt" || {
    echo 'Missing proof that FFmpeg CONFIG_NONFREE is disabled' >&2; exit 1;
  }
}

if [ "$os" = windows ]; then
  [ "$arch" = amd64 ] || { echo 'Only windows/amd64 is supported' >&2; exit 2; }
  case "$variant" in
    "") asset="$WHISPER_WINDOWS_CPU_ASSET"; digest="$WHISPER_WINDOWS_CPU_SHA256" ;;
    cuda) asset="$WHISPER_WINDOWS_CUDA_ASSET"; digest="$WHISPER_WINDOWS_CUDA_SHA256" ;;
    *) echo "Unknown Windows variant: $variant" >&2; exit 2 ;;
  esac
  url="https://github.com/ggml-org/whisper.cpp/releases/download/${WHISPER_WINDOWS_RELEASE}/${asset}"
  download_verified "$url" "$digest" "$stage/whisper.zip"
  unzip -q "$stage/whisper.zip" -d "$stage/x"
  release="$stage/x/Release"
  for file in whisper-cli.exe whisper.dll ggml.dll ggml-base.dll; do
    [ -f "$release/$file" ] || { echo "Missing $file in verified whisper.cpp asset" >&2; exit 1; }
    cp "$release/$file" "$pkg/"
  done
  cp "$release"/ggml-cpu-*.dll "$pkg/"
  if [ "$variant" = cuda ]; then
    for file in ggml-cuda.dll cublas64_12.dll cublasLt64_12.dll cudart64_12.dll nvblas64_12.dll nvrtc-builtins64_124.dll nvrtc64_120_0.dll; do
      [ -f "$release/$file" ] || { echo "Missing $file in verified CUDA asset" >&2; exit 1; }
      cp "$release/$file" "$pkg/"
    done
  fi
  cp "$in/ffmpeg.exe" "$pkg/"
  source_archive="$stage/whisper-source.tar.gz"
  download_verified "$WHISPER_SOURCE_URL" "$WHISPER_SOURCE_SHA256" "$source_archive"
  tar -xOzf "$source_archive" "whisper.cpp-${WHISPER_VERSION}/LICENSE" > "$pkg/whisper.cpp-LICENSE.txt"
  printf 'version=%s\nsource_url=%s\nsource_sha256=%s\nwindows_release=%s\nasset_url=%s\nasset_sha256=%s\n' \
    "$WHISPER_VERSION" "$WHISPER_SOURCE_URL" "$WHISPER_SOURCE_SHA256" \
    "$WHISPER_WINDOWS_RELEASE" "$url" "$digest" > "$pkg/whisper.cpp-build-input.txt"
  if [ "$variant" = cuda ]; then
    cuda_eula="$pkg/NVIDIA-CUDA-12.4-EULA-and-third-party-notices.pdf"
    download_verified "$CUDA_12_4_EULA_URL" "$CUDA_12_4_EULA_SHA256" "$cuda_eula"
    cat > "$pkg/NVIDIA-CUDA-runtime-NOTICE.txt" <<'NOTICE'
This package contains CUDA runtime DLLs redistributed in the official
whisper.cpp Windows CUDA asset. NVIDIA's CUDA Toolkit End User License
Agreement, redistribution terms, and required third-party notices are included
as NVIDIA-CUDA-12.4-EULA-and-third-party-notices.pdf.
The exact upstream asset URL and SHA-256 are in whisper.cpp-build-input.txt.
NOTICE
  fi
else
  [ -z "$variant" ] || { echo 'Variants are supported only on Windows' >&2; exit 2; }
  for file in whisper-cli ffmpeg whisper.cpp-LICENSE.txt whisper.cpp-build-input.txt; do
    [ -f "$in/$file" ] || { echo "Missing sidecar input: $in/$file" >&2; exit 1; }
    cp "$in/$file" "$pkg/"
  done
  if [ "$os" = darwin ]; then
    if [ -n "${SIGN_IDENTITY:-}" ]; then
      codesign --force --options runtime --timestamp --sign "$SIGN_IDENTITY" "$pkg/whisper-cli" "$pkg/ffmpeg"
    else
      codesign --force --sign - "$pkg/whisper-cli" "$pkg/ffmpeg"
    fi
  fi
fi
copy_ffmpeg_compliance

# Archive ordering, ownership and timestamps are normalized by a portable Python
# writer. It always creates a fresh temporary file and atomically replaces the
# destination, so stale ZIP members cannot survive a rebuild.
if [ "$os" = windows ]; then
  file="$out/$name.zip"
  python3 "$SCRIPT_DIR/archive.py" --format zip --payload-dir "$pkg" --output "$file"
else
  file="$out/$name.tar.gz"
  python3 "$SCRIPT_DIR/archive.py" --format tar.gz --payload-dir "$pkg" --output "$file"
fi
# Re-extract the final archive and derive both legal outputs from those exact
# bytes, never from an unarchived CI build directory.
verify="$stage/final-payload"
mkdir -p "$verify"
if [ "$os" = windows ]; then
  unzip -q "$file" -d "$verify"
else
  tar -xzf "$file" -C "$verify"
fi
python3 "$SCRIPT_DIR/sbom.py" --artifact "$file" --payload-dir "$verify" --output "$file.cdx.json"
notice="$file.THIRD-PARTY-NOTICES.txt"
{
  printf 'Telegram MCP transcription sidecar\n\nThis notice was generated from legal files extracted from %s.\n' "$(basename "$file")"
  for legal in whisper.cpp-LICENSE.txt FFmpeg-LGPL-2.1.txt FFmpeg-LGPL-3.txt NVIDIA-CUDA-runtime-NOTICE.txt; do
    if [ -f "$verify/$legal" ]; then
      printf '\n===== %s =====\n\n' "$legal"
      cat "$verify/$legal"
      printf '\n'
    fi
  done
  printf '\nCorresponding FFmpeg source and build provenance are packaged in the archive.\n'
} > "$notice"
sum="$(sha256_file "$file")"
size="$(wc -c < "$file" | tr -d ' ')"
echo "$(basename "$file") $size $sum"
