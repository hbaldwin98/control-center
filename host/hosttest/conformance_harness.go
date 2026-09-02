package hosttest

import (
	"context"
	"strings"

	hostevents "github.com/hbaldwin98/control-center/host/events"
)

// harnessDriver runs the conformance suite against this package's own double.
//
// It is deliberately thin: every method is the Harness call a plugin author would make
// anyway. If satisfying Driver needed special-case machinery here, that would be a sign
// the suite was written around the double rather than around the contract.
type harnessDriver struct{ h *Harness }

// NewDriver adapts a Harness to the conformance Driver, so the same suite that
// exercises the real host exercises the double.
func NewDriver(h *Harness) Driver { return &harnessDriver{h: h} }

func (d *harnessDriver) Start(ctx context.Context) error {
	d.h.Run(ctx)
	return nil
}

func (d *harnessDriver) Enable(context.Context) error {
	d.h.Enable()
	return nil
}

func (d *harnessDriver) Disable(context.Context) error {
	d.h.Disable()
	return nil
}

func (d *harnessDriver) PublishExternal(ctx context.Context, eventType, subject string, payload any) error {
	// The source owns the namespace it publishes into, so it is the type's first
	// segment rather than a fixed string. The real host rejects a publisher that
	// claims someone else's namespace, and the suite depends on that being true here
	// too.
	source, _, _ := strings.Cut(eventType, ".")
	d.h.Publish(ctx, source, eventType, subject, payload)
	return nil
}

func (d *harnessDriver) Events(context.Context) ([]hostevents.Event, error) {
	return d.h.Events(), nil
}

// Settle has nothing to wait for: the double delivers before Publish returns.
func (d *harnessDriver) Settle(context.Context) {}
