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

func (Fake) Name() string { return "fake" }

func (Fake) Chat(_ context.Context, token string, a attempt, req ChatRequest) (providerResult, error) {
	if token == "" {
		return providerResult{errClass: "credential"}, ErrMissingCredential
	}
	prompt := lastText(req.Messages)
	inTok := approxTokens(prompt)
	reply := "echo: " + strings.TrimSpace(prompt)
	outTok := approxTokens(reply)
	cost, err := a.charge(inTok, outTok)
	if err != nil {
		return providerResult{}, err
	}
	return providerResult{
		text: reply, inputTokens: inTok, outputTokens: outTok, cost: cost, billed: true,
	}, nil
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

func (*HoldFake) Name() string { return "fake" }

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

func (h *HoldFake) Chat(ctx context.Context, token string, a attempt, req ChatRequest) (providerResult, error) {
	h.n.Add(1)
	h.once.Do(func() { close(h.started) })
	<-h.release
	return Fake{}.Chat(ctx, token, a, req)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}
