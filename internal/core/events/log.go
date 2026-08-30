package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// Options configures the log and its dispatchers.
type Options struct {
	// PollInterval is how often dispatchers and the tailer look for new committed rows
	// when no publisher has signalled. PublishTx commits inside a caller's transaction,
	// so polling is what makes those events visible; Publish additionally signals.
	PollInterval time.Duration

	// LeaseTTL bounds how long a dead process can hold a durable dispatch lease.
	LeaseTTL time.Duration

	// LiveQueue is the bounded depth of each live subscription's queue.
	LiveQueue int

	// Retention is the minimum age before an event may be deleted.
	Retention time.Duration

	// Owner identifies this process in dispatch leases.
	Owner string

	Now func() time.Time
}

func (o *Options) applyDefaults() {
	if o.PollInterval <= 0 {
		o.PollInterval = 150 * time.Millisecond
	}
	if o.LeaseTTL <= 0 {
		o.LeaseTTL = 30 * time.Second
	}
	if o.LiveQueue <= 0 {
		o.LiveQueue = 256
	}
	if o.Retention <= 0 {
		o.Retention = 90 * 24 * time.Hour
	}
	if o.Owner == "" {
		o.Owner = fmt.Sprintf("pid-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// Log is the concrete Bus. It also exposes the administrative operations the web layer
// needs: subscriber status, cursor reset/skip/retry, and the retained-ID boundary.
type Log struct {
	db   storage.DB
	opts Options

	// wake is a non-blocking signal that new rows are committed. Dispatchers also poll,
	// so a missed signal costs latency, never delivery.
	wake chan struct{}

	mu        sync.Mutex
	liveSubs  map[int64]*liveSub
	watchers  map[int64]chan struct{}
	nextSubID int64
	durables  map[string]*durableSub

	tailMu   sync.Mutex
	tailFrom int64

	stop     context.CancelFunc
	stopped  chan struct{}
	starting sync.Once
}

var _ Bus = (*Log)(nil)

// New applies the events migrations and prepares the log. Call Start to begin dispatch.
func New(m storage.Migrator, db storage.DB, opts Options) (*Log, error) {
	if err := m.Apply("events", migrations); err != nil {
		return nil, err
	}
	opts.applyDefaults()
	l := &Log{
		db:       db,
		opts:     opts,
		wake:     make(chan struct{}, 1),
		liveSubs: map[int64]*liveSub{},
		watchers: map[int64]chan struct{}{},
		durables: map[string]*durableSub{},
		stopped:  make(chan struct{}),
	}
	tail, err := l.Tail(context.Background())
	if err != nil {
		return nil, err
	}
	l.tailFrom = tail
	return l, nil
}

// Start runs the live-fanout tailer and every durable dispatcher until ctx is cancelled.
func (l *Log) Start(ctx context.Context) {
	l.starting.Do(func() {
		ctx, cancel := context.WithCancel(ctx)
		l.stop = cancel
		go l.runTailer(ctx)
		go func() {
			<-ctx.Done()
			close(l.stopped)
		}()
	})
}

// Stop halts dispatch and waits for the tailer to exit.
func (l *Log) Stop() {
	if l.stop != nil {
		l.stop()
		<-l.stopped
	}
}

// ---- publishing ----

// Publish commits one event in its own transaction.
func (l *Log) Publish(ctx context.Context, in Input) (int64, error) {
	var id int64
	err := l.db.Tx(ctx, func(tx storage.Tx) error {
		var err error
		id, err = l.PublishTx(ctx, tx, in)
		return err
	})
	if err != nil {
		return 0, err
	}
	l.signal()
	return id, nil
}

// PublishTx inserts into tx. A rollback removes the event; dispatchers tail the committed
// log, so they cannot observe it before the publisher commits.
func (l *Log) PublishTx(ctx context.Context, tx storage.Tx, in Input) (int64, error) {
	payload, err := prepare(in)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(ctx,
		`INSERT INTO core_events(type, source, subject, payload, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		in.Type, in.Source, in.Subject, string(payload),
		l.opts.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// prepare validates an input and serializes its payload.
func prepare(in Input) ([]byte, error) {
	if err := validateType(in.Type); err != nil {
		return nil, err
	}
	if err := validateSource(in.Source, in.Type); err != nil {
		return nil, err
	}
	payload := []byte("null")
	if in.Payload != nil {
		var err error
		payload, err = json.Marshal(in.Payload)
		if err != nil {
			return nil, fmt.Errorf("events: marshal payload for %s: %w", in.Type, err)
		}
	}
	if len(payload) > MaxPayloadBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes", ErrPayloadTooBig, in.Type, len(payload))
	}
	return payload, nil
}

// ---- reading ----

// Query reads committed events in increasing ID order.
func (l *Log) Query(ctx context.Context, q Query) ([]Event, error) {
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 100
	}
	pattern := q.Pattern
	if pattern == "" {
		pattern = "**"
	}
	p, err := CompilePattern(pattern)
	if err != nil {
		return nil, err
	}

	sql := `SELECT id, type, source, subject, payload, created_at
	          FROM core_events WHERE id > ?`
	args := []any{q.AfterID}
	if prefix := p.sqlPrefix(); prefix != "" {
		// A necessary but not sufficient condition; the exact matcher still runs below.
		sql += ` AND (type = ? OR type LIKE ?)`
		args = append(args, prefix, prefix+".%")
	}
	sql += ` ORDER BY id LIMIT ?`
	// Non-matching rows are filtered in Go, so read a wider window than the limit.
	args = append(args, q.Limit*4)

	rows, err := l.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Event, 0, q.Limit)
	for rows.Next() && len(out) < q.Limit {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		if p.Matches(e.Type) {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// Tail returns the highest committed event ID.
func (l *Log) Tail(ctx context.Context) (int64, error) {
	var id int64
	err := l.db.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM core_events`).Scan(&id)
	return id, err
}

// TailTx reads the log tail inside a caller's transaction, so a snapshot's data and its
// asOfEventId boundary come from one consistent read.
func TailTx(ctx context.Context, tx storage.Tx) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM core_events`).Scan(&id)
	return id, err
}

// OldestRetainedID is the lowest event ID a client may still resume from. A client behind
// this boundary receives a reset rather than a partial replay.
func (l *Log) OldestRetainedID(ctx context.Context) (int64, error) {
	var id int64
	err := l.db.QueryRow(ctx,
		`SELECT oldest_retained_id FROM core_event_retention WHERE id = 1`).Scan(&id)
	return id, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEvent(rows rowScanner) (Event, error) {
	var e Event
	var payload, created string
	if err := rows.Scan(&e.ID, &e.Type, &e.Source, &e.Subject, &payload, &created); err != nil {
		return Event{}, err
	}
	e.Payload = json.RawMessage(payload)
	e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return e, nil
}

// readAfter loads the next committed events above id.
func (l *Log) readAfter(ctx context.Context, id int64, limit int) ([]Event, error) {
	rows, err := l.db.Query(ctx,
		`SELECT id, type, source, subject, payload, created_at
		   FROM core_events WHERE id > ? ORDER BY id LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Event, 0, limit)
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- live subscriptions ----

// liveSub is a bounded in-memory queue. Live subscriptions are lossy by design: restarts,
// handler failures, and slow consumers may drop events, with no retry or replay.
type liveSub struct {
	id      int64
	pattern Pattern
	handler Handler
	queue   chan Event
	done    chan struct{}
	log     *Log
	dropped int64
}

func (s *liveSub) Name() string { return fmt.Sprintf("live-%d", s.id) }

func (s *liveSub) Close() {
	s.log.mu.Lock()
	if _, ok := s.log.liveSubs[s.id]; ok {
		delete(s.log.liveSubs, s.id)
		close(s.done)
	}
	s.log.mu.Unlock()
}

// SubscribeLive delivers committed events through a bounded queue. Use it only for
// explicitly ephemeral reactions; the SSE endpoint tails the persisted log instead, so a
// queue overflow cannot silently create a client gap.
func (l *Log) SubscribeLive(pattern string, h Handler) (Subscription, error) {
	p, err := CompilePattern(pattern)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.nextSubID++
	sub := &liveSub{
		id:      l.nextSubID,
		pattern: p,
		handler: h,
		queue:   make(chan Event, l.opts.LiveQueue),
		done:    make(chan struct{}),
		log:     l,
	}
	l.liveSubs[sub.id] = sub
	l.mu.Unlock()

	go sub.run()
	return sub, nil
}

func (s *liveSub) run() {
	for {
		select {
		case <-s.done:
			return
		case e := <-s.queue:
			if err := s.handler(context.Background(), e); err != nil {
				slog.Debug("events: live handler failed", "sub", s.Name(), "event", e.ID, "err", err)
			}
		}
	}
}

// runTailer reads newly committed rows and fans them out to live subscribers.
func (l *Log) runTailer(ctx context.Context) {
	ticker := time.NewTicker(l.opts.PollInterval)
	defer ticker.Stop()

	for {
		l.drainOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
		case <-ticker.C:
		}
	}
}

func (l *Log) drainOnce(ctx context.Context) {
	for {
		l.tailMu.Lock()
		from := l.tailFrom
		l.tailMu.Unlock()

		batch, err := l.readAfter(ctx, from, 200)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("events: tail read failed", "err", err)
			}
			return
		}
		if len(batch) == 0 {
			return
		}
		for _, e := range batch {
			l.fanOut(e)
		}
		l.tailMu.Lock()
		l.tailFrom = batch[len(batch)-1].ID
		l.tailMu.Unlock()
		l.notifyWatchers()
	}
}

