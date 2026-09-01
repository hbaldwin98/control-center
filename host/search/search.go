// Package search is the plugin-facing host-managed web search surface.
// Plugins pass a query; they never choose an engine or see the SearXNG URL.
package search

import (
	"context"
	"errors"
)

// Search runs a web lookup the host owns. Disable rejects new queries.
type Search interface {
	Query(ctx context.Context, req Request) ([]Hit, error)
}

// Request is one lookup. MaxResults is capped by the host.
type Request struct {
	Query string
	// MaxResults is how many hits to keep after filtering. Zero uses the host default.
	MaxResults int
	// AllowedDomains, when nonempty, keeps only HTTPS results whose host is on the
	// list or is a subdomain of one (ebay.com keeps www.ebay.com). Empty means any
	// public HTTPS host. Plugins cannot search private networks.
	AllowedDomains []string
}

// Hit is one public web result. URL is the citation target a plugin may store.
type Hit struct {
	URL     string
	Title   string
	Snippet string
}

// Errors a plugin must handle.
var (
	ErrUnavailable = errors.New("search: engine unavailable")
	ErrInvalid     = errors.New("search: invalid query")
	ErrDenied      = errors.New("search: result denied")
)
