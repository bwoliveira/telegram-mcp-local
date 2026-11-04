# Native Windows test portability

Follow-up: [the seven remaining failing packages](windows-native-followup.md)
now have a comparable native before/after run and per-test classification.
The original inventory below remains historical evidence for PR #61.

## Scope and baseline

The investigation starts from `origin/pkg-upd-integration` at
`1ee86e1a867a32e2d27674533c0b1c4121fb8c70` (PR #60). It does not change the
changelog, publish an artifact, connect to Telegram, or start the installed
service. PR #60 is an integration prerequisite, not a claim that these changes
are already present on `main`.

Tests were cross-compiled using Go 1.26.10 with `GOOS=windows GOARCH=amd64
CGO_ENABLED=0 GOFLAGS=`, then executed natively on
a Windows 11 host over SSH. Cross-compilation alone is not counted as execution.

## Root causes and fixes

| Failure | Classification | Fix |
| --- | --- | --- |
| `TestStatsRegistrationValidatesAndReadsBack` | Application defect: Windows reports synthetic POSIX mode bits, so requiring directory `0700` and file `0600` rejects valid exports. | Validate real Windows DACLs using the existing data-access policy; preserve exact Unix modes, regular-file, symlink, root, size and metadata checks. |
| `TestStatsReservedExportsSurviveRestartAndExpire` | Same application defect in reservation. | Use the same platform-aware validation for reservation and completion. Both interrupted filenames remain subject to retention. |
| `TestStatsReservationCommitAndAbort` | Same application defect. | Validate private files without pretending that Windows `Chmod` sets a DACL. |
| `TestStatsLocalTimeCalendarAndDefaultLimit` | Test dependency: `unknown time zone America/New_York`, **not** `engine: invalid request`. | Embed `time/tzdata` in the test binary. Keep the real IANA DST and UTC/local month-boundary assertions, restore `time.Local`, and do not replace the zone with a fixed offset or skip the test. |
| `pocstore.TestPrivateFilesReopenTombstonesAndForensicDeletion` | Invalid fixture: `?` is forbidden in a Windows filename. | Keep `escaped?#.db` on Unix and use `escaped #.db` on Windows. The Windows fixture still exercises URI escaping; reopen, tombstone and forensic-deletion checks remain active. POSIX-mode assertions remain Unix-only. |
| Profile-photo reads in `gotdengine` and the managed daemon | Application defect: the root walker treated `C:` as an `os.Root` child and failed with `statat C:: path escapes from parent`. | Start at the supplied path's volume root and walk only its remaining components. Retain handle-relative traversal and symlink rejection. |

Read-only SQL statistics already worked on Windows; the production permission
bug affected the export path, including its MCP entry point, rather than all
statistics calculations or the entire statistics page.

Additional fixture fixes retain their original contracts:

- Shared public-permission fixtures use an actual Everyone-read DACL on Windows
  and loose POSIX modes on Unix, then restore the original permissions. Both
  store registration and MCP export tests still reject public data.
- Download, profile-photo, backup and QR assertions check actual platform access
  rules instead of comparing synthetic Windows modes to `0600`.
- The Unix FIFO test is build-constrained; cancellation tests still run on Windows.
- The English-literal scanner uses the package working directory, not the build
  machine's embedded `runtime.Caller` path.
- Sidecar archive fixtures contain `.exe` names on Windows and compare the quoted
  manifest path exactly as logged, including escaped backslashes.
- Panel fixtures isolate `USERPROFILE`, `APPDATA` and `LOCALAPPDATA`, not just
  `HOME`. They must never read or overwrite a real client configuration.

## Native before/after evidence

The original binaries were rerun beneath the same private test workspace used
for the fixed binaries, so the POSIX-mode failure is not explained away by a
change in fixture ACLs. Relevant original output:

```text
store exit=1
    stats_registration_test.go:35: engine: invalid request
--- FAIL: TestStatsRegistrationValidatesAndReadsBack
    stats_reservation_test.go:35: engine: invalid request
--- FAIL: TestStatsReservedExportsSurviveRestartAndExpire
    stats_reservation_test.go:87: engine: invalid request
--- FAIL: TestStatsReservationCommitAndAbort
    stats_test.go:111: unknown time zone America/New_York
--- FAIL: TestStatsLocalTimeCalendarAndDefaultLimit
pocstore exit=1
--- FAIL: TestPrivateFilesReopenTombstonesAndForensicDeletion
```

The last failure reported the Windows invalid-filename error for `escaped?#.db`.
Native output after the fixes:

```text
--- PASS: TestStatsRegistrationValidatesAndReadsBack
--- PASS: TestStatsReservedExportsSurviveRestartAndExpire
--- PASS: TestStatsReservationCommitAndAbort
--- PASS: TestStatsLocalTimeCalendarAndDefaultLimit
PASS
--- PASS: TestPrivateFilesReopenTombstonesAndForensicDeletion
PASS
internal/store exit=0
internal/engine/gotdengine/pocstore exit=0
internal/engine/gotdengine exit=0
internal/engine/fake exit=0
internal/panel exit=0
internal/sidecar exit=0
```

Full package executions, not only the targeted tests:

| Package | Before: pass / fail / skip | After: pass / fail / skip |
| --- | --- | --- |
| `internal/store` | 185 / 4 / 1 | 189 / 0 / 1 |
| `internal/engine/gotdengine/pocstore` | 14 / 1 / 0 | 15 / 0 / 0 |
| `internal/engine/gotdengine` | Failed private-mode and profile-photo assertions | 319 / 0 / 0 |
| `internal/panel` | Failed isolation and backup-mode assertions | 115 / 0 / 7 |
| `internal/sidecar` | Failed executable-name and quoted-path fixtures | 6 / 0 / 1 |

Counts are top-level Go tests; nested subtests are not counted again. Browser and
opt-in performance skips are not represented as executed browser verification.

## Complete native sweep and remaining limitations

The sweep compiled every package separately. It executed 27 Windows test
binaries: 20 package suites passed and 7 retained pre-existing failures listed
below. `cmd/telegram-mcp` has no tests. The new `internal/testutil` package also
cross-compiles with no test files. `cmd/telegram-poc` cannot cross-compile because
its experimental CLI directly uses Unix `Flock`, termios and polling APIs; it
was not silently omitted or presented as a passing Windows package.

| Remaining failing package | Cause / boundary |
| --- | --- |
| `cmd/app` | Shared fixtures explicitly force the Linux-only encrypted-file secrets backend. The native first-run fixture additionally depends on a usable Credential Manager. A separate read-only diagnostic outside the test Temp tree confirmed `PrepareData` succeeds but Credential Manager reads return `secrets: keyring unavailable` in this SSH session. |
| `cmd/telegram-mcp-bridge` | The persisted-token fixture explicitly requests the Linux-only `file` secrets backend, which Windows rejects with `secrets: invalid configuration or key`. |
| `internal/cli` | Mode/install fixtures likewise force `file`. The transcription fixture installs Unix-named sidecar programs rather than Windows `.exe` programs and fails at the programs-download stage before reaching its model-download expectation. |
| `internal/daemon` | Credential onboarding fixtures use `file` beneath the machine's Temp ancestry. The Windows data policy rejects that ancestry before opening secrets. The managed-gotd all-capabilities fixture now passes, including profile photos and export. |
| `internal/httpserver` | Contract fixtures use the same Linux-file credential setup/Temp ancestry and are refused before their intended assertions. The transcription fixture uses a Unix shell executable, which is not a native Windows transcription runner. |
| `internal/mcp` | The remaining failure is `TestDownloadMediaTightensExistingRootPermissions`, a separate POSIX `Chmod` assertion (`0777` versus `0700`) in the media-download contract. The statistics/export tests pass, including real public-DACL rejection. This investigation does not claim that media-directory ACL tightening has been implemented. |
| `internal/service` | Systemd/launchd generators are tested with Unix absolute paths that Windows `filepath.IsAbs` rejects, and default-backend assertions expect Linux `file` instead of Windows `keyring`. Data preparation under the SSH test Temp tree is also intentionally refused because an ancestor grants Modify/DeleteChild to sandbox trustees. No existing user-folder ACL was weakened to make these tests pass. |

The test workspace alone was given a protected DACL for the current user, SYSTEM
and Administrators. Its existing Temp ancestors were not modified. `HOME`,
`USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `TEMP` and `TMP` were isolated for the
final native sweep. The affected service/environment failures are documented,
not converted into blanket skips or fake passing results.

## Isolation incident during baseline discovery

The original panel fixture only changed `HOME`. During the baseline sweep it
wrote synthetic Claude Desktop configuration and a synthetic backup in the real
Windows `APPDATA` location. The fixture writes its initial content directly, so
that backup is **not evidence of the path's pre-test contents**. Both files were
identified by their exact synthetic structure and run timestamp and removed;
no claim is made that an unknown previous configuration was recovered. The
fixture now redirects the Windows-specific environment variables as well.

Native binaries, source copies, scripts and temporary databases are removed from
the Windows test workspace after evidence collection. Logs remain local to the
investigation, outside version control; only this redacted summary is committed.
