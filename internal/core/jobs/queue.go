package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// Options configures the queue and its workers.
type Options struct {
	PollInterval   time.Duration
	LeaseTTL       time.Duration
	Heartbeat      time.Duration
	CancelGrace    time.Duration
	ProgressMinGap time.Duration
	Owner          string
	// Retention is the minimum age before a finished job may be deleted.
	Retention time.Duration
	Now       func() time.Time
}

func (o *Options) applyDefaults() {
	if o.PollInterval <= 0 {
		o.PollInterval = 150 * time.Millisecond
	}
	if o.LeaseTTL <= 0 {
		o.LeaseTTL = 30 * time.Second
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = 10 * time.Second
	}
	if o.CancelGrace <= 0 {
		o.CancelGrace = 10 * time.Second
	}
	if o.Retention <= 0 {
		// Long enough that a person can still read why last week's run failed, short
		// enough that the table does not grow without bound.
		o.Retention = 14 * 24 * time.Hour
	}
	if o.ProgressMinGap <= 0 {
		o.ProgressMinGap = time.Second
	}
	if o.Owner == "" {
		o.Owner = fmt.Sprintf("pid-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// Queue is the concrete unscoped job store and worker.
type Queue struct {
	db   storage.DB
	bus  events.Bus
	gate policy.Gate
	opts Options

	mu   sync.Mutex
	defs map[string]Def // pluginID + "\x00" + name

	wake chan struct{}

	attMu    sync.Mutex
	attempts map[int64]*runningAttempt

	stop    context.CancelFunc
	stopped chan struct{}
	unwatch func()
}

type runningAttempt struct {
	jobID    int64
	pluginID string
	name     string
	attempt  int
	fence    int64
	owner    string
	cancel   context.CancelCauseFunc
	timedOut bool
}

func defKey(pluginID, name string) string { return pluginID + "\x00" + name }

func (q *Queue) now() time.Time { return q.opts.Now().UTC() }

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func parseTimePtr(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return nil
	}
	return &t
}

// New applies migrations and returns a queue. Call Start to run workers and cron.
func New(m storage.Migrator, db storage.DB, bus events.Bus, gate policy.Gate, opts Options) (*Queue, error) {
	if err := m.Apply("jobs", migrations); err != nil {
		return nil, err
	}
	opts.applyDefaults()
	return &Queue{
		db:       db,
		bus:      bus,
		gate:     gate,
		opts:     opts,
		defs:     map[string]Def{},
		wake:     make(chan struct{}, 1),
		attempts: map[int64]*runningAttempt{},
		stopped:  make(chan struct{}),
	}, nil
}

// Register installs or replaces a job definition. It is safe before or after Start.
func (q *Queue) Register(pluginID string, def Def) error {
	if !validName(pluginID) {
		return fmt.Errorf("%w: plugin %q", ErrInvalidName, pluginID)
	}
	if !validName(def.Name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, def.Name)
	}
	if def.Handler == nil {
		return fmt.Errorf("jobs: %s.%s needs a handler", pluginID, def.Name)
	}
	if def.Timeout <= 0 {
		def.Timeout = 30 * time.Minute
	}
	if def.MaxAttempts <= 0 {
		def.MaxAttempts = 3
	}
	if def.Concurrency <= 0 {
		def.Concurrency = 1
	}
	if def.Backoff.Initial <= 0 {
		def.Backoff.Initial = time.Second
	}
	if def.Backoff.Maximum <= 0 {
		def.Backoff.Maximum = 5 * time.Minute
	}
	if def.Backoff.Maximum < def.Backoff.Initial {
		return fmt.Errorf("jobs: %s.%s backoff maximum is below initial", pluginID, def.Name)
	}
	if def.Schedule != "" {
		if def.TimeZone == "" {
			return fmt.Errorf("%w: %s.%s needs an IANA TimeZone", ErrInvalidSchedule, pluginID, def.Name)
		}
		if _, err := time.LoadLocation(def.TimeZone); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidSchedule, def.TimeZone, err)
		}
		if _, err := parseCron(def.Schedule); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidSchedule, def.Schedule, err)
		}
	}

	q.mu.Lock()
	q.defs[defKey(pluginID, def.Name)] = def
	q.mu.Unlock()
	return nil
}

// UnregisterAll drops in-memory handlers for pluginID. Schedule rows stay so a later
// CatchUpSchedules can skip slots that passed while the plugin was down.
func (q *Queue) UnregisterAll(pluginID string) {
	prefix := defKey(pluginID, "")
	q.mu.Lock()
	defer q.mu.Unlock()
	for k := range q.defs {
		if strings.HasPrefix(k, prefix) {
			delete(q.defs, k)
		}
	}
}

func (q *Queue) lookupDef(pluginID, name string) (Def, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	d, ok := q.defs[defKey(pluginID, name)]
	return d, ok
}

// Start runs the claim loop, cron ticks, and policy-disable cancellation until ctx ends.
func (q *Queue) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	q.stop = cancel
	if q.gate != nil {
		q.unwatch = q.gate.Watch(func(pluginID string, enabled bool) {
			if !enabled {
				q.cancelPlugin(pluginID, ReasonPluginDisabled)
			}
		})
	}
	go q.loop(runCtx)
}

// Stop cancels admitted attempts with shutdown and waits for the loop to exit.
func (q *Queue) Stop() {
	if q.unwatch != nil {
		q.unwatch()
		q.unwatch = nil
	}
	q.cancelAll(ReasonShutdown)
	if q.stop != nil {
		q.stop()
		<-q.stopped
	}
}

func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *Queue) loop(ctx context.Context) {
	defer close(q.stopped)
	ticker := time.NewTicker(q.opts.PollInterval)
	defer ticker.Stop()
	for {
		q.claimUntilEmpty(ctx)
		q.tickCron(ctx)
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		case <-ticker.C:
		}
	}
}

func (q *Queue) publishJob(ctx context.Context, tx storage.Tx, typ string, job jobRow, extra map[string]any) error {
	payload := map[string]any{
		"jobId":  job.id,
		"plugin": job.pluginID,
		"name":   job.name,
	}
	for k, v := range extra {
		payload[k] = v
	}
	_, err := q.bus.PublishTx(ctx, tx, events.Input{
		Type:    typ,
		Source:  events.SourceJobs,
		Subject: fmt.Sprintf("%d", job.id),
		Payload: payload,
	})
	return err
}

func argsDigest(args []byte) string {
	sum := sha256.Sum256(args)
	return hex.EncodeToString(sum[:])
}

func marshalArgs(args any) ([]byte, error) {
	if args == nil {
		return []byte("null"), nil
	}
	b, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("jobs: marshal args: %w", err)
	}
	if len(b) > maxArgsBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrArgsTooLarge, len(b))
	}
	return b, nil
}

func validName(id string) bool {
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

func (b BackoffPolicy) delay(attempt int) time.Duration {
	d := b.Initial
	for range attempt - 1 {
		d *= 2
		if d >= b.Maximum {
			return b.Maximum
		}
	}
	return d
}
