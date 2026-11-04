#!/usr/bin/env bash
# Shared, fail-closed source preparation. This never changes refs or publishes.
release_prepare() {
  VERSION="${1:-}"; [ "$#" -gt 0 ] && shift
  local ref= output= origin private resolved
  [[ "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
    echo 'Expected a strict X.Y.Z version (without v or prerelease suffix)' >&2; return 2;
  }
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --ref|--output)
        [ "$#" -ge 2 ] && [ -n "$2" ] || { echo "Missing value for $1" >&2; return 2; }
        if [ "$1" = --ref ]; then ref="$2"; else output="$2"; fi; shift 2 ;;
      *) echo "Unknown argument: $1" >&2; return 2 ;;
    esac
  done
  TAG="v$VERSION"
  if [ -n "$ref" ]; then
    [[ "$ref" =~ ^[0-9a-f]{40}$ ]] && [ "$VERSION" = 0.0.0 ] && [ -n "$output" ] || {
      echo '--ref requires a full commit, version 0.0.0 and explicit --output (private test only)' >&2; return 2;
    }
    resolved="$(git rev-parse --verify "$ref^{commit}")"
    [ "$resolved" = "$ref" ] || return 2
    BUILD_MODE=private-test
  else
    [ "$VERSION" != 0.0.0 ] || { echo '0.0.0 requires explicit --ref and --output' >&2; return 2; }
    resolved="$(git rev-parse --verify "refs/tags/$TAG^{commit}")" || {
      echo "There is no release tag $TAG" >&2; return 2;
    }
    BUILD_MODE=tag
  fi
  REPOSITORY="bwoliveira/telegram-mcp-local"
  origin="$(git remote get-url origin)"
  case "$origin" in
    "https://github.com/$REPOSITORY"|"https://github.com/$REPOSITORY.git"|"git@github.com:$REPOSITORY.git") ;;
    *) echo "release builds require origin $REPOSITORY (got $origin)" >&2; return 1 ;;
  esac
  command -v gh >/dev/null || { echo 'gh is required to verify the repository is private' >&2; return 1; }
  private="$(gh api "repos/$REPOSITORY" --jq .private)"
  if [ "$private" != true ]; then
    echo "release builds are restricted to the private repository $REPOSITORY" >&2; return 1
  fi
  [ -z "${PRO_DIR:-}" ] && [ -z "${TAGS:-}" ] || {
    echo 'Untracked overlays and custom build tags are not allowed in release snapshots' >&2; return 2;
  }
  release_signing_preflight || return 1
  # Do not allow a caller's -overlay/-modfile/-tags or a parent go.work/global
  # go env file to replace tracked snapshot inputs. Docker has its own GTK tag.
  export GOFLAGS= GOWORK=off GOENV=off
  OUT="${output:-$PWD/dist/$TAG}"
  [ ! -e "$OUT" ] || { echo "Output already exists; refusing to overwrite: $OUT" >&2; return 2; }
  mkdir -p "$OUT"
  OUT="$(cd "$OUT" && pwd -P)"
  release_work="$(mktemp -d)"
  trap 'rm -rf "$release_work"' EXIT
  src="$release_work/source"
  git clone --quiet --no-local --no-checkout "$PWD" "$src"
  git -C "$src" -c advice.detachedHead=false checkout --quiet --detach "$resolved"
  [ -z "$(git -C "$src" status --porcelain --untracked-files=all)" ] || {
    echo 'Snapshot is not clean' >&2; return 1;
  }
  SOURCE_COMMIT="$resolved"
  SOURCE_TREE="$(git -C "$src" rev-parse HEAD^{tree})"
  SOURCE_REF="${ref:-$TAG}"
  export VERSION SOURCE_REF SOURCE_COMMIT SOURCE_TREE BUILD_MODE
  python3 - "$OUT/build-source.json" <<'PY'
import json, os, sys
from pathlib import Path
Path(sys.argv[1]).write_text(json.dumps({k.lower(): os.environ[k] for k in
    ('VERSION', 'SOURCE_COMMIT', 'SOURCE_TREE', 'BUILD_MODE')}, indent=2) + '\n')
PY
  echo "==> $BUILD_MODE $VERSION from clean commit $SOURCE_COMMIT ($SOURCE_TREE)"
}

