# Dependency security baseline

This project treats the Go toolchain and the desktop runtime as security-sensitive build inputs. The module requires Go `1.26.10`; Linux packaging and CI use the same patched toolchain. Keeping the patch version in the `go` directive makes vulnerable older toolchains fail even when `GOTOOLCHAIN=local` disables automatic toolchain selection.

## Current security pins

| Component | Version | Reason |
| --- | --- | --- |
| Go toolchain | `go1.26.10` | Includes the standard-library fixes required by the November 2026 Go vulnerability database. |
| Wails | `github.com/wailsapp/wails/v3 v3.1.0` | Current stable Wails v3 release; replaces the pre-stable `v3.0.0-beta.27` runtime. |
| `x/net` | `golang.org/x/net v0.62.0` | Includes the current networking security fixes. |
| `x/crypto` | `v0.58.1-0.20261009181718-2f1f834330b2` | First fixed revision named by GO-2026-5932. Stable `v0.58.0` is still reported at module level because its deprecated `openpgp` package remains affected. |

The Wails stable migration keeps the existing `github.com/wailsapp/wails/v3` API surface. Native Linux smoke tests exercise the window, tray, close/reopen lifecycle, fake-engine state changes, delayed startup, and hidden startup. Windows and macOS compile checks cover the desktop app and stdio bridge. macOS release packages still use the native cgo/Cocoa implementation; the `purego` build is a compile-only cross-platform check and uses no-op Dock integration fallbacks.

## Vulnerability gate

Install the scanner outside the repository, then run it with the pinned toolchain and normal Linux build tags:

```bash
GOPATH=/path/to/isolated/gopath go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
env -u GOPATH \
  PATH=/path/to/go1.26.10/bin:$PATH \
  GOTOOLCHAIN=local GOFLAGS=-tags=gtk3 \
  /path/to/isolated/gopath/bin/govulncheck -show verbose ./...
```

Baseline scan on 2026-11-03 (`go1.26.5`, Wails `v3.0.0-beta.27`, `x/net v0.59.0`, and `x/crypto v0.57.0`):

- 20 reachable standard-library vulnerabilities;
- 2 additional imported-package vulnerabilities with no reachable symbols;
- 3 additional required-module vulnerabilities with no reachable symbols;
- exit status 3.

Post-upgrade scan on 2026-11-03 (`govulncheck v1.9.0`, database updated 2026-11-02):

```text
No vulnerabilities found.
```

The post-upgrade scan exits successfully and reports no symbol-, package-, or module-level findings.

## Upgrade procedure

1. Check the latest stable Go patch and Wails release from their official release channels.
2. Update `go.mod`, the Linux builder checksums, and the CI container together.
3. Run `go mod tidy` and `go mod verify` with the selected toolchain.
4. Run `govulncheck -show verbose ./...`; do not accept reachable findings. Document any module-only finding and its unreachable call path if no fixed revision exists.
5. Run formatting, vet, the full race suite, secret and brand guards, native Linux GUI smoke tests, and Windows/macOS compile checks.

References:

- [Go downloads](https://go.dev/dl/)
- [Wails v3.1.0 release](https://github.com/wailsapp/wails/releases/tag/v3.1.0)
- [Go vulnerability database](https://vuln.go.dev/)
- [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932)
