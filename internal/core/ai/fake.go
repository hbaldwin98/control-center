package ai

import (
	"context"
	"encoding/json"
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
	inTok := approxTokens(prompt) + int64(imageCount(req))*85
	reply, parsed, citations, sources := fakeReply(req, prompt)
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
		text: reply, parsed: parsed, citations: citations, sources: sources,
		inputTokens: inTok, outputTokens: outTok, cost: cost, billed: true,
	}, nil
}

// fakeReply produces structured output when the caller asked for a schema, and
// inspectable citations when they asked for grounding. Echo remains the default so
// existing cheap-chat tests keep seeing "echo: …".
func fakeReply(req ChatRequest, prompt string) (string, json.RawMessage, []Citation, []Source) {
	if len(req.Schema) > 0 && strings.Contains(string(req.Schema), "price_cents") {
		return fakePrice(prompt)
	}
	if req.Grounding != nil {
		return fakeGrounded(prompt)
	}
	if len(req.Schema) > 0 && strings.Contains(string(req.Schema), "basis") {
		return fakeAnalysis(prompt)
	}
	if len(req.Schema) > 0 {
		raw := json.RawMessage(`{"ok":true}`)
		return string(raw), raw, nil, nil
	}
	return "echo: " + strings.TrimSpace(prompt), nil, nil, nil
}

func fakeAnalysis(prompt string) (string, json.RawMessage, []Citation, []Source) {
	title := fieldAfter(prompt, "Title:")
	out := map[string]any{
		"identification":  title,
		"basis":           "category_only",
		"model_or_sku":    "",
		"category":        "other",
		"search_terms":    []string{},
		"title_agreement": 0.9,
		"notes":           "fake analysis",
	}
	switch {
	case strings.Contains(strings.ToLower(title), "keurig") || strings.Contains(strings.ToLower(prompt), "k-supreme"):
		out["identification"] = "Keurig K-Supreme Plus"
		out["basis"] = "exact_text"
		out["model_or_sku"] = "K-Supreme Plus"
		out["category"] = "appliances"
		out["search_terms"] = []string{"keurig", "coffee maker"}
		out["title_agreement"] = 0.85
		out["notes"] = "Model number is legible on the machine."
	case strings.Contains(strings.ToLower(title), "aeron"):
		out["identification"] = "Herman Miller Aeron-like mesh chair"
		out["basis"] = "distinctive_visual_match"
		out["model_or_sku"] = ""
		out["category"] = "furniture"
		out["search_terms"] = []string{"herman miller", "aeron"}
		out["title_agreement"] = 0.2
		out["notes"] = "Looks like an Aeron; no model plate readable."
	case strings.Contains(strings.ToLower(title), "chair"):
		out["category"] = "furniture"
	}
	raw, _ := json.Marshal(out)
	return string(raw), raw, nil, nil
}

func fakePrice(prompt string) (string, json.RawMessage, []Citation, []Source) {
	model := fieldAfter(prompt, "Model:")
	if model == "" {
		model = "K-Supreme Plus"
	}
	srcURL := fieldAfter(prompt, "URL:")
	if srcURL == "" || !strings.HasPrefix(srcURL, "https://") {
		srcURL = "https://example-market.test/" + strings.ReplaceAll(strings.ToLower(model), " ", "-")
	}
	kind, cents := "sold", int64(12900)
	if strings.Contains(strings.ToLower(prompt), "asking") {
		kind, cents = "asking", 8900
	}
	text := "Sold listing for " + model + " at $129 used."
	parsed, _ := json.Marshal(map[string]any{
		"price_cents":   cents,
		"currency":      "USD",
		"condition":     "used",
		"kind":          kind,
		"model_or_code": model,
		"source_url":    srcURL,
	})
	return text, parsed, nil, nil
}

func fakeGrounded(prompt string) (string, json.RawMessage, []Citation, []Source) {
	model := fieldAfter(prompt, "Model:")
	if model == "" {
		model = fieldAfter(prompt, "model_or_sku")
	}
	if model == "" {
		model = "K-Supreme Plus"
	}
	text := "Sold listing for " + model + " at $129 used."
	src := Source{
		URL:   "https://example-market.test/" + strings.ReplaceAll(strings.ToLower(model), " ", "-"),
		Title: model + " sold listing",
	}
	parsed, _ := json.Marshal(map[string]any{
		"price_cents":   12900,
		"currency":      "USD",
		"condition":     "used",
		"kind":          "sold",
		"model_or_code": model,
	})
	return text, parsed, []Citation{{Start: 0, End: len(text), Source: 0}}, []Source{src}
}

func fieldAfter(s, label string) string {
	i := strings.Index(s, label)
	if i < 0 {
		return strings.TrimSpace(s)
	}
	rest := s[i+len(label):]
	if j := strings.IndexAny(rest, "\n\r"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
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
