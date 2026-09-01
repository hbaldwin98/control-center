// Package jobs is the plugin-facing durable work surface.
package jobs

import (
	"context"
	"errors"
	"time"
)

// Def is one named job a plugin declares. Handlers are not invoked during registration.
type Def struct {
	Name        string
	Handler     Handler
	Schedule    string // standard five-field cron; empty means enqueue-only
	TimeZone    string // IANA name; required when Schedule is set
	Timeout     time.Duration
	MaxAttempts int
	Backoff     BackoffPolicy
	Concurrency int
}

// BackoffPolicy is exponential delay between retryable failures.
type BackoffPolicy struct {
	Initial time.Duration
	Maximum time.Duration
}

// Handler performs one attempt.
type Handler func(jc Context) error

// Context is a context.Context cancelled on stop, timeout, and plugin disable.
type Context interface {
	context.Context

	JobID() int64
	Attempt() int
	Args(into any) error

	Progress(fraction float64, message string) error
	Logf(format string, args ...any) error
}

// Jobs is scoped to one plugin.
type Jobs interface {
	Enqueue(ctx context.Context, name string, args any, opts ...Opt) (int64, error)
	Cancel(ctx context.Context, id int64) error
	Get(ctx context.Context, id int64) (*Job, error)
	List(ctx context.Context, f Filter) ([]Job, error)
}

// Job is one persisted row plus, on Get, its log lines.
type Job struct {
	ID              int64
	Name            string
	Args            string
	State           string
	Attempt         int
	MaxAttempts     int
	Progress        float64
	ProgressMessage string
	LastError       string
	CancelReason    string
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
	Logs            []LogLine
}

// LogLine is one persisted handler log row.
type LogLine struct {
	Attempt int
	At      time.Time
	Line    string
}

// Filter selects jobs for List.
type Filter struct {
	Name  string
	State string
	Limit int
}

// Opt configures one Enqueue call.
type Opt func(*EnqueueOpts)

// EnqueueOpts is the resolved form of Opt. Pluginhost maps it onto the core queue.
type EnqueueOpts struct {
	RunAt          time.Time
	HasRunAt       bool
	IdempotencyKey string
	Priority       int
}

// ApplyOpts reduces opts to a single EnqueueOpts.
func ApplyOpts(opts []Opt) EnqueueOpts {
	var o EnqueueOpts
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// WithRunAt delays the first claim until t.
func WithRunAt(t time.Time) Opt {
	return func(o *EnqueueOpts) {
		o.RunAt = t
		o.HasRunAt = true
	}
}

// WithIdempotencyKey scopes uniqueness to (plugin, name, key) among nonterminal jobs.
func WithIdempotencyKey(k string) Opt {
	return func(o *EnqueueOpts) { o.IdempotencyKey = k }
}

// WithPriority prefers higher values at claim time. Default 0.
func WithPriority(p int) Opt {
	return func(o *EnqueueOpts) { o.Priority = p }
}

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

// IsPermanent reports whether err was wrapped with Permanent.
func IsPermanent(err error) bool {
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

// RetryAfterDelay returns the override delay if err was wrapped with RetryAfter.
func RetryAfterDelay(err error) (time.Duration, bool) {
	var r retryAfterError
	if errors.As(err, &r) {
		return r.d, true
	}
	return 0, false
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
)
