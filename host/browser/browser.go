// Package browser is the plugin-facing host-managed headless session surface.
// Plugins pass the DNS names they intend to touch; they never import an engine.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"
)

// Browser opens allowlisted sessions. The host owns the engine and teardown.
type Browser interface {
	Open(ctx context.Context, opts OpenOptions) (Session, error)
	Do(ctx context.Context, opts OpenOptions, req Request) (Resource, error)
	Read(ctx context.Context, opts OpenOptions, url string) (Document, error)
}

// Document is readable text extracted from one rendered public web page.
type Document struct {
	URL     string
	Content string
}

// Request is one direct allowlisted HTTP request. Credential, when set, is
// injected into the top-level JSON object by the host and never exposed to the plugin.
type Request struct {
	Method     string
	URL        string
	Headers    map[string]string
	Body       json.RawMessage
	Credential *JSONCredential
}

type JSONCredential struct {
	ID    string
	Field string
}

// OpenOptions names the hosts this session may touch. The list is required.
type OpenOptions struct {
	// AllowedHosts is required and nonempty. Every navigation, redirect, subresource,
	// and Get is checked against it. Hosts are lowercase DNS names, no ports, no IPs.
	AllowedHosts []string
}

// Session is one isolated browser context: cookies, pages, and cache.
type Session interface {
	NewPage(ctx context.Context) (Page, error)
	// Subscribe opens a read-only websocket to an allowlisted wss:// URL and streams
	// the text frames the server sends. The plugin declares every frame the host will
	// write -- the handshake, and the heartbeat replies -- before the socket opens;
	// there is no send channel afterwards, for the same reason Post takes a form and
	// not a body. The dial does not go through the engine and carries no cookies, so a
	// subscription can only join channels that need no authenticated handshake.
	Subscribe(ctx context.Context, url string, opts SubscribeOptions) (Subscription, error)
	Close(ctx context.Context) error
}

// SubscribeOptions declares the whole write side of a subscription up front.
type SubscribeOptions struct {
	// Handshake frames are written once, in order, as soon as the socket opens. Each
	// must be a JSON value under 4 KiB; the host does not interpret them.
	Handshake []json.RawMessage
	// KeepAlive answers a received frame whose top-level "event" field equals Event by
	// writing Reply. It exists so an application-level heartbeat does not require the
	// plugin to hold a send channel.
	KeepAlive []KeepAliveRule
}

// KeepAliveRule is one declared heartbeat answer.
type KeepAliveRule struct {
	Event string
	Reply json.RawMessage
}

// Subscription is one live feed. Frames is closed when the connection ends -- by
// Close, by plugin disable, by session teardown, or by the server -- and Err then says
// why. Drain Frames promptly: the host drops frames rather than growing its heap
// behind a slow reader.
type Subscription interface {
	Frames() <-chan Frame
	Err() error
	Close(ctx context.Context) error
}

// Frame is one received text message and the time the host received it.
type Frame struct {
	At   time.Time
	Data []byte
}

// Page is one document. Conversation and DOM state belong here.
type Page interface {
	Goto(ctx context.Context, url string) error
	WaitFor(ctx context.Context, selector string, d time.Duration) error
	Content(ctx context.Context) (string, error)
	Get(ctx context.Context, url string) (Resource, error)
	// Post sends application/x-www-form-urlencoded to an allowlisted URL. Same cookie
	// jar, SSRF checks, and size bound as Get. Form is the only body; there is no JSON
	// POST, so a plugin cannot smuggle an arbitrary payload through the session.
	Post(ctx context.Context, url string, form url.Values) (Resource, error)
	// Responses are JSON/CSV bodies this page fetched (XHR/fetch), already allowlisted.
	// An Angular app that POSTs usage to its API shows up here; Get only does GET and
	// does not send in-page Authorization headers.
	Responses(ctx context.Context) ([]Resource, error)
	// Fill sets the value of the first matching input, textarea, or contenteditable.
	Fill(ctx context.Context, selector, value string) error
	// Click the first matching element. A navigation that follows is allowlist-checked
	// the same way as Goto.
	Click(ctx context.Context, selector string) error
	// FillCredential fills selector with the secret of a stored credential. The plugin
	// never receives the secret; the host reads it and types it.
	FillCredential(ctx context.Context, selector, credentialID string) error
	Close(ctx context.Context) error
}

// Resource is one allowlisted fetch (image, static file).
type Resource struct {
	URL    string
	MIME   string
	Body   []byte
	Status int
}

// Errors a plugin must handle. Disabled means stop; Denied means fix the URL.
var (
	ErrInvalidAllowlist = errors.New("browser: allowlist is empty or invalid")
	ErrDenied           = errors.New("browser: url denied")
	ErrLimit            = errors.New("browser: session or page limit")
	ErrEngine           = errors.New("browser: engine failed")
	ErrReader           = errors.New("browser: reader unavailable")
)
