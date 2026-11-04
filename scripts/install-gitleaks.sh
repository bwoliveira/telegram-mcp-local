#!/usr/bin/env bash
set -euo pipefail

readonly GITLEAKS_VERSION="8.30.1"
readonly GITLEAKS_ASSET="gitleaks_${GITLEAKS_VERSION}_linux_x64.tar.gz"
readonly GITLEAKS_ARCHIVE_SHA256="551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb"
readonly GITLEAKS_BINARY_SHA256="88f91962aa2f93ac6ab281d553b9e125f5197bbbce38f9f2437f7299c32e5509"
readonly GITLEAKS_URL="https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/${GITLEAKS_ASSET}"

destination="${1:-${GITLEAKS_BIN:-}}"
if [[ -z "$destination" ]]; then
  printf 'uso: %s CAMINHO_DESTINO\n' "$0" >&2
  exit 2
fi

if [[ "$(uname -s)" != "Linux" || "$(uname -m)" != "x86_64" ]]; then
  printf 'ERRO: gitleaks fixado somente para o runner Linux x86_64.\n' >&2
  exit 2
fi

verify_binary() {
  local binary="$1" actual version
  actual="$(sha256sum "$binary" | cut -d' ' -f1)"
  [[ "$actual" == "$GITLEAKS_BINARY_SHA256" ]] || return 1
  version="$("$binary" version)"
  [[ "$version" == "$GITLEAKS_VERSION" ]]
}

if [[ -e "$destination" ]]; then
  if verify_binary "$destination"; then
    printf 'OK: gitleaks %s já instalado; SHA-256 do binário conferido.\n' "$GITLEAKS_VERSION"
    exit 0
  fi
  printf 'ERRO: binário existente em %s não corresponde à versão/checksum fixados.\n' "$destination" >&2
  exit 1
fi

install_dir="$(dirname "$destination")"
mkdir -p "$install_dir"
tmp_dir="$(mktemp -d "${install_dir}/.gitleaks-install.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT
archive="$tmp_dir/$GITLEAKS_ASSET"

curl --fail --location --silent --show-error \
  --proto '=https' --tlsv1.2 \
  "$GITLEAKS_URL" -o "$archive"
printf '%s  %s\n' "$GITLEAKS_ARCHIVE_SHA256" "$archive" | sha256sum --check --status || {
  printf 'ERRO: SHA-256 do arquivo gitleaks baixado não confere.\n' >&2
  exit 1
}

tar -xzf "$archive" -C "$tmp_dir" gitleaks
if ! verify_binary "$tmp_dir/gitleaks"; then
  printf 'ERRO: versão ou SHA-256 do binário gitleaks extraído não confere.\n' >&2
  exit 1
fi
install -m 0755 "$tmp_dir/gitleaks" "$destination"
printf 'OK: gitleaks %s instalado; arquivo e binário conferidos por SHA-256.\n' "$GITLEAKS_VERSION"
