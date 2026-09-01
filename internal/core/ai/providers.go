package ai

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// ProviderKind selects the adapter that speaks to a provider. It is the wire protocol
// and auth style, not the vendor: OpenAI, OpenRouter, and a local server all speak
// KindOpenAICompatible, and they differ only in base URL and credential.
type ProviderKind string

const (
	// KindOpenAICompatible is the /v1 chat-completions, /v1 embeddings, and /v1 models
	// contract that OpenAI, OpenRouter, and most self-hosted servers implement,
	// authorized by a bearer API key.
	KindOpenAICompatible ProviderKind = "openai_compatible"
	// KindCodex is the ChatGPT subscription backend the Codex CLI talks to. It is
	// authorized by a subscription OAuth credential rather than an API key, and it
	// bills against the plan instead of per token.
	KindCodex ProviderKind = "codex"
	// KindFake is the in-process provider used by tests and local development.
	KindFake ProviderKind = "fake"
)

// Billing says how a provider charges, which decides what a request must reserve.
type Billing string

const (
	// BillingMetered charges per token. Every attempt needs pricing, and a request
	// reserves the most the whole attempt plan could cost before it dispatches.
	BillingMetered Billing = "metered"
	// BillingSubscription charges a flat fee already paid outside this system. The
	// marginal cost of a call is zero, so it reserves zero — which is a bounded
	// estimate, not a missing one. The plan's own rate limits are the real ceiling,
	// and they surface as provider errors rather than as budget.
	BillingSubscription Billing = "subscription"
)

// CodexBaseURL is the ChatGPT backend the Codex CLI uses. It is not the OpenAI platform
// API: it accepts a subscription bearer token plus an account id, and it is reachable
// only with the scopes the ChatGPT consent screen grants.
const CodexBaseURL = "https://chatgpt.com/backend-api/codex"

// ProviderConfig is one configured upstream. It is administrator-owned state in the
// database, not compiled-in configuration.
type ProviderConfig struct {
	ID           string       `json:"id"`
	Kind         ProviderKind `json:"kind"`
	BaseURL      string       `json:"baseUrl"`
	CredentialID string       `json:"credentialId"`
	Billing      Billing      `json:"billing"`
}

// Model is one entry in a provider's discovered catalog.
type Model struct {
	Provider        string `json:"provider"`
	ID              string `json:"id"`
	DisplayName     string `json:"displayName"`
	ContextWindow   int64  `json:"contextWindow"`
	MaxOutputTokens int64  `json:"maxOutputTokens"`

	// InputMicroUSDPerMillion and OutputMicroUSDPerMillion are set only when the
	// provider publishes prices. Priced says whether they mean anything: a zero price
	// that came from a provider that does not publish prices is not free, it is
	// unknown, and a route may not be built on it.
	InputMicroUSDPerMillion  policy.MicroUSD `json:"inputMicroUsdPerMillion"`
	OutputMicroUSDPerMillion policy.MicroUSD `json:"outputMicroUsdPerMillion"`
	Priced                   bool            `json:"priced"`

	FetchedAt time.Time `json:"fetchedAt"`
}

// RouteInput is an administrator-supplied logical route.
type RouteInput struct {
	Name            string              `json:"name"`
	Capabilities    []string            `json:"capabilities"`
	MaxInputTokens  int                 `json:"maxInputTokens"`
	MaxOutputTokens int                 `json:"maxOutputTokens"`
	Attempts        []RouteAttemptInput `json:"attempts"`
}

// RouteAttemptInput is one step of an attempt plan. Pricing is required for a metered
// provider and ignored for a subscription one.
type RouteAttemptInput struct {
	Provider                 string `json:"provider"`
	Model                    string `json:"model"`
	InputMicroUSDPerMillion  int64  `json:"inputMicroUsdPerMillion"`
	OutputMicroUSDPerMillion int64  `json:"outputMicroUsdPerMillion"`
}

// Admin is the administrator-only surface injected into web. It manages providers,
// discovers models, and edits routes. Nothing here returns credential material.
type Admin interface {
	Providers(ctx context.Context) ([]ProviderConfig, error)
	PutProvider(ctx context.Context, p ProviderConfig) error
	DeleteProvider(ctx context.Context, id string) error
	Models(ctx context.Context, providerID string, refresh bool) ([]Model, error)
	Routes(ctx context.Context) ([]RouteDescriptor, error)
	PutRoute(ctx context.Context, in RouteInput) error
	DeleteRoute(ctx context.Context, name string) error
}

var (
	ErrUnknownProvider = errors.New("ai: unknown provider")
	ErrProviderInUse   = errors.New("ai: provider is referenced by a route")
	ErrInvalidProvider = errors.New("ai: invalid provider configuration")
	ErrInvalidRoute    = errors.New("ai: invalid route")
	ErrDiscovery       = errors.New("ai: provider model discovery failed")
	ErrNotDiscoverable = errors.New("ai: provider does not publish a model catalog")
	// ErrRouteUncompiled is what a plugin sees when a route exists but its
	// configuration no longer resolves. It is deliberately distinct from
	// ErrUnknownRoute: the model name is right, the host's configuration is not.
	ErrRouteUncompiled  = errors.New("ai: route is not usable; see its last error")
	ErrSubscriptionOnly = errors.New("ai: provider requires a subscription oauth credential")
)

func (p ProviderConfig) validate() error {
	if !validName(p.ID) {
		return fmt.Errorf("%w: id must be lowercase letters, digits, hyphen, or underscore", ErrInvalidProvider)
	}
	if p.CredentialID == "" {
		return fmt.Errorf("%w: credential is required", ErrInvalidProvider)
	}
	switch p.Billing {
	case BillingMetered, BillingSubscription:
	default:
		return fmt.Errorf("%w: billing must be metered or subscription", ErrInvalidProvider)
	}
	switch p.Kind {
	case KindOpenAICompatible:
		if err := validBaseURL(p.BaseURL); err != nil {
			return err
		}
	case KindCodex:
		if p.Billing != BillingSubscription {
			return fmt.Errorf("%w: the codex backend bills against a ChatGPT plan", ErrInvalidProvider)
		}
		if err := validBaseURL(p.BaseURL); err != nil {
			return err
		}
	case KindFake:
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidProvider, p.Kind)
	}
	return nil
}

// normalize fills in what the administrator does not have to type.
func (p ProviderConfig) normalize() ProviderConfig {
	p.ID = strings.TrimSpace(p.ID)
	p.CredentialID = strings.TrimSpace(p.CredentialID)
	p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if p.Kind == KindCodex {
		// The ChatGPT backend is the default, not the only possibility: a deployment
		// may front it with a proxy. Billing is not negotiable, though — this adapter
		// authorizes with a plan, and a plan is what it charges.
		if p.BaseURL == "" {
			p.BaseURL = CodexBaseURL
		}
		p.Billing = BillingSubscription
	}
	if p.Billing == "" {
		p.Billing = BillingMetered
	}
	return p
}

// validBaseURL keeps a provider from being pointed at something that is not an HTTP
// API. Plain HTTP is allowed only on loopback, where a local model server lives.
func validBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: base url must be an absolute http(s) url", ErrInvalidProvider)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" {
			return nil
		}
		return fmt.Errorf("%w: plain http is allowed only on loopback", ErrInvalidProvider)
	default:
		return fmt.Errorf("%w: base url must be an absolute http(s) url", ErrInvalidProvider)
	}
}

func validName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, c := range s {
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
		if i == 0 && (c < 'a' || c > 'z') {
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}
