package web

import (
	"sort"

	"github.com/hbaldwin98/control-center/host"
	"github.com/hbaldwin98/control-center/internal/core/ai"
)

const (
	needMissing            = "missing"
	needReady              = "ready"
	needUnhealthy          = "unhealthy"
	needCapabilityMismatch = "capability_mismatch"
	defaultChatMaxInput    = 4000
	defaultChatMaxOutput   = 1024
	defaultVisionMaxInput  = 16000
	defaultEmbedMaxInput   = 8192
	defaultEmbedMaxOutput  = 1
	defaultMaxInput        = 8000
	defaultMaxOutput       = 2000
)

// modelNeedView is one declared logical route plus whether a matching healthy
// route currently exists. Status is computed at read time from live AI config.
type modelNeedView struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
	Purpose      string   `json:"purpose"`
	Status       string   `json:"status"`
	Healthy      bool     `json:"healthy"`
	LastError    string   `json:"lastError,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
}

func bindModelNeeds(needs []host.ModelNeed, routes []ai.RouteDescriptor) []modelNeedView {
	if len(needs) == 0 {
		return nil
	}
	byName := make(map[string]ai.RouteDescriptor, len(routes))
	for _, r := range routes {
		byName[r.LogicalName] = r
	}
	out := make([]modelNeedView, 0, len(needs))
	for _, n := range needs {
		v := modelNeedView{
			Name:         n.Name,
			Capabilities: append([]string(nil), n.Capabilities...),
			Purpose:      n.Purpose,
			Status:       needMissing,
		}
		r, ok := byName[n.Name]
		if !ok {
			out = append(out, v)
			continue
		}
		v.Healthy = r.Healthy
		v.LastError = r.LastError
		if len(r.AttemptPlan) > 0 {
			v.Provider = r.AttemptPlan[0].Provider
			v.Model = r.AttemptPlan[0].Model
		}
		switch {
		case !hasAllCapabilities(r.Capabilities, n.Capabilities):
			v.Status = needCapabilityMismatch
		case !r.Healthy:
			v.Status = needUnhealthy
		default:
			v.Status = needReady
		}
		out = append(out, v)
	}
	return out
}

func hasAllCapabilities(have, want []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, c := range have {
		set[c] = struct{}{}
	}
	for _, c := range want {
		if _, ok := set[c]; !ok {
			return false
		}
	}
	return true
}

func unionNeed(name string, declared []host.Manifest) (host.ModelNeed, bool) {
	var found host.ModelNeed
	caps := map[string]struct{}{}
	ok := false
	for _, m := range declared {
		for _, n := range m.Models {
			if n.Name != name {
				continue
			}
			ok = true
			if found.Name == "" {
				found = n
			}
			for _, c := range n.Capabilities {
				caps[c] = struct{}{}
			}
		}
	}
	if !ok {
		return host.ModelNeed{}, false
	}
	found.Capabilities = make([]string, 0, len(caps))
	for c := range caps {
		found.Capabilities = append(found.Capabilities, c)
	}
	sort.Strings(found.Capabilities)
	return found, true
}

func defaultRouteLimits(caps []string) (in, out int) {
	in, out = defaultMaxInput, defaultMaxOutput
	onlyChat := true
	onlyEmbed := true
	hasVision := false
	for _, c := range caps {
		if c == "vision" {
			hasVision = true
		}
		if c != "chat" {
			onlyChat = false
		}
		if c != "embed" {
			onlyEmbed = false
		}
	}
	if hasVision {
		return defaultVisionMaxInput, defaultMaxOutput
	}
	if onlyChat {
		return defaultChatMaxInput, defaultChatMaxOutput
	}
	if onlyEmbed {
		return defaultEmbedMaxInput, defaultEmbedMaxOutput
	}
	return in, out
}

func embedOnly(caps []string) bool {
	if len(caps) == 0 {
		return false
	}
	for _, c := range caps {
		if c != "embed" {
			return false
		}
	}
	return true
}

func pricesForAttempt(p ai.ProviderConfig, catalog []ai.Model, model string, inOverride, outOverride int64) (in, out int64, errMsg string) {
	return pricesForAttemptCaps(p, catalog, model, inOverride, outOverride, false)
}

func pricesForAttemptCaps(p ai.ProviderConfig, catalog []ai.Model, model string, inOverride, outOverride int64, embedOnlyRoute bool) (in, out int64, errMsg string) {
	if p.Billing == ai.BillingSubscription {
		return 0, 0, ""
	}
	if embedOnlyRoute {
		if inOverride > 0 {
			if outOverride < 0 {
				outOverride = 0
			}
			return inOverride, outOverride, ""
		}
		for _, m := range catalog {
			if m.ID != model {
				continue
			}
			if m.Priced && m.InputMicroUSDPerMillion > 0 {
				return int64(m.InputMicroUSDPerMillion), int64(m.OutputMicroUSDPerMillion), ""
			}
			return 0, 0, "that model has no published input price; enter dollars per million tokens"
		}
		return 0, 0, "unknown model; enter input dollars per million tokens, or load the catalog first"
	}
	if inOverride > 0 && outOverride > 0 {
		return inOverride, outOverride, ""
	}
	for _, m := range catalog {
		if m.ID != model {
			continue
		}
		if m.Priced && m.InputMicroUSDPerMillion > 0 && m.OutputMicroUSDPerMillion > 0 {
			return int64(m.InputMicroUSDPerMillion), int64(m.OutputMicroUSDPerMillion), ""
		}
		return 0, 0, "that model has no published price; enter input and output dollars per million tokens"
	}
	if inOverride > 0 && outOverride > 0 {
		return inOverride, outOverride, ""
	}
	return 0, 0, "unknown model; enter input and output dollars per million tokens, or load the catalog first"
}

func existingLimits(routes []ai.RouteDescriptor, name string) (in, out int, ok bool) {
	for _, r := range routes {
		if r.LogicalName == name && r.MaxInputTokens > 0 && r.MaxOutputTokens > 0 {
			return r.MaxInputTokens, r.MaxOutputTokens, true
		}
	}
	return 0, 0, false
}

func catalogLimits(catalog []ai.Model, model string) (in, out int) {
	for _, m := range catalog {
		if m.ID != model {
			continue
		}
		if m.ContextWindow > 0 {
			in = int(m.ContextWindow)
			if in > 32000 {
				in = 32000
			}
		}
		if m.MaxOutputTokens > 0 {
			out = int(m.MaxOutputTokens)
			if out > 4096 {
				out = 4096
			}
		}
		return in, out
	}
	return 0, 0
}
