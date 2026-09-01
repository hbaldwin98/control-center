package bidrl

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	bidrlMinInterval = 400 * time.Millisecond
	rateLimitMax     = 3
)

// pacer spaces BidRL origin requests so a collect cannot stampede /api/ItemData.
type pacer struct {
	min         time.Duration
	mu          sync.Mutex
	last        time.Time
	consecutive int
}

func newPacer(min time.Duration) *pacer {
	if min <= 0 {
		min = bidrlMinInterval
	}
	return &pacer{min: min}
}

func (p *pacer) wait(ctx context.Context) error {
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

func (p *pacer) observe(ctx context.Context, status int) error {
	if status != 429 && status != 403 {
		p.mu.Lock()
		p.consecutive = 0
		p.mu.Unlock()
		return nil
	}
	p.mu.Lock()
	p.consecutive++
	n := p.consecutive
	p.mu.Unlock()
	if n >= rateLimitMax {
		return fmt.Errorf("bidrl: HTTP %d from BidRL %d times; stopping to avoid a ban", status, n)
	}
	backoff := time.Duration(n) * 2 * time.Second
	t := time.NewTimer(backoff)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
