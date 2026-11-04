package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/localasr"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"github.com/bwoliveira/telegram-mcp-local/internal/webhook"
)

// RegisterMediaAPIs registers private JSON APIs, not authentication middleware.
// Call only from Routes' Options.Register, without the inherited panel APIs.
// Routes already serves /media/ behind its bearer/session boundary. Unsafe
// session requests consequently use the same CSRF checks as all private APIs.
//
// Webhook POST/PATCH accept webhook.Settings (url, events, chats, include_own,
// enabled); PATCH preserves omitted fields and the secret. There is no name
// field in the webhook store. All successful requests return HTTP 200. Purge
// returns a preview unless confirm is explicitly true. Updating retention
// explicitly applies the media policy immediately; export retention is separate.
func RegisterMediaAPIs(mux *http.ServeMux, server *mcp.Server, hooks *webhook.Manager) {
	api := func(pattern string, needsHooks bool, handler func(*http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if (needsHooks && hooks == nil) || (!needsHooks && server == nil) {
				mediaAPIWrite(w, http.StatusServiceUnavailable, map[string]any{"error": "service unavailable"})
				return
			}
			out, err := handler(r)
			if err != nil {
				mediaAPIError(w, err)
				return
			}
			mediaAPIWrite(w, http.StatusOK, out)
		})
	}
	mux.HandleFunc("GET /api/webhooks", func(w http.ResponseWriter, r *http.Request) {
		list := []webhook.Status{}
		if hooks != nil {
			var err error
			list, err = hooks.List(r.Context())
			if err != nil {
				mediaAPIError(w, err)
				return
			}
			if list == nil {
				list = []webhook.Status{}
			}
			for i := range list {
				list[i] = mediaAPIHookStatus(list[i])
			}
		}
		mediaAPIWrite(w, 200, map[string]any{"available": hooks != nil, "webhooks": list, "events": webhook.Events,
			"signature":  "X-Telegram-MCP-Signature: sha256=<HMAC-SHA256 of the body with the webhook secret, hexadecimal>",
			"queue_note": "Process-local queue: pending deliveries are lost on restart; catch-up does not replay persisted facts."})
	})
	api("POST /api/webhooks", true, func(r *http.Request) (any, error) {
		var settings webhook.Settings
		if err := mediaAPIDecode(r, &settings); err != nil {
			return nil, err
		}
		h, secret, err := hooks.Create(r.Context(), settings)
		if err != nil {
			return nil, err
		}
		st, err := hooks.Get(r.Context(), h.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"webhook": mediaAPIHookStatus(st), "secret": secret, "note": "Save the secret now; it is never returned again. Deliveries use X-Telegram-MCP-Signature."}, nil
	})
	api("GET /api/webhooks/{id}", true, func(r *http.Request) (any, error) {
		st, err := hooks.Get(r.Context(), r.PathValue("id"))
		return mediaAPIHookStatus(st), err
	})
	api("PATCH /api/webhooks/{id}", true, func(r *http.Request) (any, error) {
		var settings webhook.Settings
		if err := mediaAPIDecode(r, &settings); err != nil {
			return nil, err
		}
		if _, err := hooks.Update(r.Context(), r.PathValue("id"), settings); err != nil {
			return nil, err
		}
		st, err := hooks.Get(r.Context(), r.PathValue("id"))
		return mediaAPIHookStatus(st), err
	})
	api("DELETE /api/webhooks/{id}", true, func(r *http.Request) (any, error) {
		if err := hooks.Delete(r.Context(), r.PathValue("id")); err != nil {
			return nil, err
		}
		return map[string]bool{"deleted": true}, nil
	})
	api("POST /api/webhooks/{id}/test", true, func(r *http.Request) (any, error) {
		out, err := hooks.Test(r.Context(), r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		// Manager.Test's diagnostic can contain a destination URL or transport error.
		result := "delivery succeeded"
		if out["ok"] != true {
			result = "delivery failed"
		}
		return map[string]any{"ok": out["ok"], "result": result, "ms": out["ms"]}, nil
	})
	api("GET /api/media", false, func(r *http.Request) (any, error) { return server.Inventory(r.Context()) })
	api("POST /api/media/retention", false, func(r *http.Request) (any, error) {
		var body struct {
			Days *int `json:"days"`
		}
		if err := mediaAPIDecode(r, &body); err != nil {
			return nil, err
		}
		if body.Days == nil || *body.Days < 0 || *body.Days > 365000 {
			return nil, engine.ErrInvalid
		}
		if err := server.SetRetention(r.Context(), *body.Days); err != nil {
			return nil, err
		}
		var files int
		var bytes int64
		if *body.Days > 0 {
			var err error
			files, bytes, _, err = server.PurgeMediaContext(r.Context(), "", *body.Days, 0, false)
			if err != nil {
				return nil, err
			}
		}
		return map[string]any{"retention_days": *body.Days, "deleted_files": files, "freed_bytes": bytes}, nil
	})
	api("POST /api/media/purge", false, func(r *http.Request) (any, error) {
		var body struct {
			What          string `json:"what"`
			OlderThanDays int    `json:"older_than_days"`
			Confirm       bool   `json:"confirm"`
		}
		if err := mediaAPIDecode(r, &body); err != nil {
			return nil, err
		}
		if body.OlderThanDays < 0 || body.OlderThanDays > 365000 {
			return nil, engine.ErrInvalid
		}
		var files int
		var bytes int64
		var err error
		switch body.What {
		case "media":
			files, bytes, _, err = server.PurgeMediaContext(r.Context(), "", body.OlderThanDays, 0, !body.Confirm)
		case "exports":
			files, bytes, err = server.PurgeExportsContext(r.Context(), body.OlderThanDays, !body.Confirm)
		default:
			return nil, engine.ErrInvalid
		}
		if err != nil {
			return nil, err
		}
		if !body.Confirm {
			return map[string]any{"preview": true, "would_delete_files": files, "would_free_bytes": bytes}, nil
		}
		return map[string]any{"deleted_files": files, "freed_bytes": bytes}, nil
	})
	// MediaTool is the shared exported account-scoped dispatcher. It validates
	// canonical IDs and resolves the engine account, not the legacy panel slug.
	// HTTP unwraps its JSON text DTO and always requests links, never base64 audio.
	api("POST /api/media/download", false, func(r *http.Request) (any, error) {
		var body struct {
			ChatID    string `json:"chat_id"`
			MessageID string `json:"message_id"`
		}
		if err := mediaAPIDecode(r, &body); err != nil {
			return nil, err
		}
		return mediaAPITool(r, server, "download_media", map[string]any{"chat_id": body.ChatID, "message_id": body.MessageID, "link": true})
	})
	api("POST /api/transcription/transcribe", false, func(r *http.Request) (any, error) {
		var body struct {
			ChatID    string `json:"chat_id"`
			MessageID string `json:"message_id"`
			Language  string `json:"language"`
			Refresh   bool   `json:"refresh"`
			Native    bool   `json:"native"`
		}
		if err := mediaAPIDecode(r, &body); err != nil {
			return nil, err
		}
		return mediaAPITool(r, server, "transcribe_audio", body)
	})
	api("POST /api/transcription/save", false, func(r *http.Request) (any, error) {
		var body struct {
			ChatID    string `json:"chat_id"`
			MessageID string `json:"message_id"`
			Text      string `json:"text"`
			Language  string `json:"language"`
			Model     string `json:"model"`
		}
		if err := mediaAPIDecode(r, &body); err != nil {
			return nil, err
		}
		return mediaAPITool(r, server, "save_transcript", body)
	})
	api("GET /api/transcription", false, func(r *http.Request) (any, error) {
		out := map[string]any{"available": false, "ready": false, "supported": false, "local": true, "engine": localasr.Label,
			"model": localasr.ModelName, "installation": "explicit only; transcription never installs models or sidecars"}
		if asr := server.ASR(); asr != nil {
			st := asr.Status()
			install := st.Install
			if install.Error != "" {
				install.Error = "local transcription setup failed"
			}
			out["available"], out["ready"], out["supported"] = st.Ready, st.Ready, st.Supported
			out["accel"], out["missing"], out["install"] = st.Accel, st.Missing, install
			out["components"] = map[string]bool{"whisper": st.Whisper != "", "ffmpeg": st.FFmpeg != "", "model": st.Model != ""}
		}
		return out, nil
	})
}

