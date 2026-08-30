package policy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// Reasons policy itself records for a disable.
const (
	ReasonAwaitingConfiguration = "awaiting_configuration"
	ReasonBudgetExceeded        = "budget_exceeded"
	ReasonAccountingInvariant   = "accounting_invariant_failed"
	ReasonAutomatedNoBudget     = "automated_without_daily_budget"

	// ActorSystem is the actor recorded for a transition policy made itself.
	ActorSystem = "system"
)

// Store is the concrete Gate and Admin.
type Store struct {
	db  storage.DB
	bus events.Bus
	now func() time.Time

	mu        sync.Mutex
	watchers  map[int64]func(string, bool)
	nextWatch int64
}

var (
	_ Gate  = (*Store)(nil)
	_ Admin = (*Store)(nil)
)

// New applies the policy migrations and returns the store.
func New(m storage.Migrator, db storage.DB, bus events.Bus, now func() time.Time) (*Store, error) {
	if err := m.Apply("policy", migrations); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, bus: bus, now: now, watchers: map[int64]func(string, bool){}}, nil
}

// ---- admission ----

// CheckWork reports whether a plugin may start host-managed work.
func (s *Store) CheckWork(ctx context.Context, pluginID string) error {
	st, err := s.readStateRow(ctx, s.db, pluginID)
	if err != nil {
		return err
	}
	if !st.enabled {
		return ErrPluginDisabled
	}
	return nil
}

// CheckWorkTx is CheckWork inside a caller's transaction, so admission and the work it
// admits commit or roll back together.
func (s *Store) CheckWorkTx(ctx context.Context, tx storage.Tx, pluginID string) error {
	st, err := s.readStateRow(ctx, tx, pluginID)
	if err != nil {
		return err
	}
	if !st.enabled {
		return ErrPluginDisabled
	}
	return nil
}

// ---- reservations ----

// ReserveSpendTx admits one paid call. Admission succeeds only when, for every finite
// limit, reserved + committed + maximum <= limit.
//
// A budget rejection returns ErrBudgetExceeded after writing its events into tx. The
// caller must commit that transaction; see IsDomainRejection.
func (s *Store) ReserveSpendTx(ctx context.Context, tx storage.Tx, pluginID string, maximum MicroUSD) (Reservation, error) {
	if maximum < 0 {
		return Reservation{}, fmt.Errorf("%w: maximum %d", ErrNegativeAmount, maximum)
	}
	st, err := s.readStateRow(ctx, tx, pluginID)
	if err != nil {
		return Reservation{}, err
	}
	if !st.enabled {
		return Reservation{}, ErrPluginDisabled
	}

	now := s.now().UTC()
	starts := map[Window]time.Time{}
	for _, w := range AllWindows {
		starts[w] = periodStart(w, now)
	}

	for _, w := range AllWindows {
		limit := st.budget.limitFor(w)
		if limit == 0 {
			continue // unlimited
		}
		reserved, committed, err := s.windowUsage(ctx, tx, pluginID, w, starts[w])
		if err != nil {
			return Reservation{}, err
		}
		if reserved+committed+maximum <= limit {
			continue
		}
		// A domain rejection: record it, act on OnExceed, and let the caller commit.
		if err := s.recordBudgetExceeded(ctx, tx, pluginID, w, starts[w],
			limit, reserved, committed, st.budget.OnExceed); err != nil {
			return Reservation{}, err
		}
		if st.budget.OnExceed == ExceedDisable {
			if err := s.disableTx(ctx, tx, pluginID, ActorSystem, ReasonBudgetExceeded); err != nil {
				return Reservation{}, err
			}
		}
		return Reservation{}, fmt.Errorf("%w: %s %s limit %d", ErrBudgetExceeded, pluginID, w, limit)
	}

	res := Reservation{
		ID:         newID(),
		PluginID:   pluginID,
		Maximum:    maximum,
		AdmittedAt: now,
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO core_plugin_spend_reservations
		     (id, plugin_id, maximum_microusd, admitted_at, hour_start, day_start, month_start)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		res.ID, pluginID, int64(maximum), rfc(now),
		rfc(starts[WindowHour]), rfc(starts[WindowDay]), rfc(starts[WindowMonth]))
	if err != nil {
		return Reservation{}, err
	}
	return res, nil
}

