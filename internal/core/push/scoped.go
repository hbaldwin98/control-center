package push

import "context"

// Push is the capability surface pluginhost adapts to host/push.
type Push interface {
	Publish(ctx context.Context, topic string, payload any) error
	Subscribers(topic string) int
	SetWatcher(w Watcher) func()
}

// Scoped binds the service to one plugin's namespace. Topics are per plugin, so two
// plugins naming the same topic never see each other's subscribers or payloads.
// pluginhost builds one per plugin.
func Scoped(s *Service, pluginID string) Push {
	return &scoped{s: s, pluginID: pluginID}
}

type scoped struct {
	s        *Service
	pluginID string
}

func (a *scoped) Publish(ctx context.Context, topic string, payload any) error {
	return a.s.Publish(ctx, a.pluginID, topic, payload)
}

func (a *scoped) Subscribers(topic string) int {
	return a.s.Subscribers(a.pluginID, topic)
}

func (a *scoped) SetWatcher(w Watcher) func() {
	return a.s.SetWatcher(a.pluginID, w)
}
