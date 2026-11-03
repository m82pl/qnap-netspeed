package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{code, msg})
}

// maxJSONBody is the largest request body of the JSON endpoints.
const maxJSONBody = 64 << 10

// decodeJSON reads one JSON object into v; an empty body leaves v untouched
// when allowEmpty. It writes the error response itself and reports success.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, allowEmpty bool) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	var tooBig *http.MaxBytesError
	switch {
	case err == nil:
		if dec.More() {
			writeError(w, http.StatusBadRequest, "bad_request", "unexpected data after the JSON object")
			return false
		}
		return true
	case errors.Is(err, io.EOF) && allowEmpty:
		return true
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body is too large")
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
	}
	return false
}
