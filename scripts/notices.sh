#!/usr/bin/env bash
# Print notices for the modules actually linked into the supplied Go binaries.
#   scripts/notices.sh [--output-dir DIR] [--binary] app.exe [bridge.exe ...]
# With --output-dir, keep per-binary notices and CycloneDX SBOMs there as well.
# No arguments is a curated preview only, retained for legacy packaging callers.
set -euo pipefail
cd "$(dirname "$0")/.."
output=
binaries=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-dir)
      [ "$#" -ge 2 ] || { echo 'Missing --output-dir value' >&2; exit 2; }
      output="$2"; shift 2 ;;
    --binary)
      [ "$#" -ge 2 ] || { echo 'Missing --binary value' >&2; exit 2; }
      binaries+=(--binary "$2"); shift 2 ;;
    --help|-h)
      echo 'Usage: scripts/notices.sh [--output-dir DIR] [--binary] BINARY [BINARY ...]'
      exit 0 ;;
    --)
      shift
      while [ "$#" -gt 0 ]; do binaries+=(--binary "$1"); shift; done ;;
    -*) echo "Unknown argument: $1" >&2; exit 2 ;;
    *) binaries+=(--binary "$1"); shift ;;
  esac
done
if [ "${#binaries[@]}" -eq 0 ]; then
  [ -z "$output" ] || { echo '--output-dir requires at least one binary' >&2; exit 2; }
  echo 'WARNING: curated preview only; pass built binaries for complete release notices.' >&2
  cat THIRD_PARTY_NOTICES.md
  printf '\n\n# Application license\n\n'
  cat LICENSE
  exit 0
fi
if [ -z "$output" ]; then
  output="$(mktemp -d)"
  trap 'rm -rf "$output"' EXIT
fi
python3 scripts/build-inventory.py "${binaries[@]}" --output-dir "$output"
cat "$output/THIRD-PARTY-NOTICES.txt"
