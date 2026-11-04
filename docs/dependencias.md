# Dependencies

> **Historical reference:** This document describes the historical upstream messaging project, not the current Telegram MCP implementation. Product-specific predecessor names and identifiers are replaced with descriptive placeholders. Identifiers and paths using `predecessor-cli` or `predecessor-protocol-library` are schematic, not literal. Versions, behavior, and paths below may no longer apply.

What the project needs to work, from most critical to least critical, and what happens when each component changes or is missing.

## Overview

```text
                       ┌──────── Predecessor MCP app (Wails, one process) ───────────────┐
Claude Desktop ─stdio─► <predecessor-bridge> ─┐                                           │
Claude Code ───HTTP───┤ MCP + dashboard ─ supervisor ──► predecessor-cli ──► predecessor-protocol-library ──► predecessor messaging service │
                       │      │             (inside app) │                               │
                       │      │                          ├─ session.db (session)        │
                       │      │                          └─ predecessor-cli.db   (messages)       │
                       │      └── state.db (transcripts, clients)  config.json (port)    │
                       │      └── transcription ──► ffmpeg ──► whisper-cli ──► model     │
                       │           (downloaded when transcription is enabled)           │
                       └─────────────────────────────────────────────────────────────────┘
     window: WKWebView (macOS) · WebView2 (Windows) · WebKitGTK 4.1 (Linux)
     command line: same core as a service, launchd (macOS) · systemd --user (Linux)
```

## Critical: nothing works without them

### predecessor-cli

| | |
|---|---|
| What it is | A historical messaging command-line client, MIT license; Bruno Orlandi upstream source credit: dependency authored by OpenClaw, <https://github.com/openclaw/wacli> |
| Version in use | 0.25.1, bundled in the app, pinned in `build/predecessor-cli.env` with the SHA-256 for each system |
| Purpose | Pairs the computer as a linked device, maintains the session, receives and stores messages, sends messages, and downloads media |

This is the most important dependency. The project depends on several parts of it, not only its commands:

| What the daemon depends on | Where | If it changes |
|---|---|---|
| Command output from `--json` (`{success, data, error}`) | All tools | A tool fails to read the response |
| `--events` events from `sync` and `auth` (`connected`, `qr_code`, `pair_code`, `history_sync`, `progress`…) | Supervisor, pairing, dashboard | The QR code or sync state stops appearing |
| The `sync --follow` delegation socket (`.send.sock` on Unix; named pipe `\\.\pipe\predecessor-cli-send-<hash>` on Windows since predecessor-cli 0.23), which receives outgoing sends and conversation state changes | Supervisor | Sends time out instead of going through; archiving and marking as read begin to pause sync |
| The `sync` webhook (`--webhook`, `--webhook-secret`, `--webhook-events`, `--webhook-allow-private`) and the format of its posts | Webhook relay (`internal/webhook`) | Webhooks stop receiving events; without the flags, sync runs without them and the API reports `"available": false` |
| The `LOCK` file, with `pid=` | Recovery after a crash | An orphaned sync is not stopped automatically |
| The `predecessor-cli.db` schema (`messages`, `chats`, `contacts`, `groups` tables…), read in read-only mode | Previews, names, audio context, health | The affected query fails with an SQL error |
| The predecessor-protocol-library `session.db` schema (`predecessor-protocol-library_contacts`, `predecessor-protocol-library_lid_map`, `predecessor-protocol-library_device`), read only for names and the user's own number and LID | People’s names, the user's own name, mentions | Numbers appear instead of names again; mentions are no longer found |

In the app, predecessor-cli is pinned. Each app version bundles a tested version, downloaded from official releases and checked against their published SHA-256. It changes only with a new app version. The predecessor-cli documentation warns that the database schema can change between versions. Before changing the version in `build/predecessor-cli.env`, run the tests and check the Status tab and conversation preview. The command line uses predecessor-cli installed alongside it by `install.sh`, or the Homebrew version.

### predecessor-protocol-library

| | |
|---|---|
| What it is | The historical Web-protocol library used internally by predecessor-cli; Bruno Orlandi upstream source credit: dependency authored by Tulir, <https://github.com/tulir/whatsmeow> |
| Risk | It is unofficial. When predecessor messaging service changes the protocol, the connection can stop until a new predecessor-protocol-library version is released, followed by a new predecessor-cli version |

The project does not communicate with predecessor-protocol-library directly. Fixes arrive through a predecessor-cli update.

### predecessor messaging service

The account must be active on the phone. predecessor messaging service disconnects a linked device that remains offline for weeks, and the user must scan the QR code again. The phone also needs an internet connection to send older history and resend expired media.

### What starts the app or service at login

