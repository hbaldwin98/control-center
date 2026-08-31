package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// CodexClientVersion is sent as the client version this adapter claims to be. The
// ChatGPT backend uses it to decide which models a client may list and call, so it has
// to name a version that understands the responses it will get back.
const CodexClientVersion = "0.151.0"

// codexOriginator identifies the client to the ChatGPT backend, the same way the Codex
// CLI identifies itself.
const codexOriginator = "codex_cli_rs"

// Codex talks to the ChatGPT backend the Codex CLI uses, authorized by a subscription
// OAuth credential rather than an API key.
//
// It speaks the Responses API, always streaming, and never stores conversation state
// server-side: this host owns conversation state, and a plugin's transcript is not the
// provider's to keep. Usage is reported and recorded, but the call costs nothing
// marginal, because the ChatGPT plan already paid for it.
type Codex struct {
	Client *http.Client
}

func (Codex) Kind() ProviderKind { return KindCodex }

func (p Codex) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return defaultClient()
}

func (p Codex) headers(d Dispatch) map[string]string {
	h := map[string]string{
		"Authorization": "Bearer " + d.Token,
		"originator":    codexOriginator,
		"version":       CodexClientVersion,
	}
	if d.AccountID != "" {
		h["ChatGPT-Account-Id"] = d.AccountID
	}
	return h
}

type responsesRequest struct {
	Model             string          `json:"model"`
	Instructions      string          `json:"instructions,omitempty"`
	Input             []responsesItem `json:"input"`
	ToolChoice        string          `json:"tool_choice"`
	ParallelToolCalls bool            `json:"parallel_tool_calls"`
	Store             bool            `json:"store"`
	Stream            bool            `json:"stream"`
	Include           []string        `json:"include"`
}

type responsesItem struct {
	Type    string             `json:"type"`
	Role    string             `json:"role"`
	Content []responsesContent `json:"content"`
}

type responsesContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

