package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// OpenAICompatible speaks the /v1 chat-completions, /v1 embeddings, and /v1 models
// contract that OpenAI, OpenRouter, and most self-hosted servers implement, authorized
// by a bearer API key.
//
// One adapter serves every such provider. What differs between them is the base URL and
// the credential, which are per-provider configuration, not per-vendor code.
type OpenAICompatible struct {
	Client *http.Client
}

func (OpenAICompatible) Kind() ProviderKind { return KindOpenAICompatible }

func (p OpenAICompatible) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return defaultClient()
}

type chatCompletionsRequest struct {
	Model          string              `json:"model"`
	Messages       []chatWireMessage   `json:"messages"`
	MaxTokens      int                 `json:"max_completion_tokens,omitempty"`
	Stream         bool                `json:"stream"`
	Usage          *streamUsageOptions `json:"stream_options,omitempty"`
	ResponseFormat json.RawMessage     `json:"response_format,omitempty"`
	Plugins        []map[string]any    `json:"plugins,omitempty"`
}

type chatWireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type streamUsageOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatCompletionsResponse struct {
	Choices []struct {
		Message      chatWireMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

func (p OpenAICompatible) Chat(ctx context.Context, d Dispatch, req ChatRequest) (providerResult, error) {
	if d.Token == "" {
		return providerResult{errClass: "credential"}, ErrMissingCredential
	}
	body := chatCompletionsRequest{Model: d.Model, MaxTokens: req.MaxTokens}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, chatWireMessage{Role: wireRole(m.Role), Content: wireContent(m)})
	}
	if len(req.Schema) > 0 {
		body.ResponseFormat = jsonSchemaFormat(req.Schema)
	}
	if req.Grounding != nil {
		body.Plugins = []map[string]any{{
			"id": "web", "max_results": req.Grounding.MaxQueries,
		}}
	}
	status, raw, err := httpDo(ctx, p.client(), http.MethodPost, d.BaseURL+"/chat/completions",
		map[string]string{"Authorization": "Bearer " + d.Token}, body)
	if err != nil {
		// The request may or may not have reached the provider. Say so rather than
		// asserting it was free.
		return providerResult{errClass: "transport", billed: false}, err
	}
	if status < 200 || status >= 300 {
		// A rejected request is not billed; a 5xx after the model ran might be. Only
		// the unambiguous case claims to be free.
		class := errorClass(status)
		return providerResult{
			errClass:       class,
			providerStatus: strconv.Itoa(status),
			billed:         class == "provider_5xx",
		}, providerError(d.ProviderID, status, raw)
	}
	var parsed chatCompletionsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return providerResult{errClass: "provider", billed: true}, err
	}
	if len(parsed.Choices) == 0 {
		return providerResult{errClass: "provider", billed: true}, providerError(d.ProviderID, status, raw)
	}
	in, out := parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens
	text := decodeWireContent(parsed.Choices[0].Message.Content)
	structured := jsonIfObject(text)
	cites, sources := annotationsFrom(raw)
	// A provider that reports no usage still charged for the call. Fall back to a
	// local estimate so the attempt is accounted for rather than recorded as free.
	if in == 0 && out == 0 {
		in, out = approxTokens(lastText(req.Messages)), approxTokens(text)
	}
	cost, err := d.charge(in, out)
	if err != nil {
		return providerResult{errClass: "accounting_invariant", billed: true}, err
	}
	return providerResult{
		text: text, parsed: structured, citations: cites, sources: sources,
		inputTokens: in, outputTokens: out, cost: cost, billed: true,
		providerStatus: strconv.Itoa(status),
	}, nil
}

func (p OpenAICompatible) Embed(ctx context.Context, d Dispatch, req EmbedRequest) (providerResult, error) {
	if d.Token == "" {
		return providerResult{errClass: "credential"}, ErrMissingCredential
	}
	status, raw, err := httpDo(ctx, p.client(), http.MethodPost, d.BaseURL+"/embeddings",
		map[string]string{"Authorization": "Bearer " + d.Token},
		embeddingsRequest{Model: d.Model, Input: req.Inputs})
	if err != nil {
		return providerResult{errClass: "transport", billed: false}, err
	}
	if status < 200 || status >= 300 {
		class := errorClass(status)
		return providerResult{
			errClass:       class,
			providerStatus: strconv.Itoa(status),
			billed:         class == "provider_5xx",
		}, providerError(d.ProviderID, status, raw)
	}
	var parsed embeddingsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return providerResult{errClass: "provider", billed: true}, err
	}
	if len(parsed.Data) != len(req.Inputs) {
		return providerResult{errClass: "provider", billed: true},
			fmt.Errorf("ai: provider %s returned %d embeddings for %d inputs", d.ProviderID, len(parsed.Data), len(req.Inputs))
	}
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })
	vecs := make([][]float64, len(parsed.Data))
	for i, row := range parsed.Data {
		if len(row.Embedding) == 0 {
			return providerResult{errClass: "provider", billed: true},
				fmt.Errorf("ai: provider %s returned an empty embedding", d.ProviderID)
		}
		vecs[i] = row.Embedding
	}
	in := parsed.Usage.PromptTokens
	if in == 0 {
		for _, s := range req.Inputs {
			in += approxTokens(s)
		}
	}
	cost, err := d.charge(in, 0)
	if err != nil {
		return providerResult{errClass: "accounting_invariant", billed: true}, err
	}
	return providerResult{
		vectors: vecs, inputTokens: in, billed: true, cost: cost,
		providerStatus: strconv.Itoa(status),
	}, nil
}

