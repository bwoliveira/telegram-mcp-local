# Development

> **Historical predecessor documentation, not Telegram instructions.** This page describes the predecessor product and its build, preview, test, and release workflows at the time. Product-specific names, identifiers, paths, and URLs have been replaced with schematic placeholders. The examples are not executable as written and do not establish current Telegram MCP behavior.

Go 1.26 or newer. The app uses cgo on macOS (Xcode) and Linux (GTK 3 and
WebKitGTK 4.1); the command-line tool and the bridge do not.

```sh
go test ./...
go build -o bin/<predecessor-cli> ./cmd/<predecessor-cli>         # command-line tool
go build -o bin/<predecessor-bridge> ./cmd/<predecessor-bridge>
```

## The programs

| Directory | What it is |
|---|---|
| `cmd/app` | the app (Wails v3): window, tray, lifecycle, migration, updates |
| `cmd/<predecessor-cli>` | the command-line tool: `serve`, `service install`, `bridge`… |
| `cmd/<predecessor-bridge>` | the stdio bridge that Claude Desktop starts, inside the app |
| `internal/daemon` | the core run by the app and command-line tool: supervisor, index, MCP, dashboard, HTTP |
| `internal/platform` | what varies by operating system: directories, processes (groups and signals; Job Object and `CTRL_BREAK` on Windows) |
| `internal/sidecar` | downloading and verifying the transcription programs |
| `internal/updater` | app updates |

## The app

```sh
scripts/build-macos.sh              # build/bin/<predecessor-app>.app and <predecessor-app>.dmg (universal)
ARCHS=arm64 scripts/build-macos.sh  # only for this Mac, faster
```

The `.dmg` opens with a background instructing the user to drag the app to
Applications (`build/darwin/dmg-settings.py`); it is built by `dmgbuild`, run
through `uv` (`brew install uv`).

Without `SIGN_IDENTITY`, the app is signed ad hoc and runs only on this Mac. With
`SIGN_IDENTITY="Developer ID Application: …"` it is signed, and with
`NOTARY_KEY`, `NOTARY_KEY_ID`, and `NOTARY_ISSUER` (an App Store Connect API
key), the `.dmg` is also notarized.

Windows and Linux builds can be produced on any machine with Docker:

```sh
# Windows: the NSIS installer, built without cgo
docker run --rm -v "$PWD:/src" -w /src golang:1.26 bash -c \
  'apt-get update -qq && apt-get install -y -qq nsis unzip && scripts/build-windows.sh'

# Linux: .deb and AppImage, on Ubuntu 24.04 (the oldest supported) from build/linux/Dockerfile,
# for the machine's architecture; building elsewhere fails the AppImage ABI check
docker build -t <predecessor-linux-build> build/linux
docker run --rm --privileged -v "$PWD:/src" -w /src <predecessor-linux-build> scripts/build-linux.sh
```

### Run the app with sample data

With `<PREDECESSOR_DATA>` pointing to another directory, the app runs separately
from the installed copy (including another open copy), without touching real data:

```sh
T=$(mktemp -d); mkdir -p $T/store $T/data
<UPSTREAM_STORE_DIR>=$T/store <upstream-cli> --json doctor >/dev/null
sqlite3 $T/store/<upstream-store>.db < scripts/preview/seed.sql
echo '{"port":47892,"autostart":false,"seen":["first-run"]}' > $T/data/config.json
<PREDECESSOR_DATA>=$T/data <UPSTREAM_STORE_DIR>=$T/store <UPSTREAM_BIN>=$PWD/scripts/preview/<fake-upstream-cli> \
  "build/bin/<predecessor-app>.app/Contents/MacOS/<predecessor-app>"
```

With the QR code on screen, `touch $T/store/SCAN` pretends the phone scanned it.
The `config.json` with `autostart: false` prevents the test build from being
registered to start with the system.

## Browser dashboard with sample data

```sh
scripts/preview.sh                    # first time: the QR code screen
PREVIEW_PAIRED=1 scripts/preview.sh   # already paired, with sample conversations
```

