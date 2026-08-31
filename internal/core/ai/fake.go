package ai

import (
	"context"
	"strings"
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

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}
