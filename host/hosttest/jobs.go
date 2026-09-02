package hosttest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

// jobsImpl is the durable work surface. Nothing runs on its own: Enqueue persists a
// row and returns, and the test decides when work happens by calling Harness.RunJob or
// Harness.Drain. Cron schedules are recorded but never fire; a test that wants the
// scheduled path calls the job by name.
type jobsImpl struct {
	mu     sync.Mutex
	id     string
	defs   map[string]hostjobs.Def
	jobs   map[int64]*hostjobs.Job
	nextID int64
	clock  *Clock
	gate   func(mutating bool) error
	logs   map[int64][]hostjobs.LogLine
	idemp  map[string]int64
}

const (
	stateQueued    = "queued"
	stateRunning   = "running"
	stateSucceeded = "succeeded"
	stateFailed    = "failed"
	stateCancelled = "cancelled"
)

func terminal(state string) bool {
	return state == stateSucceeded || state == stateFailed || state == stateCancelled
}

func (j *jobsImpl) Enqueue(ctx context.Context, name string, args any, opts ...hostjobs.Opt) (int64, error) {
	if err := j.gate(true); err != nil {
		return 0, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return 0, err
	}
	o := hostjobs.ApplyOpts(opts)

	j.mu.Lock()
	defer j.mu.Unlock()
	def, ok := j.defs[name]
	if !ok {
		return 0, fmt.Errorf("%w: %s", hostjobs.ErrUnknownDef, name)
	}
	// Idempotency is scoped to (plugin, name, key) among nonterminal jobs, so a retry
	// after a job finished enqueues a new one.
	var key string
	if o.IdempotencyKey != "" {
		key = name + "\x00" + o.IdempotencyKey
		if id, ok := j.idemp[key]; ok {
			if existing, ok := j.jobs[id]; ok && !terminal(existing.State) {
				return id, nil
			}
			delete(j.idemp, key)
		}
	}
	j.nextID++
	if key != "" {
		j.idemp[key] = j.nextID
	}
	max := def.MaxAttempts
	if max <= 0 {
		max = 1
	}
	j.jobs[j.nextID] = &hostjobs.Job{
		ID:          j.nextID,
		Name:        name,
		Args:        string(raw),
		State:       stateQueued,
		MaxAttempts: max,
		CreatedAt:   j.clock.Now(),
	}
	return j.nextID, nil
}

func (j *jobsImpl) Cancel(ctx context.Context, id int64) error {
	if err := j.gate(true); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.jobs[id]
	if !ok {
		return fmt.Errorf("%w: %d", hostjobs.ErrUnknownJob, id)
	}
	if terminal(job.State) {
		return fmt.Errorf("%w: %d", hostjobs.ErrAlreadyTerminal, id)
	}
	now := j.clock.Now()
	job.State = stateCancelled
	job.CancelReason = "cancelled by plugin"
	job.FinishedAt = &now
	return nil
}

func (j *jobsImpl) Get(ctx context.Context, id int64) (*hostjobs.Job, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.jobs[id]
	if !ok {
		return nil, fmt.Errorf("%w: %d", hostjobs.ErrUnknownJob, id)
	}
	out := *job
	out.Logs = append([]hostjobs.LogLine(nil), j.logs[id]...)
	return &out, nil
}

