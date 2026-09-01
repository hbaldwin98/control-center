package browser

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func playwrightEngine(t *testing.T) *Playwright {
	t.Helper()
	eng, err := startPlaywright(false)
	if err != nil {
		t.Skipf("playwright unavailable: %v", err)
	}
	return eng
}

func TestPlaywrightDeniesPrivateConnect(t *testing.T) {
	h := newHarnessWithEngine(t, playwrightEngine(t))
	ctx := h.ctxHello()
	sess, err := h.svc.Open(ctx, OpenOptions{AllowedHosts: []string{"localhost"}})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close(ctx)
	page, err := sess.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close(ctx)

	if err := page.Goto(ctx, "https://localhost/"); !errors.Is(err, ErrDenied) {
		t.Fatalf("localhost: %v", err)
	}
}

func TestPlaywrightGotoPublicSite(t *testing.T) {
	h := newHarnessWithEngine(t, playwrightEngine(t))
	ctx := h.ctxHello()
	sess, err := h.svc.Open(ctx, OpenOptions{AllowedHosts: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close(ctx)
	page, err := sess.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close(ctx)

	if err := page.Goto(ctx, "https://example.com/"); err != nil {
		if errors.Is(err, ErrDenied) {
			t.Fatalf("example.com denied: %v", err)
		}
		t.Skipf("public fetch failed (offline?): %v", err)
	}
	html, err := page.Content(ctx)
	if err != nil || !strings.Contains(strings.ToLower(html), "example") {
		t.Fatalf("content %q %v", html, err)
	}
	if err := page.WaitFor(ctx, "h1", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPlaywrightCloseIsIdempotent(t *testing.T) {
	eng := playwrightEngine(t)
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
}

// A denied subresource must not fail the operation: the route is aborted and the
// request reported, so the page loads without the third-party asset.
func TestBlockedSubresourceIsReportedNotFatal(t *testing.T) {
	p := &pwPage{}
	p.block(false, "https://cdn.example.com/a.js", denyTarget("host", "cdn.example.com is not allowed"))
	p.block(false, "https://cdn.example.com/a.js", denyTarget("host", "duplicate"))
	if p.takeDenied() != nil {
		t.Fatal("subresource denial must not fail the operation")
	}
	blocked := p.TakeBlocked()
	if len(blocked) != 1 {
		t.Fatalf("blocked = %d, want 1 (deduped)", len(blocked))
	}
	if blocked[0].Reason != "host" || blocked[0].URL != "https://cdn.example.com/a.js" {
		t.Fatalf("blocked = %+v", blocked[0])
	}
	if p.TakeBlocked() != nil {
		t.Fatal("TakeBlocked must clear")
	}
}

// A denied navigation is the document the caller asked for, so it must fail.
func TestBlockedNavigationIsFatal(t *testing.T) {
	p := &pwPage{}
	p.block(true, "https://evil.example.com/", denyTarget("host", "evil.example.com is not allowed"))
	err := p.takeDenied()
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("denied = %v, want ErrDenied", err)
	}
	if len(p.TakeBlocked()) != 0 {
		t.Fatal("navigation denial must not be reported as a blocked subresource")
	}
}

func TestBlockedReportIsCapped(t *testing.T) {
	p := &pwPage{}
	for i := 0; i < maxBlockedReports*2; i++ {
		p.block(false, fmt.Sprintf("https://cdn%d.example.com/a.js", i), denyTarget("host", "nope"))
	}
	if got := len(p.TakeBlocked()); got != maxBlockedReports {
		t.Fatalf("blocked = %d, want %d", got, maxBlockedReports)
	}
}
