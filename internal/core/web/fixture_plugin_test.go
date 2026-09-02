package web

import (
	"context"

	"github.com/hbaldwin98/control-center/host"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

// modelPlugin is a plugin that exists only to declare one logical model route.
//
// The admin tests need a registered plugin with a known model need; they do not need
// any particular plugin's behaviour. Using a fixture instead of a real plugin keeps
// core's test suite from failing whenever a plugin changes its manifest -- the
// coupling the plugin boundary exists to prevent.
type modelPlugin struct {
	id     string
	models []host.ModelNeed
}

func (p *modelPlugin) Manifest() host.Manifest {
	return host.Manifest{
		ID:     p.id,
		Name:   "Fixture",
		Models: p.models,
	}
}

func (p *modelPlugin) Jobs() []hostjobs.Def                  { return nil }
func (p *modelPlugin) Subscriptions() []host.Subscription    { return nil }
func (p *modelPlugin) Routes() []host.Route                  { return nil }
func (p *modelPlugin) Migrate(host.Migrator) error           { return nil }
func (p *modelPlugin) Init(context.Context, host.Host) error { return nil }
func (p *modelPlugin) Shutdown(context.Context) error        { return nil }

var _ host.Plugin = (*modelPlugin)(nil)
