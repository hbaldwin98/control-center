package search

import (
	"context"
	"sync"
	"time"
)

const defaultPace = 800 * time.Millisecond

// Pace wraps an Engine so lookups run one at a time with a gap between them.
// HTML backends (DuckDuckGo, Brave) hang up when a sidecar stampsedes them.
func Pace(inner Engine, min time.Duration) Engine {
	if inner == nil {
		return nil
	}
	if min <= 0 {
		min = defaultPace
	}
	return &paced{inner: inner, min: min}
}

type paced struct {
	inner Engine
	min   time.Duration
	mu    sync.Mutex
	last  time.Time
}

func (p *paced) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	if err := p.wait(ctx); err != nil {
		return nil, err
	}
	return p.inner.Search(ctx, query, limit)
}

func (p *paced) wait(ctx context.Context) error {
	p.mu.Lock()
	wait := time.Duration(0)
	if !p.last.IsZero() {
		wait = p.min - time.Since(p.last)
	}
	p.mu.Unlock()
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	p.mu.Lock()
	p.last = time.Now()
	p.mu.Unlock()
	return ctx.Err()
}
