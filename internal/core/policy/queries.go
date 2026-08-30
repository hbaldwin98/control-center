package policy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// querier is the read surface shared by storage.DB and storage.Tx, so every helper works
// both standalone and inside a caller's transaction.
type querier interface {
	Query(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, args ...any) *sql.Row
}

// stateRow is the raw joined state and budget.
type stateRow struct {
	pluginID           string
	enabled            bool
	automated          bool
	disabledAt         *time.Time
	disabledBy         string
	disabledReason     string
	accountingFailedAt *time.Time
	budget             Budget
}

const stateSelect = `
SELECT st.plugin_id, st.enabled, st.automated, st.disabled_at, st.disabled_by,
       st.disabled_reason, st.accounting_failed_at,
       coalesce(b.hourly_microusd, 0), coalesce(b.daily_microusd, 0),
       coalesce(b.monthly_microusd, 0), coalesce(b.on_exceed, 'reject')
  FROM core_plugin_state st
  LEFT JOIN core_plugin_budget b ON b.plugin_id = st.plugin_id`

type scanner interface{ Scan(dest ...any) error }

func scanStateRow(row scanner) (stateRow, error) {
	var r stateRow
	var enabled, automated int
	var disabledAt, accountingFailedAt *string
	var hourly, daily, monthly int64
	var onExceed string

	if err := row.Scan(&r.pluginID, &enabled, &automated, &disabledAt, &r.disabledBy,
		&r.disabledReason, &accountingFailedAt,
		&hourly, &daily, &monthly, &onExceed); err != nil {
		return stateRow{}, err
	}
	r.enabled = enabled != 0
	r.automated = automated != 0
	r.disabledAt = parseTime(disabledAt)
	r.accountingFailedAt = parseTime(accountingFailedAt)
	r.budget = Budget{
		Hourly:   MicroUSD(hourly),
		Daily:    MicroUSD(daily),
		Monthly:  MicroUSD(monthly),
		OnExceed: ExceedAction(onExceed),
	}
	return r, nil
}

// readStateRow loads one plugin. An unknown plugin ID is denied by every gate, so the
// error wraps ErrPluginDisabled while keeping the real cause visible.
func (s *Store) readStateRow(ctx context.Context, q querier, pluginID string) (stateRow, error) {
	r, err := scanStateRow(q.QueryRow(ctx, stateSelect+` WHERE st.plugin_id = ?`, pluginID))
	if storage.IsNoRows(err) {
		return stateRow{}, unknownPlugin(pluginID)
	}
	if err != nil {
		return stateRow{}, err
	}
	return r, nil
}

// unknownPluginError denies the gate and names the cause.
type unknownPluginError struct{ id string }

func (e unknownPluginError) Error() string {
	return fmt.Sprintf("policy: unknown plugin %q", e.id)
}

// Is makes both errors.Is(err, ErrUnknownPlugin) and errors.Is(err, ErrPluginDisabled)
// true: an unregistered plugin is denied, and the reason is that policy has never heard
// of it.
func (e unknownPluginError) Is(target error) bool {
	return target == ErrUnknownPlugin || target == ErrPluginDisabled
}

func unknownPlugin(id string) error { return unknownPluginError{id: id} }

func isUnknown(err error) bool { return errors.Is(err, ErrUnknownPlugin) }

// windowUsage reports outstanding reservations and committed spend for one plugin, window,
// and period.
func (s *Store) windowUsage(ctx context.Context, q querier, pluginID string, w Window, start time.Time) (reserved, committed MicroUSD, err error) {
	column := map[Window]string{
		WindowHour:  "hour_start",
		WindowDay:   "day_start",
		WindowMonth: "month_start",
	}[w]
	if column == "" {
		return 0, 0, fmt.Errorf("policy: unknown window %q", w)
	}

	var res int64
	// The column name comes from the fixed map above, never from a caller.
	if err := q.QueryRow(ctx,
		`SELECT coalesce(sum(maximum_microusd), 0)
		   FROM core_plugin_spend_reservations
		  WHERE plugin_id = ? AND `+column+` = ?`, pluginID, rfc(start)).Scan(&res); err != nil {
		return 0, 0, err
	}

	var com int64
	err = q.QueryRow(ctx,
		`SELECT committed_microusd FROM core_plugin_spend
		  WHERE plugin_id = ? AND window = ? AND period_start = ?`,
		pluginID, string(w), rfc(start)).Scan(&com)
	if err != nil && !storage.IsNoRows(err) {
		return 0, 0, err
	}
	return MicroUSD(res), MicroUSD(com), nil
}

// takeReservation reads and removes a reservation, returning it with the periods it
// pinned at admission.
func (s *Store) takeReservation(ctx context.Context, tx storage.Tx, id string) (Reservation, map[Window]time.Time, error) {
	var res Reservation
	var maximum int64
	var admitted, hour, day, month string

	err := tx.QueryRow(ctx,
		`SELECT plugin_id, maximum_microusd, admitted_at, hour_start, day_start, month_start
		   FROM core_plugin_spend_reservations WHERE id = ?`, id).
		Scan(&res.PluginID, &maximum, &admitted, &hour, &day, &month)
	if storage.IsNoRows(err) {
		return Reservation{}, nil, fmt.Errorf("%w: %s", ErrUnknownReservation, id)
	}
	if err != nil {
		return Reservation{}, nil, err
	}

	res.ID = id
	res.Maximum = MicroUSD(maximum)
	res.AdmittedAt, _ = time.Parse(time.RFC3339Nano, admitted)

	starts := map[Window]time.Time{}
	for w, raw := range map[Window]string{WindowHour: hour, WindowDay: day, WindowMonth: month} {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Reservation{}, nil, fmt.Errorf("policy: reservation %s has an unparseable %s period: %w", id, w, err)
		}
		starts[w] = t
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM core_plugin_spend_reservations WHERE id = ?`, id); err != nil {
		return Reservation{}, nil, err
	}
	return res, starts, nil
}

// recordBudgetExceeded publishes core.plugin.budget_exceeded at most once per plugin,
// window, and period, so repeated rejected calls cannot flood alerting.
func (s *Store) recordBudgetExceeded(ctx context.Context, tx storage.Tx, pluginID string,
	w Window, start time.Time, limit, reserved, committed MicroUSD, action ExceedAction) error {

	var existing int64
	err := tx.QueryRow(ctx,
		`SELECT event_id FROM core_plugin_budget_events
		  WHERE plugin_id = ? AND window = ? AND period_start = ?`,
		pluginID, string(w), rfc(start)).Scan(&existing)
	if err == nil {
		return nil // already reported for this period
	}
	if !storage.IsNoRows(err) {
		return err
	}

	eventID, err := s.bus.PublishTx(ctx, tx, events.Input{
		Type:    events.TypePluginBudgetExceeded,
		Source:  events.SourcePolicy,
		Subject: pluginID,
		Payload: map[string]any{
			"plugin":    pluginID,
			"window":    string(w),
			"limit":     int64(limit),
			"reserved":  int64(reserved),
			"committed": int64(committed),
			"action":    string(action),
		},
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO core_plugin_budget_events(plugin_id, window, period_start, event_id)
		 VALUES (?, ?, ?, ?)`,
		pluginID, string(w), rfc(start), eventID)
	return err
}