func (p Codex) Chat(ctx context.Context, d Dispatch, req ChatRequest) (providerResult, error) {
	if d.Token == "" {
		return providerResult{errClass: "credential"}, ErrMissingCredential
	}
	if d.AccountID == "" {
		// Without the account id the backend cannot tell which plan to bill against,
		// and answers 401 for every request. Fail before dispatch so the attempt is
		// recorded as a credential problem rather than a provider one.
		return providerResult{errClass: "credential"}, fmt.Errorf(
			"%w: %s has no ChatGPT account id; reauthorize the credential", ErrMissingCredential, d.ProviderID)
	}

	// The ChatGPT backend rejects max_output_tokens outright ("Unsupported parameter"),
	// so the caller's cap cannot be pushed down the wire here. It still bounds the
	// reservation host-side; this path is subscription-billed, so an answer longer than
	// the cap costs nothing extra and the caller truncates what it stores.
	body := responsesRequest{
		Model: d.Model, ToolChoice: "auto", Store: false, Stream: true,
		Include: []string{},
	}
	for _, m := range req.Messages {
		// The Responses API separates the standing instruction from the turn history.
		if m.Role == "system" {
			body.Instructions = joinInstructions(body.Instructions, m.Text)
			continue
		}
		content := "input_text"
		if m.Role == "assistant" {
			content = "output_text"
		}
		body.Input = append(body.Input, responsesItem{
			Type: "message", Role: wireRole(m.Role),
			Content: []responsesContent{{Type: content, Text: m.Text}},
		})
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return providerResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL+"/responses", strings.NewReader(string(raw)))
	if err != nil {
		return providerResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	for k, v := range p.headers(d) {
		httpReq.Header.Set(k, v)
	}

	res, err := p.client().Do(httpReq)
	if err != nil {
		return providerResult{errClass: "transport"}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		class := errorClass(res.StatusCode)
		return providerResult{
			errClass: class, providerStatus: strconv.Itoa(res.StatusCode),
		}, providerError(d.ProviderID, res.StatusCode, msg)
	}

	text, usage, err := readResponsesStream(res.Body)
	if err != nil {
		// The stream broke partway. The plan was still consumed, so report usage as
		// far as it got rather than claiming the turn never happened.
		return providerResult{
			text: text, inputTokens: usage.InputTokens, outputTokens: usage.OutputTokens,
			errClass: "transport", providerStatus: strconv.Itoa(res.StatusCode),
		}, err
	}
	in, out := usage.InputTokens, usage.OutputTokens
	if in == 0 && out == 0 {
		in, out = approxTokens(lastText(req.Messages)), approxTokens(text)
	}
	cost, err := d.charge(in, out)
	if err != nil {
		return providerResult{errClass: "accounting_invariant"}, err
	}
	// billed stays false: the subscription was charged, this request was not. The
	// reservation was zero, so there is nothing to settle beyond the usage record.
	return providerResult{
		text: text, inputTokens: in, outputTokens: out, cost: cost,
		providerStatus: strconv.Itoa(res.StatusCode),
	}, nil
}

func joinInstructions(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + "\n\n" + next
}

// readResponsesStream aggregates a Responses API event stream into one turn.
//
// Text arrives as deltas and again in the completed event. Only the deltas are
// accumulated, and the completed event supplies usage and the authoritative text when
// no delta arrived at all.
func readResponsesStream(body io.Reader) (string, responsesUsage, error) {
	var (
		text  strings.Builder
		usage responsesUsage
		final string
	)
	scanner := bufio.NewScanner(io.LimitReader(body, maxRespBytes))
	scanner.Buffer(make([]byte, 0, 64<<10), maxRespBytes)
	for scanner.Scan() {
		line := scanner.Text()
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var evt struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Output []struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
				Usage responsesUsage `json:"usage"`
			} `json:"response"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			continue // an event shape this adapter does not model is not an error
		}
		switch evt.Type {
		case "response.output_text.delta":
			text.WriteString(evt.Delta)
		case "response.completed", "response.incomplete":
			usage = evt.Response.Usage
			for _, item := range evt.Response.Output {
				for _, c := range item.Content {
					if c.Type == "output_text" {
						final += c.Text
					}
				}
			}
		case "response.failed", "error":
			msg := evt.Error.Message
			if msg == "" {
				msg = "provider reported a failed response"
			}
			return text.String(), usage, fmt.Errorf("ai: codex stream: %s", msg)
		}
	}
	if err := scanner.Err(); err != nil {
		return text.String(), usage, err
	}
	if text.Len() == 0 {
		return final, usage, nil
	}
	return text.String(), usage, nil
}

type codexModelsResponse struct {
	Models []struct {
		Slug           string `json:"slug"`
		DisplayName    string `json:"display_name"`
		ContextWindow  int64  `json:"context_window"`
		Visibility     string `json:"visibility"`
		SupportedInAPI bool   `json:"supported_in_api"`
	} `json:"models"`
}

// Models asks the ChatGPT backend what this plan can reach. The answer depends on the
// subscription, so it is discovered per credential rather than compiled in.
func (p Codex) Models(ctx context.Context, d Dispatch) ([]Model, error) {
	if d.Token == "" {
		return nil, ErrMissingCredential
	}
	url := d.BaseURL + "/models?client_version=" + CodexClientVersion
	status, raw, err := httpDo(ctx, p.client(), http.MethodGet, url, p.headers(d), nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, providerError(d.ProviderID, status, raw)
	}
	var parsed codexModelsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		if m.Slug == "" || m.Visibility == "hidden" {
			continue
		}
		out = append(out, Model{
			Provider: d.ProviderID, ID: m.Slug, DisplayName: m.DisplayName,
			ContextWindow: m.ContextWindow,
			// A plan-backed model has no per-token price. Priced stays false, and a
			// subscription route does not need one.
			Priced: false,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
