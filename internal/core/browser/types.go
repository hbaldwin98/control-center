package browser

import (
	"context"
	"net/url"
	"time"
)

// Browser opens allowlisted sessions. The scoped wrapper stamps plugin identity.
type Browser interface {
	Open(ctx context.Context, opts OpenOptions) (Session, error)
}

// OpenOptions names the hosts this session may touch. The list is required.
type OpenOptions struct {
	AllowedHosts []string
}

// Session is one isolated browser context.
type Session interface {
	NewPage(ctx context.Context) (Page, error)
	Close(ctx context.Context) error
}

// Page is one document.
type Page interface {
	Goto(ctx context.Context, url string) error
	WaitFor(ctx context.Context, selector string, d time.Duration) error
	Content(ctx context.Context) (string, error)
	Get(ctx context.Context, url string) (Resource, error)
	Responses(ctx context.Context) ([]Resource, error)
	Fill(ctx context.Context, selector, value string) error
	Click(ctx context.Context, selector string) error
	Close(ctx context.Context) error
}

// Resource is one allowlisted fetch.
type Resource struct {
	URL    string
	MIME   string
	Body   []byte
	Status int
}

// Engine creates isolated sessions. Fake never opens a socket. Playwright launches
// Chromium and calls checkResolvedIP at connect time. An Engine that also implements
// io.Closer is shut down with the Service.
type Engine interface {
	NewSession(ctx context.Context, pluginID string) (EngineSession, error)
}

// EngineSession is one engine-owned browser context.
type EngineSession interface {
	NewPage(ctx context.Context) (EnginePage, error)
	Close(ctx context.Context) error
}

// EnginePage fetches documents and resources. check is called for the start URL and
// every redirect before Fake issues the in-process request.
type EnginePage interface {
	Goto(ctx context.Context, u *url.URL, check func(*url.URL) error) (doc string, final *url.URL, err error)
	Get(ctx context.Context, u *url.URL, check func(*url.URL) error) (Resource, error)
	Resources() []Resource
	Fill(ctx context.Context, selector, value string) error
	Click(ctx context.Context, selector string, check func(*url.URL) error) (doc string, final *url.URL, err error)
	Close(ctx context.Context) error
}
