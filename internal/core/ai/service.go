package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type providerResult struct {
	text                      string
	parsed                    json.RawMessage
	citations                 []Citation
	sources                   []Source
	vectors                   [][]float64
	inputTokens, outputTokens int64
	cost                      policy.MicroUSD
	billed                    bool
	errClass, providerStatus  string
}

type Service struct {
	db    storage.DB
	bus   events.Bus
	gate  policy.Gate
	creds credentials.Runtime
	refs  credentials.ReferenceStore
	now   func() time.Time
	http  *http.Client
	log   *slog.Logger

	// adapters are keyed by ProviderKind. They are stateless and shared by every
	// configured provider of that kind.
	adapters map[ProviderKind]Provider

	// mu guards the compiled view of administrator-owned configuration, which changes
	// while requests are in flight.
	mu          sync.RWMutex
	routes      []route
	providerCfg map[string]ProviderConfig
}

type Options struct {
	// Log receives one line per failed provider attempt. Defaults to slog.Default().
	Log *slog.Logger

	// Seed configures an empty database. It is ignored once anything is configured.
	Seed Seed
	// Providers replaces the built-in adapter for its kind. Tests use it; production
	// leaves it empty and gets the real ones.
	Providers []Provider
	// Refs records which credentials configured providers use, so deletion is refused
	// while a provider still names them.
	Refs       credentials.ReferenceStore
	Now        func() time.Time
	HTTPClient *http.Client
}

var _ Admin = (*Service)(nil)

func New(m storage.Migrator, db storage.DB, bus events.Bus, gate policy.Gate, creds credentials.Runtime, opts Options) (*Service, error) {
	if err := m.Apply("ai", migrations); err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	client := opts.HTTPClient
	if client == nil {
		client = defaultClient()
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		db: db, bus: bus, gate: gate, creds: creds, refs: opts.Refs,
		now: now, http: client, log: log, providerCfg: map[string]ProviderConfig{},
		adapters: map[ProviderKind]Provider{
			KindOpenAICompatible: OpenAICompatible{Client: client},
			KindCodex:            Codex{Client: client},
			KindFake:             Fake{},
		},
	}
	for _, p := range opts.Providers {
		s.adapters[p.Kind()] = p
	}
	ctx := context.Background()
	if err := s.seed(ctx, opts.Seed); err != nil {
		return nil, err
	}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	if err := s.recover(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) recover(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT id, reservation_id, reserved_micro_usd FROM core_ai_calls WHERE finalized_at IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pending struct {
		id, res string
		max     policy.MicroUSD
	}
	var list []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.res, &p.max); err != nil {
			return err
		}
		list = append(list, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range list {
		if err := s.finalizeConservative(ctx, p.id, p.res, p.max, "recovered"); err != nil {
			return err
		}
	}
	return nil
}

// dispatchFor resolves an attempt into everything its adapter needs. The credential is
// read here, once per attempt, so a token that expires between attempts is refreshed
// rather than reused.
func (s *Service) dispatchFor(ctx context.Context, a attempt) (Dispatch, error) {
	token, err := s.creds.Token(ctx, a.credential)
	if err != nil {
		return Dispatch{}, err
	}
	d := Dispatch{
		ProviderID: a.providerID, BaseURL: a.baseURL, Token: token,
		Model: a.model, Billing: a.billing, attempt: a,
	}
	if !a.metered() {
		attrs, err := s.creds.Attributes(ctx, a.credential)
		if err != nil {
			return Dispatch{}, err
		}
		d.AccountID = attrs.AccountID
	}
	return d, nil
}

func imageCount(req ChatRequest) int {
	n := 0
	for _, m := range req.Messages {
		n += len(m.Images)
	}
	return n
}

type attemptOutcome struct {
	a        attempt
	ordinal  int
	result   providerResult
	latency  time.Duration
	errClass string
	billed   bool
	// detail is what the provider said, kept so a failure can be read without a
	// packet capture. Empty on success.
	detail string
}

func spillover(class string) bool {
	switch class {
	case "rate_limit", "quota", "provider_5xx", "unsupported":
		return true
	}
	return false
}

func (s *Service) markDispatching(ctx context.Context, callID string, ordinal int, a attempt) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO core_ai_attempts(call_id, ordinal, provider, provider_model, status)
			 VALUES (?, ?, ?, ?, 'dispatching')`,
			callID, ordinal, a.providerID, a.model)
		return err
	})
}

func (s *Service) finalizeConservative(ctx context.Context, callID, resID string, max policy.MicroUSD, reason string) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		now := rfc(s.now())
		if _, err := tx.Exec(ctx,
			`UPDATE core_ai_calls SET status = 'failed', error_class = ?, settled_micro_usd = ?, finalized_at = ?
			 WHERE id = ? AND finalized_at IS NULL`, reason, int64(max), now, callID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE core_ai_attempts SET status = 'failed', billing_state = 'ambiguous' WHERE call_id = ? AND status = 'dispatching'`,
			callID); err != nil {
			return err
		}
		if resID == "" {
			return nil
		}
		return s.gate.SettleSpendTx(ctx, tx, resID, max)
	})
}

func (s *Service) route(name string) (route, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.routes {
		if r.name == name {
			return r, nil
		}
	}
	return route{}, ErrUnknownRoute
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("ai: rand: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func approxTokens(s string) int64 {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return 1
	}
	t := int64((n + 3) / 4)
	if t < 1 {
		return 1
	}
	return t
}

func lastText(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.TrimSpace(msgs[i].Text) != "" {
			return msgs[i].Text
		}
	}
	return ""
}

// truncateDetail bounds a provider message so a failure is legible in a log line and in
// the attempt row, without a hostile or verbose provider setting the size of either.
func truncateDetail(s string) string {
	const max = 500
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return s[:end] + "…"
}
