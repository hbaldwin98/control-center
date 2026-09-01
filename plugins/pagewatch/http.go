package pagewatch

import (
	"encoding/json"
	"errors"
	"net/http"

	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
)

type checkView struct {
	ID            int64  `json:"id"`
	CheckedAt     string `json:"checkedAt"`
	URL           string `json:"url"`
	Status        string `json:"status"`
	ContentHash   string `json:"contentHash"`
	ExpectedText  string `json:"expectedText"`
	ExpectedFound bool   `json:"expectedFound"`
	Summary       string `json:"summary"`
	AIRan         bool   `json:"aiRan"`
	InputTokens   int64  `json:"inputTokens"`
	OutputTokens  int64  `json:"outputTokens"`
	CostMicroUSD  int64  `json:"costMicroUsd"`
	BrowserMS     int64  `json:"browserMs"`
	AIMS          int64  `json:"aiMs"`
	JobID         int64  `json:"jobId"`
	EventID       int64  `json:"eventId"`
}

type checksPage struct {
	TargetURL    string      `json:"targetUrl"`
	ExpectedText string      `json:"expectedText"`
	Schedule     string      `json:"schedule"`
	SnapshotURL  string      `json:"snapshotUrl"`
	Checks       []checkView `json:"checks"`
}

func (p *Plugin) handleGetChecks(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	rows, err := h.Store().Query(r.Context(), `SELECT id, checked_at, url, status, content_hash,
		expected_text, expected_found, summary, ai_ran, input_tokens, output_tokens,
		cost_micro_usd, browser_ms, ai_ms, job_id, event_id
		FROM pagewatch_checks ORDER BY id DESC LIMIT 100`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	s := p.settings()
	out := checksPage{
		TargetURL: s.URL, ExpectedText: s.ExpectedText, Schedule: checkSchedule,
		SnapshotURL: h.Blobs().URL(snapshotKey), Checks: []checkView{},
	}
	for rows.Next() {
		var c checkView
		var expectedFound, aiRan int
		if err := rows.Scan(&c.ID, &c.CheckedAt, &c.URL, &c.Status, &c.ContentHash,
			&c.ExpectedText, &expectedFound, &c.Summary, &aiRan, &c.InputTokens,
			&c.OutputTokens, &c.CostMicroUSD, &c.BrowserMS, &c.AIMS, &c.JobID, &c.EventID); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.ExpectedFound = expectedFound != 0
		c.AIRan = aiRan != 0
		out.Checks = append(out.Checks, c)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (p *Plugin) handlePostCheck(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id, err := h.Jobs().Enqueue(r.Context(), "check", struct{}{}, hostjobs.WithIdempotencyKey("manual"))
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int64{"jobId": id})
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

func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
