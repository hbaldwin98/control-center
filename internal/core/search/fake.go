package search

import (
	"context"
	"strings"
)

// Fake is the engine a deployment gets when no real one is configured, and the one
// core's own tests use. It returns one plausible result so the search path is
// exercisable without a network.
//
// It is deliberately generic. It used to carry fixtures for particular products a
// particular plugin looks up, which meant core knew what that plugin searches for. A
// plugin that needs specific results programs them in its own tests, through
// host/hosttest.
type Fake struct{}

func (Fake) Search(_ context.Context, query string, limit int) ([]Hit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	hits := []Hit{{
		URL:     "https://example-market.test/search",
		Title:   "Results for " + q,
		Snippet: "No exact match in fixtures.",
	}}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
