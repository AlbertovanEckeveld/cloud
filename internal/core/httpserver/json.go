package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
)

// maxJSONBody bounds request bodies; API requests are small.
const maxJSONBody = 16 << 10

// ErrorBody is the JSON shape of every API error.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries a stable machine-readable code and a human message.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent; all we can do is record it.
		slog.Error("write json response", "error", err)
	}
}

// WriteError writes an API error. message is shown to users, so it must never
// contain internal details such as error strings, hostnames or SQL.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}

// DecodeJSON reads a JSON request body into dst. It requires the
// application/json content type (an HTML form on another site cannot send
// that without a CORS preflight, which is one more CSRF barrier), limits the
// size, and rejects unknown fields and trailing data. On failure it writes
// the error response and returns false.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		WriteError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Verwacht een JSON-verzoek.")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "Het verzoek is te groot.")
			return false
		}
		WriteError(w, http.StatusBadRequest, "invalid_request", "Het verzoek is ongeldig.")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, "invalid_request", "Het verzoek is ongeldig.")
		return false
	}
	return true
}
