package notifications

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hbaldwin98/control-center/internal/core/credentials"
)

// Delivery to a push endpoint is a retry decision as much as a request: the queue keeps
// trying until a channel says the failure is permanent. Getting that classification wrong
// either drops a notification that would have arrived, or retries forever against a
// subscription the browser has already thrown away. This file pins the classification.

// fakeCreds stands in for the credential runtime, which the channel only asks for a token.
type fakeCreds struct {
	token string
	err   error
	asked []string
}

func (f *fakeCreds) Token(_ context.Context, id string) (string, error) {
	f.asked = append(f.asked, id)
	if f.err != nil {
		return "", f.err
	}
	return f.token, nil
}

func (f *fakeCreds) Attributes(context.Context, string) (credentials.Attributes, error) {
	return credentials.Attributes{}, nil
}

func delivery() Delivery {
	return Delivery{
		Notification:   Notification{Title: "A job failed", Body: "hello: tick"},
		IdempotencyKey: "key-1",
	}
}

// webPushTo builds a channel pointed at a test server. The endpoint is loopback http,
// which the constructor allows precisely so it can be tested.
func webPushTo(t *testing.T, server *httptest.Server, creds credentials.Runtime, credID string) Channel {
	t.Helper()
	ch, err := newWebPushChannel(
		ChannelConfig{ID: "ch-1", CredentialID: credID, Settings: map[string]string{"endpoint": server.URL}},
		creds,
		server.Client(),
	)
	if err != nil {
		t.Fatalf("newWebPushChannel: %v", err)
	}
	return ch
}

func TestWebPushSendsTitleAndBody(t *testing.T) {
	var got struct {
		body        string
		contentType string
		ttl         string
		idempotency string
		auth        string
		method      string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.body = string(raw)
		got.contentType = r.Header.Get("Content-Type")
		got.ttl = r.Header.Get("TTL")
		got.idempotency = r.Header.Get("Idempotency-Key")
		got.auth = r.Header.Get("Authorization")
		got.method = r.Method
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	ch := webPushTo(t, server, nil, "")
	if err := ch.Send(context.Background(), delivery()); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got.method != http.MethodPost {
		t.Fatalf("method = %s", got.method)
	}
	if got.body != "A job failed\nhello: tick" {
		t.Fatalf("body = %q", got.body)
	}
	if !strings.HasPrefix(got.contentType, "text/plain") {
		t.Fatalf("content-type = %q", got.contentType)
	}
	if got.ttl != "86400" {
		t.Fatalf("ttl = %q", got.ttl)
	}
	// The key is what stops a retry becoming a second notification on the device.
	if got.idempotency != "key-1" {
		t.Fatalf("idempotency-key = %q", got.idempotency)
	}
	if got.auth != "" {
		t.Fatalf("no credential was configured, so no Authorization should be sent: %q", got.auth)
	}
}

func TestWebPushSendsTitleAloneWhenThereIsNoBody(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ch := webPushTo(t, server, nil, "")
	d := delivery()
	d.Notification.Body = ""
	if err := ch.Send(context.Background(), d); err != nil {
		t.Fatalf("send: %v", err)
	}
	if body != "A job failed" {
		t.Fatalf("body = %q, want the title with no trailing newline", body)
	}
}

func TestWebPushOmitsIdempotencyKeyWhenThereIsNone(t *testing.T) {
	var present bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["Idempotency-Key"]
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ch := webPushTo(t, server, nil, "")
	d := delivery()
	d.IdempotencyKey = ""
	if err := ch.Send(context.Background(), d); err != nil {
		t.Fatalf("send: %v", err)
	}
	if present {
		t.Fatal("an empty key must be omitted rather than sent blank")
	}
}

func TestWebPushSendsVAPIDTokenWhenCredentialled(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	creds := &fakeCreds{token: "tok-abc"}
	ch := webPushTo(t, server, creds, "cred-1")
	if err := ch.Send(context.Background(), delivery()); err != nil {
		t.Fatalf("send: %v", err)
	}
	if auth != "vapid tok-abc" {
		t.Fatalf("authorization = %q", auth)
	}
	if len(creds.asked) != 1 || creds.asked[0] != "cred-1" {
		t.Fatalf("asked = %v, want the configured credential id", creds.asked)
	}
}

// A credential that cannot be read is not a delivery failure to retry against the
// endpoint -- nothing was sent, so the request must not go out at all.
func TestWebPushFailsWithoutSendingWhenTheTokenCannotBeRead(t *testing.T) {
	var reached bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	boom := errors.New("credential is locked")
	ch := webPushTo(t, server, &fakeCreds{err: boom}, "cred-1")
	if err := ch.Send(context.Background(), delivery()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the credential error", err)
	}
	if reached {
		t.Fatal("no request should be sent when the token could not be read")
	}
}

// The retry classification, which is the whole point of this channel's error handling.
func TestWebPushClassifiesResponses(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		wantErr       bool
		wantPermanent bool
	}{
		{"200 is delivered", http.StatusOK, false, false},
		{"201 is delivered", http.StatusCreated, false, false},
		{"202 is delivered", http.StatusAccepted, false, false},
		{"204 is delivered", http.StatusNoContent, false, false},

		// The subscription is gone for good; retrying can never succeed.
		{"404 is permanent", http.StatusNotFound, true, true},
		{"410 is permanent", http.StatusGone, true, true},
		{"400 is permanent", http.StatusBadRequest, true, true},
		{"401 is permanent", http.StatusUnauthorized, true, true},
		{"403 is permanent", http.StatusForbidden, true, true},

		// 429 is the one 4xx that is worth trying again.
		{"429 is retried", http.StatusTooManyRequests, true, false},

		// The push service is having a bad time; the notification is still valid.
		{"500 is retried", http.StatusInternalServerError, true, false},
		{"502 is retried", http.StatusBadGateway, true, false},
		{"503 is retried", http.StatusServiceUnavailable, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			err := webPushTo(t, server, nil, "").Send(context.Background(), delivery())
			if tc.wantErr && err == nil {
				t.Fatalf("status %d: expected an error", tc.status)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("status %d: %v", tc.status, err)
			}
			if got := isPermanent(err); got != tc.wantPermanent {
				t.Fatalf("status %d: permanent = %v, want %v (err %v)", tc.status, got, tc.wantPermanent, err)
			}
		})
	}
}

func TestWebPushReportsATransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ch := webPushTo(t, server, nil, "")
	server.Close() // nothing is listening any more

	if err := ch.Send(context.Background(), delivery()); err == nil {
		t.Fatal("expected a transport error")
	}
}

func TestWebPushStopsOnACancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := webPushTo(t, server, nil, "").Send(ctx, delivery()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// The endpoint is operator-supplied, so the constructor is a validation boundary: a
// plaintext endpoint would put a notification's contents on the wire in clear.
func TestNewWebPushChannelValidatesTheEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		ok       bool
	}{
		{"https", "https://push.example.com/abc", true},
		{"loopback http is allowed for local testing", "http://127.0.0.1:8080/abc", true},
		{"localhost http is allowed", "http://localhost:8080/abc", true},
		{"public http is refused", "http://push.example.com/abc", false},
		{"other schemes are refused", "ftp://push.example.com/abc", false},
		{"userinfo is refused", "https://user:pass@push.example.com/abc", false},
		{"empty is refused", "", false},
		{"no host is refused", "https:///abc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newWebPushChannel(
				ChannelConfig{ID: "ch-1", Settings: map[string]string{"endpoint": tc.endpoint}},
				nil,
				nil,
			)
			if tc.ok && err != nil {
				t.Fatalf("endpoint %q rejected: %v", tc.endpoint, err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("endpoint %q accepted", tc.endpoint)
				}
				// A bad endpoint is a configuration error: retrying cannot fix it.
				if !isPermanent(err) {
					t.Fatalf("endpoint %q: rejection should be permanent, got %v", tc.endpoint, err)
				}
			}
		})
	}
}
