package jobs

import (
	"context"
	"fmt"
)

// Scoped stamps plugin identity onto every call. pluginhost builds one per plugin.
func Scoped(q *Queue, pluginID string) Jobs {
	return &scoped{q: q, pluginID: pluginID}
}

type scoped struct {
	q        *Queue
	pluginID string
}

func (s *scoped) Enqueue(ctx context.Context, name string, args any, opts ...Opt) (int64, error) {
	return s.q.Enqueue(ctx, s.pluginID, name, args, opts...)
}

func (s *scoped) Cancel(ctx context.Context, id int64) error {
	return s.q.cancel(ctx, id, ReasonUser, s.pluginID)
}

func (s *scoped) Get(ctx context.Context, id int64) (*Job, error) {
	j, err := s.q.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if j.PluginID != s.pluginID {
		return nil, fmt.Errorf("%w: %d", ErrPluginMismatch, id)
	}
	return j, nil
}

func (s *scoped) List(ctx context.Context, f Filter) ([]Job, error) {
	f.PluginID = s.pluginID
	return s.q.List(ctx, f)
}
