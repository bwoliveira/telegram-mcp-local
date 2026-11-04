# Build input security

Release builds treat every network response as untrusted until it matches an
immutable digest. Versions, URLs and SHA-256 values live in
`scripts/sidecars/pins.sh`; release jobs must not override them with environment
variables. Downloads use HTTPS, a temporary file and a fail-closed digest check
before extraction or execution. No script streams `curl` directly into an
extractor.

## Pinned inputs

| Input | Version/release | SHA-256 |
|---|---|---|
| whisper.cpp source | v1.9.5 | `ff1a9053feb509ff9d7729703355541ae9690073a6b1c40eb692c962e0dc1720` |
| whisper.cpp Windows CPU asset | b5454 (`whisper-bin-x64.zip`) | `6ba69e3482d7826214f90a6a9c84ca07782aec1e1d0c6a7c30c994fd5d816ccb` |
| whisper.cpp Windows CUDA 12.4 asset | b5454 | `afef0b881c500958921c3f5523b50e59ee2ec9b6f5cbd25b324c51ed308a957a` |
| NVIDIA CUDA 12.4 EULA and third-party notices | 12.4.0 PDF | `6ada441ae5a45a4a3d51ade8850fa2229fc6dd95de0c9da6e2cdd7a46701b844` |
| FFmpeg source | 7.1.5 | `de668509caf9e35e3cd162473441fdb29538c6d96ed080292b3cf9e6fc5d558f` |
| FFmpeg detached signature | 7.1.5 | `7ce4b9d56e3ef0cd4c3a9c0b2c8a034ac91e6c4c011c1f10b05a8aa7292fca35` |
| Microsoft WebView2 Evergreen bootstrapper | immutable delivery object | `cf2f2445fef0c5c14e372045f84998771eeb8bb01d59be4afd00692fa160082e` |
| WebView2 Microsoft leaf signing certificate (DER) | bootstrapper signer | `cb97e8e85e8e9321fb2646e9574efd17669b3b0581d24262ac7c8a227433a244` |
| Whisper large-v3-turbo q5_0 model | runtime pin | `394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2` |
| linuxdeploy AppImage (x86_64 / aarch64) | `continuous`, pinned bytes | `df908b19f1ed1cdafa79508d415aa12d6c56ce4d56d4149aa8c09286718706e5` / `f38e4d7cebf20d8073ec4e00e0a167ac146cd8585fe2f8fe82df27d74f2bd8e1` |
| AppImageKit `AppRun` (x86_64 / aarch64) | `continuous`, pinned bytes | `f30140a43a0a59e46db21bdefdf749b9e9f2c6946e92afabbacf98b8ae73fb4f` / `072f17c0895a85c490282fe5395c5007e5fc75da727e553b3b8fb680feb11578` |
| AppImage type-2 runtime (x86_64 / aarch64) | commit `fedfad51acc2860b2916a1eef32ba4a500ff31ef` | `617aa77314d57c8b8e4fae9c3f0b35af7b30d5529cb69e98f64f92510ed98a46` / `9a72a744d221576c9347ec074243acc5c3c7568896b12714e0e82e9f0bf47900` |

The three AppImage tools are also recorded, with provenance, static
components and pinned legal texts, in `scripts/linux-native/pinned-inputs.json`;
the native inventory re-verifies the packed bytes against those records (see
"Linux native payload inventory" below). `appimagetool` is given the pinned
runtime with `--runtime-file`, so it never downloads one at pack time.

whisper.cpp v1.9.5 and Windows build b5454 are the same upstream commit,
`d1be6fde11ac6e0407606b4e42fe72d34add8037`. The Windows asset digest covers
`whisper-cli.exe` and every DLL copied from that archive.

FFmpeg has two independent checks. The source digest is verified first, then the
detached signature is verified with the vendored release key. The build checks
the imported primary-key fingerprint exactly:

```text
FCF986EA15E6E293A5644F10B4322F04D67658D8
```

The key fingerprint is published by FFmpeg at <https://ffmpeg.org/download.html>.
The vendored key avoids trusting a live keyserver during a release build.