release_signing_preflight() {
  command -v minisign >/dev/null || { echo 'minisign is required for every release, including rehearsals' >&2; return 1; }
  python3 - "$PWD" "$(git rev-parse --git-common-dir)" <<'PY'
import base64, os, sys
from pathlib import Path
try:
    public = os.environ.get('TELEGRAM_MCP_MINISIGN_PUBLIC_KEY', '')
    raw = base64.b64decode(public, validate=True)
    if len(raw) != 42 or raw[:2] != b'Ed' or raw[10:] == bytes(32):
        raise ValueError('public key must be a non-placeholder minisign Ed public key (base64 line only)')
    secret = os.environ.get('TELEGRAM_MCP_MINISIGN_SECRET_KEY', '')
    if not secret or not Path(secret).is_absolute():
        raise ValueError('secret key must be an explicit absolute path outside the repository')
    path = Path(secret).resolve(strict=True)
    roots = (Path(sys.argv[1]).resolve(), Path(sys.argv[2]).resolve().parent)
    if not path.is_file() or any(path.is_relative_to(root) for root in roots):
        raise ValueError('secret key must be a file outside the repository (including symlink targets)')
except (ValueError, OSError) as exc:
    sys.exit(f'Release signing configuration refused: {exc}')
PY
}

release_embed_public_key() {
  # Build scripts supply their own -ldflags (macOS also resets GOFLAGS). A narrow
  # go wrapper merges the trust anchor without modifying the clean source tree.
  export TELEGRAM_MCP_RELEASE_GO_REAL="$(command -v go)"
  export TELEGRAM_MCP_MINISIGN_PUBLIC_KEY
  mkdir -p "$release_work/release-go"
  python3 - "$release_work/release-go/go" <<'PY'
from pathlib import Path
import sys
Path(sys.argv[1]).write_text('''#!/usr/bin/env bash
set -euo pipefail
args=() flags= subcommand="${1:-}"
[ "$#" -gt 0 ] && shift
if [ "$subcommand" = build ]; then
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -ldflags) flags="$2"; shift 2 ;;
      -ldflags=*) flags="${1#-ldflags=}"; shift ;;
      *) args+=("$1"); shift ;;
    esac
  done
  exec "$TELEGRAM_MCP_RELEASE_GO_REAL" build -ldflags "$flags -X github.com/bwoliveira/telegram-mcp-local/internal/updater.ReleasePublicKey=$TELEGRAM_MCP_MINISIGN_PUBLIC_KEY" "${args[@]}"
fi
exec "$TELEGRAM_MCP_RELEASE_GO_REAL" "$subcommand" "$@"
''')
PY
  chmod 0755 "$release_work/release-go/go"
  export PATH="$release_work/release-go:$PATH"
}

release_verify_signed_bundle() {
  local bundle="$1" verified="$2"
  [ -f "$bundle/checksums.txt" ] && [ -f "$bundle/checksums.txt.minisig" ] || {
    echo 'Imported release bundle lacks its signed checksums manifest' >&2; return 1;
  }
  [ ! -e "$verified" ] || { echo 'Verified bundle staging path already exists' >&2; return 1; }
  mkdir -m 0700 "$verified"
  # Snapshot the manifest and signature first. Verification and every later read
  # use this private copy, never the caller-controlled source path again.
  python3 - "$bundle" "$verified" <<'PY'
from pathlib import Path
import os, stat, sys
source, target = map(Path, sys.argv[1:])
directory = os.open(source, os.O_RDONLY | os.O_DIRECTORY)
try:
    for name in ('checksums.txt', 'checksums.txt.minisig'):
        descriptor = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=directory)
        try:
            if not stat.S_ISREG(os.fstat(descriptor).st_mode):
                raise SystemExit(f'Unsafe signed bundle metadata: {name}')
            with os.fdopen(descriptor, 'rb', closefd=False) as stream:
                data = stream.read()
        finally:
            os.close(descriptor)
        (target / name).write_bytes(data)
finally:
    os.close(directory)
PY
  minisign -V -q -P "$TELEGRAM_MCP_MINISIGN_PUBLIC_KEY" \
    -m "$verified/checksums.txt" -x "$verified/checksums.txt.minisig" >/dev/null || {
    echo 'Imported release bundle uses a different or invalid release signing key' >&2; return 1;
  }
  # Read each external file once, hash those exact bytes, then write those bytes
  # to private staging. Provenance import consumes only this immutable snapshot.
  python3 - "$bundle" "$verified" <<'PY'
import hashlib, os, re, stat, sys
from pathlib import Path
source, target = map(Path, sys.argv[1:])
manifest = (target / 'checksums.txt').read_text().splitlines()
if len(manifest) < 4 or not manifest[0].startswith('# version: ') or not manifest[1].startswith('# source_commit: ') or not manifest[2].startswith('# date: '):
    raise SystemExit('Imported signed manifest has invalid metadata')
