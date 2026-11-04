# Workstream J APIs — Stage 3

Routes must be registered by `httpserver.RegisterMediaAPIs(mux, server, hooks)`
on the private mux (before the single authentication boundary of `httpserver.Routes`).
Bearer or local session is mandatory, including `/media/{token}`; a session on an
unsafe method requires CSRF. Do not also register the panel's inherited APIs.

## Webhooks

- `GET /api/webhooks`: `{available, webhooks, events, signature, queue_note}`.
- `POST /api/webhooks`: Settings `{url,events,chats,include_own,enabled}`;
  response `{webhook,secret,note}`. Secret only in this response.
- `GET /api/webhooks/{id}`: Status without secret.
- `PATCH /api/webhooks/{id}`: partial Settings, preserves omitted fields and secret; returns Status.
- `DELETE /api/webhooks/{id}`: `{deleted:true}`.
- `POST /api/webhooks/{id}/test`: result of the signed test delivery.
- Missing ID: 404; invalid configuration: 400.

Headers `X-Telegram-MCP-{Signature,Event,Delivery,Attempt}`. Envelope schema 1,
`delivery_id` deterministic and stable across attempts. The queue is in memory:
pending deliveries are lost on restart; the engine's catch-up does not re-emit facts already stored.

## Media and transcription

- `GET /api/media`: local inventory (default retention media 30 days, exports 7).
- `POST /api/media/retention`: `{days:N}`; applies the configuration and the media sweep
  immediately (A28), returning `{retention_days,deleted_files,freed_bytes}`.
  Zero turns off media retention without removing files; exports keep a separate policy.
- `POST /api/media/purge`: `{what:"media"|"exports",older_than_days:N,confirm:true}`;
  without confirm returns `{preview,would_delete_files,would_free_bytes}`, never deletes
  on Telegram; confirmed returns `{deleted_files,freed_bytes}`.
- `POST /api/media/download`: `{chat_id,message_id}`; local download recorded.
  IDs are separate canonical strings, for example `chat_id:"user:123"`, `message_id:"45"`.
- `POST /api/transcription/transcribe`: `{chat_id,message_id,language,refresh,native}`;
  default native=false, local Whisper, cache with raw_text; native requires an explicit true.
- `POST /api/transcription/save`: `{chat_id,message_id,text,language,model}`;
  local correction preserves raw_text and updates FTS.
- `GET /api/transcription`: local configuration/availability.

Install model/sidecars only by explicit action, never during transcription.
The inherited configuration/installation may stay in the panel, but must not duplicate
the routes above. Errors do not expose paths, messages or subprocess stderr.

## Integration

`Server.MediaTool` must be wired to the shared dispatcher before the
legacy index/state guards. Requests outside J's ownership are in
`PEDIDOS-E3-J.md`. No real Telegram evidence in this workstream.