func (l *Log) fanOut(e Event) {
	l.mu.Lock()
	subs := make([]*liveSub, 0, len(l.liveSubs))
	for _, s := range l.liveSubs {
		if s.pattern.Matches(e.Type) {
			subs = append(subs, s)
		}
	}
	l.mu.Unlock()

	for _, s := range subs {
		select {
		case s.queue <- e:
		default:
			// Bounded and lossy on purpose. Anything that must not be dropped uses a
			// durable subscription or the SSE log tail.
			s.dropped++
			slog.Debug("events: live queue overflow", "sub", s.Name(), "event", e.ID)
		}
	}
}

func (l *Log) signal() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Watch returns a channel that receives a value whenever the tailer observes newly
// committed rows, plus a function that stops watching. The channel has depth one and is
// signalled without blocking, so it is a hint to read again, never a delivery mechanism.
//
// The SSE endpoint uses this to avoid polling on every connection while still reading the
// persisted log itself.
func (l *Log) Watch() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	l.mu.Lock()
	l.nextSubID++
	id := l.nextSubID
	l.watchers[id] = ch
	l.mu.Unlock()

	return ch, func() {
		l.mu.Lock()
		delete(l.watchers, id)
		l.mu.Unlock()
	}
}

func (l *Log) notifyWatchers() {
	l.mu.Lock()
	chans := make([]chan struct{}, 0, len(l.watchers))
	for _, ch := range l.watchers {
		chans = append(chans, ch)
	}
	l.mu.Unlock()

	for _, ch := range chans {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Signal wakes dispatchers after a caller's own transaction commits an event through
// PublishTx. It is an optimisation: dispatchers poll regardless.
func (l *Log) Signal() { l.signal() }
