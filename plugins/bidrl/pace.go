package bidrl

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// errThrottled marks the stop that BidRL itself asked for. A user-triggered job just
// reports it; a scheduled one must also latch, because a cron that quietly retries
// into a ban is the failure worth engineering against.
var errThrottled = errors.New("bidrl: BidRL is refusing requests")

const (
	bidrlMinInterval = 400 * time.Millisecond
	rateLimitMax     = 3
)

// pacer spaces BidRL origin requests so a collect cannot stampede /api/ItemData.
type pacer struct {
	min time.Duration
	// backoff is the step waited after each refusal. A field only so a test can
	// exercise the latch without sleeping through the real one.
	backoff     time.Duration
	mu          sync.Mutex
	last        time.Time
	consecutive int
}

func newPacer(min time.Duration) *pacer {
	if min <= 0 {
		min = bidrlMinInterval
	}
	return &pacer{min: min, backoff: 2 * time.Second}
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
		return fmt.Errorf("%w: HTTP %d %d times in a row; stopping rather than continuing into a ban", errThrottled, status, n)
	}
	t := time.NewTimer(time.Duration(n) * p.backoff)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
