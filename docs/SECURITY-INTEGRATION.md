# Security wave integration

Base: `ebcd74e` (includes the Windows first-run work in #35).
Integration branch: `security-wave-integration`. All fronts are merged with
`--no-ff`; no squash, rebase, force-push, release or main merge is authorized.

## Integrated heads

- #36: public/private CI isolation and pinned actions.
- #37: Go 1.26.10, Wails 3.1.0 and patched module baseline.
- #38: verified native build inputs, LGPL checks, binary-derived notices/SBOMs.
- #39: signed release manifests and fail-closed updater/installer verification.
- #41: webhook SSRF defenses and bounded delivery resources.
- #42: MCP argument/file/network capabilities and purge approval.
- #40: route-specific panel authorization, included only after its exact head
  `a3008d8b95ee6bd78224205bad4858d8c8f275ba` passed run `41695923912`.

## Conflicts and integration repairs

The only textual merge conflict was `.github/workflows/ci.yml` (#36 versus #37).
The result keeps visibility-based runner selection and SHA-pinned checkout,
using Go 1.26.10 with the manifest-list digest independently obtained from
`docker buildx imagetools inspect golang:1.26.10-bookworm`:
`sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb`.

`go.mod`, `go.sum`, Wails/platform files and updater changes merged cleanly.
The Windows first-run files from #35 remain present and unchanged. #40/#41
media API test changes both survive (413 body limit and explicit private-network
permission); #40/#42 daemon capability tests merged without conflict.

The integration also closes concrete branch review gaps:

- macOS cross-architecture FFmpeg builds validate `config.h` without executing
  foreign binaries; native builds retain the executable configuration check;
- Linux/macOS/release notices receive the actual built payloads, preserving
  linked Wails copyright instead of relying on the headless fallback;
- trusted in-process Wails requests are authorized before browser-only Origin
  and Fetch Metadata restrictions; external HTTP keeps both restrictions;
- webhook creation and re-enable expose explicit localized private-network
  consent; per-address dialing has a bounded budget even when Go's HTTP
  transport removes the request deadline;
- interrupted purge quarantines preserve original names and recover on the next
  confirmed purge without overwriting recreated files; collisions and legacy
  unmapped quarantines retain bytes and fail visibly;
- Windows avoids unsupported directory `FlushFileBuffers` on read-only handles.
  Process-crash recovery is covered; Windows power-loss durability is not claimed.

CI now runs the #38 build-inventory and sidecar-security regressions, module
verification, govulncheck, and generates/checks inventories for real Linux app,
CLI and bridge binaries after the build.

## Verification

All commands use Go 1.26.10, `HOME=/root`, `GOFLAGS=-tags=gtk3`, with `GOPATH`
and Telegram secrets/data/store/port overrides unset. Async tests use the
30-minute package timeout and are allowed to finish.

- Formatting, `go vet ./...`, `go mod verify`, and `go mod tidy -diff`: passed.
- `govulncheck -show verbose ./...`: **No vulnerabilities found** (39 modules
  plus the Go 1.26.10 standard library).
- Both complete `go test -race -count=1 -timeout 30m ./...` runs: exit 0.
  The final tree passed 27 packages (one additional package has no test files),
  including gotdengine in 791.658s, panel in 435.195s and store in 493.345s.
- `actionlint v1.7.12`: passed.
- Gitleaks 8.30.1: full history/current tree and negative scanner tests passed.
- Python unit/contract suites: brand guard 4, build inventory 23, sidecar security
  11, packaging 9, installer 15, release 19: all passed.
- All Linux `./cmd/...` programs built. Windows amd64 and macOS amd64/arm64
  app/CLI/bridge cross-builds passed; macOS uses compile-only `purego` fallbacks.
  The historical `telegram-poc` is Linux-specific and is not a release payload.
- Real Linux binary-derived notices and CycloneDX inventories generated and
  `--check` verified.
- Standalone `telegram-mcp-bridge` against an isolated fake daemon listed exactly
  **40 unique tools**; the same daemon returned **403** for bearer `GET /`.
  The smoke used port 35733 and only scratch-owned HOME/data/store directories.

Local logs and generated binaries are outside Git under
`/root/.hermes/cache/scratch/sec-integration/`.

## Explicit non-claims

No real Telegram account, installed service, port 47831, or production application
state was accessed. No new native Windows/macOS GUI acceptance is implied by
cross-compilation. No release was built/published or signed with a production key.

`scripts/test-artifacts.py` is an inspector requiring a complete real release
output directory (AppImage/DEB/NSIS and five CLI archives), not a standalone unit
suite. Invoking it without that input fails; actual artifact inspection, native
sidecar rebuilds and Windows Authenticode validation remain release gates, not
claimed integration results. See `docs/security/build-inputs.md`.
