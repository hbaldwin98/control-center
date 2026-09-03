package tid

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

var insightSchema = json.RawMessage(`{
	"type":"object",
	"additionalProperties":false,
	"required":["summary","recommendation","anomalies"],
	"properties":{
		"summary":{"type":"string"},
		"recommendation":{"type":"string"},
		"anomalies":{"type":"array","items":{"type":"string"}}
	}
}`)

type insightPayload struct {
	At             string   `json:"at"`
	Summary        string   `json:"summary"`
	Recommendation string   `json:"recommendation"`
	Anomalies      []string `json:"anomalies"`
	Body           string   `json:"body"`
}

func (p *Plugin) writeInsight(jc hostjobs.Context, h host.Host, cfg settings) error {
	now := h.Clock().Now().In(localZone())
	from := now.AddDate(0, 0, -29).Format("2006-01-02")
	to := now.Format("2006-01-02")
	days, err := listDays(jc, h, from, to)
	if err != nil {
		return err
	}
	if len(days) < 3 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Daily TID electric usage for the last %d days (kWh", len(days))
	if cfg.CentsPerKWh > 0 {
		fmt.Fprintf(&b, ", estimated at %.1f cents/kWh", cfg.CentsPerKWh)
	}
	b.WriteString("). Oldest first:\n")
	for i := len(days) - 1; i >= 0; i-- {
		d := days[i]
		fmt.Fprintf(&b, "%s %.1f", d.Day, d.KWh)
		if d.CostCents != nil {
			fmt.Fprintf(&b, " $%.2f", float64(*d.CostCents)/100)
		}
		b.WriteByte('\n')
	}

	_ = jc.Logf("asking model for an insight over %d days", len(days))
	aiStart := time.Now()
	resp, err := h.AI().Chat(jc, hostai.ChatRequest{
		Model:  "cheap-chat",
		Schema: insightSchema,
		Messages: []hostai.Message{{
			Role: hostai.RoleUser,
			Text: "You are looking at one household's electricity use from Turlock Irrigation District. " +
				"Write a short factual summary, list any days that look unusual versus the rest, and give one concrete recommendation. " +
				"Do not invent appliances or weather. If you are not sure why usage moved, say so.\n\n" + b.String(),
		}},
	})
	if err != nil {
		_ = jc.Logf("insight model call failed after %dms: %v", time.Since(aiStart).Milliseconds(), err)
		return err
	}
	_ = jc.Logf("insight model answered in %dms (%d in / %d out tokens)",
		time.Since(aiStart).Milliseconds(), resp.Usage.InputTokens, resp.Usage.OutputTokens)

	var parsed struct {
		Summary        string   `json:"summary"`
		Recommendation string   `json:"recommendation"`
		Anomalies      []string `json:"anomalies"`
	}
	if len(resp.Parsed) > 0 {
		if err := json.Unmarshal(resp.Parsed, &parsed); err != nil {
			_ = jc.Logf("structured insight did not unmarshal, falling back to plain text: %v", err)
		}
	} else {
		_ = jc.Logf("model returned no structured output; falling back to plain text")
	}
	if parsed.Summary == "" {
		parsed.Summary = strings.TrimSpace(resp.Text)
	}
	if parsed.Summary == "" {
		return fmt.Errorf("tid: empty insight")
	}
	if parsed.Anomalies == nil {
		parsed.Anomalies = []string{}
	}
	anomalies, _ := json.Marshal(parsed.Anomalies)
	at := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.Store().Exec(jc, `
		INSERT INTO tid_insights(at, summary, recommendation, anomalies) VALUES (?, ?, ?, ?)`,
		at, parsed.Summary, parsed.Recommendation, string(anomalies)); err != nil {
		return err
	}
	body := parsed.Summary
	if parsed.Recommendation != "" {
		body = parsed.Summary + "\n" + parsed.Recommendation
	}
	return h.Events().Publish(jc, "insight", at, insightPayload{
		At:             at,
		Summary:        parsed.Summary,
		Recommendation: parsed.Recommendation,
		Anomalies:      parsed.Anomalies,
		Body:           body,
	})
}