The WebView2 URL pins the bootstrapper bytes, not the Evergreen runtime selected
by Microsoft when the bootstrapper runs. Windows builds additionally require a
valid Microsoft Authenticode signature when PowerShell is available. Linux and
macOS builds use `osslsigncode` to require the pinned Microsoft leaf certificate
hash and exact signer subject; a platform trust store may not contain Microsoft's
code-signing roots, so full chain validation remains mandatory on the Windows
release runner. Without either verifier the script reports that only the
mandatory SHA-256 check was performed.

## FFmpeg license boundary

`scripts/sidecars/build-ffmpeg.sh` starts from `--disable-everything` and never
enables external codecs. It rejects `--enable-gpl` and `--enable-nonfree` in the
requested arguments and then requires `CONFIG_GPL=0` and `CONFIG_NONFREE=0` in
FFmpeg's generated `config.h`, including cross-builds that cannot execute on the
host. Native builds also validate `ffmpeg -buildconf`. GPL and AGPL code are
forbidden by project policy.

Each packaged sidecar contains:

- the exact `ffmpeg-7.1.5-source.tar.xz` and detached signature;
- LGPL 2.1 and LGPL 3 license texts from that source tree;
- `ffmpeg-build-config.txt` and provenance metadata;
- whisper.cpp's MIT license and provenance metadata;
- a CycloneDX 1.6 SBOM bound to the archive and every packaged file.

This makes the corresponding source and rebuild recipe travel with the binary.
The Windows CUDA variant also carries a notice linking NVIDIA's CUDA runtime
redistribution terms. Legal review remains required before public distribution.

### `sidecars-2` release assets

The embedded runtime manifest accepts only `sidecars-2` and points at the new
public project, `github.com/bwoliveira/telegram-mcp-local`. It pins the reviewed
universal macOS, Linux amd64/arm64 and Windows CPU/CUDA bytes. The complete
five-entry manifest is generated by `scripts/sidecars/manifest.py sidecars-2 ...`,
which checks the final archive's payload hashes, SBOM and complete legal notices.
The five archives, each archive's `.cdx.json`
and `.THIRD-PARTY-NOTICES.txt`, and `manifest.json` must be uploaded together
under the immutable `sidecars-2` tag on launch day. Until that coordinated upload
is complete, transcription downloads from a build carrying this manifest are
intentionally unavailable; do not fall back to the old upstream `sidecars-1`
assets.

The runtime manifest override `TELEGRAM_MCP_SIDECARS=<path>` exists only for
testing unpublished archives. It is accepted only when `TELEGRAM_MCP_DEV=1` is
also set (exactly `1`), and every accepted load logs a `WARNING: DEVELOPMENT
MODE` line naming the file, because the override replaces the pinned archive
digests and therefore the programs that will run. Without the development flag
the override is refused, not silently ignored: the refusal is logged and no
sidecar package is offered until the variable is unset. Release builds,
installers and service units must never set either variable.

Reviewed macOS/Linux/Windows bytes and `checksums.txt` are retained outside Git in
`<private-build-artifact-directory>/sidecars-2/`. All five packages were built
twice. Archive metadata and ordering are deterministic, but independent native
builds are not byte-for-byte identical because the pinned whisper.cpp/FFmpeg
build systems embed temporary build-directory data/build identifiers. The
manifest therefore pins one reviewed build of each asset; changing any bytes
requires regenerating the manifest and repeating the review.

#### macOS universal build evidence

Two independent source/build/output directories on macOS 27 arm64 used the
unchanged pinned builders, Apple clang 21.0.0 and CMake 4.4.5:

| Build | `transcription_darwin_universal.tar.gz` SHA-256 |
|---|---|
| 1 (selected and embedded) | `56c63a1c5ff7615dd25dcc0ac179ab234f95bae46cdf28e83cfa1af956018280` |
| 2 (comparison) | `bafd6992e587adc9e7a42706bf2e2bd9d7150465bad7d6fe4fce8640853cce16` |

Only `whisper-cli` and `ffmpeg` differ between the two extracted archives; all
legal/provenance/source files and archive member metadata are identical.
Inspection found absolute random temporary source paths in whisper.cpp's
assertion strings and FFmpeg's embedded `--prefix`, plus different Mach-O
`LC_UUID` values in both slices of each executable. Ad-hoc signatures bind those
differing executable bytes. This is native-build nondeterminism, not archive
ordering/timestamps: repacking build 1's extracted payload is byte-identical to
build 1. The second build is evidence, not interchangeable release bytes.

