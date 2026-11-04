#!/bin/sh
# Install the CLI only; never start a daemon or request elevated privileges.
# TELEGRAM_MCP_RELEASE_BASE_URL may point to an offline release fixture.
set -eu
umask 077
fail() { printf 'telegram-mcp installer: %s\n' "$1" >&2; exit 1; }
prefix="${HOME:?HOME must be set}/.local/bin"
while [ "$#" -gt 0 ]; do
    case "$1" in
        --prefix)
            [ "$#" -ge 2 ] && [ -n "$2" ] || fail '--prefix requires an installation directory'
            prefix=$2; shift 2 ;;
        -h|--help)
            printf '%s\n' 'Usage: sh install.sh [--prefix DIRECTORY]' \
                'Default directory: ~/.local/bin (no sudo).' \
                'Override release downloads with TELEGRAM_MCP_RELEASE_BASE_URL.'
            exit 0 ;;
        *) fail 'unknown option (use --help)' ;;
    esac
done
# Turn relative destinations into paths that cannot be mistaken for options.
case "$prefix" in /*) ;; *) prefix="$PWD/$prefix" ;; esac
case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) fail 'unsupported operating system (Linux and macOS only)' ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) fail 'unsupported architecture (amd64 and arm64 only)' ;; esac
base=${TELEGRAM_MCP_RELEASE_BASE_URL:-https://github.com/bwoliveira/telegram-mcp-local/releases/latest/download}
base=${base%/}
# HTTP is allowed solely through the explicit local release-base override.
# Redirects always remain HTTPS-only, including for local fixtures.
protocols='=https'
# Production release trust anchor: minisign key ID CF15D44BF3EA0B47, published in
# docs/security/release-signing.md. scripts/release.sh refuses a tag build unless
# this key and release_version match the selected key and version exactly.
public_key=RWRHC+rzS9QVzybIt8aMwMD9cNnK4ju6/vyLeATd+6FBuoDzRG6pk0yB
release_version=1.0.0
authority=${base#*://}
authority=${authority%%/*}
case "$authority" in *'@'*|*'?'*|*'#'*|'') fail 'release base URL has an invalid authority' ;; esac
case "$base" in
    https://*)
        case "$authority" in github.com|github.com:443) ;; *) fail 'HTTPS release base host must be github.com' ;; esac ;;
    http://*)
        host=${authority%:*}
        port=${authority##*:}
        [ "$host" != "$authority" ] || fail 'local HTTP release base requires an explicit port'
        case "$host" in 127.0.0.1|localhost) ;; *) fail 'HTTP release base must be a loopback fixture' ;; esac
        case "$port" in ''|*[!0-9]*) fail 'local HTTP release base has an invalid port' ;; esac
        protocols='=http,https'
        # Test-only override: a network release can never replace the pinned key.
        public_key=${TELEGRAM_MCP_MINISIGN_PUBLIC_KEY:-$public_key}
        release_version=${TELEGRAM_MCP_RELEASE_VERSION:-$release_version} ;;
    *) fail 'release base URL must use HTTPS (or a local HTTP fixture)' ;;
esac
command -v curl >/dev/null 2>&1 || fail 'curl is required'
command -v tar >/dev/null 2>&1 || fail 'tar is required'
command -v minisign >/dev/null 2>&1 || fail 'minisign is required; install it with brew install minisign (macOS) or your Linux package manager (for example: apt install minisign)'
if command -v sha256sum >/dev/null 2>&1; then
    hash() { sha256sum "$1"; }
elif command -v shasum >/dev/null 2>&1; then
    hash() { shasum -a 256 "$1"; }
else
    fail 'sha256sum or shasum is required'
fi
work=$(mktemp -d "${TMPDIR:-/tmp}/telegram-mcp-install.XXXXXXXX") || fail 'could not create temporary directory'
trap 'rm -rf "$work"' EXIT
trap 'exit 1' HUP INT TERM
asset="telegram-mcp_${os}_${arch}.tar.gz"
# Quiet errors intentionally avoid echoing arbitrary server responses or URLs.
curl -q -fsL --proto "$protocols" --proto-redir '=https' --tlsv1.2 --connect-timeout 15 --max-time 300 --output "$work/$asset" "$base/$asset" 2>/dev/null || fail 'release download failed'
curl -q -fsL --proto "$protocols" --proto-redir '=https' --tlsv1.2 --connect-timeout 15 --max-time 60 --output "$work/checksums.txt" "$base/checksums.txt" 2>/dev/null || fail 'checksum download failed'
curl -q -fsL --proto "$protocols" --proto-redir '=https' --tlsv1.2 --connect-timeout 15 --max-time 60 --output "$work/checksums.txt.minisig" "$base/checksums.txt.minisig" 2>/dev/null || fail 'checksum signature download failed'
# Verify with the pinned release key. The environment override above is
# accepted only by explicit loopback fixtures, never for a network release.
minisign -V -q -P "$public_key" -m "$work/checksums.txt" -x "$work/checksums.txt.minisig" >/dev/null 2>&1 || fail 'checksum signature verification failed; nothing installed'
manifest_version=$(awk '
    /^# version: / {
        value = substr($0, 12)
        if (value !~ /^[0-9]+\.[0-9]+\.[0-9]+$/) exit 1
        version = value
        versions++
    }
    /^# source_commit: / {
        value = substr($0, 18)
        if (length(value) != 40 || value ~ /[^0-9a-f]/) exit 1
        commits++
    }
    /^# date: / {
        value = substr($0, 9)
        if (length(value) < 20 || value !~ /T/ || value !~ /Z$/) exit 1
        dates++
    }
    END {
        if (versions != 1 || commits != 1 || dates != 1) exit 1
        print version
    }
' "$work/checksums.txt") || fail 'checksum manifest has missing or invalid signed release metadata'
[ "$manifest_version" = "$release_version" ] || fail 'signed release version does not match the installer; refusing a replayed or unexpected release'
expected=$(awk -v name="$asset" '
    $2 == name || $2 == "*" name {
        if (NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-fA-F]/) exit 1
        value = tolower($1); count++
    }
    END { if (count != 1) exit 1; print value }
' "$work/checksums.txt") || fail 'missing, duplicate or invalid checksum entry'
actual=$(hash "$work/$asset") || fail 'could not calculate checksum'
actual=${actual%% *}
[ "$actual" = "$expected" ] || fail 'checksum mismatch; nothing installed'
# Extract only the two release members, never arbitrary archive paths.
mkdir "$work/unpacked"
tar -xzf "$work/$asset" -C "$work/unpacked" telegram-mcp THIRD-PARTY-NOTICES.txt 2>/dev/null || fail 'invalid release archive'
for name in telegram-mcp THIRD-PARTY-NOTICES.txt; do
    [ -f "$work/unpacked/$name" ] && [ ! -L "$work/unpacked/$name" ] || fail 'invalid release archive member'
done
mkdir -p "$prefix" || fail 'could not create installation directory (choose a writable --prefix)'
# Stage beside the destination for an atomic executable replacement.
stage=$(mktemp -d "$prefix/.telegram-mcp-install.XXXXXXXX") || fail 'installation directory is not writable'
trap 'rm -rf "$work" "$stage"' EXIT
cp "$work/unpacked/telegram-mcp" "$stage/telegram-mcp"
cp "$work/unpacked/THIRD-PARTY-NOTICES.txt" "$stage/THIRD-PARTY-NOTICES.txt"
chmod 755 "$stage/telegram-mcp"
chmod 644 "$stage/THIRD-PARTY-NOTICES.txt"
mv -f "$stage/THIRD-PARTY-NOTICES.txt" "$prefix/THIRD-PARTY-NOTICES.txt"
mv -f "$stage/telegram-mcp" "$prefix/telegram-mcp"
printf 'Installed Telegram MCP CLI in %s\n' "$prefix"
printf '%s\n' 'Add this directory to PATH if needed. No service was started.'
