# Stage 4 — desktop, private packages and opt-in updates

## Scope and boundaries

Workstreams L, M and N reuse Bruno Orlandi's Wails app, packaging and updater
from foundation commit `e579f0a1ddade312dc4cb0f7e628e7bd7f6b19db`.
This delivery adds Telegram state presentation, native per-user autostart, strict
clean-snapshot builds, update integrity tests and the three owner-approved gaps
(first-use credentials, the CLI installer and remote panel access).

No real Telegram account was connected, no installed service was changed, no
release/tag was created, and nothing was published. The private repository,
retained upstream licenses, attribution and independent blue app glyph remain.
The separate site workstream owns `site/`; its release manifest determines the
unchanged download asset names.

## Implemented behavior and evidence

| Seam | Implementation / verification | Limits |
|---|---|---|
| Desktop window and tray | `cmd/app/app_test.go`, `cmd/app/testdata/gui_smoke.py`: native Linux fakeengine window, connected/disconnected/login-required menu states, close retains daemon, tray reopens panel, own glyph and Claude Desktop/Code setup retained. | Tested on isolated amd64 X11, including the real AppImage with extraction mode. Not every desktop shell/Wayland or native Windows/macOS acceptance. |
| Start at login | `internal/platform/autostart_test.go`: owned LaunchAgent, Windows Run entry and Linux desktop entry with escaped absolute executable paths; native Linux enable/disable is read back. | No system-wide install or administrator; actual logout/login cycles and Windows registry/native macOS acceptance remain to be checked. |
| First-use API credentials | `cmd/app/credentials_test.go`, `internal/daemon/credentials_onboarding_test.go`, `internal/panel/credentials_test.go`: missing credentials show a local form, numeric positive ID and exactly 32 hex hash, existing vault path, no echoed/logged values, no silent replacement, headless environment seeding retained. An opt-in native fixture (`TestCredentialSetupGUI`) was exercised with an empty synthetic vault, invalid retry and progression to fake QR. | No real API credentials or Telegram login. File-backed synthetic fixture is not native keychain UI acceptance on every OS. The form is only mounted in the trusted embedded webview, not on the network panel. |
| CLI installer | `python3 scripts/test-install.py`: 18 local-server and command-boundary tests cover all four OS/arch selections, a writable custom prefix, required minisign verification, the tracked production key/version pin, refusal of other keys and placeholders for network releases, missing/invalid/wrong-key/tampered signatures, transport flags, checksum/archive failures and unsupported architecture. | macOS selection is simulated via uname in Linux tests. Public GitHub downloads will return 404 while the repository is private. No token is requested or embedded; minisign must be installed. |
| Remote panel URL | `internal/cli/open_test.go`, `serve_test.go`: `open --print`, automatic Linux no-display fallback, possession warning, successful single-use redemption, expiry, and foreign Host/Origin refusal through the authenticated bootstrap endpoint. | Use an SSH local forward, never a public listener. The short-lived URL is a credential, not a persistent bearer token. |
| Source/package contract | `python3 scripts/test-release.py` (19 tests), `scripts/test-packaging.py` (9 tests): exact tag snapshot, dirty/untracked/ignored/overlay exclusion, private target guard, no overwrite, signed metadata/checksum manifest and embedded trust anchor; digest-bound signed Mac bundle import; packages include notices and bridge. | These contract tests do not replace actual package inspection or execution. |
| Update v1 to v2 | `TestReleaseServerReplacesAndRestartsExecutable`: real Go executable v1 checks an injected local HTTP fixture, sees v2 with zero payload downloads before explicit Apply, verifies the detached minisign signature before the signed SHA-256, replaces itself, exits, and the real detached helper runs v2 and writes its version marker. Signature/key/metadata/downgrade/host/HTTP negatives and checksum, staging, retry/state and concurrency negatives also pass. | Synthetic executable uses the AppImage installation seam. It is not a Wails settings-button upgrade of each installed native package. DEB updates are manual/package-manager controlled. Native macOS monitor/rollback-command and Windows PowerShell/NSIS recovery execution remain release gates; first-update and retained-installer exclusions apply. |
| Native update rehearsal | `internal/updater/devfeed_test.go` (development feed accepted only with `TELEGRAM_MCP_DEV=1`, refused in release builds, integrity rules unchanged) and `detach_windows_test.go` (monitor runs after its parent and its kill-on-close job are gone; fails with the old `DETACHED_PROCESS` flags). The real installed app was rehearsed with fakeengine and a disposable key on Windows 11 and on macOS 27 (arm64, Developer ID–signed, from a user folder). Both ran 0.9.0 → 0.9.1 from the Settings update routes, wrote the health marker and promoted it on the next start. A broken 0.9.2 then rolled back automatically to 0.9.1. | Rehearsal builds, not release artifacts: unsigned Windows installer, unnotarized bundle, no SmartScreen/Gatekeeper download path, no real Telegram account. The panel routes were driven through a bootstrap browser session, not mouse clicks. |

