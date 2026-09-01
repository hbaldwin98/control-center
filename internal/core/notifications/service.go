package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

const (
	durableName = "core.notifications"
	maxAttempts = 8
)

type Options struct {
	PollInterval time.Duration
	LeaseTTL     time.Duration
	Owner        string
	Now          func() time.Time
	HTTPClient   *http.Client
	// Channels, when set, replaces the built-in constructors for tests.
	Channels map[string]func(ChannelConfig, credentials.Runtime) (Channel, error)
}

func (o *Options) applyDefaults() {
	if o.PollInterval <= 0 {
		o.PollInterval = 150 * time.Millisecond
	}
	if o.LeaseTTL <= 0 {
		o.LeaseTTL = 30 * time.Second
	}
	if o.Owner == "" {
		o.Owner = fmt.Sprintf("pid-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.HTTPClient == nil {
		o.HTTPClient = http.DefaultClient
	}
}

// Service is the concrete Admin, Inbox, durable handler, and delivery worker.
type Service struct {
	db    storage.DB
	bus   events.Bus
	creds credentials.Runtime
	refs  credentials.ReferenceStore
	opts  Options
	env   *cel.Env

	mu       sync.Mutex
	channels map[string]Channel

	sub events.Subscription

	stop    context.CancelFunc
	stopped chan struct{}
	wake    chan struct{}
}

func New(ctx context.Context, m storage.Migrator, db storage.DB, bus events.Bus, creds credentials.Runtime, refs credentials.ReferenceStore, opts Options) (*Service, error) {
	if err := m.Apply("notifications", migrations); err != nil {
		return nil, err
	}
	opts.applyDefaults()
	env, err := newCELEnv()
	if err != nil {
		return nil, err
	}
	s := &Service{
		db: db, bus: bus, creds: creds, refs: refs, opts: opts, env: env,
		channels: map[string]Channel{},
		stopped:  make(chan struct{}),
		wake:     make(chan struct{}, 1),
	}
	if err := s.seedDefaults(ctx); err != nil {
		return nil, err
	}
	sub, err := bus.SubscribeDurableTx(events.DurableConfig{
		Name:    durableName,
		Pattern: "**",
		Start:   events.CursorStart{Mode: events.FromNow},
	}, s.handleEvent)
	if err != nil {
		return nil, err
	}
	s.sub = sub
	return s, nil
}

func (s *Service) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	s.stop = cancel
	go s.loop(runCtx)
}

func (s *Service) Stop() {
	if s.sub != nil {
		s.sub.Close()
		s.sub = nil
	}
	if s.stop != nil {
		s.stop()
		<-s.stopped
	}
}

func (s *Service) now() time.Time { return s.opts.Now().UTC() }

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) loop(ctx context.Context) {
	defer close(s.stopped)
	tick := time.NewTicker(s.opts.PollInterval)
	defer tick.Stop()
	for {
		s.releaseWindows(ctx)
		s.claimUntilEmpty(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-tick.C:
		}
	}
}

func (s *Service) seedDefaults(ctx context.Context) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM core_notification_channels`).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := tx.Exec(ctx,
				`INSERT INTO core_notification_channels(id, kind, config, credential_id, enabled)
				 VALUES ('inbox', 'inbox', '{}', '', 1)`); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM core_notification_rules`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return nil
		}
		defaults := []Rule{
			{ID: "plugin-alert", Enabled: true, Match: "*.alert",
				Where:    `string(event.source) != "core" && !string(event.source).startsWith("core.")`,
				Channels: []string{"inbox"}, Title: "{event.type}", Body: "{event.subject}"},
			{ID: "job-dead", Enabled: true, Match: "core.job.dead",
				Channels: []string{"inbox"}, Title: "Job exhausted retries", Body: "{event.subject}"},
			{ID: "plugin-budget-exceeded", Enabled: true, Match: "core.plugin.budget_exceeded",
				Channels: []string{"inbox"}, Title: "Budget exceeded", Body: "{event.subject}"},
			{ID: "plugin-accounting-failed", Enabled: true, Match: "core.plugin.accounting_invariant_failed",
				Channels: []string{"inbox"}, Title: "Accounting invariant failed", Body: "{event.subject}"},
			{ID: "credential-needs-reauth", Enabled: true, Match: "core.credential.needs_reauth",
				Channels: []string{"inbox"}, Title: "Credential needs reauthorization", Body: "{event.subject}"},
			{ID: "event-subscription-paused", Enabled: true, Match: "core.event.subscription_paused",
				Channels: []string{"inbox"}, Title: "Durable subscriber paused", Body: "{event.subject}"},
		}
		for _, r := range defaults {
			if err := s.insertRuleTx(ctx, tx, r); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) insertRuleTx(ctx context.Context, tx storage.Tx, r Rule) error {
	ch, err := encodeChannels(r.Channels)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO core_notification_rules
		(id, enabled, match, where_expr, channels, title, body, url, throttle_s)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, boolInt(r.Enabled), r.Match, r.Where, ch, r.Title, r.Body, r.URL, int64(r.Throttle/time.Second))
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Service) logRuleErr(ruleID string, err error) {
	slog.Warn("notifications: rule evaluation failed", "rule", ruleID, "err", err)
}