type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		PromptTokens int64 `json:"prompt_tokens"`
	} `json:"usage"`
}

func wireContent(m Message) json.RawMessage {
	if len(m.Images) == 0 {
		b, _ := json.Marshal(m.Text)
		return b
	}
	parts := []any{map[string]any{"type": "text", "text": m.Text}}
	for _, img := range m.Images {
		mime := img.MIME
		if mime == "" {
			mime = "image/jpeg"
		}
		detail := "low"
		if img.Resolution == ResolutionHigh || img.Resolution == ResolutionMedium {
			detail = "high"
		}
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url":    "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Blob),
				"detail": detail,
			},
		})
	}
	b, _ := json.Marshal(parts)
	return b
}

func jsonSchemaFormat(schema json.RawMessage) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "result", "strict": true, "schema": json.RawMessage(schema),
		},
	})
	if err != nil {
		return nil
	}
	return raw
}

func decodeWireContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return strings.TrimSpace(string(raw))
}

func jsonIfObject(s string) json.RawMessage {
	trim := strings.TrimSpace(s)
	if len(trim) == 0 || (trim[0] != '{' && trim[0] != '[') {
		return nil
	}
	if json.Valid([]byte(trim)) {
		return json.RawMessage(trim)
	}
	return nil
}

func annotationsFrom(raw []byte) ([]Citation, []Source) {
	var wrap struct {
		Choices []struct {
			Message struct {
				Annotations []struct {
					Type        string `json:"type"`
					URL         string `json:"url"`
					Title       string `json:"title"`
					StartIndex  int    `json:"start_index"`
					EndIndex    int    `json:"end_index"`
					URLCitation struct {
						URL        string `json:"url"`
						Title      string `json:"title"`
						StartIndex int    `json:"start_index"`
						EndIndex   int    `json:"end_index"`
					} `json:"url_citation"`
				} `json:"annotations"`
			} `json:"message"`
		} `json:"choices"`
		Citations []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"citations"`
	}
	if json.Unmarshal(raw, &wrap) != nil {
		return nil, nil
	}
	var cites []Citation
	var sources []Source
	add := func(url, title string, start, end int) {
		if url == "" {
			return
		}
		sources = append(sources, Source{URL: url, Title: title})
		cites = append(cites, Citation{Start: start, End: end, Source: len(sources) - 1})
	}
	for _, c := range wrap.Choices {
		for _, a := range c.Message.Annotations {
			url, title, start, end := a.URL, a.Title, a.StartIndex, a.EndIndex
			if a.URLCitation.URL != "" {
				url, title, start, end = a.URLCitation.URL, a.URLCitation.Title, a.URLCitation.StartIndex, a.URLCitation.EndIndex
			}
			add(url, title, start, end)
		}
	}
	if len(sources) == 0 {
		for _, c := range wrap.Citations {
			add(c.URL, c.Title, 0, 0)
		}
	}
	return cites, sources
}

type modelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		ContextLength int64  `json:"context_length"`
		TopProvider   struct {
			ContextLength       int64 `json:"context_length"`
			MaxCompletionTokens int64 `json:"max_completion_tokens"`
		} `json:"top_provider"`
		// OpenRouter publishes USD per token as decimal strings. OpenAI does not
		// publish prices at all, which is why Priced exists.
		Pricing struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing"`
	} `json:"data"`
}

func (p OpenAICompatible) Models(ctx context.Context, d Dispatch) ([]Model, error) {
	if d.Token == "" {
		return nil, ErrMissingCredential
	}
	status, raw, err := httpDo(ctx, p.client(), http.MethodGet, d.BaseURL+"/models",
		map[string]string{"Authorization": "Bearer " + d.Token}, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, providerError(d.ProviderID, status, raw)
	}
	var parsed modelsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID == "" {
			continue
		}
		entry := Model{
			Provider: d.ProviderID, ID: m.ID, DisplayName: m.Name,
			ContextWindow:   pickPositive(m.ContextLength, m.TopProvider.ContextLength),
			MaxOutputTokens: m.TopProvider.MaxCompletionTokens,
		}
		in, okIn := usdPerTokenToMicroPerMillion(m.Pricing.Prompt)
		outPrice, okOut := usdPerTokenToMicroPerMillion(m.Pricing.Completion)
		if okIn && okOut {
			entry.InputMicroUSDPerMillion, entry.OutputMicroUSDPerMillion = in, outPrice
			entry.Priced = true
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func pickPositive(vals ...int64) int64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// usdPerTokenToMicroPerMillion converts a published price into the integer unit the
// reservation ledger uses. It rounds up: a route may overestimate what a call will cost
// and refund the difference, but it may never underestimate it.
//
// A published price of exactly zero is reported as unpriced. Providers use "0" both for
// genuinely free models and as a placeholder, and a route built on a placeholder would
// reserve nothing for a call that does charge.
func usdPerTokenToMicroPerMillion(s string) (policy.MicroUSD, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, false
	}
	// USD per token → micro-USD per million tokens is a factor of 1e6 × 1e6.
	scaled := math.Ceil(v * 1e12)
	if scaled > float64(math.MaxInt64) {
		return 0, false
	}
	return policy.MicroUSD(int64(scaled)), true
}

func wireRole(role string) string {
	switch role {
	case "system", "assistant", "user":
		return role
	default:
		return "user"
	}
}
