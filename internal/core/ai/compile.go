package ai

import (
	"fmt"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// route is a compiled logical name: a finite attempt plan with hard request limits and
// a bounded maximum cost. A route that cannot be compiled is kept with lastError set
// and refuses to dispatch, so an administrator can see and fix it rather than having
// the process fail to start over an edit made from the UI.
type route struct {
	name            string
	capabilities    map[string]struct{}
	capList         []string
	maxInputTokens  int
	maxOutputTokens int
	attempts        []attempt
	lastError       string
}

// attempt is one step of a plan, resolved against its provider.
type attempt struct {
	providerID string
	kind       ProviderKind
	baseURL    string
	credential string
	billing    Billing
	model      string

	inPerM, outPerM policy.MicroUSD
}

func (a attempt) metered() bool { return a.billing != BillingSubscription }

// compileRoute resolves a stored route against the current provider set.
func compileRoute(in RouteInput, providers map[string]ProviderConfig) (route, error) {
	r := route{
		name:            in.Name,
		maxInputTokens:  in.MaxInputTokens,
		maxOutputTokens: in.MaxOutputTokens,
		capabilities:    map[string]struct{}{},
		capList:         append([]string{}, in.Capabilities...),
	}
	for _, c := range in.Capabilities {
		r.capabilities[c] = struct{}{}
	}
	if !validName(in.Name) {
		return r, fmt.Errorf("%w: name must be lowercase letters, digits, hyphen, or underscore", ErrInvalidRoute)
	}
	if in.MaxInputTokens <= 0 || in.MaxOutputTokens <= 0 {
		return r, fmt.Errorf("%w: route %q: limits must be finite and positive", ErrInvalidRoute, in.Name)
	}
	if len(in.Attempts) == 0 {
		return r, fmt.Errorf("%w: route %q: attempt plan is empty", ErrInvalidRoute, in.Name)
	}
	if len(r.capList) == 0 {
		return r, fmt.Errorf("%w: route %q: declare at least one capability", ErrInvalidRoute, in.Name)
	}
	for _, ain := range in.Attempts {
		p, ok := providers[ain.Provider]
		if !ok {
			return r, fmt.Errorf("%w: route %q names provider %q", ErrUnknownProvider, in.Name, ain.Provider)
		}
		if ain.Model == "" {
			return r, fmt.Errorf("%w: route %q: attempt on %q names no model", ErrInvalidRoute, in.Name, ain.Provider)
		}
		a := attempt{
			providerID: p.ID, kind: p.Kind, baseURL: p.BaseURL,
			credential: p.CredentialID, billing: p.Billing, model: ain.Model,
			inPerM:  policy.MicroUSD(ain.InputMicroUSDPerMillion),
			outPerM: policy.MicroUSD(ain.OutputMicroUSDPerMillion),
		}
		if a.metered() {
			if a.inPerM <= 0 {
				return r, fmt.Errorf("%w: route %q attempt %s/%s", ErrMissingPrice, in.Name, p.ID, ain.Model)
			}
			// Embedding-only routes charge input tokens. Chat still needs an output price
			// because a completion is what the reservation is bounding.
			if r.has("chat") && a.outPerM <= 0 {
				return r, fmt.Errorf("%w: route %q attempt %s/%s", ErrMissingPrice, in.Name, p.ID, ain.Model)
			}
		}
		r.attempts = append(r.attempts, a)
	}
	if !r.has("chat") && !r.has("embed") {
		return r, fmt.Errorf("%w: route %q: declare chat or embed", ErrInvalidRoute, in.Name)
	}
	// Reject at build time what would otherwise only be discovered on the first paid
	// request: an attempt plan whose maximum cost overflows is not a bounded plan.
	if r.has("chat") {
		if _, err := r.estimateChat(0); err != nil {
			return r, fmt.Errorf("%w: route %q: %v", ErrUnbounded, in.Name, err)
		}
	} else if _, err := r.estimateEmbed(); err != nil {
		return r, fmt.Errorf("%w: route %q: %v", ErrUnbounded, in.Name, err)
	}
	return r, nil
}

func (r route) has(cap string) bool {
	_, ok := r.capabilities[cap]
	return ok
}

func (r route) usable() bool { return r.lastError == "" }

// estimateChat is the conservative maximum the whole attempt plan could cost.
//
// A subscription attempt contributes zero. That is a computed bound, not a missing
// one: the marginal cost of the call really is zero, because the plan was paid for
// outside this system. Budgets therefore do not constrain subscription routes, and the
// provider's own rate limits do.
func (r route) estimateChat(maxTokens int) (policy.MicroUSD, error) {
	outTok := r.maxOutputTokens
	if maxTokens > 0 && maxTokens < outTok {
		outTok = maxTokens
	}
	var total policy.MicroUSD
	for _, a := range r.attempts {
		if !a.metered() {
			continue
		}
		in, err := tokensCost(int64(r.maxInputTokens), a.inPerM)
		if err != nil {
			return 0, err
		}
		out, err := tokensCost(int64(outTok), a.outPerM)
		if err != nil {
			return 0, err
		}
		sum := in + out
		if sum < in {
			return 0, ErrUnbounded
		}
		next := total + sum
		if next < total {
			return 0, ErrUnbounded
		}
		total = next
	}
	return total, nil
}

// estimateEmbed is the conservative maximum of embedding the route's full input
// window. Embedding providers do not emit completion tokens, so output price is
// not part of the bound.
func (r route) estimateEmbed() (policy.MicroUSD, error) {
	var total policy.MicroUSD
	for _, a := range r.attempts {
		if !a.metered() {
			continue
		}
		in, err := tokensCost(int64(r.maxInputTokens), a.inPerM)
		if err != nil {
			return 0, err
		}
		next := total + in
		if next < total {
			return 0, ErrUnbounded
		}
		total = next
	}
	return total, nil
}

func tokensCost(tokens int64, perMillion policy.MicroUSD) (policy.MicroUSD, error) {
	if tokens < 0 || perMillion <= 0 {
		return 0, ErrUnbounded
	}
	// Round up: (tokens * perMillion + 999_999) / 1_000_000
	n := tokens * int64(perMillion)
	if tokens != 0 && n/tokens != int64(perMillion) {
		return 0, ErrUnbounded
	}
	return policy.MicroUSD((n + 999_999) / 1_000_000), nil
}

// charge is what an attempt actually cost, given the tokens the provider reported.
func (a attempt) charge(inTok, outTok int64) (policy.MicroUSD, error) {
	if !a.metered() {
		return 0, nil
	}
	in, err := tokensCost(inTok, a.inPerM)
	if err != nil {
		return 0, err
	}
	if outTok == 0 || a.outPerM == 0 {
		return in, nil
	}
	out, err := tokensCost(outTok, a.outPerM)
	if err != nil {
		return 0, err
	}
	sum := in + out
	if sum < in {
		return 0, ErrUnbounded
	}
	return sum, nil
}
