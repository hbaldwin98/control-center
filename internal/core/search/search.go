// Package search owns web lookup for plugins: query admission, engine dispatch, and
// filtering of result URLs. Plugins never choose SearXNG or hold its address.
//
// Layer 3. It imports policy. The engine (fake or SearXNG) is an implementation detail.
package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

var (
	ErrUnavailable = errors.New("search: engine unavailable")
	ErrInvalid     = errors.New("search: invalid query")
	ErrDenied      = errors.New("search: result denied")
	ErrNoPlugin    = errors.New("search: plugin identity missing")
)

const (
	defaultMaxResults = 4
	hardMaxResults    = 8
	maxQueryBytes     = 200
)

// Engine runs one lookup. Fake answers from fixtures; SearXNG calls the sidecar.
type Engine interface {
	Search(ctx context.Context, query string, limit int) ([]Hit, error)
}

// Hit is one public result after host filtering.
type Hit struct {
	URL     string
	Title   string
	Snippet string
}

// Options configures the engine. Zero values take defaults.
type Options struct {
	Engine Engine
	Log    *slog.Logger
}

func (o *Options) applyDefaults() {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Engine == nil {
		o.Engine = Fake{}
	}
}

// Service is the host search capability.
type Service struct {
	engine Engine
	gate   policy.Gate
	log    *slog.Logger
}

// New returns a search service. Engine is required after defaults (fake if unset).
func New(gate policy.Gate, opts Options) *Service {
	opts.applyDefaults()
	return &Service{engine: opts.Engine, gate: gate, log: opts.Log}
}

// Query admits a lookup, runs the engine, and drops non-public or off-allowlist hits.
func (s *Service) Query(ctx context.Context, req Request) ([]Hit, error) {
	pluginID, err := pluginID(ctx)
	if err != nil {
		return nil, err
	}
	if s.gate != nil {
		if err := s.gate.CheckWork(ctx, pluginID); err != nil {
			return nil, err
		}
	}
	q := strings.Join(strings.Fields(req.Query), " ")
	if q == "" || len(q) > maxQueryBytes {
		return nil, fmt.Errorf("%w: query must be 1–%d characters", ErrInvalid, maxQueryBytes)
	}
	limit := req.MaxResults
	if limit <= 0 {
		limit = defaultMaxResults
	}
	if limit > hardMaxResults {
		limit = hardMaxResults
	}
	allow, err := normalizeAllowlist(req.AllowedDomains)
	if err != nil {
		return nil, err
	}
	engineLimit := limit
	if allow != nil {
		engineLimit = hardMaxResults
	}
	raw, err := s.engine.Search(ctx, q, engineLimit)
	if err != nil {
		return nil, err
	}
	out := make([]Hit, 0, len(raw))
	for _, h := range raw {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cleaned, err := publicHit(h, allow)
		if err != nil {
			s.log.Debug("search dropped hit", "plugin", pluginID, "url", h.URL, "err", err)
			continue
		}
		out = append(out, cleaned)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Request is the internal query shape. pluginhost maps the host DTO onto this.
type Request struct {
	Query          string
	MaxResults     int
	AllowedDomains []string
}
