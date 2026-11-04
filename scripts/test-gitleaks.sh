#!/usr/bin/env bash
set -euo pipefail

repo="$(git rev-parse --show-toplevel)"
config="$repo/.gitleaks.toml"
gitleaks="${GITLEAKS_BIN:-$(command -v gitleaks || true)}"

if [[ -z "$gitleaks" || ! -x "$gitleaks" ]]; then
  printf 'ERRO: defina GITLEAKS_BIN para executar a prova negativa.\n' >&2
  exit 2
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
fixture="$tmp_dir/repo"
report="$tmp_dir/gitleaks.json"
report_without_merges="$tmp_dir/gitleaks-sem-merges.json"
mkdir -p "$fixture"
cp "$config" "$fixture/.gitleaks.toml"
git -C "$fixture" init -q
git -C "$fixture" config user.name 'SEC-L03 fixture'
git -C "$fixture" config user.email 'sec-l03@example.invalid'
printf 'fixture limpa\n' >"$fixture/README.md"
git -C "$fixture" add README.md .gitleaks.toml
git -C "$fixture" commit -q -m 'fixture limpa'
base_branch="$(git -C "$fixture" symbolic-ref --short HEAD)"

# Montado em runtime para que o próprio script não versione o token detector.
prefix='AKIA'
part_a='A2B3C4D5'
part_b='E6F7G2H3'
printf 'AWS_ACCESS_KEY_ID=%s%s%s\n' "$prefix" "$part_a" "$part_b" >"$fixture/fixture.env"
git -C "$fixture" add fixture.env
git -C "$fixture" commit -q -m 'adiciona segredo sintético linear'
rm "$fixture/fixture.env"
git -C "$fixture" add -u
git -C "$fixture" commit -q -m 'remove segredo sintético linear do HEAD'

# O caminho e a fixture são permitidos apenas quando ocupam a linha inteira.
# Um segundo segredo na mesma linha precisa continuar bloqueado.
mkdir -p "$fixture/internal/daemon"
fake_a='wamcp_key_01234567'
fake_b='89abcdef0123456789abcdef'
real_a='z9Y8x7W6v5U4t3S2'
real_b='r1Q0p9O8n7M6l5K4'
printf 'const testAPIKey = "%s%s"; const productionAPIKey = "%s%s"\n' \
  "$fake_a" "$fake_b" "$real_a" "$real_b" >"$fixture/internal/daemon/apikeys_test.go"
git -C "$fixture" add internal/daemon/apikeys_test.go
git -C "$fixture" commit -q -m 'tenta contrabandear segredo na linha permitida'
rm "$fixture/internal/daemon/apikeys_test.go"
git -C "$fixture" add -u
git -C "$fixture" commit -q -m 'remove contrabando sintético do HEAD'

# Cria um segredo que existe somente no conteúdo de um commit de merge.
git -C "$fixture" checkout -q -b sec-l03-feature
printf 'ramo auxiliar\n' >"$fixture/feature.txt"
git -C "$fixture" add feature.txt
git -C "$fixture" commit -q -m 'ramo auxiliar sem segredo'
git -C "$fixture" checkout -q "$base_branch"
printf 'ramo principal\n' >"$fixture/main.txt"
git -C "$fixture" add main.txt
git -C "$fixture" commit -q -m 'diverge ramo principal'
git -C "$fixture" merge -q --no-ff --no-commit sec-l03-feature
merge_part_a='J2K3L4M5'
merge_part_b='N6P7Q2R3'
printf 'AWS_ACCESS_KEY_ID=%s%s%s\n' "$prefix" "$merge_part_a" "$merge_part_b" >"$fixture/merge-only.env"
git -C "$fixture" add merge-only.env
git -C "$fixture" commit -q -m 'merge introduz segredo sintético'
merge_sha="$(git -C "$fixture" rev-parse HEAD)"
read -r -a merge_parents <<<"$(git -C "$fixture" show -s --format=%P "$merge_sha")"
if [[ "${#merge_parents[@]}" -ne 2 ]]; then
  printf 'ERRO: fixture esperava commit de merge com dois pais.\n' >&2
  exit 1
fi
rm "$fixture/merge-only.env"
git -C "$fixture" add -u
git -C "$fixture" commit -q -m 'remove segredo do merge no HEAD'

# Controle: sem diffs de merge, o valor criado no merge não pode aparecer. Isso
# prova que a asserção seguinte depende de --diff-merges, não de outro commit.
set +e
(
  cd "$fixture"
  "$gitleaks" git --config .gitleaks.toml --redact --no-banner --no-color \
    --log-opts='--all --full-history' --report-format json \
    --report-path "$report_without_merges" .
)
status_without_merges=$?
set -e
if [[ "$status_without_merges" -ne 1 || ! -s "$report_without_merges" ]] || \
   grep -q 'merge-only.env' "$report_without_merges"; then
  printf 'ERRO: controle sem diffs de merge não isolou a fixture de merge.\n' >&2
  exit 1
fi

set +e
(
  cd "$fixture"
  "$gitleaks" git --config .gitleaks.toml --redact --no-banner --no-color \
    --log-opts='--all --full-history --diff-merges=first-parent' \
    --report-format json --report-path "$report" .
)
status=$?
set -e

if [[ "$status" -ne 1 ]]; then
  printf 'ERRO: prova negativa esperava exit 1, recebeu %s.\n' "$status" >&2
  exit 1
fi
if [[ ! -s "$report" ]] || ! grep -q 'fixture.env' "$report" || \
   ! grep -q 'merge-only.env' "$report" || \
   ! grep -q 'internal/daemon/apikeys_test.go' "$report" || \
   ! grep -q 'aws-access-token' "$report" || ! grep -q 'generic-api-key' "$report"; then
  printf 'ERRO: gitleaks não registrou todas as fixtures sintéticas esperadas.\n' >&2
  exit 1
fi
if [[ -e "$fixture/fixture.env" || -e "$fixture/merge-only.env" || \
      -e "$fixture/internal/daemon/apikeys_test.go" ]]; then
  printf 'ERRO: uma fixture negativa ainda existe no HEAD sintético.\n' >&2
  exit 1
fi

printf 'OK: prova negativa bloqueou segredos removidos do HEAD, inclusive em merge e em linha allowlisted.\n'
