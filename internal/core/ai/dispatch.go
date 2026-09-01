package ai

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

const (
	maxEmbedInputs = 64
	maxEmbedRunes  = 8192
)

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
	if imageCount(req) > 0 && !rt.has("vision") {
		return nil, fmt.Errorf("%w: route %q has no vision capability", ErrCapability, rt.name)
	}
	if req.Grounding != nil {
		if !rt.has("grounding") {
			return nil, fmt.Errorf("%w: route %q has no grounding capability", ErrCapability, rt.name)
		}
		if req.Grounding.MaxQueries <= 0 {
			return nil, fmt.Errorf("%w: grounding maxQueries must be positive", ErrCapability)
		}
	}
	max, err := rt.estimateChat(req.MaxTokens)
	if err != nil {
		return nil, err
	}
	out, usage, err := s.execute(ctx, plugin, "chat", req.Model, rt, max, func(ctx context.Context, d Dispatch, prov Provider) (providerResult, error) {
		return prov.Chat(ctx, d, req)
	})
	if err != nil {
		return nil, err
	}
	return &ChatResponse{
		Text: out.text, Parsed: out.parsed, Citations: out.citations, Sources: out.sources,
		Finish: "stop",
		Usage:  usage,
	}, nil
}

func (s *Service) ChatStream(ctx context.Context, req ChatRequest) (Stream, error) {
	resp, err := s.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return &oneShot{resp: resp}, nil
}

func (s *Service) Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	plugin, err := pluginID(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateEmbed(req); err != nil {
		return nil, err
	}
	rt, err := s.route(req.Model)
	if err != nil {
		return nil, err
	}
	if !rt.usable() {
		return nil, fmt.Errorf("%w: %s: %s", ErrRouteUncompiled, rt.name, rt.lastError)
	}
	if !rt.has("embed") {
		return nil, ErrCapability
	}
	max, err := rt.estimateEmbed()
	if err != nil {
		return nil, err
	}
	out, usage, err := s.execute(ctx, plugin, "embed", req.Model, rt, max, func(ctx context.Context, d Dispatch, prov Provider) (providerResult, error) {
		return prov.Embed(ctx, d, req)
	})
	if err != nil {
		return nil, err
	}
	if len(out.vectors) != len(req.Inputs) {
		return nil, fmt.Errorf("ai: embedding count %d does not match %d inputs", len(out.vectors), len(req.Inputs))
	}
	return &EmbedResponse{Vectors: out.vectors, Usage: usage}, nil
}

func validateEmbed(req EmbedRequest) error {
	if len(req.Inputs) == 0 || len(req.Inputs) > maxEmbedInputs {
		return fmt.Errorf("%w: need 1..%d inputs", ErrInvalidInput, maxEmbedInputs)
	}
	for _, in := range req.Inputs {
		n := utf8.RuneCountInString(in)
		if strings.TrimSpace(in) == "" || n > maxEmbedRunes {
			return fmt.Errorf("%w: each input must be 1..%d characters", ErrInvalidInput, maxEmbedRunes)
		}
	}
	return nil
}

func (s *Service) execute(ctx context.Context, plugin, operation, model string, rt route, max policy.MicroUSD, invoke func(context.Context, Dispatch, Provider) (providerResult, error)) (providerResult, Usage, error) {
	callID := newID()
	started := s.now().UTC()
	var reservation policy.Reservation
	var rejected error
	err := s.db.Tx(ctx, func(tx storage.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO core_ai_calls(id, reservation_id, plugin_id, job_id, operation, logical_model, status, reserved_micro_usd, started_at)
			 VALUES (?, '', ?, ?, ?, ?, 'pending', ?, ?)`,
			callID, plugin, jobID(ctx), operation, model, int64(max), rfc(started)); err != nil {
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
		return providerResult{}, Usage{}, err
	}
	if rejected != nil {
		return providerResult{}, Usage{}, rejected
	}

	var (
		okResult    providerResult
		inTok       int64
		outTok      int64
		cost        policy.MicroUSD
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
			return providerResult{}, Usage{}, err
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
		out, perr := invoke(ctx, d, prov)
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
			s.log.Warn("ai: attempt failed",
				"call", callID, "plugin", plugin, "job", jobID(ctx),
				"model", model, "attempt", i+1,
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
			return providerResult{}, Usage{}, ErrUnbounded
		}
		cost = next
		if out.billed {
			billedAny = true
		}
		if perr == nil && oc.errClass == "" {
			okResult = out
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
	if errClass != "" {
		status = "failed"
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
		settled = cost
	}

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
				"plugin": plugin, "job": jobID(ctx), "operation": operation,
				"logicalModel": model, "status": status,
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
		return providerResult{}, Usage{}, err
	}
	if status != "succeeded" {
		if errDetail != "" {
			return providerResult{}, Usage{}, fmt.Errorf("ai: call %s %s: %s: %s", callID, status, errClass, errDetail)
		}
		return providerResult{}, Usage{}, fmt.Errorf("ai: call %s %s: %s", callID, status, errClass)
	}
	return okResult, Usage{
		InputTokens: inTok, OutputTokens: outTok, CostMicroUSD: settled,
		Attempts: len(attempts),
	}, nil
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
