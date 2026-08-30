// Package web serves the HTTP API, the SSE stream, authentication, and the static shell.
//
// Layer 6. It may import any lower layer.
package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Error codes used by the JSON error envelope. The frontend branches on these, never on
// the message text.
const (
	CodeBadRequest      = "bad_request"
	CodeUnauthorized    = "unauthorized"
	CodeForbidden       = "forbidden"
	CodeNotFound        = "not_found"
	CodeConflict        = "conflict"
	CodeCSRF            = "csrf_invalid"
	CodeReauthRequired  = "reauth_required"
	CodeRateLimited     = "rate_limited"
	CodePayloadTooLarge = "payload_too_large"
	CodePluginDisabled  = "plugin_disabled"
	CodeInternal        = "internal"
)

// errorEnvelope is the single error shape every endpoint returns.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// snapshotEnvelope is the shape of every state snapshot response. The client uses
// asOfEventId as the boundary above which buffered stream events are applied.
type snapshotEnvelope struct {
	Data        any    `json:"data"`
	AsOfEventID string `json:"asOfEventId"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("web: write response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: msg}})
}

// writeSnapshot emits {data, asOfEventId} with the event-log tail read in the same
// transaction that produced data.
func writeSnapshot(w http.ResponseWriter, data any, asOfEventID string) {
	if asOfEventID == "" {
		asOfEventID = "0"
	}
	writeJSON(w, http.StatusOK, snapshotEnvelope{Data: data, AsOfEventID: asOfEventID})
}

// decodeJSON reads a bounded JSON request body into v.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if ok := asMaxBytes(err, &mbe); ok {
			writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, CodeBadRequest, "malformed JSON body")
		return false
	}
	return true
}

func asMaxBytes(err error, target **http.MaxBytesError) bool {
	for err != nil {
		if e, ok := err.(*http.MaxBytesError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
