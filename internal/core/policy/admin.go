package policy

import (
	"context"
	"fmt"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// Register creates an unknown plugin disabled with reason awaiting_configuration. It never
// overwrites existing enabled state or budgets, and is idempotent.
//
// Changing Automated from false to true disables the plugin unless it already has a finite
// nonzero daily budget, so unattended work cannot start uncapped.
func (s *Store) Register(ctx context.Context, pluginID string, automated bool) error {
	if !validPluginID(pluginID) {
		return fmt.Errorf("%w: %q", ErrInvalidPluginID, pluginID)
	}

	return s.db.Tx(ctx, func(tx storage.Tx) error {
		st, err := s.readStateRow(ctx, tx, pluginID)
		switch {
		case err == nil:
		case isUnknown(err):
			if _, err := tx.Exec(ctx,
				`INSERT INTO core_plugin_state
				     (plugin_id, enabled, automated, disabled_at, disabled_by, disabled_reason)
				 VALUES (?, 0, ?, ?, ?, ?)`,
				pluginID, boolInt(automated), rfc(s.now().UTC()), ActorSystem,
				ReasonAwaitingConfiguration); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO core_plugin_budget(plugin_id) VALUES (?)`, pluginID)
			return err
		default:
			return err
		}

		if st.automated == automated {
			return nil // idempotent
		}
		if _, err := tx.Exec(ctx,
			`UPDATE core_plugin_state SET automated = ? WHERE plugin_id = ?`,
			boolInt(automated), pluginID); err != nil {
			return err
		}
		// Becoming automated without a finite daily cap closes the plugin.
		if automated && st.enabled && st.budget.Daily == 0 {
			return s.disableTx(ctx, tx, pluginID, ActorSystem, ReasonAutomatedNoBudget)
		}
		return nil
	})
}

// Enable turns a plugin on. Repeating an already-satisfied enable is a no-op and does not
// emit a duplicate transition event.
func (s *Store) Enable(ctx context.Context, pluginID, actor, reason string) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		st, err := s.readStateRow(ctx, tx, pluginID)
		if err != nil {
			return err
		}
		if st.enabled {
			return nil
		}
		// An automated plugin cannot be enabled with an unlimited daily budget.
		if st.automated && st.budget.Daily == 0 {
			return ErrAutomatedNeedsBudget
		}
		if _, err := tx.Exec(ctx,
			`UPDATE core_plugin_state
			    SET enabled = 1, disabled_at = NULL, disabled_by = '', disabled_reason = '',
			        accounting_failed_at = NULL
			  WHERE plugin_id = ?`, pluginID); err != nil {
			return err
		}
		if _, err := s.bus.PublishTx(ctx, tx, events.Input{
			Type:    events.TypePluginEnabled,
			Source:  events.SourcePolicy,
			Subject: pluginID,
			Payload: map[string]any{"plugin": pluginID, "actor": actor, "reason": reason},
		}); err != nil {
			return err
		}
		tx.AfterCommit(func() { s.notify(pluginID, true) })
		return nil
	})
}

// Disable closes new work and spend admission before it returns, then cancels registered
// contexts through Watch. It does not wait for arbitrary plugin code.
//
// A paid call whose reservation committed before this transaction is already in flight; it
// may finish and settle its charge. A competing reservation that commits after is rejected
// by the serialized state check.
func (s *Store) Disable(ctx context.Context, pluginID, actor, reason string) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		return s.disableTx(ctx, tx, pluginID, actor, reason)
	})
}

// disableTx writes the state change and its required event in one transaction. Watchers
// run only after commit, via AfterCommit, so a disable that joined a caller's transaction
// still cancels admitted contexts once that caller commits.
func (s *Store) disableTx(ctx context.Context, tx storage.Tx, pluginID, actor, reason string) error {
	st, err := s.readStateRow(ctx, tx, pluginID)
	if err != nil {
		return err
	}
	if !st.enabled {
		return nil // already satisfied: no duplicate transition event
	}
	if _, err := tx.Exec(ctx,
		`UPDATE core_plugin_state
		    SET enabled = 0, disabled_at = ?, disabled_by = ?, disabled_reason = ?
		  WHERE plugin_id = ?`,
		rfc(s.now().UTC()), actor, reason, pluginID); err != nil {
		return err
	}
	if _, err := s.bus.PublishTx(ctx, tx, events.Input{
		Type:    events.TypePluginDisabled,
		Source:  events.SourcePolicy,
		Subject: pluginID,
		Payload: map[string]any{"plugin": pluginID, "actor": actor, "reason": reason},
	}); err != nil {
		return err
	}
	tx.AfterCommit(func() { s.notify(pluginID, false) })
	return nil
}

// SetBudget replaces a plugin's budget.
//
// A negative limit is rejected. Lowering a finite limit below the current reserved plus
// committed spend is rejected, so a budget change can never retroactively invalidate an
// admitted call. An enabled automated plugin may not have an unlimited daily budget; the
// user must disable it first.
func (s *Store) SetBudget(ctx context.Context, pluginID string, b Budget) error {
	if b.Hourly < 0 || b.Daily < 0 || b.Monthly < 0 {
		return fmt.Errorf("%w: budget limits must be non-negative", ErrNegativeAmount)
	}
	if b.OnExceed == "" {
		b.OnExceed = ExceedReject
	}
	if !b.OnExceed.valid() {
		return fmt.Errorf("%w: %q", ErrInvalidExceed, b.OnExceed)
	}

	return s.db.Tx(ctx, func(tx storage.Tx) error {
		st, err := s.readStateRow(ctx, tx, pluginID)
		if err != nil {
			return err
		}
		if st.enabled && st.automated && b.Daily == 0 {
			return ErrAutomatedNeedsBudget
		}

		now := s.now().UTC()
		for _, w := range AllWindows {
			limit := b.limitFor(w)
			if limit == 0 {
				continue
			}
			reserved, committed, err := s.windowUsage(ctx, tx, pluginID, w, periodStart(w, now))
			if err != nil {
				return err
			}
			if reserved+committed > limit {
				return fmt.Errorf("%w: %s %s has %d reserved plus committed, above the new limit %d",
					ErrBudgetBelowSpend, pluginID, w, reserved+committed, limit)
			}
		}

		_, err = tx.Exec(ctx,
			`INSERT INTO core_plugin_budget
			     (plugin_id, hourly_microusd, daily_microusd, monthly_microusd, on_exceed)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(plugin_id) DO UPDATE SET
			     hourly_microusd  = excluded.hourly_microusd,
			     daily_microusd   = excluded.daily_microusd,
			     monthly_microusd = excluded.monthly_microusd,
			     on_exceed        = excluded.on_exceed`,
			pluginID, int64(b.Hourly), int64(b.Daily), int64(b.Monthly), string(b.OnExceed))
		return err
	})
}

// State reports one plugin's enabled state, budget, and live spend counters.
func (s *Store) State(ctx context.Context, pluginID string) (State, error) {
	st, err := s.readStateRow(ctx, s.db, pluginID)
	if err != nil {
		return State{}, err
	}
	return s.hydrate(ctx, s.db, st)
}

// List reports every registered plugin, ordered by ID.
func (s *Store) List(ctx context.Context) ([]State, error) {
	rows, err := s.db.Query(ctx, stateSelect+` ORDER BY st.plugin_id`)
	if err != nil {
		return nil, err
	}
	var raw []stateRow
	for rows.Next() {
		r, err := scanStateRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		raw = append(raw, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]State, 0, len(raw))
	for _, r := range raw {
		st, err := s.hydrate(ctx, s.db, r)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// hydrate fills the live reserved and committed counters for the current periods.
func (s *Store) hydrate(ctx context.Context, q querier, r stateRow) (State, error) {
	now := s.now().UTC()
	st := State{
		PluginID:       r.pluginID,
		Enabled:        r.enabled,
		Automated:      r.automated,
		DisabledBy:     r.disabledBy,
		DisabledReason: r.disabledReason,
		Budget:         r.budget,
	}
	if r.disabledAt != nil {
		st.DisabledAt = r.disabledAt
	}
	if r.accountingFailedAt != nil {
		st.AccountingFailed = r.accountingFailedAt
	}

	for _, w := range AllWindows {
		reserved, committed, err := s.windowUsage(ctx, q, r.pluginID, w, periodStart(w, now))
		if err != nil {
			return State{}, err
		}
		switch w {
		case WindowHour:
			st.ReservedHour, st.CommittedHour = reserved, committed
		case WindowDay:
			st.ReservedDay, st.CommittedDay = reserved, committed
		case WindowMonth:
			st.ReservedMonth, st.CommittedMonth = reserved, committed
		}
	}
	return st, nil
}

// validPluginID mirrors the event-source grammar: a plugin ID is one [a-z][a-z0-9_]*
// segment, so it can prefix both its tables and its event types.
func validPluginID(id string) bool {
	if id == "" || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			continue
		}
		return false
	}
	return true
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// parseTime returns nil for an absent or unparseable timestamp.
func parseTime(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return nil
	}
	return &t
}
