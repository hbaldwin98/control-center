package tid

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
)

type dayView struct {
	Day       string  `json:"day"`
	KWh       float64 `json:"kwh"`
	CostCents *int64  `json:"costCents"`
}

type insightView struct {
	At             string   `json:"at"`
	Summary        string   `json:"summary"`
	Recommendation string   `json:"recommendation"`
	Anomalies      []string `json:"anomalies"`
	EventID        int64    `json:"eventId"`
}

type syncView struct {
	At      string `json:"at"`
	Status  string `json:"status"`
	Rows    int    `json:"rows"`
	Source  string `json:"source"`
	Error   string `json:"error"`
	EventID int64  `json:"eventId"`
}

type summaryPage struct {
	MonthKWh     float64      `json:"monthKwh"`
	LastMonthKWh float64      `json:"lastMonthKwh"`
	LastYearKWh  float64      `json:"lastYearKwh"`
	Avg7         float64      `json:"avg7"`
	PeakDay      string       `json:"peakDay"`
	PeakKWh      float64      `json:"peakKwh"`
	EstCostCents *int64       `json:"estCostCents"`
	Spark        []float64    `json:"spark"`
	Days         []dayView    `json:"days"`
	Insight      *insightView `json:"insight"`
	LastSync     *syncView    `json:"lastSync"`
	LatestEvent  int64        `json:"latestEventId"`
}

func (p *Plugin) handleGetSummary(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	page, err := p.summary(r.Context(), h, p.settings(), h.Clock().Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (p *Plugin) handlePostSync(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id, err := h.Jobs().Enqueue(r.Context(), "sync", syncArgs{Source: "portal"})
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"jobId": id})
}

func (p *Plugin) handlePostUpload(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	var body struct {
		CSV string `json:"csv"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.CSV == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "csv is required")
		return
	}
	id, err := h.Jobs().Enqueue(r.Context(), "sync", syncArgs{Source: "upload", CSV: body.CSV})
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"jobId": id})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return false
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(raw) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return false
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
