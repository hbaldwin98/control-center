package search

import "context"

type pluginKey struct{}

// WithPlugin stamps the calling plugin. Query rejects a context without one.
func WithPlugin(ctx context.Context, pluginID string) context.Context {
	return context.WithValue(ctx, pluginKey{}, pluginID)
}

func pluginID(ctx context.Context) (string, error) {
	id, _ := ctx.Value(pluginKey{}).(string)
	if id == "" {
		return "", ErrNoPlugin
	}
	return id, nil
}

// Scoped stamps plugin identity onto every Query. pluginhost builds one per plugin.
func Scoped(s *Service, pluginID string) Search {
	return &scoped{s: s, pluginID: pluginID}
}

// Search is the capability surface pluginhost adapts to host/search.
type Search interface {
	Query(ctx context.Context, req Request) ([]Hit, error)
}

type scoped struct {
	s        *Service
	pluginID string
}

func (a *scoped) Query(ctx context.Context, req Request) ([]Hit, error) {
	return a.s.Query(WithPlugin(ctx, a.pluginID), req)
}
