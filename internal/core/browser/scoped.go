package browser

import (
	"context"
	"net/url"
	"time"
)

type pluginKey struct{}
type jobKey struct{}

// WithPlugin stamps the calling plugin. Open and page I/O reject a context without one.
func WithPlugin(ctx context.Context, pluginID string) context.Context {
	return context.WithValue(ctx, pluginKey{}, pluginID)
}

// WithJob attributes denied events and logs to a job when the caller is inside one.
func WithJob(ctx context.Context, jobID string) context.Context {
	return context.WithValue(ctx, jobKey{}, jobID)
}

func pluginID(ctx context.Context) (string, error) {
	id, _ := ctx.Value(pluginKey{}).(string)
	if id == "" {
		return "", ErrNoPlugin
	}
	return id, nil
}

func jobID(ctx context.Context) string {
	id, _ := ctx.Value(jobKey{}).(string)
	return id
}

// Scoped stamps plugin identity onto every Open. pluginhost builds one per plugin.
func Scoped(s *Service, pluginID string) Browser {
	return &scoped{s: s, pluginID: pluginID}
}

type scoped struct {
	s        *Service
	pluginID string
}

func (a *scoped) Open(ctx context.Context, opts OpenOptions) (Session, error) {
	sess, err := a.s.Open(WithPlugin(ctx, a.pluginID), opts)
	if err != nil {
		return nil, err
	}
	return &scopedSession{inner: sess, pluginID: a.pluginID}, nil
}

type scopedSession struct {
	inner    Session
	pluginID string
}

func (s *scopedSession) NewPage(ctx context.Context) (Page, error) {
	p, err := s.inner.NewPage(WithPlugin(ctx, s.pluginID))
	if err != nil {
		return nil, err
	}
	return &scopedPage{inner: p, pluginID: s.pluginID}, nil
}

func (s *scopedSession) Close(ctx context.Context) error {
	return s.inner.Close(WithPlugin(ctx, s.pluginID))
}

type scopedPage struct {
	inner    Page
	pluginID string
}

func (p *scopedPage) Goto(ctx context.Context, url string) error {
	return p.inner.Goto(WithPlugin(ctx, p.pluginID), url)
}

func (p *scopedPage) WaitFor(ctx context.Context, selector string, d time.Duration) error {
	return p.inner.WaitFor(WithPlugin(ctx, p.pluginID), selector, d)
}

func (p *scopedPage) Content(ctx context.Context) (string, error) {
	return p.inner.Content(WithPlugin(ctx, p.pluginID))
}

func (p *scopedPage) Get(ctx context.Context, url string) (Resource, error) {
	return p.inner.Get(WithPlugin(ctx, p.pluginID), url)
}

func (p *scopedPage) Post(ctx context.Context, raw string, form url.Values) (Resource, error) {
	return p.inner.Post(WithPlugin(ctx, p.pluginID), raw, form)
}

func (p *scopedPage) Responses(ctx context.Context) ([]Resource, error) {
	return p.inner.Responses(WithPlugin(ctx, p.pluginID))
}

func (p *scopedPage) Fill(ctx context.Context, selector, value string) error {
	return p.inner.Fill(WithPlugin(ctx, p.pluginID), selector, value)
}

func (p *scopedPage) Click(ctx context.Context, selector string) error {
	return p.inner.Click(WithPlugin(ctx, p.pluginID), selector)
}

func (p *scopedPage) Close(ctx context.Context) error {
	return p.inner.Close(WithPlugin(ctx, p.pluginID))
}
