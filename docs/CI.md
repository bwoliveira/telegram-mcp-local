# Foundation CI

Stage 4 adds `python3 scripts/test-brand-guard.py` to the self-hosted workflow.
It executes the requested case-insensitive Git grep over the repository,
excluding `site/`, `CHANGELOG.md`, `THIRD_PARTY_NOTICES.md` and private research.
Only exact, reviewed, path-scoped upstream attribution lines and dedicated
negative branding tests are allowed. A credit URL, author name or attribution
phrase never exempts other text on the same line; changing an approved credit
requires updating its literal allowlist entry. The guard includes negative
probes (including command-plus-credit attacks), and scans untracked files too.


The `.github/workflows/ci.yml` workflow validates the integrated Telegram tree on
Linux. Runner selection comes from the repository visibility in the immutable
event payload:

- while the repository is private, jobs require both `self-hosted` and
  `<private-runner-label>`, preserving the free trusted homelab pool;
- once the repository is public, every event, including a pull request from a
  fork, selects `ubuntu-latest` instead. Public contributions can therefore
  never select the persistent homelab runner.

The workflow does not use `pull_request_target`, publish artifacts or releases,
or connect any Telegram or upstream product account. Its token is limited to
`contents: read`, and checkout does not persist Git credentials. The checkout
action is pinned to a full commit SHA.

## Commands and dependencies

- The `golang:1.26.10-bookworm` container follows the inherited `go.mod` and is
  pinned to manifest-list digest
  `sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb`.
- GTK 3 and WebKitGTK 4.1 development libraries are installed in the container,
  without installing dependencies globally on the runners' machine.
- `GOFLAGS=-tags=gtk3` keeps the same GUI selection as upstream CI.
- `go vet ./...` and `go test -race -count=1 ./...` include **all** packages;
  no test is removed or skipped to obtain a green result.
- Building the programs in `./cmd/...` preserves the additional upstream check.

## Secret scanner (SEC-L03)

CI downloads the official **gitleaks 8.30.1 Linux x86_64 binary**. The
`scripts/install-gitleaks.sh` script pins and checks both the SHA-256 of the
release archive (`551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb`)
and the SHA-256 of the extracted binary
(`88f91962aa2f93ac6ab281d553b9e125f5197bbbce38f9f2437f7299c32e5509`).
No floating action, image, or tag is used for the scanner.

Checkout uses `fetch-depth: 0`; `scripts/scan-secrets.sh history` rejects a
shallow repository and scans `--all --full-history --diff-merges=first-parent`,
as well as the current working tree. Before the actual scan,
`scripts/test-gitleaks.sh` creates a temporary repository, commits synthetic
tokens in linear history and only in a merge, attempts to put another secret on
the same line as an allowed fixture, removes everything from HEAD, and requires
gitleaks to still exit with status 1. This negative test proves that deleting a
value in the current revision, hiding it in a merge, or exploiting a path-based
allowlist does not remove it from the history gate.

`.gitleaks.toml` inherits all default rules. Its exceptions use both path **and**
content and cover only three literal synthetic test credentials, SHA-1 keys
from historical narration caches, and an exact block of the public Telegram
prime used by the offline SRP fixture. No entire test directory, rule, or commit
is ignored; real salts, passwords, sessions, and tokens remain blocked.

For the mandatory pre-commit check, after installing the same pinned binary, run:

```bash
GITLEAKS_BIN=/path/to/gitleaks scripts/scan-secrets.sh staged
```

To repeat the negative proof and full scan locally:

```bash
scripts/install-gitleaks.sh "$HOME/.cache/gitleaks-8.30.1/gitleaks"
GITLEAKS_BIN="$HOME/.cache/gitleaks-8.30.1/gitleaks" scripts/test-gitleaks.sh
GITLEAKS_BIN="$HOME/.cache/gitleaks-8.30.1/gitleaks" scripts/scan-secrets.sh history
```

GUI dependencies are required even without opening a window, because Wails is
part of the build. Tests use upstream fixtures and fake subprocesses. Opt-in
end-to-end transcription is not enabled: it requires external binaries/models.

## First-run diagnosis

The [first run](https://github.com/bwoliveira/telegram-mcp-local/actions/runs/41375185825)
passed `go vet`, but the `internal/daemon` and upstream CLI package tests could
not compile the fake executable: `error obtaining VCS status`, exit 128. The
reproduced cause was Git's ownership protection: the checkout belongs to the
runner user, while the container runs as root. The temporary configuration made
by checkout did not reach the Go subprocesses of those tests.

The fix trusts **only the exact `$GITHUB_WORKSPACE` path** in the container's Git
configuration before the Go commands. The minimal fake `go build` reproduction
failed before and passed after this configuration. It does not use
`safe.directory=*`, disable VCS stamping, or reduce test scope.

## Limits

The upstream matrix (Linux, macOS, and Windows) is replaced by Linux at this
stage, as decided for private CI. There is no evidence of GUI runtime, packaging,
signing, auto-update, macOS/Windows, a real account, or Telegram functionality.
Those acceptance criteria remain pending in their respective plan stages.

The workflow does not use application secrets. Shared infrastructure remains a
trust boundary even though visibility-based routing prevents public jobs from
selecting the homelab pool.

## Public-launch checklist

Complete these controls before changing repository visibility:

- [ ] In **Settings > Actions > Runners**, remove or disable this repository's
      access to the `<private-runner-label>` runner (or its runner group). Confirm that the
      public repository lists no usable self-hosted runner.
- [ ] In **Settings > Actions > General > Approval for running fork pull request
      workflows from contributors**, select **Require approval for all external
      contributors** and save it. Review `.github/workflows/` changes before
      approving each fork workflow.
- [ ] Confirm the repository is public, then verify a `pull_request` run reports
      `ubuntu-latest`/GitHub-hosted labels and the pinned job container digest;
      cancel the run if any self-hosted label appears.
- [ ] Keep the workflow on `pull_request`, not `pull_request_target`, with
      `contents: read`, no secrets, SHA-pinned actions, and
      `persist-credentials: false`.

GitHub recommends against using self-hosted runners for public repositories
because untrusted pull requests can persistently compromise them. Runner removal
is a second boundary: it must be completed even though the workflow expression
already routes public jobs to GitHub-hosted runners.
