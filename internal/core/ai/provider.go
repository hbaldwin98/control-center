package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// Provider is one adapter: a wire protocol plus an auth style. Adapters are chosen by
// ProviderKind, and a single adapter serves every configured provider of that kind.
type Provider interface {
	Kind() ProviderKind
	// Chat runs one attempt. Returning a result with billed set means the provider
	// charged for it, whatever the outcome.
	Chat(ctx context.Context, d Dispatch, req ChatRequest) (providerResult, error)
	// Embed runs one embedding attempt. billed has the same meaning as Chat.
	Embed(ctx context.Context, d Dispatch, req EmbedRequest) (providerResult, error)
	// Models lists what this credential can reach. ErrNotDiscoverable is a valid
	// answer for a provider with no catalog endpoint.
	Models(ctx context.Context, d Dispatch) ([]Model, error)
}

// Dispatch is everything an adapter needs for one call: the resolved endpoint, the
// resolved credential, and the pricing the reservation was computed from.
type Dispatch struct {
	ProviderID string
	BaseURL    string
	Token      string
	// AccountID is the subscription account a plan-backed provider requires on every
	// request. It is empty for API-key providers.
	AccountID string
	Model     string
	Billing   Billing

	attempt attempt
}

func (d Dispatch) charge(in, out int64) (policy.MicroUSD, error) { return d.attempt.charge(in, out) }

// maxRespBytes bounds what an adapter will read from a provider. A provider that
// streams without end must not be able to exhaust this process's memory.
const maxRespBytes = 8 << 20

// httpDo issues a JSON request and returns the decoded status and body. Errors are
// classified so routing can decide whether to spill over to the next attempt.
func httpDo(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxRespBytes))
	if err != nil {
		return res.StatusCode, nil, err
	}
	return res.StatusCode, raw, nil
}

// errorClass normalizes an HTTP status into the vocabulary routing spills over on.
// Only transient, capacity-shaped failures spill; a refusal or a bad request does not
// become someone else's problem.
func errorClass(status int) string {
	switch {
	case status == http.StatusTooManyRequests:
		return "rate_limit"
	case status == http.StatusPaymentRequired || status == http.StatusForbidden:
		return "quota"
	case status == http.StatusUnauthorized:
		return "credential"
	case status >= 500:
		return "provider_5xx"
	case status >= 400:
		return "provider"
	default:
		return ""
	}
}

// providerError keeps the provider's own words out of plugin-facing errors while
// leaving enough in the attempt audit to debug with.
func providerError(providerID string, status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return fmt.Errorf("ai: provider %s returned %d: %s", providerID, status, msg)
}

func defaultClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute}
}
