package browser

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// contentStub stands in for the driver's page. playwright-go replies to Content on the
// same goroutine that runs onRoute and onResponse, so a reply can only land once any
// handler already running has taken and released the page lock. onEvent plays that
// handler; block, when set, makes the call never return.
type contentStub struct {
	playwright.Page
	onEvent func()
	block   chan struct{}
}

func (s *contentStub) Content() (string, error) {
	if s.block != nil {
		<-s.block
	}
	if s.onEvent != nil {
		s.onEvent()
	}
	return "<html><body>ok</body></html>", nil
}

func TestPlaywrightContentDoesNotHoldThePageLockAcrossTheDriverCall(t *testing.T) {
	stub := &contentStub{}
	p := &pwPage{sess: &pwSession{}, page: stub}
	stub.onEvent = func() {
		p.mu.Lock()
		p.mu.Unlock()
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.Content(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("content = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Content deadlocked against a response handler taking the page lock")
	}
}

func TestPlaywrightContentStopsOnACancelledContext(t *testing.T) {
	stub := &contentStub{block: make(chan struct{})}
	defer close(stub.block)
	p := &pwPage{sess: &pwSession{}, page: stub}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Content(ctx)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("content = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Content ignored a cancelled context while the driver call hung")
	}
}