Build recipe in each independent directory:

1. Run `scripts/sidecars/build-whisper.sh <built-dir>`; its macOS configuration
   produces arm64+x86_64 with embedded Metal support.
2. Run `ARCH=arm64 scripts/sidecars/build-ffmpeg.sh <arm64-dir>` and
   `ARCH=x86_64 scripts/sidecars/build-ffmpeg.sh <x86_64-dir>`. Combine the two
   executables using `lipo -create`; copy the arm64 compliance files and retain
   both complete configure-argument lists in `ffmpeg-build-config.txt` (comment
   headers distinguish the slices). Require the generated license proofs from
   both builds to match: `CONFIG_GPL=0`, `CONFIG_NONFREE=0`.
3. Run `scripts/sidecars/package.sh darwin universal <built-dir> <out-dir>` with
   `SIGN_IDENTITY` unset. This builder explicitly supports ad-hoc signing; no
   Developer ID identity, Keychain credentials or notarization is required or
   used. Both extracted executables passed `codesign --verify --strict` and
   `lipo -verify_arch` separately for arm64 and x86_64 in both builds.
4. Verify the final archive's CycloneDX 1.6 root/payload hashes and complete MIT
   and LGPL notices using the manifest generator. Each package contains exact
   FFmpeg source, detached signature, configuration and both LGPL texts. Neither
   FFmpeg slice enables GPL/nonfree or external codecs. `otool -L` shows only
   system libraries/frameworks, not build-host Homebrew libraries.

Both builds' arm64 and x86_64 slices executed `whisper-cli --help` and
`ffmpeg -version` successfully (x86_64 via Rosetta). No model inference was
performed. Native runtime verification used Go 1.26.10 darwin/arm64, downloaded
from the official Go archive after checking its official SHA-256
`0b5183492292ada170f9b561a834863808dd02edba599e4ad0e89899330020b3`.
With `TELEGRAM_MCP_SIDECAR_FIXTURE` pointing at selected build 1,
`go test -count=1 -v ./internal/sidecar/...` passed natively: the local HTTP test
installed the exact embedded bytes with macOS signature checks enabled, then
rejected a mismatching digest. A separate non-skipped macOS test rejected
`sidecars-1`. The older synthetic unsigned-install test still skips on macOS;
it is not the release-byte proof. Native result:

```text
--- PASS: TestEmbeddedManifestIncludesAllReleasePlatforms (0.00s)
--- PASS: TestManifestRejectsPreviousVersionOnEveryPlatform (0.00s)
--- PASS: TestEmbeddedManifestAcceptsReleaseBytes (0.28s)
PASS
ok  github.com/bwoliveira/telegram-mcp-local/internal/sidecar  0.556s
```

Build logs, both packages, slice smoke-test logs and native test output are
retained in `<private-build-artifact-directory>/mac-sidecar-evidence/` alongside
the selected release set. The compiler emitted upstream availability warnings
for APIs newer than the builders' macOS 11 deployment target; verification on
macOS 27 does not establish compatibility with macOS 11 or every older release.
No public assets have been uploaded.

## Go notices and SBOMs

`scripts/build-inventory.py` reads `go version -m -json` from each built binary,
then copies legal texts from the exact module versions in `GOMODCACHE`. It also
requires the complete `$GOROOT/LICENSE` from a toolchain whose `GOVERSION`
exactly matches the binary and records the Go standard library in every notice
and SBOM. It does not use `go.mod` as a proxy for linked code. Missing source,
legal text, or a mismatched toolchain fails the build. Outputs are deterministic
and include per-binary notices, a consolidated notice, and CycloneDX 1.6 SBOMs.
Artifact inventories are generated from executables extracted back out of each
final installer/archive, then bind that final artifact's SHA-256. Use `--check`
to detect stale or missing generated files.

The curated `THIRD_PARTY_NOTICES.md` remains the preamble for adapted-source
credits, per-file exceptions and non-Go runtime components. The optional model
is recorded there because it is downloaded at runtime rather than linked into a
Go binary.

## Linux native payload inventory