func mediaAPIHookStatus(st webhook.Status) webhook.Status {
	// Persistent delivery diagnostics can contain arbitrary transport details.
	if st.LastResult != "" {
		st.LastResult = "delivery attempted"
	}
	if st.DisabledReason != "" {
		st.DisabledReason = "delivery failed repeatedly"
	}
	return st
}

func mediaAPIDecode(r *http.Request, dst any) error {
	// Impose the same limit even when registered on a directly exercised mux.
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return engine.ErrInvalid
	}
	// Reject null, arrays, trailing JSON and unknown fields before side effects.
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return engine.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil {
		return engine.ErrInvalid
	}
	return nil
}

// mediaAPIHTTPError contains only a fixed public message, never a wrapped error.
type mediaAPIHTTPError struct {
	status  int
	message string
}

func (e mediaAPIHTTPError) Error() string { return e.message }

func mediaAPITool(r *http.Request, server *mcp.Server, name string, input any) (any, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, engine.ErrInvalid
	}
	var identity struct {
		ChatID    string `json:"chat_id"`
		MessageID string `json:"message_id"`
		Text      string `json:"text"`
	}
	if json.Unmarshal(raw, &identity) != nil {
		return nil, engine.ErrInvalid
	}
	kind, id, ok := strings.Cut(identity.ChatID, ":")
	peerID, peerErr := strconv.ParseInt(id, 10, 64)
	messageID, messageErr := strconv.ParseInt(identity.MessageID, 10, 32)
	if !ok || (kind != "user" && kind != "chat" && kind != "channel") || peerErr != nil || peerID <= 0 || strconv.FormatInt(peerID, 10) != id || messageErr != nil || messageID <= 0 || strconv.FormatInt(messageID, 10) != identity.MessageID {
		return nil, engine.ErrInvalid
	}
	if name == "save_transcript" && strings.TrimSpace(identity.Text) == "" {
		return nil, engine.ErrInvalid
	}
	result, handled := server.MediaTool(r.Context(), name, raw)
	if !handled {
		return nil, engine.ErrNotSupported
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		return nil, errors.New("invalid media result")
	}
	item, ok := content[0].(map[string]any)
	if !ok {
		return nil, errors.New("invalid media result")
	}
	text, ok := item["text"].(string)
	if !ok {
		return nil, errors.New("invalid media result")
	}
	var out map[string]any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if decoder.Decode(&out) != nil || out == nil {
		return nil, errors.New("invalid media result")
	}
	if result["isError"] == true {
		// Do not echo arbitrary engine errors. Preserve only known sentinel meaning.
		message, _ := out["error"].(string)
		for _, sentinel := range mediaAPISentinels {
			if message == sentinel.Error() {
				return nil, sentinel
			}
		}
		if message == "local transcription is not installed" {
			return nil, mcp.ErrNoEngine
		}
		if message == "not implemented yet: Telegram engine is not configured" || message == "engine returned no account identity" {
			return nil, engine.ErrNotSupported
		}
		if message == "Telegram rate limit" || message == "Telegram rate limit; retry after flood_wait_until" {
			return nil, mediaAPIHTTPError{429, "Telegram rate limit"}
		}
		return nil, errors.New("media operation failed")
	}
	return out, nil
}

