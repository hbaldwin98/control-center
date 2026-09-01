package search

import (
	"context"
	"strings"
)

// Fake answers from in-process fixtures. Tests and local runs without SearXNG.
type Fake struct{}

func (Fake) Search(_ context.Context, query string, limit int) ([]Hit, error) {
	q := strings.ToLower(query)
	var hits []Hit
	switch {
	case strings.Contains(q, "keurig") || strings.Contains(q, "k-supreme") || strings.Contains(q, "k supreme"):
		hits = []Hit{{
			URL:     "https://www.ebay.com/itm/k-supreme-plus",
			Title:   "Keurig K-Supreme Plus sold listing",
			Snippet: "Sold listing for K-Supreme Plus at $129 used.",
		}}
	case strings.Contains(q, "dewalt") || strings.Contains(q, "dcd791"):
		hits = []Hit{{
			URL:     "https://www.ebay.com/itm/dewalt-dcd791",
			Title:   "DeWalt DCD791 used drill",
			Snippet: "Asking $89 for a used DeWalt DCD791 20V drill.",
		}}
	default:
		hits = []Hit{{
			URL:     "https://example-market.test/search",
			Title:   "Market listings",
			Snippet: "No exact model match in fixtures.",
		}}
	}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