The updater defaults to `bwoliveira/telegram-mcp-local`. Its local HTTP API
fixture is available to tests that inject an HTTP client and, in explicit
development mode only, to a real app pointed at a loopback or tailnet test feed
(see [Testing updates locally](#testing-updates-locally)). Production API,
manifest, signature and artifact requests require HTTPS and an allow-listed
GitHub host, including every redirect. It never downloads or installs on a
version check alone. macOS verifies the candidate bundle signature and current
signer's team, then atomically exchanges the installed and staged bundles with
`renameatx_np(RENAME_SWAP)`. AppImage retains the previous executable; macOS
retains the previous bundle. Their helpers monitor the candidate's versioned
health marker and restore and relaunch the previous installation if health is
not confirmed. Cleanup occurs only after a later startup verifies the matching
candidate identity and marker.

Windows verifies the same signed release manifest and SHA-256 before starting
setup. Its PowerShell monitor starts with `CREATE_NO_WINDOW` and
`CREATE_BREAKAWAY_FROM_JOB`, not `DETACHED_PROCESS`: Windows PowerShell 5.1
exits without running its script under `DETACHED_PROCESS`, so the monitor never
ran. Breakaway also keeps a host's kill-on-close job, such as an OpenSSH
session, from ending it with the app. The installer renames the running app
aside and retries replacing it for 30 seconds, failing instead of leaving the
old program under the new version. Its monitor can restore the previous installation only when a matching
last-known-good installer has already been retained. Installer slots are
versioned and immutable so a second update cannot overwrite the healthy rollback
installer. The current setup remains unsigned at the Authenticode layer; no
Windows directory-fsync guarantee is claimed.

Automatic recovery does not cover the first update initiated by the old updater
on AppImage, macOS, or Windows, or a manual Windows installation without a
retained installer. See [automatic update recovery limits](security/release-signing.md#automatic-update-recovery-limits).
Native macOS and Windows update/recovery acceptance remains a release gate.
Release builders now require an
`checksums.txt.minisig` over the checksums, version, source commit and UTC date.
They use legacy raw-message `Ed` signatures for Go Ed25519 compatibility. This
change does not retroactively authenticate older rehearsal artifacts. No
production key exists in this tree; production builds also refuse the installer
until it pins the exact release version, preventing a valid old release from
being replayed through the mutable latest-release path. See the release-day
owner ceremony and rotation/revocation/rollback policy in
[release signing](security/release-signing.md).
The CLI install flow requires minisign; missing tools/keys/signatures must not
fall back to unauthenticated hashes.

## Build commands

The scripts are artifact builders, not publishers. Normal release mode takes a
strict owner-approved `X.Y.Z` and requires the existing `vX.Y.Z` tag. It clones
that exact commit into a clean temporary checkout. It does **not** create tags,
change repository visibility, or invoke `gh release create`.

Both release entry points require minisign on PATH and these explicit inputs
before the commands below (also for a private `0.0.0` build):

```sh
export TELEGRAM_MCP_MINISIGN_SECRET_KEY=/absolute/path/outside/repository/release.key
export TELEGRAM_MCP_MINISIGN_PUBLIC_KEY="$(sed -n '2p' /absolute/path/release.pub)"
```

Use only a disposable test key for rehearsals; the production key is generated
by the owner on release day and stored in the owner's 1Password Personal vault.
The values above are path examples, not working production credentials. The
release wrappers merge the public key into desktop linker flags without editing
the clean build snapshot and pass only the public key into Linux containers.

```sh
# On Linux, with Docker (ARM binfmt/QEMU), Go, NSIS, zip and Python 3.11+:
scripts/release.sh X.Y.Z --mac-artifact /path/to/mac-output-bundle
# Default output: dist/vX.Y.Z. Existing output directories are never overwritten.

# Private rehearsal, without creating a version or tag:
scripts/release.sh 0.0.0 --ref FULL_COMMIT --output /your/new/test-output
# LXC hosts without Docker bridge/NAT may use RELEASE_DOCKER_NETWORK=host.
```

On amd64, the Linux Docker ARM target uses an architecture-native Go compiler
with the native arm64 GTK/WebKit toolchain; QEMU runs the final linker/tools.
Cross-architecture AppImages are extracted with `scripts/extract-appimage.py`,
not by trying to execute their foreign ELF runtime. Linux packages are built in
`build/linux/Dockerfile` on Ubuntu 24.04, the oldest supported distribution (see
[Supported Linux distributions](#supported-linux-distributions)).
`python3 scripts/test-artifacts.py DIR` verifies real
payload architectures, notices, launcher/wrapped binary, every manifest hash,
and the required per-artifact CycloneDX/notices pair. Builders extract each
final installer, AppImage, DEB, DMG, and CLI archive before inventorying its Go
payloads; pre-package CI binaries are not accepted as release evidence. The
signed checksum manifest covers those inventory files.

The AppImage and DEB inventories also cover every non-Go file of the extracted
payload (`scripts/native_inventory.py`, see
[build input security](security/build-inputs.md#linux-native-payload-inventory)):
each GTK/WebKitGTK library, helper, module, loader, schema and copied copyright
file is mapped through `dpkg-query -S` and `md5sums` to its Debian binary
package, with version, architecture, source package/version, complete copyright
text and every referenced `/usr/share/common-licenses/*` text in the notices and
SBOM. If payload bytes differ from the package, the pinned linuxdeploy `patchelf`
RUNPATH operation and fixed WebKit helper-path rewrite must reproduce them
exactly; a same-architecture ELF substitution fails. The build fails on an
unmapped file, missing legal text or unreviewed GPL/AGPL marker. The AppImage
embeds the manifest and notices in `usr/share/doc/telegram-mcp/`, including the
written offer for corresponding source and LGPL replacement/repacking
instructions.
`scripts/build-linux.sh` honors `OUT=DIR` to write outside `build/bin`, e.g.
`VERSION=0.0.0 OUT=/tmp/linux-out scripts/build-linux.sh` for an isolated
rehearsal. The native mapping requires the build root to be the Debian/Ubuntu
system whose packages were copied (the release container), because ownership and
copyright files are read from its dpkg database.

### Supported Linux distributions

| Package | Supported | Requires from the system |
|---|---|---|
| `telegram-mcp_<version>_<arch>.deb` | Ubuntu 24.04 or newer, Debian 13 or newer (amd64, arm64) | `libgtk-3-0` and `libwebkit2gtk-4.1-0` through package dependencies (`t64` variants satisfy them). |
| `Telegram-MCP-<arch>.AppImage` | Ubuntu 24.04 or newer, Debian 13 or newer, and other glibc distributions at least as new, with a graphical desktop | The libraries below; everything else is bundled. |

Like other AppImages, ours bundles GTK, WebKitGTK and their dependencies but
deliberately leaves the C runtime, graphics drivers, display protocol, fonts
and audio to the computer, together with the libraries on linuxdeploy's exclude
list. A bundled library copied from a newer distribution can need a symbol that
the older system library lacks (a Debian 13 `libgcrypt.so.20` needs
`gpgrt_add_post_log_func@GPG_ERROR_1.0`, absent from Ubuntu 24.04's
`libgpg-error.so.0`, and the app then fails to start). The AppImage is therefore
built on the oldest supported distribution, and `scripts/check-appimage-abi.py`
fails the build unless every versioned symbol each bundled ELF file needs from
a library that is not bundled is exported by that library in the Ubuntu 24.04
build root (`scripts/test-appimage-abi.py` covers it with compiled fixtures).
Libraries the AppImage expects from the system (static `DT_NEEDED` entries of
bundled files, newest symbol version required):

| Area | Libraries (Ubuntu/Debian package) |
|---|---|
| C/C++ runtime | `libc.so.6`, `libm.so.6`, `libresolv.so.2`, `ld-linux-*.so` (`libc6`, glibc 2.38 symbols at most); `libstdc++.so.6`, `libgcc_s.so.1` (`libstdc++6`, `libgcc-s1`) |
| Graphics and display | `libEGL.so.1`, `libGL.so.1` (`libegl1`, `libgl1`), `libgbm.so.1` (`libgbm1`), `libdrm.so.2` (`libdrm2`), `libX11.so.6`, `libX11-xcb.so.1` (`libx11-6`, `libx11-xcb1`), `libxcb.so.1` (`libxcb1`), `libwayland-client.so.0` (`libwayland-client0`) |
| Text and fonts | `libfontconfig.so.1` (`libfontconfig1`), `libfreetype.so.6` (`libfreetype6`), `libharfbuzz.so.0` (`libharfbuzz0b`), `libfribidi.so.0` (`libfribidi0`) |
| Other | `libgpg-error.so.0` (`libgpg-error0`), `libz.so.1` (`zlib1g`), `libexpat.so.1` (`libexpat1`), `libgmp.so.10` (`libgmp10`), `libcom_err.so.2` (`libcom-err2`) |

A graphical desktop (`ubuntu-desktop-minimal`, `task-gnome-desktop` or similar)
installs all of them. The reverse direction is not checked: GIO may try to load
the system's own optional modules into the bundled GLib 2.80. On Debian 13 the
gvfs module needs a newer GLib, so GIO prints `Failed to load module:
.../gio/modules/libgvfsdbus.so` and skips it; the app does not use gvfs. The build log prints the exact list after the check. The
AppImage still needs FUSE 2 to mount itself; without it, run it with
`--appimage-extract-and-run` (or `APPIMAGE_EXTRACT_AND_RUN=1`).

### Command line in the desktop packages

The desktop executable is also the command line. With a subcommand (`serve`,
`bridge`, `service`, `open`, `config`, `auth`, `transcription`, `version`,
`help`) it runs that command headless, exactly as the standalone `telegram-mcp`
CLI does, without opening a window or requiring a display; with no argument (or
only `--hidden`) it opens the app. So the DEB's `/usr/bin/telegram-mcp serve`
and `Telegram-MCP-x86_64.AppImage serve` run the daemon on a server without a
desktop, and `service install` registers the package's own executable. The
same applies to `telegram-mcp.exe` on Windows and the binary inside the macOS
app; on Windows the GUI-subsystem executable prints only when its output is
redirected or captured, so terminal use there remains the standalone CLI.

### macOS import contract

`--mac-artifact` now takes the **entire `release-mac.sh` output directory**, not
an individual DMG. Build both sides from the same tag (or the same full commit
with `0.0.0 --ref ...` for a private rehearsal). A bare path, old rehearsal DMG,
or manually renamed unsigned image is not a qualified release import.

The Mac wrapper emits `mac-build.json` only after the signed build and native
image, Developer ID signature, stapler and Gatekeeper checks succeed. It records
schema version, version, source ref/commit/tree, build mode, final stapled DMG
SHA-256, signature authority/team and notarization submission/status. The bundle
retains `notarization.json` and `mac-{build,image-verify,signature,stapler,gatekeeper}.log`;
each evidence file's SHA-256 is bound into the manifest. The unsigned private
path emits an explicitly unverified manifest and cannot be imported, even into
a `0.0.0` combined build.

Before any platform build, Linux requires matching version/ref/commit/tree/mode,
verified Developer ID and Accepted/stapled notarization, complete digest-matching
evidence, a UDIF disk-image trailer and a matching DMG digest. It copies the
validated bytes, manifest and evidence into the combined output; `checksums.txt`
covers all of them without including itself. There is no arbitrary
source-mismatch override. Rebuild older private rehearsals from one common
snapshot rather than combining their historically different commits.

**Trust boundary:** this is a local builder attestation with integrity checks,
not a detached cryptographic signature of the manifest. Transfer the complete
bundle from the trusted Mac builder through an authenticated channel. Linux
cannot independently run Apple's trust/notarization service or prove that a
hostile author did not forge an entire bundle; native verification remains a
Mac gate. No signing credentials are exported in the bundle.

`python3 scripts/test-release.py` exercises real release entry points against
explicit offline Git/compiler/OS test doubles: plain text, wrong source/version/
digest, missing/tampered verification evidence, unsigned/unverified imports,
failed native gates, successful matching import and retained checksums. These
are orchestration regressions, not a new native Mac build or signing acceptance.

### macOS signing and notarization

Signed builds require a Developer ID Application identity and a notarization
keychain profile available to the build user in an unlocked keychain. Provision
those credentials separately; do not put certificates, passwords or account
identifiers in this repository. The default script fails clearly instead of
falling back to ad-hoc signing. `--unsigned` is restricted to private `0.0.0`
tests and produces `Telegram-MCP-unsigned.dmg`.

Run the rehearsal locally on an authorized Mac checkout. The identity and
profile below are placeholders, not credentials or evidence of a completed build:

```sh
set -eu
cd /path/to/telegram-mcp-local
commit=$(git rev-parse HEAD)
output="$HOME/build-output/telegram-mcp-$commit"
SIGN_IDENTITY='Developer ID Application: Example Developer (TEAMID)' \
  NOTARY_PROFILE='example-notary-profile' \
  scripts/release-mac.sh 0.0.0 --ref "$commit" --output "$output"
dmg="$output/Telegram-MCP.dmg"
xcrun stapler validate "$dmg"
spctl -a -vv -t open --context context:primary-signature "$dmg"
```

For a later authorized tag, replace `0.0.0 --ref "$commit" --output ...` with
`X.Y.Z`. For ad-hoc local tests add `--unsigned` and omit `SIGN_IDENTITY`.
`build-macos.sh` builds both arm64 and x86_64 slices, signs inside-out with
`--options runtime --timestamp`, submits with
`xcrun notarytool submit --keychain-profile "$NOTARY_PROFILE" --wait`, staples,
and verifies with stapler and Gatekeeper. Output defaults to `dist/vX.Y.Z`.

## Testing updates locally

A real installed app can rehearse a complete update and rollback against a
local stand-in for GitHub Releases, signed with a disposable minisign key, before
anything is published. Nothing here is for production: release builds,
installers and service units never set these variables.

| Variable | Value | Notes |
|---|---|---|
| `TELEGRAM_MCP_DEV` | exactly `1` | Without it every override below is refused: the app logs a `WARNING`, update checks stay off and Settings shows the refusal. |
| `TELEGRAM_MCP_UPDATES_API` | `http://127.0.0.1:<port>` | Replaces `https://api.github.com`. http or https on a literal loopback address (or `localhost`) or a Tailscale address in `100.64.0.0/10`; no other host, credentials, query or fragment. Every asset URL and redirect must satisfy the same rule. |
| `TELEGRAM_MCP_UPDATES_PUBLIC_KEY` | the base64 line of a test `minisign.pub` | Replaces the embedded trust anchor; accepted only with the feed override. |
| `TELEGRAM_MCP_ENGINE` | `fake` | Synthetic account; no real Telegram login. |
| `TELEGRAM_MCP_PORT` | a free port, for example `18766` | Keeps the rehearsal off the default port. |

An accepted override logs `WARNING: DEVELOPMENT MODE: TELEGRAM_MCP_UPDATES_API
replaces GitHub Releases ...` at every start. A binary whose
`ReleasePublicKey` was stamped by the release scripts refuses the overrides
even with `TELEGRAM_MCP_DEV=1`. Signature, signed checksum and version, the
authenticated-version high-water mark, staging, health marker and rollback are
exactly the production rules; only the feed location and the key differ.

**Disposable key and feed.** On the build host:

```sh
minisign -G -W -p test.pub -s test.key          # unencrypted, rehearsal only
VERSION=0.9.0 scripts/build-windows.sh           # or build-macos.sh; keep each output
```

The feed is a directory served over HTTP with
`repos/bwoliveira/telegram-mcp-local/releases/latest` (the GitHub release JSON:
`tag_name` `vX.Y.Z`, and `assets` with `name`, `browser_download_url` and
`size` for the platform asset, `checksums.txt` and `checksums.txt.minisig`) and
the files it names. `checksums.txt` follows the release format: `# version:`,
`# source_commit:` (40 hex), `# date:` (UTC RFC 3339), then
`<sha256>  <asset>`; sign it with
`minisign -S -l -s test.key -m checksums.txt`. Serve the directory on the test
machine itself, for example `python3 -I -m http.server 18765 --bind 127.0.0.1
--directory feed`. Publishing the next version is rewriting `latest`. A machine
that cannot reach the build host can use `ssh -R
127.0.0.1:18765:127.0.0.1:18765`, but serving locally is simpler. Asset names
are `Telegram-MCP-Setup.exe` (Windows) and `Telegram-MCP.dmg` (macOS). The
broken candidate for the rollback step must still be a signed, well-formed
package. Use an app executable that exits without serving the panel, so it
never writes its health marker.

**Windows.** Install the old version with `Telegram-MCP-Setup.exe /S` (into
`%LOCALAPPDATA%\Programs\telegram-mcp`). Then start
`telegram-mcp.exe` **in the interactive desktop session** with the variables
above. An OpenSSH session cannot: it is logon session 0, Credential Manager
refuses to store there, and the app's HTTP credential lives in Credential
Manager. Use a scheduled task with `/IT` or the user's own shell. Data
stays in `%LOCALAPPDATA%\Telegram MCP`; updates, installer slots,
`last-known-good-setup.json`, `pending-update.json` and health markers are in
its `updates` folder. Do not point `TELEGRAM_MCP_DATA` at a folder under
`%TEMP%`: when the temporary folder grants other trustees (sandbox or
service groups), the app refuses it as not private. Click
*Procurar atualização* and *Instalar e reiniciar* in Settings, or call the same
panel routes (`/api/atualizacao/verificar`, `/api/atualizacao/instalar`)
with a browser session from `panel-bootstrap.key`. The monitor
`powershell.exe ... monitor-update.ps1` must survive the app's exit. After the
new version is healthy, restart it once to promote its installer. Publish the
broken version and install it; about two minutes later the monitor reinstalls
the retained installer. To clean up, run `uninstall.exe /S` and remove
`%LOCALAPPDATA%\Telegram MCP`. Delete the `telegram-mcp/<hash>:session`
credential with `cmdkey /delete`, from the interactive session.

**macOS.** Run each bundle from a folder you own, never `/Applications`, for
example `~/tmp/tgmcp-upd-qa/Applications/Telegram MCP.app`. The folder must be
writable, because the update swaps the bundle in place. Sign every bundle with
the same Developer ID team (`codesign --force --options runtime --sign
"Developer ID Application: ..."`, inside-out). The updater refuses a candidate
whose signature fails `codesign --verify --deep --strict` or whose team differs.
Notarization is not required for the swap. `TELEGRAM_MCP_SECRETS=file` exists
only on Linux. On macOS the HTTP credential goes to the login Keychain, so start
the app inside the logged-in user's GUI session, for example
`launchctl asuser $(id -u USER) sudo -u USER env ... ".../Contents/MacOS/telegram-mcp"`;
a plain SSH shell has no unlocked Keychain. Data defaults to
`~/Library/Application Support/Telegram MCP`, with updates under its
`updates` folder. `TELEGRAM_MCP_DATA` may point at a private (0700) folder
instead. The rollback copy is `Telegram MCP.app.old` beside the bundle until
the next healthy start. To clean up, quit the app, delete the folder and
remove the `telegram-mcp/<hash>` items from the login Keychain with
`security delete-generic-password -s telegram-mcp/<hash>`.

## Artifact acceptance checklist

Private rehearsal archives, signing identities, submission identifiers, hashes
and machine-specific logs are not included here. A rehearsal labeled `0.0.0`
is not a release or permission to publish. For each authorized build:

- Retain the complete Mac output bundle for the import checks described above;
  transfer it only through an authenticated channel.
- Run `python3 scripts/test-artifacts.py DIR` against the combined output.
- Publish the Debian source packages listed in each AppImage's native notices
  beside the release, or keep them for at least three years to honor the
  embedded written offer.
  Inspect the actual CLI archives, DEBs, AppImages and Windows installer for
  notices, executable architectures and expected payloads.
- Check `checksums.txt` against the final artifacts and provenance/evidence files;
  the checksum file must not include itself.
- Exercise CLI version output with isolated homes and native app behavior on
  each supported platform. Cross-compilation or QEMU output is not native UX QA.
- On macOS, require successful signature, notarization, stapler and Gatekeeper
  gates for the exact final DMG. Preserve that evidence privately; do not replace
  a failed gate with a historical result from a different build.

## Verification and remaining acceptance

Remaining before a public distribution claim: native Windows/macOS app UX and
login-cycle QA, native keychain UX, Wayland/ARM desktop acceptance and real
packaged-GUI update clicks on each OS. These limits do not block private build
artifacts, but are not proven by the synthetic executable update fixture. Real
Telegram acceptance and Stage 6 publication remain separate owner-controlled
gates. CI status and PR URL are read back from GitHub in the delivery report,
not predicted here.
