#!/usr/bin/env bash
set -euo pipefail

repo="$(git rev-parse --show-toplevel)"
config="$repo/.gitleaks.toml"
gitleaks="${GITLEAKS_BIN:-$(command -v gitleaks || true)}"
mode="${1:-history}"

if [[ -z "$gitleaks" || ! -x "$gitleaks" ]]; then
  printf 'ERRO: defina GITLEAKS_BIN para o binário verificado ou instale gitleaks no PATH.\n' >&2
  exit 2
fi
if [[ ! -f "$config" ]]; then
  printf 'ERRO: configuração ausente: %s\n' "$config" >&2
  exit 2
fi

common=(--config "$config" --redact --no-banner --no-color --timeout 300)
case "$mode" in
  history)
    if [[ "$(git -C "$repo" rev-parse --is-shallow-repository)" != "false" ]]; then
      printf 'ERRO: checkout raso; a varredura SEC-L03 exige todo o histórico.\n' >&2
      exit 2
    fi
    (
      cd "$repo"
      "$gitleaks" git "${common[@]}" --log-opts='--all --full-history --diff-merges=first-parent' .
      "$gitleaks" dir "${common[@]}" .
    )
    printf 'OK: gitleaks não encontrou segredos no histórico completo nem na árvore atual.\n'
    ;;
  staged)
    (
      cd "$repo"
      "$gitleaks" git "${common[@]}" --staged .
    )
    printf 'OK: gitleaks não encontrou segredos nas mudanças staged.\n'
    ;;
  *)
    printf 'uso: %s [history|staged]\n' "$0" >&2
    exit 2
    ;;
esac
