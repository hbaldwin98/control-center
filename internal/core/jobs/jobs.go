// Package jobs runs work durably with at-least-once delivery, leases, retries,
// cancellation, and fenced progress.
//
// Layer 3. It imports storage, events, and policy. ai does not import it.
package jobs

import (
	"context"
	"errors"
	"time"
)

// State is the persisted lifecycle of one job.
type State string

const (
	StatePending         State = "pending"
	StateRunning         State = "running"
	StateRetryWait       State = "retry_wait"
	StateSucceeded       State = "succeeded"
	StateFailed          State = "failed"
	StateDead            State = "dead"
	StateCancelRequested State = "cancel_requested"
	StateCancelled       State = "cancelled"
)

// Terminal reports whether the state cannot claim or run again.
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateDead, StateCancelled:
		return true
	}
	return false
}

// Cancel reasons recorded on a cancelled job.
const (
	ReasonUser           = "user"
	ReasonPluginDisabled = "plugin_disabled"
	ReasonShutdown       = "shutdown"
)

// Error classes persisted with failed attempts.
const (
	ClassError     = "error"
	ClassPanic     = "panic"
	ClassTimeout   = "timeout"
	ClassPermanent = "permanent"
)

// Def is one named job a plugin declares.
type Def struct {
	Name        string
	Handler     Handler
	Schedule    string        // standard five-field cron; empty means enqueue-only
	TimeZone    string        // IANA name; required when Schedule is set
	Timeout     time.Duration // default 30m
	MaxAttempts int           // default 3
	Backoff     BackoffPolicy
	Concurrency int // per plugin and job name; default 1
}

// BackoffPolicy is exponential delay between retryable failures.
type BackoffPolicy struct {
	Initial time.Duration // default 1s
	Maximum time.Duration // default 5m
}

// Handler is the plugin function that performs one attempt.
type Handler func(jc Context) error

// Context is a context.Context cancelled on stop, timeout, and plugin disable.
// Progress and Logf return a lease/fence error when the worker no longer owns the
// attempt; handlers should stop when either fails.
type Context interface {
	context.Context

	JobID() int64
	Attempt() int
	Args(into any) error

	Progress(fraction float64, message string) error
	Logf(format string, args ...any) error
}

// Jobs is the plugin-facing surface. pluginhost scopes it to one plugin ID.
type Jobs interface {
	Enqueue(ctx context.Context, name string, args any, opts ...Opt) (int64, error)
	Cancel(ctx context.Context, id int64) error
	Get(ctx context.Context, id int64) (*Job, error)
	List(ctx context.Context, f Filter) ([]Job, error)
}

// Job is one persisted row plus, on Get, its log lines.
type Job struct {
	ID              int64      `json:"id"`
	PluginID        string     `json:"pluginId"`
	Name            string     `json:"name"`
	Args            string     `json:"args"`
	State           State      `json:"state"`
	Attempt         int        `json:"attempt"`
	MaxAttempts     int        `json:"maxAttempts"`
	Priority        int        `json:"priority"`
	IdempotencyKey  string     `json:"idempotencyKey,omitempty"`
	RunAt           time.Time  `json:"runAt"`
	NextRetryAt     *time.Time `json:"nextRetryAt,omitempty"`
	FenceGeneration int64      `json:"fenceGeneration"`
	CancelReason    string     `json:"cancelReason,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	Progress        float64    `json:"progress"`
	ProgressMessage string     `json:"progressMessage,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	LastErrorClass  string     `json:"lastErrorClass,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	Logs            []LogLine  `json:"logs,omitempty"`
}

// LogLine is one persisted handler log row.
type LogLine struct {
	ID      int64     `json:"id"`
	Attempt int       `json:"attempt"`
	At      time.Time `json:"at"`
	Line    string    `json:"line"`
}

// Filter selects jobs for List. An empty State returns every state.
type Filter struct {
	PluginID string
	Name     string
	State    State
	Limit    int
}

// Opt configures one Enqueue call.
type Opt func(*enqueueOpts)

type enqueueOpts struct {
	runAt          time.Time
	idempotencyKey string
	priority       int
	hasRunAt       bool
}

// WithRunAt delays the first claim until t.
func WithRunAt(t time.Time) Opt {
	return func(o *enqueueOpts) {
		o.runAt = t
		o.hasRunAt = true
	}
}

// WithIdempotencyKey scopes uniqueness to (plugin, name, key) among nonterminal jobs.
func WithIdempotencyKey(k string) Opt {
	return func(o *enqueueOpts) { o.idempotencyKey = k }
}

// WithPriority prefers higher values at claim time. Default 0.
func WithPriority(p int) Opt {
	return func(o *enqueueOpts) { o.priority = p }
}

// Errors returned across the jobs boundary.
var (
	ErrUnknownJob      = errors.New("jobs: unknown job")
	ErrUnknownDef      = errors.New("jobs: unknown job definition")
	ErrInvalidName     = errors.New("jobs: invalid name")
	ErrInvalidSchedule = errors.New("jobs: invalid cron schedule")
	ErrLostLease       = errors.New("jobs: lost lease")
	ErrAlreadyTerminal = errors.New("jobs: job is already terminal")
	ErrNotCancellable  = errors.New("jobs: job cannot be cancelled")
	ErrPluginMismatch  = errors.New("jobs: job belongs to another plugin")
	ErrArgsTooLarge    = errors.New("jobs: args exceed 256 KiB")
)

const maxArgsBytes = 256 << 10

// Permanent records failed without retry.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func isPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// RetryAfter overrides backoff for this failure.
func RetryAfter(err error, d time.Duration) error {
	if err == nil {
		return nil
	}
	return retryAfterError{err: err, d: d}
}

type retryAfterError struct {
	err error
	d   time.Duration
}

func (e retryAfterError) Error() string { return e.err.Error() }
func (e retryAfterError) Unwrap() error { return e.err }

func retryAfterDelay(err error) (time.Duration, bool) {
	var r retryAfterError
	if errors.As(err, &r) {
		return r.d, true
	}
	return 0, false
}