The AppImage carries far more than Go: linuxdeploy and its GTK plugin copy
GTK, WebKitGTK (including the `WebKitWebProcess`/`WebKitNetworkProcess`
helpers and the injected bundle), GLib, GStreamer and their dependencies, plus
GTK input-method/print modules, gdk-pixbuf loaders, GSettings schemas and
generated caches. `scripts/native_inventory.py` (stdlib only, driven by
`build-inventory.py --native-root`) inventories the payload extracted back out
of the final artifact, not the build tree. Every regular file and symlink must
match exactly one rule, otherwise the build fails before writing:

| Class | Rule |
|---|---|
| `go-payload` | the two Go executables, byte-equal to the `--binary` inputs |
| `project-asset` | icon and desktop entries, byte-checked against `build/linux/` (only the documented `Exec=`/`X-AppImage-Version=` transforms) |
| `generated-inventory` | packaged Go notices, which must equal the generated Go notices |
| `native-inventory` | the embedded manifest and notices themselves (verified by regeneration) |
| `pinned-input` | AppImageKit `AppRun.wrapped`, SHA-256 pinned |
| `generated` | linuxdeploy's `AppRun` shell launcher and GTK hook (marker-checked), and caches written by Debian-packaged tools (`glib-compile-schemas`, `gtk-query-immodules-3.0`, `gdk-pixbuf-query-loaders`) |
| `symlink` | must resolve, inside the payload, to a covered regular file |
| `debian-package` | AppImage only: a host origin under the Debian library paths, owned by exactly one installed package (`dpkg-query -S`), whose installed bytes match that package's `md5sums`; the payload must be identical or exactly reproduced by the allowed pinned linuxdeploy/WebKit transformations |

Each Debian package contributes binary name, version, architecture, source
package and source version (`dpkg-query -W`), and the complete
`/usr/share/doc/<package>/copyright` (also md5sums-verified). Every referenced
`/usr/share/common-licenses/*` file is independently ownership/md5sums-verified,
then embedded in both the notices and that package's SBOM license entries; a
missing, changed or empty common-license file fails closed. A differing payload
file is accepted only when replaying the exact permitted build operations on the
md5sums-verified package bytes reproduces its SHA-256: the SHA-256-pinned
`patchelf` extracted from the pinned linuxdeploy AppImage sets `RUNPATH` to
`$ORIGIN`, followed, only for WebKitGTK, by the fixed-width helper-path rewrite.
The manifest and SBOM record those transformations plus both the shipped and
original SHA-256 values. Arbitrary same-architecture ELF substitutions fail.
Any ELF for the wrong architecture, an ELF posing as an asset, a missing or empty
copyright file, or an unmapped file is an error. The DEB profile has no Debian
rule at all: it may only contain the Go payloads, project assets, packaged
notices and the `usr/bin` symlink, so any embedded native library would fail the
build.

Outputs: `usr/share/doc/telegram-mcp/native-payload.json` and
`NATIVE-THIRD-PARTY-NOTICES.txt` inside the AppImage (embedded before packing,
then regenerated from the extracted AppImage and required to be byte-identical),
a `# Native payload inventory` section in the artifact notices (written offer,
LGPL replacement instructions, per-file list, every copyright text, pinned-input
legal texts), and in the artifact CycloneDX SBOM one `file` component per payload
path (SHA-256, mode, class, owner, origin) with a dependency edge to its owning
Debian package, pinned input, Go payload or symlink target, one `pkg:deb`
component per package carrying the full copyright text and source retrieval,
and the pinned inputs with their statically linked components. The AppImage
runtime prefix (bytes before the SquashFS) must equal the pinned type-2 runtime
outside the four sections `appimagetool` fills (`.digest_md5`, `.upd_info`,
`.sha256_sig`, `.sig_key`); its actual prefix hash is recorded. `--check`
regenerates everything and fails, without writing, on any difference (for
example a removed SBOM component or a removed embedded-manifest entry).

### GPL/AGPL detection

The project forbids GPL/AGPL code. Each copyright file is classified
conservatively; LGPL, "Lesser"/"Library" GPL and licence names that only mention
GPL inside an exception are not markers:

- `no-gpl-marker`: no GPL/AGPL token or "GNU (Affero) General Public License"
  phrase at all;
- `gpl-only-as-alternative-or-definition`: machine-readable DEP-5 where every
  `License:` expression is satisfiable without GPL/AGPL (for example
  `LGPL-3+ or GPL-2+`), with GPL appearing only in standalone licence texts;
