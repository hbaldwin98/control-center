package ai

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"math"
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

func (Fake) Embed(_ context.Context, d Dispatch, req EmbedRequest) (providerResult, error) {
	if d.Token == "" {
		return providerResult{errClass: "credential"}, ErrMissingCredential
	}
	vecs := make([][]float64, len(req.Inputs))
	var inTok int64
	for i, in := range req.Inputs {
		vecs[i] = fakeEmbed(in)
		inTok += approxTokens(in)
	}
	cost, err := d.charge(inTok, 0)
	if err != nil {
		return providerResult{}, err
	}
	return providerResult{
		vectors: vecs, inputTokens: inTok, cost: cost, billed: true,
	}, nil
}

const (
	fakeEmbedDims    = 48
	fakeConceptCount = 8
)

// fakeConcepts map tokens onto a few shared axes so tests can ask for "coffee" and
// still retrieve a Keurig, the way a real embedding model would.
var fakeConcepts = map[string]int{
	"coffee": 0, "keurig": 0, "espresso": 0, "brew": 0, "kcup": 0, "k-cup": 0,
	"k-supreme": 0, "ksupreme": 0,
	"camp": 1, "camping": 1, "tent": 1, "stove": 1, "cooler": 1, "lantern": 1,
	"sleeping": 1, "backpack": 1, "hike": 1, "hiking": 1, "outdoor": 1,
	"headlamp": 1, "flashlight": 1, "canopy": 1, "popup": 1, "shelter": 1,
	"chair": 2, "chairs": 2, "seating": 2, "seat": 2, "sit": 2, "desk": 2, "office": 2,
	"mesh": 2, "herman": 2, "miller": 2, "aeron": 2, "stool": 2,
}

func fakeEmbed(text string) []float64 {
	v := make([]float64, fakeEmbedDims)
	for _, tok := range fakeEmbedTokens(text) {
		if dim, ok := fakeConcepts[tok]; ok {
			v[dim] += 4
		}
		h := fakeTokenHash(tok) % uint32(fakeEmbedDims-fakeConceptCount)
		v[fakeConceptCount+int(h)] += 1
	}
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		v[0] = 1
		return v
	}
	inv := 1 / math.Sqrt(sum)
	for i := range v {
		v[i] *= inv
	}
	return v
}

func fakeEmbedTokens(s string) []string {
	s = strings.ToLower(s)
	var out []string
	var b strings.Builder
	flush := func() {
		t := b.String()
		b.Reset()
		if t == "" {
			return
		}
		out = append(out, t)
		if strings.Contains(t, "-") {
			for _, p := range strings.Split(t, "-") {
				if p != "" {
					out = append(out, p)
				}
			}
		}
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

func fakeTokenHash(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// fakeReply produces structured output when the caller asked for a schema, and
// inspectable citations when they asked for grounding. Echo remains the default, so
// existing cheap-chat tests keep seeing "echo: …".
//
// It is deliberately generic. This fake used to recognise particular schemas by the
// names of their fields and synthesize answers shaped for them, which meant core knew
// what a plugin's fields meant -- exactly the knowledge the plugin boundary exists to
// keep out. A plugin that needs its model answered in its own terms now programs that
// in its own tests, through host/hosttest.
func fakeReply(req ChatRequest, prompt string) (string, json.RawMessage, []Citation, []Source) {
	if req.Grounding != nil {
		return fakeGrounded(prompt)
	}
	if len(req.Schema) > 0 {
		raw := json.RawMessage(`{"ok":true}`)
		return string(raw), raw, nil, nil
	}
	return "echo: " + strings.TrimSpace(prompt), nil, nil, nil
}

// fakeGrounded answers with one citation over the whole answer and one source, so the
// grounding plumbing is exercisable without reaching a provider.
func fakeGrounded(prompt string) (string, json.RawMessage, []Citation, []Source) {
	subject := strings.TrimSpace(prompt)
	if i := strings.IndexAny(subject, "\n\r"); i >= 0 {
		subject = subject[:i]
	}
	if len(subject) > 60 {
		subject = subject[:60]
	}
	text := "Grounded answer about " + subject + "."
	src := Source{URL: "https://example-source.test/reference", Title: "Reference"}
	return text, nil, []Citation{{Start: 0, End: len(text), Source: 0}}, []Source{src}
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

func (h *HoldFake) Embed(ctx context.Context, d Dispatch, req EmbedRequest) (providerResult, error) {
	return Fake{}.Embed(ctx, d, req)
}

func (h *HoldFake) Models(ctx context.Context, d Dispatch) ([]Model, error) {
	return Fake{}.Models(ctx, d)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}
