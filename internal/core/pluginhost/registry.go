package pluginhost

import (
	"context"
	"fmt"
	"strings"

	"github.com/hbaldwin98/control-center/host"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// RegisterAll validates every plugin before migrating or initializing any of them.
// A collision or invalid declaration rejects the whole call.
func (r *Registry) RegisterAll(ps ...host.Plugin) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return ErrAlreadyRegistered
	}
	if len(r.byID) > 0 {
		return ErrAlreadyRegistered
	}

	seen := map[string]struct{}{}
	jobKeys := map[string]struct{}{}
	routeKeys := map[string]struct{}{}
	subKeys := map[string]struct{}{}
	slots := make([]*slot, 0, len(ps))

	for i, p := range ps {
		if p == nil {
			return fmt.Errorf("%w: plugin %d is nil", ErrInvalidPlugin, i)
		}
		d, err := inspect(p)
		if err != nil {
			return err
		}
		id := d.manifest.ID
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: duplicate plugin id %q", ErrInvalidPlugin, id)
		}
		seen[id] = struct{}{}

		for _, name := range d.jobNames {
			k := id + "\x00" + name
			if _, ok := jobKeys[k]; ok {
				return fmt.Errorf("%w: duplicate job %s.%s", ErrInvalidPlugin, id, name)
			}
			jobKeys[k] = struct{}{}
		}
		for _, rd := range d.routePat {
			method, path, err := parseRoutePattern(rd.Pattern)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidPlugin, err)
			}
			k := id + "\x00" + method + "\x00" + path
			if _, ok := routeKeys[k]; ok {
				return fmt.Errorf("%w: duplicate route %s %s on %s", ErrInvalidPlugin, method, path, id)
			}
			routeKeys[k] = struct{}{}
		}
		for _, sub := range d.subs {
			if sub.Durable != nil {
				k := durableName(id, sub.Durable.Name)
				if _, ok := subKeys[k]; ok {
					return fmt.Errorf("%w: duplicate durable subscription %s", ErrInvalidPlugin, k)
				}
				subKeys[k] = struct{}{}
			}
		}
		slots = append(slots, &slot{
			plugin: p,
			decl:   d,
			health: Health{Runtime: runtimeDisabled},
		})
	}

	for _, s := range slots {
		id := s.decl.manifest.ID
		r.byID[id] = s
		r.order = append(r.order, id)
	}
	return nil
}

func inspect(p host.Plugin) (declaration, error) {
	m := p.Manifest()
	if !validPluginID(m.ID) {
		return declaration{}, fmt.Errorf("%w: id %q", ErrInvalidPlugin, m.ID)
	}
	if m.Name == "" {
		return declaration{}, fmt.Errorf("%w: %s has an empty name", ErrInvalidPlugin, m.ID)
	}
	if err := validateConfigSpec(hostConfigSpec{Schema: m.Config.Schema, Defaults: m.Config.Defaults}); err != nil {
		return declaration{}, fmt.Errorf("%s: %w", m.ID, err)
	}

	d := declaration{manifest: m, jobs: p.Jobs(), subs: p.Subscriptions(), routes: p.Routes()}

	for _, j := range d.jobs {
		if !validName(j.Name) {
			return declaration{}, fmt.Errorf("%w: %s job %q", ErrInvalidPlugin, m.ID, j.Name)
		}
		if j.Handler == nil {
			return declaration{}, fmt.Errorf("%w: %s.%s needs a handler", ErrInvalidPlugin, m.ID, j.Name)
		}
		if j.Schedule != "" {
			if j.TimeZone == "" {
				return declaration{}, fmt.Errorf("%w: %s.%s needs an IANA TimeZone", ErrInvalidPlugin, m.ID, j.Name)
			}
			if len(strings.Fields(j.Schedule)) != 5 {
				return declaration{}, fmt.Errorf("%w: %s.%s cron must be five fields", ErrInvalidPlugin, m.ID, j.Name)
			}
		}
		d.jobNames = append(d.jobNames, j.Name)
	}

	for _, sub := range d.subs {
		if _, err := events.CompilePattern(sub.Pattern); err != nil {
			return declaration{}, fmt.Errorf("%w: %s subscription %q: %v", ErrInvalidPlugin, m.ID, sub.Pattern, err)
		}
		live := sub.Handler != nil
		dur := sub.Durable != nil
		if live == dur {
			return declaration{}, fmt.Errorf("%w: %s subscription %q must be live or durable, not both or neither", ErrInvalidPlugin, m.ID, sub.Pattern)
		}
		if dur {
			if !validName(sub.Durable.Name) {
				return declaration{}, fmt.Errorf("%w: %s durable name %q", ErrInvalidPlugin, m.ID, sub.Durable.Name)
			}
			if sub.Durable.Handler == nil {
				return declaration{}, fmt.Errorf("%w: %s durable %q needs a handler", ErrInvalidPlugin, m.ID, sub.Durable.Name)
			}
		}
	}

	seenPath := map[string]struct{}{}
	for _, rt := range d.routes {
		method, path, err := parseRoutePattern(rt.Pattern)
		if err != nil {
			return declaration{}, fmt.Errorf("%w: %s: %v", ErrInvalidPlugin, m.ID, err)
		}
		if rt.Handler == nil {
			return declaration{}, fmt.Errorf("%w: %s route %q needs a handler", ErrInvalidPlugin, m.ID, rt.Pattern)
		}
		k := method + " " + path
		if _, ok := seenPath[k]; ok {
			return declaration{}, fmt.Errorf("%w: %s duplicate route %s", ErrInvalidPlugin, m.ID, k)
		}
		seenPath[k] = struct{}{}
		d.routePat = append(d.routePat, RouteDescriptor{Pattern: rt.Pattern})
	}
	return d, nil
}

// Describe returns one plugin's combined descriptor.
func (r *Registry) Describe(id string) (Descriptor, error) {
	s, ok := r.lookup(id)
	if !ok {
		return Descriptor{}, fmt.Errorf("%w: %s", ErrUnknownPlugin, id)
	}
	return r.descriptor(s)
}

// List returns every registered plugin in registration order.
func (r *Registry) List() []Descriptor {
	r.mu.Lock()
	order := append([]string(nil), r.order...)
	r.mu.Unlock()
	out := make([]Descriptor, 0, len(order))
	for _, id := range order {
		s, ok := r.lookup(id)
		if !ok {
			continue
		}
		d, err := r.descriptor(s)
		if err != nil {
			d = Descriptor{Manifest: s.decl.manifest, Health: s.snapshotHealth()}
		}
		out = append(out, d)
	}
	return out
}

func (r *Registry) descriptor(s *slot) (Descriptor, error) {
	s.mu.Lock()
	m := s.decl.manifest
	jobs := append([]string(nil), s.decl.jobNames...)
	routes := append([]RouteDescriptor(nil), s.decl.routePat...)
	health := s.health
	s.mu.Unlock()

	st := policy.State{PluginID: m.ID}
	if r.opts.Policy != nil {
		got, err := r.opts.Policy.State(context.Background(), m.ID)
		if err == nil {
			st = got
		}
	}
	health.DesiredEnabled = st.Enabled
	if health.Runtime == "" {
		if st.Enabled {
			health.Runtime = runtimeDegraded
		} else {
			health.Runtime = runtimeDisabled
		}
	}
	return Descriptor{Manifest: m, State: st, Jobs: jobs, Routes: routes, Health: health}, nil
}

func (s *slot) snapshotHealth() Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.health
}
