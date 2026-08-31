package ai

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Fake is the in-process provider used by tests and local development. It never
// contacts a network. Cost is computed from the route attempt's pricing.
type Fake struct{}

func (Fake) Kind() ProviderKind { return KindFake }

func (Fake) Chat(_ context.Context, d Dispatch, req ChatRequest) (providerResult, error) {
	if d.Token == "" {
		return providerResult{errClass: "credential"}, ErrMissingCredential
	}
	prompt := lastText(req.Messages)
	inTok := approxTokens(prompt)
	reply := "echo: " + strings.TrimSpace(prompt)
	if req.MaxTokens > 0 {
		runes := []rune(reply)
		if limit := req.MaxTokens * 4; len(runes) > limit {
			reply = string(runes[:limit])
		}
	}
	outTok := approxTokens(reply)
	cost, err := d.charge(inTok, outTok)
	if err != nil {
		return providerResult{}, err
	}
	return providerResult{
		text: reply, inputTokens: inTok, outputTokens: outTok, cost: cost, billed: true,
	}, nil
}

// Models gives the fake a catalog, so the discovery path is exercisable without a
// network in tests and in a local deployment alike.
func (Fake) Models(_ context.Context, d Dispatch) ([]Model, error) {
	return []Model{{
		Provider: d.ProviderID, ID: "echo", DisplayName: "Echo",
		ContextWindow: 4096, MaxOutputTokens: 1024,
		InputMicroUSDPerMillion: 1_000_000, OutputMicroUSDPerMillion: 2_000_000,
		Priced: true,
	}}, nil
}

// HoldFake is Fake with a latch inside Chat so a test can disable the plugin
// after admission and before the provider returns.
type HoldFake struct {
	started chan struct{}
	release chan struct{}
	n       atomic.Int32
	once    sync.Once
}

// NewHoldFake returns a provider that blocks in Chat until Release.
func NewHoldFake() *HoldFake {
	return &HoldFake{started: make(chan struct{}), release: make(chan struct{})}
}

func (*HoldFake) Kind() ProviderKind { return KindFake }

// Calls is the number of times Chat has been entered.
func (h *HoldFake) Calls() int { return int(h.n.Load()) }

// Started closes when the first Chat has been admitted into the provider.
func (h *HoldFake) Started() <-chan struct{} { return h.started }

// Release unblocks Chat. Safe to call more than once.
func (h *HoldFake) Release() {
	select {
	case <-h.release:
	default:
		close(h.release)
	}
}

func (h *HoldFake) Chat(ctx context.Context, d Dispatch, req ChatRequest) (providerResult, error) {
	h.n.Add(1)
	h.once.Do(func() { close(h.started) })
	<-h.release
	return Fake{}.Chat(ctx, d, req)
}

func (h *HoldFake) Models(ctx context.Context, d Dispatch) ([]Model, error) {
	return Fake{}.Models(ctx, d)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}