var mediaAPISentinels = []error{engine.ErrInvalid, engine.ErrAccountMismatch, engine.ErrMessageNotFound, engine.ErrProtectedContent, engine.ErrNoAccess, engine.ErrPaused, engine.ErrNotAuthorized, engine.ErrNotSupported, context.Canceled, context.DeadlineExceeded}

func mediaAPIError(w http.ResponseWriter, err error) {
	status, message := http.StatusInternalServerError, "media operation failed"
	var user webhook.UserError
	var public mediaAPIHTTPError
	switch {
	case errors.As(err, &public):
		status, message = public.status, public.message
	case errors.Is(err, state.ErrNoWebhook), errors.Is(err, engine.ErrMessageNotFound):
		status, message = 404, "not found"
	case errors.Is(err, engine.ErrInvalid), errors.As(err, &user):
		status, message = 400, "invalid request"
	case errors.Is(err, engine.ErrAccountMismatch):
		status, message = 409, "account mismatch"
	case errors.Is(err, engine.ErrProtectedContent), errors.Is(err, engine.ErrNoAccess):
		status, message = 403, "media access denied"
	case errors.Is(err, engine.ErrNotAuthorized):
		status, message = 503, "Telegram account unavailable"
	case errors.Is(err, engine.ErrNotSupported), errors.Is(err, mcp.ErrNoEngine), errors.Is(err, engine.ErrPaused):
		status, message = 503, "operation unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		status, message = 504, "operation timed out"
	case errors.Is(err, context.Canceled):
		status, message = 408, "operation canceled"
	}
	mediaAPIWrite(w, status, map[string]any{"error": message})
}

func mediaAPIWrite(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
