package bidrl

import (
	"strings"
	"time"
)

const compReuseTTL = 7 * 24 * time.Hour

func normalizeModel(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func modelsEqual(a, b string) bool {
	n := normalizeModel(a)
	return n != "" && n == normalizeModel(b)
}

// conditionClass splits working-used lots from ones that should not share a street price.
func conditionClass(texts ...string) string {
	blob := strings.ToLower(strings.Join(texts, " "))
	for _, k := range impairedPhrases {
		if strings.Contains(blob, k) {
			return "impaired"
		}
	}
	return "typical"
}

var impairedPhrases = []string{
	"for parts", "parts only", "broken", "cracked", "smashed",
	"not working", "doesn't work", "does not work", "non-working", "non working",
	"missing parts", "missing piece", "incomplete",
}

func shouldReuseComp(model, priorModel string, retrieved time.Time, now time.Time, priorSignals, lotSignals []string) bool {
	if !modelsEqual(model, priorModel) {
		return false
	}
	if retrieved.IsZero() || now.Sub(retrieved) > compReuseTTL || retrieved.After(now.Add(time.Minute)) {
		return false
	}
	return conditionClass(priorSignals...) == conditionClass(lotSignals...)
}

func parseRetrieved(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

func reuseOrigin(lotID, reusedFrom string) string {
	if strings.TrimSpace(reusedFrom) != "" {
		return strings.TrimSpace(reusedFrom)
	}
	return lotID
}
