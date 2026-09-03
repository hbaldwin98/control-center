package bidrl

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
)

// Reading a request and writing a reply. Every handler in the plugin ends here.

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return false
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || json.Unmarshal(raw, dst) != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return false
	}
	return true
}

func writeHostErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hostpolicy.ErrPluginDisabled):
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
	case errors.Is(err, hostpolicy.ErrBudgetExceeded):
		writeErr(w, http.StatusConflict, "budget_exceeded", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}