expected = {}
for line in manifest[3:]:
    match = re.fullmatch(r'([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)', line)
    if not match or match[2] in expected:
        raise SystemExit('Imported signed manifest has an invalid or duplicate file entry')
    expected[match[2]] = match[1]
directory = os.open(source, os.O_RDONLY | os.O_DIRECTORY)
try:
    names = set(os.listdir(directory)) - {'checksums.txt', 'checksums.txt.minisig'}
    if names != set(expected):
        raise SystemExit('Imported bundle files do not match the signed manifest')
    for name, digest in expected.items():
        descriptor = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=directory)
        try:
            if not stat.S_ISREG(os.fstat(descriptor).st_mode):
                raise SystemExit(f'Unsafe imported bundle file: {name}')
            with os.fdopen(descriptor, 'rb', closefd=False) as stream:
                data = stream.read()
        finally:
            os.close(descriptor)
        if hashlib.sha256(data).hexdigest() != digest:
            raise SystemExit(f'Imported bundle hash does not match the signed manifest: {name}')
        with (target / name).open('xb') as output:
            output.write(data)
finally:
    os.close(directory)
PY
}

release_validate_installer_key() {
  [ "$BUILD_MODE" != tag ] && return 0
  python3 - "$src/install.sh" "$TELEGRAM_MCP_MINISIGN_PUBLIC_KEY" "$VERSION" <<'PY'
from pathlib import Path
import re, sys
text = Path(sys.argv[1]).read_text()
key, version = sys.argv[2:]
if 'REPLACE_WITH_PRODUCTION_MINISIGN_PUBLIC_KEY' in text or 'REPLACE_WITH_PRODUCTION_RELEASE_VERSION' in text:
    raise SystemExit('Production release refused: install.sh still contains a release trust placeholder')
assignments = re.findall(r'(?m)^\s*public_key=([^\s#]+)\s*$', text)
fixture_override = '${TELEGRAM_MCP_MINISIGN_PUBLIC_KEY:-$public_key}'
if assignments not in ([key], [key, fixture_override]):
    raise SystemExit('Production release refused: install.sh is not pinned exactly once to the selected release key')
versions = re.findall(r'(?m)^\s*release_version=([^\s#]+)\s*$', text)
version_override = '${TELEGRAM_MCP_RELEASE_VERSION:-$release_version}'
if versions not in ([version], [version, version_override]):
    raise SystemExit('Production release refused: install.sh is not pinned exactly once to the selected release version')
PY
}

release_checksums() {
  release_signing_preflight || return 1
  # Never leave an old valid signature beside a newly written manifest on failure.
  rm -f "$1/checksums.txt.minisig"
  python3 - "$1" <<'PY' || return 1
import datetime, hashlib, json, re, sys
from pathlib import Path
root = Path(sys.argv[1])
info = json.loads((root / 'build-source.json').read_text())
version, commit = info['version'], info['source_commit']
if not re.fullmatch(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version):
    sys.exit('Invalid manifest version')
if not re.fullmatch(r'[0-9a-f]{40}', commit):
    sys.exit('Invalid manifest source commit')
files = sorted(p for p in root.iterdir() if p.is_file() and p.name not in
               ('checksums.txt', 'checksums.txt.minisig'))
if any(p.is_symlink() or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', p.name) for p in files):
    sys.exit('Refusing symlink or unsafe manifest filename')
with (root / 'checksums.txt').open('w') as out:
    out.write(f'# version: {version}\n# source_commit: {commit}\n')
    out.write(f'# date: {datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")}\n')
    for p in files:
        with p.open('rb') as src:
            h = hashlib.file_digest(src, 'sha256').hexdigest()
        out.write(f'{h}  {p.name}\n')
PY
  # Legacy Ed signs the raw bytes, compatible with Go crypto/ed25519 (not Ed prehash).
  if ! minisign -S -l -s "$TELEGRAM_MCP_MINISIGN_SECRET_KEY" -m "$1/checksums.txt" \
      -x "$1/checksums.txt.minisig" -t 'Telegram MCP release manifest' ||
      ! minisign -V -P "$TELEGRAM_MCP_MINISIGN_PUBLIC_KEY" -m "$1/checksums.txt" \
      -x "$1/checksums.txt.minisig"; then
    rm -f "$1/checksums.txt.minisig"
    echo 'Release manifest signing or public-key verification failed' >&2
    return 1
  fi
}
