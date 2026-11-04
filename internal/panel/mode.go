package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// ModeSetting is the state key where the person's choice of write mode is
// kept, so the daemon can restore it when it starts.
const ModeSetting = "engine.mode"

// maxModeBody bounds POST /api/mode: the only valid body is a few dozen bytes.
const maxModeBody = 1 << 10

var errModePayload = errors.New(`the body must be exactly {"mode":"read_only"|"read_write","confirm":true}`)

// apiMode is POST /api/mode (security.md SEC-R04). Allowing writes is always
// a person's act on this computer: the outer HTTP guard already demands a
// panel session and its CSRF token and refuses the bearer token; here the
// panel refuses, as well, anything that is not its own page (or the app's
// window), any request carrying a bearer token, and any body that is not
// exactly {"mode": ..., "confirm": true}.
func (p *Panel) apiMode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fail := func(status int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if len(r.Header.Values("Authorization")) > 0 || !sameOrigin(r) {
		fail(http.StatusForbidden, "the write mode changes only on the panel, by a person")
		return
	}
	if ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || ct != "application/json" {
		fail(http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxModeBody))
	if err != nil {
		fail(http.StatusBadRequest, errModePayload.Error())
		return
	}
	mode, err := parseModeRequest(raw)
	if err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := p.setMode(ctx, mode); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, engine.ErrInvalid) {
			status = http.StatusBadRequest
		}
		fail(status, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"mode": string(mode)})
}

// setMode applies the mode through the engine (the coordinator's stable
// proxy: never replaced here) and then records it. A failure to record a
// change to read_write puts the engine back in read-only, so writes are
// never left enabled without the record that a person chose them.
func (p *Panel) setMode(ctx context.Context, mode engine.Mode) error {
	p.modeMu.Lock()
	defer p.modeMu.Unlock()
	if p.State == nil {
		return errors.New(i18n.T("não foi possível guardar o modo agora"))
	}
	if err := p.Engine.SetMode(ctx, mode); err != nil {
		if errors.Is(err, engine.ErrInvalid) {
			return err
		}
		return errors.New(i18n.T("não foi possível trocar o modo agora"))
	}
	if err := p.State.SetSetting(ctx, ModeSetting, string(mode)); err != nil {
		if mode == engine.ModeReadWrite {
			_ = p.Engine.SetMode(context.WithoutCancel(ctx), engine.ModeReadOnly)
			return errors.New(i18n.T("não foi possível guardar o modo agora; a escrita continua bloqueada"))
		}
		return errors.New(i18n.T("o modo somente leitura foi aplicado, mas não ficou guardado"))
	}
	return nil
}

// parseModeRequest accepts exactly one JSON object with the two keys "mode"
// (read_only or read_write, a string) and "confirm" (the boolean true): no
// other key, no key twice, no other case, nothing after it.
func parseModeRequest(raw []byte) (engine.Mode, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", errModePayload
	}
	var mode engine.Mode
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok || seen[key] {
			return "", errModePayload
		}
		seen[key] = true
		value, err := dec.Token()
		if err != nil {
			return "", errModePayload
		}
		switch key {
		case "mode":
			s, ok := value.(string)
			if !ok || (s != string(engine.ModeReadOnly) && s != string(engine.ModeReadWrite)) {
				return "", errModePayload
			}
			mode = engine.Mode(s)
		case "confirm":
			if b, ok := value.(bool); !ok || !b {
				return "", errModePayload
			}
		default:
			return "", errModePayload
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return "", errModePayload
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", errModePayload
	}
	if !seen["mode"] || !seen["confirm"] {
		return "", errModePayload
	}
	return mode, nil
}
