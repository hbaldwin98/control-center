package tid

import (
	"encoding/json"
	"errors"
	"net/http"

	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
)

type dayView struct {
	Day        string   `json:"day"`
	KWh        float64  `json:"kwh"`
	CostCents  *int64   `json:"costCents"`
	OnPeakKWh  *float64 `json:"onPeakKwh"`
	OffPeakKWh *float64 `json:"offPeakKwh"`
}

type billingPeriodView struct {
	Start          string    `json:"start"`
	End            string    `json:"end"`
	TotalKWh       float64   `json:"totalKwh"`
	TotalCostCents *int64    `json:"totalCostCents"`
	OnPeakKWh      *float64  `json:"onPeakKwh"`
	OffPeakKWh     *float64  `json:"offPeakKwh"`
	PeakDemandDate string    `json:"peakDemandDate"`
	PeakDemandKW   *float64  `json:"peakDemandKw"`
	Days           []dayView `json:"days"`
}

type historyPage struct {
	Periods     []billingPeriodView `json:"periods"`
	LatestEvent int64               `json:"latestEventId"`
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
	MonthKWh              float64      `json:"monthKwh"`
	LastMonthKWh          float64      `json:"lastMonthKwh"`
	LastBillingPeriodKWh  float64      `json:"lastBillingPeriodKwh"`
	LastBillingPeriodFrom string       `json:"lastBillingPeriodFrom"`
	LastBillingPeriodTo   string       `json:"lastBillingPeriodTo"`
	LastYearKWh           float64      `json:"lastYearKwh"`
	PeakDay               string       `json:"peakDay"`
	PeakKWh               float64      `json:"peakKwh"`
	EstCostCents          *int64       `json:"estCostCents"`
	Spark                 []float64    `json:"spark"`
	Days                  []dayView    `json:"days"`
	Insight               *insightView `json:"insight"`
	LastSync              *syncView    `json:"lastSync"`
	LatestEvent           int64        `json:"latestEventId"`
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

func (p *Plugin) handlePostHistorySync(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id, err := h.Jobs().Enqueue(r.Context(), "sync", syncArgs{Source: "history", History: true})
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"jobId": id})
}

func (p *Plugin) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	page, err := p.history(r.Context(), h, 24)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, page)
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
