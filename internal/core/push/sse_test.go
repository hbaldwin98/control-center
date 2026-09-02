package push

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseClient opens a streaming request against a handler and reads whole frames.
type sseClient struct {
	t      *testing.T
	body   *bufio.Reader
	close  func()
	server *httptest.Server
}

func openSSE(t *testing.T, h *harness, pluginID, query string) *sseClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.svc.ServeSSE(w, r, pluginID)
	}))
	t.Cleanup(server.Close)

	resp, err := server.Client().Get(server.URL + "?" + query)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}
	return &sseClient{t: t, body: bufio.NewReader(resp.Body), server: server,
		close: func() { _ = resp.Body.Close() }}
}

// frame reads up to the blank line that terminates one event.
func (c *sseClient) frame() string {
	c.t.Helper()
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		for {
			line, err := c.body.ReadString('\n')
			if err != nil {
				done <- b.String()
				return
			}
			if line == "\n" {
				done <- b.String()
				return
			}
			b.WriteString(line)
		}
	}()
	select {
	case got := <-done:
		return got
	case <-time.After(5 * time.Second):
		c.t.Fatal("no frame")
	}
	return ""
}

func TestServeSSEFramesTheTopicAndPayload(t *testing.T) {
	h := newHarness(t, Options{})
	client := openSSE(t, h, "hello", "topics=lot:1,lot:2")

	if got := client.frame(); !strings.Contains(got, "event: open") {
		t.Fatalf("first frame = %q", got)
	}

	// Wait for the handler's Open to have registered before publishing, so the test
	// is not racing the connection it just made.
	waitSubscribers(t, h, "hello", "lot:1", 1)
	if err := h.svc.Publish(h.ctx, "hello", "lot:1", map[string]any{"bid": 1500}); err != nil {
		t.Fatal(err)
	}

	got := client.frame()
	// The topic rides in the body, not the event name, so a client registers one
	// listener per kind rather than one per row on screen.
	for _, want := range []string{"event: message", `"topic":"lot:1"`, `"bid":1500`} {
		if !strings.Contains(got, want) {
			t.Fatalf("frame = %q, want %q", got, want)
		}
	}
}

func TestServeSSEReleasesTheTopicWhenTheClientGoesAway(t *testing.T) {
	h := newHarness(t, Options{})
	rec := newRecorder()
	defer h.svc.SetWatcher("hello", rec)()

	client := openSSE(t, h, "hello", "topics=lot:1")
	client.frame()
	waitFor(t, rec.joined, "lot:1")

	// Navigating away ends the request, which must end the upstream work with it.
	client.close()
	waitFor(t, rec.left, "lot:1")
}

func TestServeSSERejectsAnUnusableRequest(t *testing.T) {
	h := newHarness(t, Options{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.svc.ServeSSE(w, r, "hello")
	}))
	defer server.Close()

	for query, want := range map[string]int{
		"":                  http.StatusBadRequest, // no topics
		"topics=":           http.StatusBadRequest,
		"topics=lot%201":    http.StatusBadRequest, // a space is not a topic
		"topics=" + tooLong: http.StatusBadRequest,
	} {
		resp, err := server.Client().Get(server.URL + "?" + query)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET ?%s status = %d, want %d", query, resp.StatusCode, want)
		}
	}
}

func TestServeSSERefusesADisabledPlugin(t *testing.T) {
	h := newHarness(t, Options{})
	if err := h.pol.Disable(h.ctx, "hello", "test", "kill switch"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.svc.ServeSSE(w, r, "hello")
	}))
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "?topics=lot:1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

var tooLong = strings.Repeat("x", maxTopicLen+1)

func waitSubscribers(t *testing.T, h *harness, pluginID, topic string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.svc.Subscribers(pluginID, topic) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscribers of %q never reached %d", topic, want)
}
