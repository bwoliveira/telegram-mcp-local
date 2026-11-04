# How it works

> **Historical document:** This describes the predecessor application and its messaging platform, not the current Telegram implementation. Architecture, commands, paths, settings, and capabilities below are historical and are not instructions for the current application. Predecessor-specific names and tokens are replaced with neutral placeholders.

```
Predecessor MCP (the app: one process)
├── window ──────────── dashboard, served in memory, without network access
├── menu bar / system tray icon
├── MCP at 127.0.0.1:47821 ◄── Claude Code (HTTP)
│                            ◄── <predecessor-bridge> (stdio) ◄── Claude Desktop / Cowork
├── supervisor ──► <messaging-cli> sync --follow   (owns session and lock)
│        ├─ sends through the sync delegation socket
│        └─ each new message ──► relay at 127.0.0.1:<random port> ──► webhooks
├── reads ───► <messaging-cli> --read-only --json + <messaging-store>.db (read-only)
└── transcription on request ──► ffmpeg ──► whisper-cli
```

The command-line version (`<predecessor-cli> serve`) runs the same core as a system service, without a window or tray icon.

- **A single daemon owns the session.** The messaging CLI permits only one process to hold the store lock. The daemon keeps `<messaging-cli> sync --follow` running, and all clients talk to it. No client starts a second messaging session.
- **Sends** (text, file, location, poll, reaction, edit, and “typing”) and chat state changes (mark as read, archive, pin, and mute) go to the running sync process without interrupting it. An older messaging CLI that cannot accept these changes over the socket falls back to the pause below.
- **Operations requiring the lock** (history, message deletion, forwarding, group administration, number checks, and profile photos) pause sync for a few seconds, run, and restart sync. On reconnection, the messaging service delivers what arrived during that interval. Sends made during the pause wait in a queue rather than fail.
- **Triage, statistics, and export** query `<messaging-store>.db`. The app keeps “handled” and “snoozed” markers in `state.db`, along with transcriptions. None of that reaches the messaging service.
- **Webhooks.** Sync delivers each new message to an app-owned relay at a loopback address with a random port and a new secret for each run. The app forwards it to each configured webhook, with a queue, retries, and automatic disabling ([api.md](api.md#webhooks)).
- **Reads** use `<messaging-cli> --read-only` and a read-only SQLite connection, which the messaging CLI documents as safe during sync. Nothing writes to `<messaging-store>.db`.
- **Names** come from these sources, in order: the group name, nickname, phone contacts (read-only from the messaging session), names stored in messages, and profile name. If none is available, the number appears.
- **Claude Desktop** starts local servers only over stdio. It therefore runs the `<predecessor-bridge>` bundled with the app (`<predecessor-cli> bridge` on the command line), which forwards each message to the same process. The bridge reads the port from the app’s `config.json` and reads it again when the app does not respond. Changing the port therefore requires no changes in Claude Desktop. Cowork uses Claude Desktop’s servers.

## The app

- **One process.** The core (`internal/daemon`) runs inside the app. Closing the window hides the app in the menu bar. **Quit** stops sync carefully, allowing up to 20 seconds for the messaging CLI to close the database, and MCP stops responding. The same happens when the computer shuts down or the user session ends.
- **Single instance.** Opening the app again only brings up the window of the instance already running.
- **The messaging CLI is bundled with the app** at a fixed, tested version (0.25.1 at the time). It changes only when a new app version is released.
- **Start with the system.** On macOS 13 or later, the app uses a login item (SMAppService), shown in *System Settings › General › Login Items*. Earlier macOS versions use a LaunchAgent. Windows uses a value in `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`. Linux uses a file in `~/.config/autostart`. On all systems, the app starts in the tray only.
- **Linux without a tray.** Stock GNOME does not show tray icons without the AppIndicator extension. Without it, closing the window only minimizes it, so the app never runs without a way to return to it.
- **Updates.** Twice a day, the app checks GitHub for the latest release. During installation, it downloads the system-specific file and checks its sha256 against the release’s `checksums.txt`. On macOS, it also checks that the signature belongs to the same developer. It then replaces the app and restarts into the new version. The package manager updates the `.deb`; the app only notifies the user and links to the release page.

## Pairing and history

The dashboard runs `<messaging-cli> auth` and shows a QR code or an 8-character code to enter on the phone. After the phone connects the device, it starts sending history. For a large account, this takes much longer than half an hour. Sync receives the history in the background while you already use Claude.

To go further back, the dashboard and the `sync_history` tool ask the phone for messages older than the oldest message already on the computer for each chat. The phone must have internet access, and each request goes back another segment.

## Audio transcription

Audio is transcribed on the computer with whisper.cpp and the large-v3-turbo model, using the chat as context, and only on request. It uses the Mac GPU, an NVIDIA GPU on Windows, or the processor. The complete path from request to Claude’s review is in [transcricao.md](transcricao.md).

## Dependencies

Critical and optional dependencies, versions in use, and data locations are in [dependencias.md](dependencias.md).

## Security

- MCP listens only on `127.0.0.1`.
- The app window loads the dashboard from memory without network access and opens any external link in the system browser.
- It rejects a non-loopback `Host` (DNS rebinding protection) and any `Origin` other than the server itself. Even another `localhost` tab on a different port cannot call the tools.
- Dashboard actions (connecting the messaging service, editing Claude’s configuration, and changing the port) accept calls only from the page itself. Network calls are checked using `Sec-Fetch-Site` and `Origin` headers. In the app, only the window can reach the in-memory dashboard.
- With `<PREDECESSOR_MCP_TOKEN>` set, every call requires `Authorization: Bearer ***`. This also closes the endpoint to other programs on the computer.
- Third parties write message content. Tool results tell the model to treat that content as data, never as instructions.

## Configuration

The app stores the settings read first and by the bridge in `config.json` in the data folder: the port, whether to start with the system, and whether closing the window hides or quits the app. Everything else (transcriptions, clients, and installation stage) is in `state.db`. The **Settings** page changes all of them.

| Data folder | |
|---|---|
| macOS | `~/Library/Application Support/<predecessor-app>` (log in `~/Library/Logs/<predecessor-app>`) |
| Windows | `%LOCALAPPDATA%\<predecessor-app>` (log in `logs\`) |
| Linux | `~/.local/share/<predecessor-app>` (log in `~/.local/state/<predecessor-app>`) |

Environment variables apply to the app and the command line, and override `config.json`:

| Variable | Default |
|---|---|
| `<PREDECESSOR_MCP_PORT>` | `47821` (in the app, the port on the Settings page) |
| `<PREDECESSOR_MCP_TOKEN>` | empty (no token) |
| `<MESSAGING_CLI_BIN>` | the messaging CLI bundled with the app; on the command line, the copy installed alongside it or in PATH |
| `<MESSAGING_STORE_DIR>` | `<data folder>/<messaging-store>`; on the command line, the messaging CLI default (`~/.<messaging-store>`) |
| `<PREDECESSOR_MCP_DATA>` | the data folder above; on the command line, `~/.<predecessor-app>` |

Command line: `<predecessor-cli> serve | bridge | service install | service uninstall | service stop | service start | open | config | transcription install | version`.
Service logs: `~/Library/Logs/<predecessor-app>.log` (macOS) or `journalctl --user -u <predecessor-app>` (Linux).

## Tools

The dashboard’s **Functions** tab lists tools directly from the server. Compared with the hosted predecessor MCP (Bruno Orlandi upstream source credit: <https://github.com/BrOrlandi/whatsapp-mcp>):

| Difference | Why |
|---|---|
| `send_contact` does not exist | The messaging CLI does not send contact cards (vCard). |
| `sync_history` has no `before`; it gained `rounds` | The messaging CLI always starts from the oldest message in each chat; `rounds` goes back more times in the same call. |
| `download_media` returns `path` | The file is already on disk, and a client with file access reads it directly. |
| `health` | A verdict (ok, warn, or fail) on the daemon, pairing, sync, and incoming messages; the same report responds at `GET /health`, with 503 when something fails. |

## Limitations of running locally

- **The index grows only while the daemon runs.** When the computer is off, the messaging service queues messages for the device and delivers them upon reconnection. Delivery is not guaranteed for long periods, and a device that is inactive for weeks is disconnected.
- **Gaps can only be filled backwards.** There is no way to request “what came after X”. A missed period in a quiet chat can only be retrieved when a new message arrives in that chat.
- **Old media expires on the messaging service’s CDN.** In that case, `<messaging-cli> media retry --chat <jid>` asks the phone to send the file again.