// SettleSpendTx replaces a reservation with the actual committed cost, in the same periods
// the reservation pinned.
//
// Accounting is never rolled back merely because a cap was crossed: the truthful charge is
// always recorded. If actual exceeds the reserved maximum, the plugin is atomically
// disabled and the invariant failure is published.
func (s *Store) SettleSpendTx(ctx context.Context, tx storage.Tx, reservationID string, actual MicroUSD) error {
	if actual < 0 {
		return fmt.Errorf("%w: actual %d", ErrNegativeAmount, actual)
	}
	res, starts, err := s.takeReservation(ctx, tx, reservationID)
	if err != nil {
		return err
	}

	for _, w := range AllWindows {
		if _, err := tx.Exec(ctx,
			`INSERT INTO core_plugin_spend(plugin_id, window, period_start, committed_microusd)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(plugin_id, window, period_start)
			 DO UPDATE SET committed_microusd = committed_microusd + excluded.committed_microusd`,
			res.PluginID, string(w), rfc(starts[w]), int64(actual)); err != nil {
			return err
		}
	}

	if actual <= res.Maximum {
		return nil
	}

	// The provider adapter must make this impossible. It happened anyway, so record the
	// truth, stop the plugin, and say so loudly.
	slog.Error("policy: settled cost exceeded its reserved maximum",
		"plugin", res.PluginID, "reservation", res.ID,
		"reservedMaximum", res.Maximum, "actual", actual)

	if _, err := tx.Exec(ctx,
		`UPDATE core_plugin_state SET accounting_failed_at = ? WHERE plugin_id = ?`,
		rfc(s.now().UTC()), res.PluginID); err != nil {
		return err
	}
	if _, err := s.bus.PublishTx(ctx, tx, events.Input{
		Type:    events.TypePluginAccountingInvariantFailed,
		Source:  events.SourcePolicy,
		Subject: res.PluginID,
		Payload: map[string]any{
			"plugin":          res.PluginID,
			"reservationId":   res.ID,
			"reservedMaximum": int64(res.Maximum),
			"actualCost":      int64(actual),
		},
	}); err != nil {
		return err
	}
	if err := s.disableTx(ctx, tx, res.PluginID, ActorSystem, ReasonAccountingInvariant); err != nil {
		return err
	}

	// Emit budget_exceeded only for each finite window the truthful charge actually
	// crossed, not for the cap violation itself.
	st, err := s.readStateRow(ctx, tx, res.PluginID)
	if err != nil {
		return err
	}
	for _, w := range AllWindows {
		limit := st.budget.limitFor(w)
		if limit == 0 {
			continue
		}
		reserved, committed, err := s.windowUsage(ctx, tx, res.PluginID, w, starts[w])
		if err != nil {
			return err
		}
		if reserved+committed <= limit {
			continue
		}
		if err := s.recordBudgetExceeded(ctx, tx, res.PluginID, w, starts[w],
			limit, reserved, committed, st.budget.OnExceed); err != nil {
			return err
		}
	}
	return nil
}

// ReleaseSpendTx removes a reservation without changing committed cost. Use it only when
// the caller knows no charge occurred.
func (s *Store) ReleaseSpendTx(ctx context.Context, tx storage.Tx, reservationID string) error {
	_, _, err := s.takeReservation(ctx, tx, reservationID)
	return err
}

// SettleOrphanedReservations settles every reservation left behind by a crash at its
// reserved maximum. A call whose outcome is unknowable is counted at the most it could
// have cost rather than silently undercounted.
func (s *Store) SettleOrphanedReservations(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, maximum_microusd FROM core_plugin_spend_reservations ORDER BY admitted_at`)
	if err != nil {
		return 0, err
	}
	type orphan struct {
		id      string
		maximum MicroUSD
	}
	var orphans []orphan
	for rows.Next() {
		var o orphan
		var max int64
		if err := rows.Scan(&o.id, &max); err != nil {
			rows.Close()
			return 0, err
		}
		o.maximum = MicroUSD(max)
		orphans = append(orphans, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, o := range orphans {
		err := s.db.Tx(ctx, func(tx storage.Tx) error {
			return s.SettleSpendTx(ctx, tx, o.id, o.maximum)
		})
		if err != nil {
			return 0, err
		}
	}
	if len(orphans) > 0 {
		slog.Warn("policy: settled reservations left by a previous run at their reserved maximum",
			"count", len(orphans))
	}
	return len(orphans), nil
}

// ---- watches ----

// Watch registers a cancellation signal invoked after an enabled-state commit.
func (s *Store) Watch(fn func(pluginID string, enabled bool)) func() {
	s.mu.Lock()
	s.nextWatch++
	id := s.nextWatch
	s.watchers[id] = fn
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		delete(s.watchers, id)
		s.mu.Unlock()
	}
}

// notify runs watchers after a commit. It does not wait for arbitrary plugin code.
func (s *Store) notify(pluginID string, enabled bool) {
	s.mu.Lock()
	fns := make([]func(string, bool), 0, len(s.watchers))
	for _, fn := range s.watchers {
		fns = append(fns, fn)
	}
	s.mu.Unlock()

	for _, fn := range fns {
		fn(pluginID, enabled)
	}
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("policy: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
