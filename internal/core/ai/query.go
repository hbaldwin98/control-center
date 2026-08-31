package ai

import (
	"context"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

func (s *Service) Routes(ctx context.Context) ([]RouteDescriptor, error) {
	out := make([]RouteDescriptor, 0, len(s.routes))
	for _, r := range s.routes {
		d := RouteDescriptor{
			LogicalName: r.name, Capabilities: r.capList, Healthy: r.lastError == "", LastError: r.lastError,
		}
		for _, a := range r.attempts {
			d.AttemptPlan = append(d.AttemptPlan, AttemptDescriptor{Provider: a.provider, Model: a.model})
		}
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
