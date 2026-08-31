// Package browser is the plugin-facing host-managed headless session surface.
// Plugins pass the DNS names they intend to touch; they never import an engine.
package browser

import (
	"context"
	"errors"
	"time"
)

// Browser opens allowlisted sessions. The host owns the engine and teardown.
type Browser interface {
	Open(ctx context.Context, opts OpenOptions) (Session, error)
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
	Close(ctx context.Context) error
}

// Page is one document. Conversation and DOM state belong here.
type Page interface {
	Goto(ctx context.Context, url string) error
	WaitFor(ctx context.Context, selector string, d time.Duration) error
	Content(ctx context.Context) (string, error)
	Get(ctx context.Context, url string) (Resource, error)
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
)