The dashboard starts at `http://127.0.0.1:47890` with a fake upstream CLI
(`scripts/preview/<fake-upstream-cli>`) that forwards reads to the real upstream
CLI, against a database populated by `scripts/preview/seed.sql`. Only things that
require a phone are simulated: pairing, sync, and history requests.

## Languages

The app speaks Brazilian Portuguese, Spanish, and English. Text is still written
in Portuguese in the code, and that text is the key: `{{t "…"}}` and
`{{tf "… %s …" .X}}` in templates, `i18n.T("…")` and `i18n.Tf("…", x)` in Go,
`t("…")` in dashboard scripts. `internal/i18n/locales/es.json` and `en.json`
map each text to its translation; anything without a translation appears in
Portuguese. A value stored for later (a list, a table label) gets `i18n.N("…")`
where it is written and is translated where it appears.

The language is selected using flag buttons during setup (the QR code screen and
the next one), with each language's name shown in the dashboard's language on
hover, or under *Settings › Language* in the app and command-line tool. It is
stored in `config.json` (`"language"`); an empty value follows the system. A
`config.json` without `"language"` comes from a version before languages were
introduced: users who upgrade remain in Portuguese, even if the computer is in
English, and only a new installation follows the system. For the command-line
tool, a data directory already in use without a `config.json` also stays in
Portuguese, while a new one gets its own on first run. Both go through
`POST /api/idioma`, and the page returns in the new language. In the command-line
tool, `<PREDECESSOR_LANG>` has the final say when the service starts.

The language applies only to the interface. What is sent to programs does not
change with it: tool descriptions and responses remain in English for the AI
(with screen names in Portuguese, as before), and webhook notifications,
including the test notification, carry messages, names, and values as usual.
`TestEventsDoNotFollowTheLanguage` (webhook),
`TestAnswersDoNotFollowTheLanguage` (mcp),
`TestTranscribeErrorsDoNotFollowTheLanguage` (localasr), and
`TestFallbackNamesDoNotFollowTheLanguage` (index) check this.

`go test ./internal/i18n` fails when new text is missing a translation in either
catalog, when a translation changes the order of `%s`/`%d`, or when a catalog
translates text no longer used by the code. `coverage_test.go`, in the same
package, looks for text missed by the catalog: words and attributes (`title`,
`aria-label`, `alt`, `placeholder`) in templates outside `{{t}}`, script text
outside `t()`, and Portuguese text in Go outside `i18n.T`/`Tf`/`N`. This is a
heuristic (an accent or a word found only in Portuguese keys); text that is the
same in every language goes in the test's own allowlists. To add another
language: a `locales/xx.json` and a line in `i18n.Languages`.

## Tests

The supervisor, pairing, orphan recovery, and core tests use a fake upstream CLI
written in Go (`internal/<upstream-cli>/testdata/<fake-upstream-cli>`), built at
the start of the tests, which mimics the store lock, delegation socket,
`sync --follow`, and `auth`. Because it is Go, it runs on all three operating
systems, and CI (`.github/workflows/ci.yml`) runs everything on macOS, Windows,
and Linux. Among other things, the tests cover:

- exclusive operations pause and restart sync, and concurrent sends wait;
- pairing proceeds from the QR code to a running sync, and hands history to sync;
- stopping the core leaves no upstream CLI process running;
- changing the port opens the new one before closing the old one, rejects an
  occupied port without bringing down the current one, and the environment
  variable overrides `config.json`;
- the bridge follows the app to the new port;
- every dashboard page renders inside the app;
- the updater rejects a file that does not match `checksums.txt` and, on macOS,
  an `.app` with a broken signature.

End-to-end transcription (downloading packages, verifying them, and
transcribing audio) runs only on request because it executes the real programs:

```sh
<PREDECESSOR_E2E_PACKAGES>=<directory with packages and a manifest.json> <PREDECESSOR_E2E_AUDIO>=voice.ogg \
  <PREDECESSOR_E2E_EXPECT>=<word> <PREDECESSOR_E2E_MODEL>=<already downloaded model> \
  go test -run TestInstallAndTranscribe ./internal/localasr
```

