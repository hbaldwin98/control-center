// Package policy owns plugin enabled state, budgets, and atomic spend reservations, and
// answers whether a plugin may start work or a paid call.
//
// Layer 2. It imports storage and events. It exists so that ai, jobs, browser, and
// pluginhost can all ask the same question without pluginhost depending on ai.
package policy

import (
	"context"
	"errors"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// MicroUSD is integer micro-USD. All money is integer; negative values are invalid at
// every boundary.
type MicroUSD int64

// Gate answers whether a plugin may act, and owns the reservation lifecycle.
type Gate interface {
	CheckWork(ctx context.Context, pluginID string) error
	CheckWorkTx(ctx context.Context, tx storage.Tx, pluginID string) error

	// ReserveSpendTx admits one paid call inside the transaction that creates its
	// pending call record.
	//
	// A budget rejection is a DOMAIN result, not a technical failure: the events it
	// generates are written into tx and must be committed. See IsDomainRejection.
	ReserveSpendTx(ctx context.Context, tx storage.Tx, pluginID string, maximum MicroUSD) (Reservation, error)

	// SettleSpendTx replaces the reservation with actual committed cost inside the
	// caller's transaction. actual must be non-negative.
	SettleSpendTx(ctx context.Context, tx storage.Tx, reservationID string, actual MicroUSD) error

	// ReleaseSpendTx removes a reservation when the caller knows no charge occurred.
	ReleaseSpendTx(ctx context.Context, tx storage.Tx, reservationID string) error

	// Watch runs after an enabled-state commit. It is a cancellation signal, not a join:
	// it does not wait for arbitrary plugin code.
	Watch(fn func(pluginID string, enabled bool)) func()
}

// Reservation is one admitted paid call's capacity hold.
type Reservation struct {
	ID         string
	PluginID   string
	Maximum    MicroUSD
	AdmittedAt time.Time
}

// Admin is the administrative surface the web layer and pluginhost use.
type Admin interface {
	Register(ctx context.Context, pluginID string, automated bool) error
	Enable(ctx context.Context, pluginID, actor, reason string) error
	Disable(ctx context.Context, pluginID, actor, reason string) error
	SetBudget(ctx context.Context, pluginID string, b Budget) error
	State(ctx context.Context, pluginID string) (State, error)
	List(ctx context.Context) ([]State, error)
}

// State is one plugin's enabled state, budget, and live spend counters.
type State struct {
	PluginID       string     `json:"pluginId"`
	Enabled        bool       `json:"enabled"`
	Automated      bool       `json:"automated"`
	DisabledAt     *time.Time `json:"disabledAt"`
	DisabledBy     string     `json:"disabledBy"`
	DisabledReason string     `json:"disabledReason"`
	Budget         Budget     `json:"budget"`
	ReservedHour   MicroUSD   `json:"reservedHour"`
	CommittedHour  MicroUSD   `json:"committedHour"`
	ReservedDay    MicroUSD   `json:"reservedDay"`
	CommittedDay   MicroUSD   `json:"committedDay"`
	ReservedMonth  MicroUSD   `json:"reservedMonth"`
	CommittedMonth MicroUSD   `json:"committedMonth"`

	// AccountingFailed is set when a settlement exceeded its reserved maximum. The AI
	// route reports unhealthy while it is set; an operator clears it by re-enabling.
	AccountingFailed *time.Time `json:"accountingFailed"`
}

// Budget bounds a plugin's spend. A zero limit is unlimited.
type Budget struct {
	Hourly   MicroUSD     `json:"hourly"`
	Daily    MicroUSD     `json:"daily"`
	Monthly  MicroUSD     `json:"monthly"`
	OnExceed ExceedAction `json:"onExceed"`
}

// ExceedAction is what happens when a reservation would cross a finite limit.
type ExceedAction string

const (
	// ExceedReject rejects the call and leaves the plugin enabled.
	ExceedReject ExceedAction = "reject"
	// ExceedDisable also disables the plugin, in the same transaction.
	ExceedDisable ExceedAction = "disable"
)

func (a ExceedAction) valid() bool {
	return a == ExceedReject || a == ExceedDisable
}

// Errors returned across the policy boundary.
var (
	// ErrPluginDisabled denies work or spend. Every gate returns it for an unknown
	// plugin ID too, wrapped in ErrUnknownPlugin so the cause stays visible.
	ErrPluginDisabled = errors.New("plugin disabled")

	// ErrBudgetExceeded is a domain rejection. Its events are already in the caller's
	// transaction and must be committed.
	ErrBudgetExceeded = errors.New("budget exceeded")

	ErrUnknownPlugin        = errors.New("policy: unknown plugin")
	ErrUnknownReservation   = errors.New("policy: unknown reservation")
	ErrInvalidPluginID      = errors.New("policy: invalid plugin ID")
	ErrNegativeAmount       = errors.New("policy: negative amount")
	ErrBudgetBelowSpend     = errors.New("policy: budget is below current reserved plus committed spend")
	ErrAutomatedNeedsBudget = errors.New("policy: an automated plugin needs a finite daily budget")
	ErrInvalidExceed        = errors.New("policy: unknown onExceed action")
)

// IsDomainRejection reports whether err is a policy decision whose accompanying events are
// already written into the caller's transaction, rather than a technical failure.
//
// A caller inside storage.DB.Tx must return nil from its callback so those events commit,
// then surface err after the transaction:
//
//	var rejected error
//	err := db.Tx(ctx, func(tx storage.Tx) error {
//	    res, rerr := gate.ReserveSpendTx(ctx, tx, id, max)
//	    if policy.IsDomainRejection(rerr) {
//	        rejected = rerr // its events are in tx; commit them
//	        return nil
//	    }
//	    if rerr != nil {
//	        return rerr // technical failure: roll back
//	    }
//	    ...
//	})
//	if err != nil { return err }
//	if rejected != nil { return rejected }
func IsDomainRejection(err error) bool {
	return errors.Is(err, ErrBudgetExceeded)
}

// Window names a budget period.
type Window string

const (
	WindowHour  Window = "hour"
	WindowDay   Window = "day"
	WindowMonth Window = "month"
)

// AllWindows is the fixed set, in increasing period length.
var AllWindows = []Window{WindowHour, WindowDay, WindowMonth}

// limitFor returns the configured limit for a window. Zero is unlimited.
func (b Budget) limitFor(w Window) MicroUSD {
	switch w {
	case WindowHour:
		return b.Hourly
	case WindowDay:
		return b.Daily
	case WindowMonth:
		return b.Monthly
	}
	return 0
}

// periodStart truncates t to the UTC period containing it. Reservations pin the UTC hour,
// UTC day, and UTC calendar month of admission.
func periodStart(w Window, t time.Time) time.Time {
	t = t.UTC()
	switch w {
	case WindowHour:
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
	case WindowDay:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	case WindowMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return t
}