| | How |
|---|---|
| App on macOS 13+ | System login item (SMAppService); on macOS 11 and 12, a LaunchAgent |
| App on Windows | `<predecessor-app-id>` value in `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` |
| App on Linux | `~/.config/autostart/` (XDG standard: GNOME, KDE, XFCE) |
| Command line on macOS | LaunchAgent `<predecessor-app-id>`, with `KeepAlive` |
| Command line on Linux | `systemd --user` unit `<predecessor-service>.service` |

### Each system's webview

| System | Webview | Notes |
|---|---|---|
| macOS 11+ | WKWebView | Included with the system |
| Windows 10 and 11 | WebView2 (Edge) | Included with Windows 11 and updated Windows 10; the installer installs it if missing |
| Linux | WebKitGTK 4.1 with GTK 3 | A `.deb` dependency; bundled in the AppImage |

## Transcription: needed only for transcription on the computer

| Component | Version | Source | Purpose |
|---|---|---|---|
| whisper.cpp (`whisper-cli`) | 1.9.2 | Built by the project (macOS, Linux) or the official build (Windows), published in a `sidecars-N` release | Runs the model |
| Minimal ffmpeg | 7.1.1 | Built by the project (`scripts/sidecars/build-ffmpeg.sh`) | Converts predecessor messaging service audio (Ogg Opus, AAC, MP3) to 16 kHz WAV |
| Model `ggml-large-v3-turbo-q5_0.bin` | 574,041,195 bytes | Hugging Face, `ggerganov/whisper.cpp`, checked by SHA-256 | Quantized Whisper large-v3-turbo |

None of these are included in the installer. The app downloads them when transcription is enabled and checks each SHA-256 (see [transcricao.md](transcricao.md)). Without them, the rest continues to work, and the `transcribe_audio` tool explains how to install them.

Risks: `whisper-cli` command-line options can change between versions, so the version is pinned and changes only with a new `sidecars-N`. The model is downloaded once and does not change.

## Optional

| Component | When used |
|---|---|
| `claude` (Claude Code) | The "Add to Claude Code" button runs `claude mcp add`/`remove` |
| Claude Desktop | The "Add to Claude Desktop" button edits `claude_desktop_config.json`, saving a copy first |
| Homebrew | Command line only: the installer uses it to install predecessor-cli and, when no release is published, Go |
| Go 1.26+ | Only for building from source |

## Go libraries

| Library | Purpose |
|---|---|
| `github.com/wailsapp/wails/v3` (beta.27, pinned) | The window, tray, single instance, notifications, and app login item |
| `golang.org/x/sys` | Job Objects, consoles, and the Windows registry |
| `github.com/godbus/dbus/v5` | Detects whether a tray is available on Linux |
| `modernc.org/sqlite` | Pure Go SQLite: reads `predecessor-cli.db` and `session.db` and maintains `state.db` |
| `github.com/skip2/go-qrcode` | Draws the pairing QR code in the dashboard |

The command line and bridge use neither cgo nor Wails. They are pure Go binaries that do not depend on system libraries. The app uses cgo on macOS and Linux for the system webview, but not on Windows.

## Where data is stored

In the app data directory (`~/Library/Application Support/<predecessor-app>` on macOS, `%LOCALAPPDATA%\<predecessor-app>` on Windows, `~/.local/share/<predecessor-app>` on Linux; for the command line, `~/.predecessor-cli` and `~/.<predecessor-app>`):

| Path | Owner | Contents | Typical size |
|---|---|---|---|
| `predecessor-cli/session.db` | predecessor-cli/predecessor-protocol-library | The linked device session and keys | Small |
| `predecessor-cli/predecessor-cli.db` | predecessor-cli | Messages, conversations, contacts, groups | ~130 MB for ~40,000 messages |
| `state.db` | App | Transcripts, connected clients, installation step, conversations marked as resolved or snoozed, webhooks | Less than 1 MB |
| `config.json` | App | Port, start with system, close = hide | Small |
| `models/` | App | The transcription model | 574 MB |
| `bin/` | App | `whisper-cli` and `ffmpeg`, with the SHA-256 of each | A few MB |
| `media/` | App | Audio and files downloaded for transcription or delivery | Grows with use; retention (`POST /api/media/retention`) deletes old files |
| `exports/` | App | `export_messages` exports in NDJSON | The size of the exported data |
| `updates/` | App | The downloaded new version until installation | The size of the installer |

The app never writes to `predecessor-cli.db` or `session.db`. The `media/` directory can be deleted at any time. If requested again, its contents are downloaded again if predecessor messaging service still has the file. Uninstalling the app does not delete the data directory. **Settings › Delete all data from this computer** deletes it after disconnecting the device from predecessor messaging service.
