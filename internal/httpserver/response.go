package httpserver

import (
	"io"
	"net/http"
)

// privateResponse suppresses arbitrary upstream error strings (which may include
// Telegram credentials or request bodies). Successful JSON/tool payloads are
// deliberately unaffected. Unwrap/Flush preserve panel event streams.
type privateResponse struct {
	http.ResponseWriter
	status  int
	refused bool
}

func (w *privateResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status >= 400 {
		w.refused = true
		w.Header().Set("Content-Type", "application/json")
		w.Header().Del("Content-Length")
		w.ResponseWriter.WriteHeader(status)
		io.WriteString(w.ResponseWriter, "{\"error\":\"request refused\"}\n")
		return
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *privateResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if w.refused {
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}
func (w *privateResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *privateResponse) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