func (j *jobsImpl) List(ctx context.Context, f hostjobs.Filter) ([]hostjobs.Job, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]hostjobs.Job, 0, len(j.jobs))
	for _, job := range j.jobs {
		if f.Name != "" && job.Name != f.Name {
			continue
		}
		if f.State != "" && job.State != f.State {
			continue
		}
		out = append(out, *job)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

var _ hostjobs.Jobs = (*jobsImpl)(nil)

// jobContext is what a handler receives for one attempt.
type jobContext struct {
	context.Context
	id      int64
	attempt int
	args    string
	owner   *jobsImpl
}

func (c *jobContext) JobID() int64 { return c.id }
func (c *jobContext) Attempt() int { return c.attempt }

func (c *jobContext) Args(into any) error {
	if c.args == "" {
		return nil
	}
	return json.Unmarshal([]byte(c.args), into)
}

func (c *jobContext) Progress(fraction float64, message string) error {
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	job, ok := c.owner.jobs[c.id]
	if !ok {
		return fmt.Errorf("%w: %d", hostjobs.ErrUnknownJob, c.id)
	}
	job.Progress = fraction
	job.ProgressMessage = message
	return nil
}

func (c *jobContext) Logf(format string, args ...any) error {
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	c.owner.logs[c.id] = append(c.owner.logs[c.id], hostjobs.LogLine{
		Attempt: c.attempt,
		At:      c.owner.clock.Now(),
		Line:    fmt.Sprintf(format, args...),
	})
	return nil
}

var _ hostjobs.Context = (*jobContext)(nil)

// run executes one job to completion, retrying under the def's policy the way the real
// queue does, and returns the error the final attempt produced.
//
// Retries do not sleep. The clock advances by the backoff delay instead, so a test
// asserting on attempt count and elapsed time runs instantly.
func (j *jobsImpl) run(ctx context.Context, id int64) error {
	j.mu.Lock()
	job, ok := j.jobs[id]
	if !ok {
		j.mu.Unlock()
		return fmt.Errorf("%w: %d", hostjobs.ErrUnknownJob, id)
	}
	if terminal(job.State) {
		j.mu.Unlock()
		return fmt.Errorf("%w: %d", hostjobs.ErrAlreadyTerminal, id)
	}
	def, ok := j.defs[job.Name]
	if !ok {
		j.mu.Unlock()
		return fmt.Errorf("%w: %s", hostjobs.ErrUnknownDef, job.Name)
	}
	args, max := job.Args, job.MaxAttempts
	j.mu.Unlock()

	var lastErr error
	for attempt := 1; attempt <= max; attempt++ {
		// A plugin disabled between attempts stops the job rather than burning the
		// remaining ones, which is what the real gate does at claim time.
		if err := j.gate(true); err != nil {
			j.finish(id, stateFailed, err)
			return err
		}
		j.begin(id, attempt)

		runCtx := ctx
		var cancel context.CancelFunc
		if def.Timeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, def.Timeout)
		}
		err := def.Handler(&jobContext{Context: runCtx, id: id, attempt: attempt, args: args, owner: j})
		if cancel != nil {
			cancel()
		}
		if err == nil {
			j.finish(id, stateSucceeded, nil)
			return nil
		}
		lastErr = err
		if hostjobs.IsPermanent(err) || attempt == max {
			break
		}
		j.clock.Advance(backoffDelay(def.Backoff, attempt, err))
	}
	j.finish(id, stateFailed, lastErr)
	return lastErr
}

// backoffDelay is the wait before the next attempt: the handler's own override if it
// set one, otherwise exponential growth capped at the policy maximum.
func backoffDelay(p hostjobs.BackoffPolicy, attempt int, err error) time.Duration {
	if d, ok := hostjobs.RetryAfterDelay(err); ok {
		return d
	}
	d := p.Initial
	if d <= 0 {
		return 0
	}
	for i := 1; i < attempt; i++ {
		d *= 2
		if p.Maximum > 0 && d >= p.Maximum {
			return p.Maximum
		}
	}
	return d
}

func (j *jobsImpl) begin(id int64, attempt int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.jobs[id]
	now := j.clock.Now()
	job.State = stateRunning
	job.Attempt = attempt
	if job.StartedAt == nil {
		job.StartedAt = &now
	}
}

func (j *jobsImpl) finish(id int64, state string, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.jobs[id]
	now := j.clock.Now()
	job.State = state
	job.FinishedAt = &now
	if err != nil {
		job.LastError = err.Error()
	}
}

// pending returns the IDs of every job still waiting to run, oldest first.
func (j *jobsImpl) pending() []int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	var ids []int64
	for id, job := range j.jobs {
		if job.State == stateQueued {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	return ids
}
