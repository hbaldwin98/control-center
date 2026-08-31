package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type Provider interface {
	Name() string
	Chat(ctx context.Context, token string, a attempt, req ChatRequest) (providerResult, error)
}

type providerResult struct {
	text                         string
	inputTokens, outputTokens    int64
	cost                         policy.MicroUSD
	billed                       bool
	errClass, providerStatus     string
}

type Service struct {
	db    storage.DB
	bus   events.Bus
	gate  policy.Gate
	creds credentials.Runtime
	now   func() time.Time

	routes    []route
	providers map[string]Provider
}

type Options struct {
	Routes    []route
	Providers []Provider
	// Refs records which credentials the compiled routes use, so deletion is refused
	// while a route still names them. Optional only when Routes is empty.
	Refs credentials.ReferenceStore
	Now  func() time.Time
}

func New(m storage.Migrator, db storage.DB, bus events.Bus, gate policy.Gate, creds credentials.Runtime, opts Options) (*Service, error) {
	if err := m.Apply("ai", migrations); err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	provs := map[string]Provider{}
	for _, p := range opts.Providers {
		provs[p.Name()] = p
	}
	s := &Service{db: db, bus: bus, gate: gate, creds: creds, now: now, routes: opts.Routes, providers: provs}
	for _, r := range s.routes {
		for _, a := range r.attempts {
			if _, ok := s.providers[a.provider]; !ok {
				return nil, fmt.Errorf("ai: route %q: unknown provider %q", r.name, a.provider)
			}
			if _, err := s.creds.Token(context.Background(), a.credential); err != nil {
				return nil, fmt.Errorf("%w: route %q credential %q: %v", ErrMissingCredential, r.name, a.credential, err)
			}
		}
	}
	if opts.Refs != nil {
		seen := map[string]struct{}{}
		var ids []string
		for _, r := range s.routes {
			for _, a := range r.attempts {
				if _, ok := seen[a.credential]; ok {
					continue
				}
				seen[a.credential] = struct{}{}
				ids = append(ids, a.credential)
			}
		}
		if err := opts.Refs.Replace(context.Background(), "ai.routes", ids); err != nil {
			return nil, err
		}
	}
	if err := s.recover(context.Background()); err != nil {
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

func (s *Service) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	plugin, err := pluginID(ctx)
	if err != nil {
		return nil, err
	}
	rt, err := s.route(req.Model)
	if err != nil {
		return nil, err
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
		text       string
		inTok      int64
		outTok     int64
		cost       policy.MicroUSD
		finish     = "stop"
		errClass   string
		attempts   []attemptOutcome
		billedAny  bool
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
		token, err := s.creds.Token(ctx, a.credential)
		if err != nil {
			attempts = append(attempts, attemptOutcome{a: a, ordinal: i + 1, errClass: "credential", billed: false})
			errClass = "credential"
			continue
		}
		prov := s.providers[a.provider]
		start := s.now()
		out, perr := prov.Chat(ctx, token, a, req)
		lat := s.now().Sub(start)
		oc := attemptOutcome{
			a: a, ordinal: i + 1, result: out, latency: lat,
			errClass: out.errClass, billed: out.billed,
		}
		if perr != nil && oc.errClass == "" {
			oc.errClass = "provider"
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
			break
		}
		errClass = oc.errClass
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

	var usage events.Event
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
			if unknownBill && oc.errClass != "" {
				bill = "ambiguous"
			}
			st := "succeeded"
			if oc.errClass != "" {
				st = "failed"
			}
			if _, err := tx.Exec(settleCtx,
				`UPDATE core_ai_attempts SET status = ?, error_class = ?, billing_state = ?,
				        input_tokens = ?, output_tokens = ?, cost_micro_usd = ?, latency_ms = ?
				  WHERE call_id = ? AND ordinal = ?`,
				st, oc.errClass, bill, oc.result.inputTokens, oc.result.outputTokens,
				int64(oc.result.cost), oc.latency.Milliseconds(), callID, oc.ordinal); err != nil {
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
		_ = usage
		return err
	})
	if err != nil {
		return nil, err
	}
	if status != "succeeded" {
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
			callID, ordinal, a.provider, a.model)
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
