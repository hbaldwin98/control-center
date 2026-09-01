package events

import (
	"context"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// Scoped stamps plugin identity onto every publish. Type is prefixed with pluginID
// and Source is forced to it, so a plugin cannot impersonate core or another plugin.
func Scoped(bus Bus, pluginID string) *ScopedBus {
	return &ScopedBus{bus: bus, pluginID: pluginID}
}

// ScopedBus is the event publisher one plugin sees.
type ScopedBus struct {
	bus      Bus
	pluginID string
}

func (s *ScopedBus) Publish(ctx context.Context, eventType, subject string, payload any) error {
	_, err := s.bus.Publish(ctx, s.input(eventType, subject, payload))
	return err
}

func (s *ScopedBus) PublishTx(ctx context.Context, tx storage.Tx, eventType, subject string, payload any) error {
	_, err := s.bus.PublishTx(ctx, tx, s.input(eventType, subject, payload))
	return err
}

func (s *ScopedBus) input(eventType, subject string, payload any) Input {
	return Input{
		Type:    s.pluginID + "." + eventType,
		Source:  s.pluginID,
		Subject: subject,
		Payload: payload,
	}
}
