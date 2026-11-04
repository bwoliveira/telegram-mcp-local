# Audio transcription

> **Historical upstream documentation:** This describes the predecessor product, its messaging service, and its CLI at the time. It is not documentation of verified Telegram MCP behavior; the upstream commands and installation steps must not be assumed to work here.
> Names and identifiers containing `predecessor` are schematic placeholders, not literal commands or configuration.

Nothing is transcribed unless requested. Audio goes through the transcription engine only when your AI tool calls `transcribe_audio`, usually because you asked it to read an audio message. The transcript is then saved and returned with the audio in other tools and in the panel.

## An audio message's journey

```text
you: "what did Ana say in the audio?"
  │
  ▼
Claude ──transcribe_audio(message_id)──► daemon
                                           │
  1. find the message       predecessor-cli.db (read-only): is it audio?
  2. already transcribed?   state.db: if so, return immediately
  3. gather context         chat name, speaker, 12 previous and 4 following messages
  4. download audio         predecessor-cli --read-only media download → <data directory>/media
  5. convert                ffmpeg: Ogg Opus → WAV 16 kHz mono
  6. transcribe             whisper-cli + large-v3-turbo, with context in --prompt
  7. save                   state.db: text, engine, language, date
  │
  ▼
Claude ◄── transcript + context messages + review instruction
  │
  └─ if context reveals a misheard word ──save_transcript(corrected text)──► state.db
                                                      (original remains in raw_text)
```

### 1 and 2. The message and existing transcript

The ID comes from the read tools. The daemon checks the `predecessor-cli` index to confirm that it is audio (`media_type = audio`). If a transcript is already saved, it is returned immediately without running anything, unless the call requests `refresh`.

### 3. Context

Context helps transcription get names and terms right. It is assembled from the index:

- the **conversation name** and **audio sender**, resolved as shown on the phone (group, address book, profile name);
- the **people** appearing in surrounding messages;
- the **12 previous text messages** and **4 following messages**.

It is used twice. First, it becomes Whisper's `--prompt`: names first, then recent sentences, up to about 800 characters. Whisper uses this text to decide how to spell what it hears, so "Capuava" comes out as written in the conversation rather than as it sounds. Then it is returned with the transcript for Claude to review.

### 4. Audio

Audio is downloaded with `predecessor-cli --read-only media download`. Read-only mode does not take the predecessor service lock, so it can run while sync is active. The file is downloaded encrypted from the predecessor service media server, decrypted using the key received in the message, and saved in `media/<conversa>/` in the data directory. Audio already downloaded is not downloaded again.

The predecessor service media server deletes old files. Audio from months earlier may no longer be available: in that case the error explains this, and `predecessor-cli media retry --chat <jid>` asks the phone to send it again.

### 5 and 6. The local engine

| Stage | Program | What it does |
|---|---|---|
| Conversion | `ffmpeg` | Converts predecessor service's Ogg Opus audio to 16 kHz mono WAV, the format whisper.cpp reads |
| Transcription | `whisper-cli` (whisper.cpp) | Runs the model with `-l auto` (or the requested language) and `--prompt` with context: on the Mac GPU (Metal), an NVIDIA card (CUDA, on Windows), or the processor, using all but one core |
| Model | `ggml-large-v3-turbo-q5_0.bin` | Quantized Whisper large-v3-turbo, 574 MB, in `models/` in the data directory |

One audio message at a time: the GPU is shared, so a second request waits for the first to finish.

| Where it runs | A 10-second audio message |
|---|---|
| Mac with Apple Silicon (M3 Pro, Metal) | About 2 seconds |
| Processor only (Linux arm64, 12 cores) | About 13 seconds |

On the processor, audio takes approximately its own duration, and the Audio Transcription section of Settings warns about this. The first use after starting the computer takes a few extra seconds to load the model.

**Why Whisper instead of Parakeet V3.** Handy offers both. Parakeet is faster on the processor but does not accept context text, and context corrects names and terms during transcription. Large-v3-turbo is the best balance of speed and Portuguese quality on this hardware. Other Whisper models (`small`, `medium`, `large-v3`) could run through the same path by changing only the file, but the app at the time used only turbo.

### 7. What is stored

In `state.db` in the data directory, the `transcripts` table has one row per audio message:

| Field | Content |
|---|---|
| `text` | The current transcript (corrected, if a correction was made) |
| `raw_text` | What the engine heard, preserved when corrected |
| `source` | `local`, `corrected` (reviewed), or `client` (made by the AI tool itself); older transcripts may have `openai` |
| `model` | The engine that generated it |
| `language`, `created_at` | Requested language and date |

After that, `get_chat_messages` and `search_messages` return the `transcript` field with the audio, and search also finds spoken words. In the panel, the transcript appears below the audio in the conversation preview.

### Review by Claude

The tool returns the transcript with the context messages and an instruction: where a word is clearly a mishearing of something mentioned in the conversation (name, place, product, term), correct it with `save_transcript`, changing only what the context makes certain, without changing the speaker's manner of speaking or adding anything. The daemon saves the correction and keeps the original in `raw_text`. The Audio Transcription section of Settings shows how many audio messages were corrected this way.

The daemon does not run its own language model for this: the AI tool that requested the transcription, already participating in the conversation, makes the correction.

## Installing the engine

The app installer does not include the transcription components. When you click **Install local transcription** (or run `predecessor-app-mcp transcription install`):

1. The app downloads the package for your system, published in the upstream repository by the `sidecars` workflow: `whisper-cli` and a minimal `ffmpeg` that can only convert predecessor service's audio formats to WAV (about 2 MB, versus ~80 MB for a full ffmpeg).
2. It checks the package SHA-256 against the value pinned in `internal/sidecar/manifest.json`, unpacks it into `bin/` in the data directory, and saves each program's SHA-256, checking it again before every use; on macOS, it also checks the signature.
3. It downloads the model from Hugging Face (`huggingface.co/ggerganov/whisper.cpp`) and checks its SHA-256 before putting it in place.

| System | whisper-cli | Acceleration |
|---|---|---|
| macOS (universal) | Built by the project | Metal, on the GPU |
| Windows | Official whisper.cpp build | CPU; CUDA when an NVIDIA card is present |
| Linux (amd64 and arm64) | Built by the project, static | CPU |

If `whisper-cli` and `ffmpeg` are already installed on the computer (via Homebrew, for example), the app uses them and downloads only the model. The engine is ready when all three exist.

## Without the local engine

The first app version transcribes only on the computer. Without the engine installed, `transcribe_audio` returns installation instructions, and the AI tool can download the audio with `download_media` (the file remains on disk, with its path in the response) and transcribe it however it prefers. The option to use an OpenAI key, useful on computers without a GPU, is planned to return in a future version.
