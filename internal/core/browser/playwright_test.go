package browser

import (
	"errors"
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
