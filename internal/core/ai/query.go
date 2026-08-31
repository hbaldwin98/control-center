package ai

import (
	"context"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// Routes describes every configured route for the administrator.
//
// The plan comes from the stored intent rather than from the compiled route, so a route
// that failed to compile still reports every attempt it was saved with — an editor that
// loaded the truncated compilation would quietly drop the attempts after the broken one.
//
// Health is the union of two questions: did the route compile against the current
// providers, and can its credentials still be resolved. Both are answered here rather
// than at startup, because routes are edited while the process runs and a route that
// broke this morning should say so instead of preventing a restart.
func (s *Service) Routes(ctx context.Context) ([]RouteDescriptor, error) {
	inputs, err := s.loadRouteInputs(ctx)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	failures := make(map[string]string, len(s.routes))
	for _, r := range s.routes {
		failures[r.name] = r.lastError
	}
	providers := make(map[string]ProviderConfig, len(s.providerCfg))
	for k, v := range s.providerCfg {
		providers[k] = v
	}
	s.mu.RUnlock()

	// One credential is usually shared by every attempt on every route. Resolve each
	// at most once.
	checked := map[string]string{}
	credError := func(id string) string {
		if msg, ok := checked[id]; ok {
			return msg
		}
		msg := ""
		if _, err := s.creds.Attributes(ctx, id); err != nil {
			msg = err.Error()
		}
		checked[id] = msg
		return msg
	}

	out := make([]RouteDescriptor, 0, len(inputs))
	for _, in := range inputs {
		d := RouteDescriptor{
			LogicalName: in.Name, Capabilities: in.Capabilities,
			MaxInputTokens: in.MaxInputTokens, MaxOutputTokens: in.MaxOutputTokens,
			LastError: failures[in.Name],
		}
		if d.Capabilities == nil {
			d.Capabilities = []string{}
		}
		for _, a := range in.Attempts {
			p, known := providers[a.Provider]
			d.AttemptPlan = append(d.AttemptPlan, AttemptDescriptor{
				Provider: a.Provider, Model: a.Model, Billing: p.Billing,
				InputMicroUSDPerMillion:  policy.MicroUSD(a.InputMicroUSDPerMillion),
				OutputMicroUSDPerMillion: policy.MicroUSD(a.OutputMicroUSDPerMillion),
			})
			if !known || d.LastError != "" {
				continue
			}
			if msg := credError(p.CredentialID); msg != "" {
				d.LastError = "credential " + p.CredentialID + ": " + msg
			}
		}
		if d.AttemptPlan == nil {
			d.AttemptPlan = []AttemptDescriptor{}
		}
		d.Healthy = d.LastError == ""
		out = append(out, d)
	}
	return out, nil
}

func (s *Service) Calls(ctx context.Context, q CallQuery) (CallPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	args := []any{}
	where := `WHERE 1=1`
	if q.PluginID != "" {
		where += ` AND plugin_id = ?`
		args = append(args, q.PluginID)
	}
	if q.LogicalModel != "" {
		where += ` AND logical_model = ?`
		args = append(args, q.LogicalModel)
	}
	if q.Status != "" {
		where += ` AND status = ?`
		args = append(args, q.Status)
	}
	if q.AfterID != "" {
		where += ` AND id > ?`
		args = append(args, q.AfterID)
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(ctx,
		`SELECT id, plugin_id, job_id, operation, logical_model, status, error_class,
		        reserved_micro_usd, settled_micro_usd, started_at, finalized_at
		   FROM core_ai_calls `+where+` ORDER BY started_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return CallPage{}, err
	}
	defer rows.Close()
	var calls []CallRecord
	for rows.Next() {
		var c CallRecord
		var reserved, settled int64
		var started string
		var finalized *string
		if err := rows.Scan(&c.ID, &c.PluginID, &c.JobID, &c.Operation, &c.LogicalModel, &c.Status, &c.ErrorClass,
			&reserved, &settled, &started, &finalized); err != nil {
			return CallPage{}, err
		}
		c.ReservedMicroUSD = policy.MicroUSD(reserved)
		c.SettledMicroUSD = policy.MicroUSD(settled)
		if t, err := parseTime(started); err == nil {
			c.StartedAt = t
		}
		if finalized != nil {
			if t, err := parseTime(*finalized); err == nil {
				c.FinalizedAt = &t
			}
		}
		calls = append(calls, c)
	}
	if err := rows.Err(); err != nil {
		return CallPage{}, err
	}
	var next string
	if len(calls) > limit {
		next = calls[limit-1].ID
		calls = calls[:limit]
	}
	if calls == nil {
		calls = []CallRecord{}
	}
	return CallPage{Calls: calls, NextAfter: next}, nil
}
