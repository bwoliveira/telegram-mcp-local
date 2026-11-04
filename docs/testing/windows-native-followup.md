# Native Windows follow-up: seven previously failing packages

Base: `afdc06c` (the merge of PR #61). This follow-up covers `cmd/app`,
`cmd/telegram-mcp-bridge`, `internal/cli`, `internal/daemon`,
`internal/httpserver`, `internal/mcp`, and `internal/service`.
It does not claim a new all-package Windows certification.

## Native results

All binaries were cross-compiled with Go 1.26.10, `GOOS=windows`,
`GOARCH=amd64`, `CGO_ENABLED=0`, and no Linux GUI build tags, then actually
executed on Windows 11. Each working directory matched its source package.
The comparable before/after runs both used the same interactive Windows
session and isolated test directories. The original base was rebuilt through
a Go overlay containing its original tracked source and hiding new files.

Counts below are **top-level tests**, not subtests; skipped subtests are listed
separately in [the machine-readable evidence](windows-native-followup-results.json).

| Package | Before pass/fail/skip | After pass/fail/skip |
|---|---:|---:|
| `cmd/app` | 10 / 2 / 2 | 12 / 0 / 2 |
| `cmd/telegram-mcp-bridge` | 1 / 1 / 0 | 2 / 0 / 0 |
| `internal/cli` | 27 / 3 / 2 | 30 / 0 / 2 |
| `internal/daemon` | 24 / 2 / 0 | 26 / 0 / 0 |
| `internal/httpserver` | 59 / 1 / 1 | 60 / 0 / 1 |
| `internal/mcp` | 191 / 1 / 2 | 196 / 0 / 1 |
| `internal/service` | 16 / 9 / 9 | 25 / 0 / 9 |
| **Total** | **328 / 19 / 16** | **351 / 0 / 15** |

The first isolated OpenSSH run had **327 pass, 20 fail, 16 skip**.
`TestWindowsFirstRunCredentialSetup` already passes on the unmodified base
when run interactively: that one failure was the session environment, not an
application defect. The other 19 failures remain in the comparable interactive
baseline. The final run exits zero in all seven packages, includes three new
Windows ACL negative tests, and executes one formerly skipped ASR test.

## Every observed failing test

“Fixture” means the application contract was correct and the test supplied an
unsupported backend, Unix path/mode, or shell executable. None of these fixes
relaxes production validation.

| Package / test | Classification | Resolution / native proof |
|---|---|---|
| `cmd/app` / `TestDesktopStartsWithPersistedCredential` | Fixture: Linux-only `file` backend | Use native Credential Manager; generate/reload the HTTP credential, start fake daemon, render panel and stop. |
| `cmd/app` / `TestDesktopFirstRunCredentialRetryAndQR` | Fixture: Linux-only `file` backend | Native keyring and isolated client directories; preserve invalid/retry, saved credential and QR assertions without connecting to Telegram. |
| `cmd/app` / `TestWindowsFirstRunCredentialSetup` | Environment: OpenSSH session 0 cannot use the interactive credential store | Base and fixed test both pass in session 1; test data now uses `t.TempDir`, with explicit namespace cleanup. |
| `cmd/telegram-mcp-bridge` / `TestBridgeLoadsPersistedTokenWithoutEnvironmentSecret` | Fixture: Linux-only `file` backend | Native credential persisted and reloaded without an environment token. |
| `internal/cli` / `TestConfigModeUsesEphemeralSessionCSRFAndExplicitConfirmation` | Fixture: Linux-only `file` backend | Real native credential/bootstrap; session CSRF and explicit confirmation assertions unchanged. |
| `internal/cli` / `TestInstallUsesAuthenticatedOpenExactlyOnce` | Fixture: Linux-only `file` backend | Native token; injected service runner and authenticated open exactly once. No real service installation. |
| `internal/cli` / `TestTranscriptionInstallEnglish` | Fixture: extensionless Unix shell files are not Windows executables | Native renamed test executables make installed tools discoverable; preserve offline installer and English-output assertions. |
| `internal/daemon` / `TestSaveAccountCredentialsFirstRun` | Fixture: Linux-only `file` backend | Native keyring; missing, malformed, persisted and conflicting credentials still tested. |
| `internal/daemon` / `TestProductionCredentialsPersistWithoutEnvironmentOrUnitSecrets` | Fixture: Linux-only `file` backend | Native persistence, environment removal, no session creation and conflicting input refusal still tested. |
| `internal/httpserver` / `TestMediaAPILocalTranscribeAndNativeExplicitOnly` | Fixture: `/bin/sh` fake ASR programs | Native subprocess fixtures exercise the unchanged production launcher, transcript/cache and explicit-native-only counters. |
| `internal/mcp` / `TestDownloadMediaTightensExistingRootPermissions` | **Real application defect:** Windows `Chmod` does not restrict NTFS access | Apply protected owner-only DACLs to root, per-chat directory, downloaded file and staging directory. The revised test first grants actual public access, then checks real ACLs on root, chat and file. It failed on the old implementation and passes with the fix. |
| `internal/service` / `TestServicePathsRejectInvalidConfiguration` | Fixture: `HOME` does not control Windows `os.UserHomeDir` | Isolate/change `USERPROFILE` too; retain invalid-home and injected-control-character refusals. |
| `internal/service` / `TestLaunchdUsesInjectedRunnerAndEscapesXML` | Fixture: `/opt/...` is not a Windows absolute path | Native volume prefix, same special-character escaping and injected command assertions. |
| `internal/service` / `TestLaunchdPropagatesCommandFailure` | Fixture: invalid positive binary path masked the intended failure | Native absolute path reaches the injected failure; no command failure is ignored. |
| `internal/service` / `TestSecretBackendValidation` | Fixture: invalid positive binary path masked backend validation | Native absolute path; original accepted/rejected backend table preserved. |
| `internal/service` / `TestLaunchdLifecycleHandlesAlreadyStoppedAndStarted` | Fixture: invalid positive binary/home path | Native absolute fixture and isolated `USERPROFILE`; lifecycle still uses only the injected runner. |
| `internal/service` / `TestManagerInstall` | Fixture: invalid binary path, then synthetic Windows POSIX modes | Portable absolute fixture; content/lifecycle assertions run everywhere. Exact Unix unit modes still asserted on Unix, never treated as Windows ACL evidence. |
| `internal/service` / `TestInstallSystemdQuoting` | Fixture: invalid positive binary path | Native volume prefix preserves spaces, quotes, backslashes, dollar and percent quoting coverage. |
| `internal/service` / `TestManagerInstallCommandErrors` | Fixture: invalid positive binary path | Reach each injected command error; retain unit for retry and redact command output. |
| `internal/service` / `TestSystemdConfigFallback` | Fixture: invalid binary path and uncontrolled Windows home | Native absolute fixture and isolated `USERPROFILE`; retain empty/relative XDG fallback. |

## Security boundaries and additional proof

The Windows media helper opens without following reparse points, pins the
object against replacement, validates the owner and object type, and checks
an engine-returned file against its open identity before changing its DACL.
It does not take ownership, rewrite ancestors, or propagate permissions into
unrelated existing children. Native tests verify refusal of reparse
directories, refusal of a replaced file even with a deletion-sharing engine
handle, and no recursive modification of unrelated children.
The Unix implementation retains its original `chmod(0700/0600)` behavior and
root-anchored staging operation. Service/data/session ACL policies are unchanged.

`TestMediaLocalTranscriptionFakeBinaryAndFTS`, previously skipped on Windows,
now also passes with native helper subprocesses and real transcript/FTS storage.
These helpers produce synthetic transcripts: this proves integration, not
real whisper.cpp recognition quality or real model/audio downloads.

An additional native panel test,
`TestIntegrationOneClickDesktopKeepsABackup`, passes outside the seven-package
counts. It writes only inside isolated `APPDATA`, preserves the pre-existing
synthetic client's other entries and theme, and verifies a byte-identical
backup. The real classic/Store Claude Desktop config inventory and SHA-256
were unchanged across the final run.

## Remaining skips (intentional and pre-existing)

No new whole-test skip was added. The final 15 top-level skips are enumerated
in the JSON evidence:

- `cmd/app`: opt-in GUI/browser test; Unix SIGTERM subprocess contract (its help
  checks run on Windows before the existing skip).
- `internal/cli`: POSIX umask fixture; Unix SIGTERM executable contract.
- `internal/httpserver`: hostile Unix umask/runtime-mode contract. Two POSIX-mode
  subtests remain skipped because native ACL tests cover the Windows policy;
  the explicitly blocked real-account L01 subtest also remains skipped.
- `internal/mcp`: migration snapshot awaiting its implementation, unrelated to
  this Windows follow-up.
- `internal/service`: five Linux encrypted-file-backend tests, one Unix-mode
  filesystem test, two real systemd package-API tests and the Linux-only offline
  systemd parser. Injected systemd/launchd render/lifecycle tests now run and
  pass on Windows; they are not evidence of Windows SCM support.

The `file` secrets backend remains Linux-only. Windows uses Credential Manager;
macOS uses its native keyring. No insecure fallback was added. The existing
unsupported Windows headless service-manager result remains deliberate; this
work does not implement or claim Windows service installation.

## Isolation and cleanup

Before execution, redirect `HOME`, `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`,
`TEMP`, `TMP`, `TMPDIR` and XDG homes. Run only synthetic engines, ephemeral
listeners, injected service commands and unique credential namespaces derived
from `t.TempDir`. Never run `cmd/telegram-poc` or use a real Telegram account.
The fixture helper registers credential deletion and verifies absence after
each native-keyring test, including test failure.

On this workstation the normal Temp ancestry contains a sandbox trustee with
mutation rights. Binaries/source remain in the requested disposable Temp
workspace, but test data uses a **new private sibling directory below the user
profile**, with only the owner, SYSTEM and Administrators permitted. Existing
Temp/profile ancestors are not rewritten and the application's ancestor
refusals remain enabled. Run credential tests through an interactive scheduled
task, not OpenSSH session 0.

Final native credential enumeration in session 1 found **zero** targets for the
test data root. After downloading evidence, remove the exact scheduled task,
verify no workspace executable is active, remove both disposable directories,
and verify their absence. Never delete or restore an unknown real client config.

Cleanup completed: both directories and the scheduled task are absent, no
workspace executable remains active, and no synthetic credential target
remains. The eight remotely executed binary SHA-256 values match the local
evidence manifest. Raw logs and scripts were retained only on the Linux
operator host, not on the Windows workstation.
