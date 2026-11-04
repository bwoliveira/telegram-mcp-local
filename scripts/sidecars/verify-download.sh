#!/usr/bin/env bash
# Shared fail-closed download helpers. Source this file after set -euo pipefail.

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

verify_sha256() {
  local file="$1" expected="$2" actual
  actual="$(sha256_file "$file")"
  if [ "$actual" != "$expected" ]; then
    printf 'SHA-256 mismatch for %s\nexpected: %s\nactual:   %s\n' \
      "$file" "$expected" "$actual" >&2
    return 1
  fi
}

download_verified() {
  local url="$1" expected="$2" destination="$3" partial cached
  partial="${destination}.part.$$"
  rm -f "$partial"
  trap 'rm -f "$partial"' RETURN
  cached="${BUILD_INPUT_CACHE:-}/$expected"
  if [ -n "${BUILD_INPUT_CACHE:-}" ] && [ -f "$cached" ]; then
    verify_sha256 "$cached" "$expected"
    cp "$cached" "$partial"
  else
    curl --proto '=https' --tlsv1.2 --fail --location --retry 5 \
      --retry-all-errors --connect-timeout 30 --output "$partial" "$url"
  fi
  verify_sha256 "$partial" "$expected"
  mv -f "$partial" "$destination"
  trap - RETURN
}