- `gpl-build-or-test-files-only`: the only GPL/AGPL paragraphs cover
  `debian/`, tests, docs, `m4`, `build-aux` or autotools helper files;
- `review-required`: everything else, including all free-form copyright files
  that mention GPL.

`review-required` packages fail the build unless
`scripts/linux-native/license-review.json` has a decision pinned to the exact
copyright SHA-256 with a written reason citing the paragraphs that cover the
shipped files. A `forbidden` decision always fails (for example
`gir1.2-freedesktop`). A changed copyright file invalidates its review.

`scripts/prune-appdir.py` removes what the app never loads before inventory:
the build host's GTK settings modules, GObject-introspection typelibs, the TIFF
pixbuf loader (libtiff pulls in GPL-2+ `libjbig0`), GSettings schemas of
unrelated packages, and the libraries that become unreferenced. It drops the
matching loader-cache stanzas and recompiles the schema cache.

### LGPL dynamic linking, replacement and corresponding source

GTK, WebKitGTK, GLib, GStreamer and the other LGPL libraries are separate shared
objects in `usr/lib` of the AppImage, loaded dynamically; none is linked into the
Go executables. A user may replace any of them:

1. `./Telegram-MCP-<arch>.AppImage --appimage-extract` (or
   `scripts/extract-appimage.py`), producing `squashfs-root/`.
2. Replace `squashfs-root/usr/lib/<library>.so.N` with a build of the same soname
   (keep the `RUNPATH`-relative layout; the executables use `$ORIGIN/../lib`).
3. Run `squashfs-root/AppRun`, or repack with `appimagetool squashfs-root`
   using the same type-2 runtime.

The exact corresponding source of each Debian package is the recorded source
package version: `apt-get source <source>=<version>` with matching `deb-src`
entries, or `https://snapshot.debian.org/package/<source>/<version>/` (Ubuntu:
`https://launchpad.net/ubuntu/+source/<source>/<version>`). The AppImage runtime
statically links libfuse 3.15.0 (LGPL-2.1, patched by the runtime's
`patches/libfuse/mount.c.diff`); its complete build scripts and pinned source
hashes are in type-2-runtime commit `fedfad51acc2860b2916a1eef32ba4a500ff31ef`.

A release must either attach those exact source packages beside the AppImage or
rely on the written offer embedded in every AppImage and its notices: for at
least three years after the release was last distributed, the maintainer
provides the complete corresponding source of every LGPL (or otherwise
source-obligated) component on request, for no more than the cost of physically
performing the distribution. Retain the source packages for every published
version for that period, because snapshot URLs are not a substitute for that
obligation.

## Required CI gates

The integration CI runs these standalone regression steps without granting
network credentials to untrusted code:

```bash
python3 scripts/test-build-inventory.py -v
python3 scripts/test-native-inventory.py -v
python3 scripts/test-sidecar-security.py -v
python3 scripts/test-packaging.py -v

# CI builds the actual Linux app/CLI/bridge, generates inventories and runs
# --check against those same binaries. Release jobs must additionally supply
# packaged native inputs and bind the final artifact, as shown below.
python3 scripts/build-inventory.py --check \
  --binary <app> --binary <bridge> --component <packaged-native-input> \
  --artifact <artifact> --output-dir <inventory-dir>
```

The sidecar build job must run both build scripts in an empty directory, retain
the full logs, inspect `ffmpeg-build-config.txt` for forbidden flags, package
each supported target, and retain every `.cdx.json`. On a Windows runner,
`Get-AuthenticodeSignature` must report `Valid` and a Microsoft Corporation
signer for the pinned WebView2 bootstrapper. CI should also reject any occurrence
of `--enable-gpl` or `--enable-nonfree` in generated FFmpeg configuration.

When network delivery is unreliable, `BUILD_INPUT_CACHE` may point to a local
content-addressed cache. Each cached object must be named by its 64-character
SHA-256; the same verification runs before use, so the cache does not become a
trust bypass.

When updating any pin, independently download the object, compute SHA-256,
verify upstream release metadata/signatures, update the table and scripts in the
same review, and rerun the isolated sidecar builds. A version-only update is not
acceptable.
