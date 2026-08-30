package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
)

// sseFrame is one parsed Server-Sent Event.
type sseFrame struct {
	ID    string
	Event string
	Data  string
}

// readFrames reads up to n frames from an SSE body, skipping heartbeat comments.
func readFrames(t *testing.T, body *bufio.Reader, n int) []sseFrame {
	t.Helper()
	var out []sseFrame
	var cur sseFrame
	for len(out) < n {
		line, err := body.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame %d/%d: %v (got %+v)", len(out), n, err, out)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if cur.Event != "" {
				out = append(out, cur)
			}
			cur = sseFrame{}
		case strings.HasPrefix(line, ":"):
			// heartbeat
		case strings.HasPrefix(line, "id: "):
			cur.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			cur.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.Data = strings.TrimPrefix(line, "data: ")
		}
	}
	return out
}

// liveHarness runs the server over a real listener so SSE streaming works end to end.
type liveHarness struct {
	*harness
	server *httptest.Server
	bus    *events.Log
	client *http.Client
	origin string
}

func newLiveHarness(t *testing.T) *liveHarness {
	t.Helper()
	h := newHarness(t)

	bus, err := events.New(h.store, h.store, events.Options{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	bus.Start(ctx)
	t.Cleanup(func() { cancel(); bus.Stop() })

	h.server.deps.Events = bus

	ts := httptest.NewServer(h.server.Handler())
	t.Cleanup(ts.Close)

	return &liveHarness{
		harness: h,
		server:  ts,
		bus:     bus,
		client:  &http.Client{Timeout: 10 * time.Second},
		origin:  ts.URL,
	}
}

// login completes first-run bootstrap over the live server and returns the session cookie.
func (h *liveHarness) login(t *testing.T) *http.Cookie {
	t.Helper()
	token := h.issueToken()
	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/api/auth/bootstrap",
		strings.NewReader(`{"token":"`+token+`","password":"`+testPassword+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.origin)

	res, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap: %d", res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

// openStream connects to /api/stream with the given resume point.
func (h *liveHarness) openStream(t *testing.T, cookie *http.Cookie, query string, lastEventID string) (*bufio.Reader, func()) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/stream"+query, nil)
	req.AddCookie(cookie)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}

	res, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		t.Fatalf("stream: %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		res.Body.Close()
		t.Fatalf("content type = %q", ct)
	}
	return bufio.NewReader(res.Body), func() { res.Body.Close() }
}

func TestStreamRequiresAuthentication(t *testing.T) {
	h := newLiveHarness(t)
	res, err := h.client.Get(h.server.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", res.StatusCode)
	}
}

func TestStreamDeliversCommittedEvents(t *testing.T) {
	ctx := context.Background()
	h := newLiveHarness(t)
	cookie := h.login(t)

	body, closeStream := h.openStream(t, cookie, "", "")
	defer closeStream()

	var ids []string
	for range 3 {
		id, err := h.bus.Publish(ctx, events.Input{
			Type: "core.job.enqueued", Source: events.SourceJobs, Subject: "job-x",
			Payload: map[string]any{"plugin": "hello"},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, formatID(id))
	}

	frames := readFrames(t, body, 3)
	for i, f := range frames {
		if f.Event != "event" {
			t.Fatalf("frame %d has event %q", i, f.Event)
		}
		if f.ID != ids[i] {
			t.Fatalf("frame %d id = %q, want %q", i, f.ID, ids[i])
		}
		var e wireEvent
		if err := json.Unmarshal([]byte(f.Data), &e); err != nil {
			t.Fatal(err)
		}
		// IDs must be decimal strings; an int64 does not survive a JavaScript Number.
		if e.ID != ids[i] {
			t.Fatalf("payload id = %q, want %q", e.ID, ids[i])
		}
		if e.Type != "core.job.enqueued" || e.Subject != "job-x" {
			t.Fatalf("unexpected envelope: %+v", e)
		}
	}
}

func TestStreamReplaysFromLastEventID(t *testing.T) {
	ctx := context.Background()
	h := newLiveHarness(t)
	cookie := h.login(t)

	var ids []int64
	for range 4 {
		id, err := h.bus.Publish(ctx, events.Input{Type: "core.job.enqueued", Source: events.SourceJobs})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	// Resuming after the second event replays exactly the third and fourth.
	body, closeStream := h.openStream(t, cookie, "", formatID(ids[1]))
	defer closeStream()

	frames := readFrames(t, body, 2)
	if frames[0].ID != formatID(ids[2]) || frames[1].ID != formatID(ids[3]) {
		t.Fatalf("replayed %q and %q, want %d and %d",
			frames[0].ID, frames[1].ID, ids[2], ids[3])
	}
}

func TestStreamAcceptsAfterQueryParam(t *testing.T) {
	ctx := context.Background()
	h := newLiveHarness(t)
	cookie := h.login(t)

	first, err := h.bus.Publish(ctx, events.Input{Type: "core.job.enqueued", Source: events.SourceJobs})
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.bus.Publish(ctx, events.Input{Type: "core.job.failed", Source: events.SourceJobs})
	if err != nil {
		t.Fatal(err)
	}

	body, closeStream := h.openStream(t, cookie, "?after="+formatID(first), "")
	defer closeStream()

	frames := readFrames(t, body, 1)
	if frames[0].ID != formatID(second) {
		t.Fatalf("got id %q, want %d", frames[0].ID, second)
	}
}

func TestStreamResetsWhenTheResumePointIsUnretained(t *testing.T) {
	ctx := context.Background()
	h := newLiveHarness(t)
	cookie := h.login(t)

	for range 3 {
		if _, err := h.bus.Publish(ctx, events.Input{Type: "core.job.enqueued", Source: events.SourceJobs}); err != nil {
			t.Fatal(err)
		}
	}
	// Age every row past retention and run a pass, so the early IDs are gone.
	old := time.Now().UTC().Add(-200 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := h.store.Exec(ctx, `UPDATE core_events SET created_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	rep, err := h.bus.RunRetention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Deleted != 3 {
		t.Fatalf("deleted %d rows, want 3", rep.Deleted)
	}

	body, closeStream := h.openStream(t, cookie, "?after=1", "")
	defer closeStream()

	// A newer event so the stream has something to send after the reset.
	newest, err := h.bus.Publish(ctx, events.Input{Type: "core.job.failed", Source: events.SourceJobs})
	if err != nil {
		t.Fatal(err)
	}

	frames := readFrames(t, body, 2)
	if frames[0].Event != "reset" {
		t.Fatalf("first frame = %+v, want a reset", frames[0])
	}
	var reset struct {
		OldestRetainedID string `json:"oldestRetainedId"`
	}
	if err := json.Unmarshal([]byte(frames[0].Data), &reset); err != nil {
		t.Fatal(err)
	}
	if reset.OldestRetainedID != formatID(rep.OldestRetainedID) {
		t.Fatalf("reset boundary = %q, want %d", reset.OldestRetainedID, rep.OldestRetainedID)
	}
	if frames[1].ID != formatID(newest) {
		t.Fatalf("post-reset frame id = %q, want %d", frames[1].ID, newest)
	}
}

func TestStreamRejectsAnInvalidResumePoint(t *testing.T) {
	h := newLiveHarness(t)
	cookie := h.login(t)

	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/stream?after=notanumber", nil)
	req.AddCookie(cookie)
	res, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", res.StatusCode)
	}
}

func TestEventsQueryEndpoint(t *testing.T) {
	ctx := context.Background()
	h := newLiveHarness(t)

	if _, err := h.bus.Publish(ctx, events.Input{Type: "core.job.enqueued", Source: events.SourceJobs}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.bus.Publish(ctx, events.Input{Type: "bidrl.deal_found", Source: "bidrl"}); err != nil {
		t.Fatal(err)
	}

	token := h.issueToken()
	if rec := h.do(http.MethodPost, "/api/auth/bootstrap",
		map[string]string{"token": token, "password": testPassword}); rec.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", rec.Code, rec.Body)
	}

	rec := h.do(http.MethodGet, "/api/events?pattern=bidrl.**", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("query: %d %s", rec.Code, rec.Body)
	}
	var snap struct {
		Data        eventsPage `json:"data"`
		AsOfEventID string     `json:"asOfEventId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Data.Events) != 1 || snap.Data.Events[0].Type != "bidrl.deal_found" {
		t.Fatalf("filtered query returned %+v", snap.Data.Events)
	}
	if snap.AsOfEventID == "" || snap.Data.OldestRetainedID == "" {
		t.Fatalf("missing boundaries: %+v", snap)
	}

	if rec := h.do(http.MethodGet, "/api/events?pattern=Not..Valid", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid pattern: got %d, want 400", rec.Code)
	}
}

func TestBootstrapSnapshotCarriesTheEventBoundary(t *testing.T) {
	ctx := context.Background()
	h := newLiveHarness(t)

	tail, err := h.bus.Publish(ctx, events.Input{Type: "core.job.enqueued", Source: events.SourceJobs})
	if err != nil {
		t.Fatal(err)
	}

	token := h.issueToken()
	if rec := h.do(http.MethodPost, "/api/auth/bootstrap",
		map[string]string{"token": token, "password": testPassword}); rec.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", rec.Code, rec.Body)
	}

	rec := h.do(http.MethodGet, "/api/bootstrap", nil)
	var snap struct {
		Data        shellBootstrap `json:"data"`
		AsOfEventID string         `json:"asOfEventId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.AsOfEventID != formatID(tail) {
		t.Fatalf("asOfEventId = %q, want %d", snap.AsOfEventID, tail)
	}
	if snap.Data.OldestRetainedID != "1" {
		t.Fatalf("oldestRetainedId = %q, want 1", snap.Data.OldestRetainedID)
	}
}
