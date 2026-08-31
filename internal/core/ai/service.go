package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
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

func (s *Service) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	plugin, err := pluginID(ctx)
	if err != nil {
		return nil, err
	}
	rt, err := s.route(req.Model)
	if err != nil {
		return nil, err
	}
	if !rt.usable() {
		return nil, fmt.Errorf("%w: %s: %s", ErrRouteUncompiled, rt.name, rt.lastError)
	}
	if !rt.has("chat") {
		return nil, ErrCapability
	}
	max, err := rt.estimateChat(req.MaxTokens)
	if err != nil {
		return nil, err
	}

	callID := newID()
	started := s.now().UTC()
	var reservation policy.Reservation
	var rejected error
	err = s.db.Tx(ctx, func(tx storage.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO core_ai_calls(id, reservation_id, plugin_id, job_id, operation, logical_model, status, reserved_micro_usd, started_at)
			 VALUES (?, '', ?, ?, 'chat', ?, 'pending', ?, ?)`,
			callID, plugin, jobID(ctx), req.Model, int64(max), rfc(started)); err != nil {
			return err
		}
		res, rerr := s.gate.ReserveSpendTx(ctx, tx, plugin, max)
		if policy.IsDomainRejection(rerr) {
			_, _ = tx.Exec(ctx, `DELETE FROM core_ai_calls WHERE id = ?`, callID)
			rejected = rerr
			return nil
		}
		if rerr != nil {
			return rerr
		}
		reservation = res
		_, err := tx.Exec(ctx, `UPDATE core_ai_calls SET reservation_id = ? WHERE id = ?`, res.ID, callID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if rejected != nil {
		return nil, rejected
	}

	var (
		text        string
		inTok       int64
		outTok      int64
		cost        policy.MicroUSD
		finish      = "stop"
		errClass    string
		errDetail   string
		attempts    []attemptOutcome
		billedAny   bool
		unknownBill bool
	)
	for i, a := range rt.attempts {
		if ctx.Err() != nil {
			unknownBill = billedAny
			errClass = "cancelled"
			break
		}
		if err := s.markDispatching(ctx, callID, i+1, a); err != nil {
			return nil, err
		}
		d, err := s.dispatchFor(ctx, a)
		if err != nil {
			attempts = append(attempts, attemptOutcome{a: a, ordinal: i + 1, errClass: "credential", billed: false})
			errClass = "credential"
			continue
		}
		prov, ok := s.adapters[a.kind]
		if !ok {
			attempts = append(attempts, attemptOutcome{a: a, ordinal: i + 1, errClass: "provider", billed: false})
			errClass = "provider"
			continue
		}
		start := s.now()
		out, perr := prov.Chat(ctx, d, req)
		lat := s.now().Sub(start)
		oc := attemptOutcome{
			a: a, ordinal: i + 1, result: out, latency: lat,
			errClass: out.errClass, billed: out.billed,
		}
		if perr != nil {
			oc.detail = truncateDetail(perr.Error())
			if oc.errClass == "" {
				oc.errClass = "provider"
			}
		}
		if oc.errClass != "" {
			// One line per failed attempt, at the moment it fails. The call-level
			// error names only the class; a provider that refuses a parameter or a
			// model id can only say so here.
			s.log.Warn("ai: attempt failed",
				"call", callID, "plugin", plugin, "job", jobID(ctx),
				"model", req.Model, "attempt", i+1,
				"provider", a.providerID, "providerModel", a.model,
				"status", out.providerStatus, "class", oc.errClass,
				"latencyMs", lat.Milliseconds(), "detail", oc.detail)
		}
		if out.cost > max {
			oc.errClass = "accounting_invariant"
		}
		attempts = append(attempts, oc)
		inTok += out.inputTokens
		outTok += out.outputTokens
		next := cost + out.cost
		if next < cost {
			return nil, ErrUnbounded
		}
		cost = next
		if out.billed {
			billedAny = true
		}
		if perr == nil && oc.errClass == "" {
			text = out.text
			errClass = ""
			errDetail = ""
			break
		}
		errClass = oc.errClass
		errDetail = oc.detail
		if !spillover(oc.errClass) {
			break
		}
	}

	status := "succeeded"
	if text == "" && errClass != "" {
		status = "failed"
		finish = "error"
	}
	settled := cost
	release := !billedAny && !unknownBill
	if unknownBill {
		settled = max
		status = "failed"
		if errClass == "" {
			errClass = "ambiguous"
		}
	}
	if settled > max {
		settled = cost // truthful charge; policy will disable
	}

	// Accounting must complete even if the caller was cancelled after admission.
	// A paid provider call that already ran still has to settle its reservation.
	settleCtx := context.WithoutCancel(ctx)
	err = s.db.Tx(settleCtx, func(tx storage.Tx) error {
		now := rfc(s.now())
		if _, err := tx.Exec(settleCtx,
			`UPDATE core_ai_calls SET status = ?, error_class = ?, settled_micro_usd = ?, finalized_at = ? WHERE id = ?`,
			status, errClass, int64(settled), now, callID); err != nil {
			return err
		}
		for _, oc := range attempts {
			bill := "unbilled"
			if oc.billed {
				bill = "billed"
			}
			if !oc.a.metered() {
				bill = "subscription"
			}
			if unknownBill && oc.errClass != "" {
				bill = "ambiguous"
			}
			st := "succeeded"
			if oc.errClass != "" {
				st = "failed"
			}
			if _, err := tx.Exec(settleCtx,
				`UPDATE core_ai_attempts SET status = ?, error_class = ?, billing_state = ?, provider_status = ?,
				        input_tokens = ?, output_tokens = ?, cost_micro_usd = ?, latency_ms = ?,
				        provider_error = ?
				  WHERE call_id = ? AND ordinal = ?`,
				st, oc.errClass, bill, oc.result.providerStatus, oc.result.inputTokens, oc.result.outputTokens,
				int64(oc.result.cost), oc.latency.Milliseconds(), oc.detail, callID, oc.ordinal); err != nil {
				return err
			}
		}
		if release {
			if err := s.gate.ReleaseSpendTx(settleCtx, tx, reservation.ID); err != nil {
				return err
			}
			settled = 0
			_, _ = tx.Exec(settleCtx, `UPDATE core_ai_calls SET settled_micro_usd = 0 WHERE id = ?`, callID)
		} else {
			if err := s.gate.SettleSpendTx(settleCtx, tx, reservation.ID, settled); err != nil {
				return err
			}
		}
		eid, err := s.bus.PublishTx(settleCtx, tx, events.Input{
			Type:    events.TypeAIUsage,
			Source:  events.SourceAI,
			Subject: callID,
			Payload: map[string]any{
				"plugin": plugin, "job": jobID(ctx), "operation": "chat",
				"logicalModel": req.Model, "status": status,
				"inputTokens": inTok, "outputTokens": outTok,
				"attempts": len(attempts), "costMicroUSD": int64(settled),
			},
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(settleCtx, `UPDATE core_ai_calls SET usage_event_id = ? WHERE id = ?`, eid, callID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if status != "succeeded" {
		if errDetail != "" {
			return nil, fmt.Errorf("ai: call %s %s: %s: %s", callID, status, errClass, errDetail)
		}
		return nil, fmt.Errorf("ai: call %s %s: %s", callID, status, errClass)
	}
	return &ChatResponse{
		Text:   text,
		Finish: finish,
		Usage: Usage{
			InputTokens: inTok, OutputTokens: outTok, CostMicroUSD: settled,
			Attempts: len(attempts),
		},
	}, nil
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
	case "rate_limit", "quota", "provider_5xx":
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

func (s *Service) ChatStream(ctx context.Context, req ChatRequest) (Stream, error) {
	resp, err := s.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return &oneShot{resp: resp}, nil
}

func (s *Service) Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	return nil, ErrCapability
}

type oneShot struct {
	resp *ChatResponse
	done bool
}

func (s *oneShot) Recv() (Chunk, error) {
	if s.resp == nil {
		return Chunk{}, io.EOF
	}
	c := Chunk{Text: s.resp.Text, Finish: s.resp.Finish, Usage: &s.resp.Usage}
	s.resp = nil
	return c, nil
}

func (s *oneShot) Close() error { s.done = true; return nil }

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