With `<PREDECESSOR_E2E_PACKAGES>=published`, it downloads the packages pinned in
`internal/sidecar/manifest.json` from the published release.

## Publishing

Every version is built and published manually on this Mac: GitHub Actions only
runs the tests. The Windows installer is not signed.

- **An app release:** with `CHANGELOG.md` written and the `vX.Y.Z` tag created
  (annotated), `scripts/release.sh X.Y.Z` builds the tag in a separate clone and
  puts the signed and notarized `.dmg`, Windows installer, AppImage and `.deb`
  (amd64 and arm64, in Docker), command-line tool for each operating system, and
  `checksums.txt` in `dist/vX.Y.Z/`. It requires `SIGN_IDENTITY`
  (`Developer ID Application: …`), `NOTARY_KEY` (the API key's `.p8` file for
  App Store Connect), `NOTARY_KEY_ID`, and `NOTARY_ISSUER`. Then:

  ```sh
  git push origin main vX.Y.Z
  gh release create vX.Y.Z --verify-tag --latest --title "<predecessor-product> X.Y.Z" \
    --notes-file <CHANGELOG entry plus installation notices> -- dist/vX.Y.Z/*
  ```

  and open `<predecessor-download-url>` to see the new version for all operating
  systems.
- **The transcription programs** change infrequently and are published in a
  separate `sidecars-N` release, not marked as latest (so the app updater does not
  mistake it for an app release). Each operating system builds with
  `scripts/sidecars/` (`build-whisper.sh` and `build-ffmpeg.sh`; on Mac,
  `cmake` is required):

  ```sh
  # macOS: universal whisper with Metal, ffmpeg for each architecture joined with lipo
  scripts/sidecars/build-whisper.sh out/mac
  ARCH=arm64 scripts/sidecars/build-ffmpeg.sh out/ff-arm64
  ARCH=x86_64 scripts/sidecars/build-ffmpeg.sh out/ff-x86_64
  lipo -create -output out/mac/ffmpeg out/ff-arm64/ffmpeg out/ff-x86_64/ffmpeg
  SIGN_IDENTITY="Developer ID Application: …" scripts/sidecars/package.sh darwin universal out/mac pkg

  # Linux, once per architecture (--platform linux/arm64 and linux/amd64)
  docker run --rm --platform linux/$ARCH -v "$PWD:/src" -w /src <predecessor-linux-build>:$ARCH bash -c \
    'scripts/sidecars/build-whisper.sh out/linux-$ARCH && scripts/sidecars/build-ffmpeg.sh out/linux-$ARCH && scripts/sidecars/package.sh linux $ARCH out/linux-$ARCH pkg'

  # Windows: ffmpeg cross-compiled with mingw; whisper comes from the official release
  docker run --rm -v "$PWD:/src" -w /src <predecessor-linux-build>:arm64 bash -c \
    'apt-get update -qq && apt-get install -y -qq mingw-w64 && scripts/sidecars/build-ffmpeg.sh out/win x86_64-w64-mingw32- &&
     scripts/sidecars/package.sh windows amd64 out/win pkg && scripts/sidecars/package.sh windows amd64 out/win pkg cuda'

  scripts/sidecars/manifest.py sidecars-N pkg   # pins the packages in internal/sidecar/manifest.json
  gh release create sidecars-N --latest=false --title "Transcription (sidecars-N)" --notes "…" pkg/*
  ```

  Before publishing, `<PREDECESSOR_E2E_PACKAGES>=pkg` tests installation and
  transcription from those packages; afterward,
  `<PREDECESSOR_E2E_PACKAGES>=published` tests from the release.
- **The upstream CLI:** its version and sha256 hashes are in
  `build/<upstream-cli>.env`. To change the version, change that file, run the
  tests, and publish a new app release.
- **The icons** are generated from `build/icons/*.svg` with `scripts/icons.sh`.

## Historical source links

Preserved from the upstream design record for provenance only. These are not
current Telegram installation instructions, release artifacts, or supported URLs.

- Bruno Orlandi upstream source credit: <https://whatsapp-mcp.brorlandi.xyz/download>.
