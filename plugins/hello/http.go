package hello

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
)

type tickView struct {
	ID      int64  `json:"id"`
	At      string `json:"at"`
	Note    string `json:"note"`
	BlobKey string `json:"blobKey"`
	AIText  string `json:"aiText"`
	EventID int64  `json:"eventId"`
}

type ticksPage struct {
	Note  string     `json:"note"`
	Ticks []tickView `json:"ticks"`
}

func (p *Plugin) handleGetTicks(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	rows, err := h.Store().Query(r.Context(),
		`SELECT id, at, note, blob_key, ai_text, event_id FROM hello_ticks ORDER BY id DESC LIMIT 50`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	out := ticksPage{Note: p.settings().Note, Ticks: []tickView{}}
	for rows.Next() {
		var t tickView
		if err := rows.Scan(&t.ID, &t.At, &t.Note, &t.BlobKey, &t.AIText, &t.EventID); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		out.Ticks = append(out.Ticks, t)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (p *Plugin) handlePostTick(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	// The tick takes no arguments; a body is accepted and ignored so the UI can post
	// an empty object.
	var ignored struct{}
	if !decodeOptionalJSON(w, r, &ignored) {
		return
	}
	id, err := h.Jobs().Enqueue(r.Context(), "tick", nil)
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"jobId": id})
}

func (p *Plugin) handlePostChat(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	resp, err := h.AI().Chat(r.Context(), hostai.ChatRequest{
		Model:    "cheap-chat",
		Messages: []hostai.Message{{Role: hostai.RoleUser, Text: "ping"}},
	})
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"text":         resp.Text,
		"costMicroUsd": resp.Usage.CostMicroUSD,
	})
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		return true
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return false
	}
	if len(raw) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, dst); err != nil {
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
