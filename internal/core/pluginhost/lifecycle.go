package pluginhost

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hbaldwin98/control-center/host"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// Start registers policy rows, migrates every plugin, and reconciles runtime to
// persisted desired state. One plugin's failure degrades it; others still start.
func (r *Registry) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return nil
	}
	r.started = true
	order := append([]string(nil), r.order...)
	r.mu.Unlock()

	if r.opts.Policy != nil {
		r.unwatch = r.opts.Policy.Watch(func(pluginID string, _ bool) {
			go func() {
				s, ok := r.lookup(pluginID)
				if !ok {
					return
				}
				_ = r.reconcile(context.Background(), s)
			}()
		})
	}

	for _, id := range order {
		s, ok := r.lookup(id)
		if !ok {
			continue
		}
		if err := r.bootstrap(ctx, s); err != nil {
			r.opts.Log.Error("pluginhost: bootstrap", "plugin", id, "err", err)
		}
	}
	return nil
}

func (r *Registry) bootstrap(ctx context.Context, s *slot) error {
	id := s.decl.manifest.ID
	if r.opts.Policy != nil {
		if err := r.opts.Policy.Register(ctx, id, s.decl.manifest.Automated); err != nil {
			r.fail(s, kindInit, err)
			return err
		}
	}
	if err := r.ensureConfigRow(ctx, s); err != nil {
		r.fail(s, kindConfig, err)
		return err
	}
	if err := s.plugin.Migrate(hostMigrator{inner: r.opts.DB.PluginMigrator(id), pluginID: id}); err != nil {
		r.fail(s, kindMigration, err)
		return err
	}
	return r.reconcile(ctx, s)
}

func (r *Registry) fail(s *slot, kind string, err error) {
	s.mu.Lock()
	s.health.Runtime = runtimeDegraded
	s.health.LastError = err.Error()
	s.health.ErrorKind = kind
	s.mu.Unlock()
}

func (r *Registry) clearHealth(s *slot, runtime string) {
	s.mu.Lock()
	s.health.Runtime = runtime
	s.health.LastError = ""
	s.health.ErrorKind = ""
	s.mu.Unlock()
}

// Stop shuts down every initialized plugin.
func (r *Registry) Stop(ctx context.Context) error {
	if r.unwatch != nil {
		r.unwatch()
		r.unwatch = nil
	}
	r.mu.Lock()
	order := append([]string(nil), r.order...)
	r.mu.Unlock()
	for _, id := range order {
		s, ok := r.lookup(id)
		if !ok {
			continue
		}
		s.mu.Lock()
		initialized := s.initialized
		s.mu.Unlock()
		if initialized {
			_ = r.teardown(ctx, s, false)
		}
	}
	return nil
}

// Enable persists policy enable then reconciles. Incomplete convergence is returned
// without rolling policy state back.
func (r *Registry) Enable(ctx context.Context, id, actor, reason string) error {
	s, ok := r.lookup(id)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPlugin, id)
	}
	if r.opts.Policy == nil {
		return ErrNotStarted
	}
	if err := r.opts.Policy.Enable(ctx, id, actor, reason); err != nil {
		return err
	}
	if err := r.reconcile(ctx, s); err != nil {
		return fmt.Errorf("%w: %v", ErrIncomplete, err)
	}
	return nil
}

// Disable persists policy revocation first, then converges runtime.
func (r *Registry) Disable(ctx context.Context, id, actor, reason string) error {
	s, ok := r.lookup(id)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPlugin, id)
	}
	if r.opts.Policy == nil {
		return ErrNotStarted
	}
	if err := r.opts.Policy.Disable(ctx, id, actor, reason); err != nil {
		return err
	}
	if err := r.reconcile(ctx, s); err != nil {
		return fmt.Errorf("%w: %v", ErrIncomplete, err)
	}
	return nil
}

func (r *Registry) reconcile(ctx context.Context, s *slot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return r.reconcileLocked(ctx, s)
}

func (r *Registry) reconcileLocked(ctx context.Context, s *slot) error {
	id := s.decl.manifest.ID
	enabled := false
	if r.opts.Policy != nil {
		st, err := r.opts.Policy.State(ctx, id)
		if err != nil {
			r.setHealthLocked(s, runtimeDegraded, kindInit, err)
			return err
		}
		enabled = st.Enabled
		s.health.DesiredEnabled = enabled
	}

	if !enabled {
		if err := r.teardownLocked(ctx, s); err != nil {
			return err
		}
		r.setHealthLocked(s, runtimeDisabled, "", nil)
		return nil
	}

	if err := r.validatePersistedConfig(ctx, s); err != nil {
		_ = r.teardownLocked(ctx, s)
		r.setHealthLocked(s, runtimeDegraded, kindConfig, err)
		return err
	}

	if s.initialized {
		r.setHealthLocked(s, runtimeEnabled, "", nil)
		return nil
	}

	raw, _, err := r.loadConfig(ctx, s)
	if err != nil {
		r.setHealthLocked(s, runtimeDegraded, kindConfig, err)
		return err
	}
	cfg := &pluginConfig{slot: s, value: append([]byte(nil), raw...)}
	h := r.facadeFor(s.decl.manifest, cfg)
	gen, cancel := context.WithCancel(context.Background())

	if err := r.mountJobs(ctx, s); err != nil {
		cancel()
		r.setHealthLocked(s, runtimeDegraded, kindScheduler, err)
		return err
	}

	if err := s.plugin.Init(gen, h); err != nil {
		cancel()
		r.setHealthLocked(s, runtimeDegraded, kindInit, err)
		return err
	}

	mux, err := buildMux(s.decl.routes)
	if err != nil {
		cancel()
		_ = s.plugin.Shutdown(ctx)
		r.setHealthLocked(s, runtimeDegraded, kindRoute, err)
		return err
	}

	if err := r.attachSubscriptions(gen, s, h, false); err != nil {
		cancel()
		_ = s.plugin.Shutdown(ctx)
		r.setHealthLocked(s, runtimeDegraded, kindSubscription, err)
		return err
	}

	s.host = h
	s.configReady(cfg)
	s.mux = mux
	s.gen = gen
	s.cancel = cancel
	s.initialized = true
	r.setHealthLocked(s, runtimeEnabled, "", nil)
	return nil
}

func (s *slot) configReady(cfg *pluginConfig) {
	if h, ok := s.host.(*scopedHost); ok {
		h.config = cfg
	}
}

func (r *Registry) teardown(ctx context.Context, s *slot, lock bool) error {
	if lock {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	return r.teardownLocked(ctx, s)
}

func (r *Registry) teardownLocked(ctx context.Context, s *slot) error {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
		s.gen = nil
	}
	for _, u := range s.watches {
		if u != nil {
			u()
		}
	}
	s.watches = nil
	if h, ok := s.host.(*scopedHost); ok && h.config != nil {
		h.config.mu.Lock()
		h.config.listeners = nil
		h.config.mu.Unlock()
	}

	r.detachSubscriptions(s)
	s.mux = nil
	if r.opts.Jobs != nil {
		r.opts.Jobs.UnregisterAll(s.decl.manifest.ID)
	}
	if r.opts.Browser != nil {
		_ = r.opts.Browser.ClosePlugin(ctx, s.decl.manifest.ID)
	}

	var shutdownErr error
	if s.initialized {
		shutCtx, cancel := context.WithTimeout(ctx, r.opts.ShutdownTimeout)
		shutdownErr = s.plugin.Shutdown(shutCtx)
		cancel()
		s.initialized = false
		s.host = nil
		if shutdownErr != nil {
			r.setHealthLocked(s, runtimeDegraded, kindShutdown, shutdownErr)
			_ = r.attachSubscriptions(context.Background(), s, nil, true)
			return shutdownErr
		}
	}

	if err := r.attachSubscriptions(context.Background(), s, nil, true); err != nil {
		r.setHealthLocked(s, runtimeDegraded, kindSubscription, err)
		return err
	}
	return nil
}

func (r *Registry) setHealthLocked(s *slot, runtime, kind string, err error) {
	s.health.Runtime = runtime
	s.health.ErrorKind = kind
	if err != nil {
		s.health.LastError = err.Error()
	} else {
		s.health.LastError = ""
	}
}

func (r *Registry) mountJobs(ctx context.Context, s *slot) error {
	if r.opts.Jobs == nil {
		return nil
	}
	id := s.decl.manifest.ID
	if err := r.opts.Jobs.CatchUpSchedules(ctx, id); err != nil {
		return err
	}
	for _, d := range s.decl.jobs {
		def := jobs.Def{
			Name: d.Name, Handler: wrapJobHandler(d.Handler), Schedule: d.Schedule,
			TimeZone: d.TimeZone, Timeout: d.Timeout, MaxAttempts: d.MaxAttempts,
			Concurrency: d.Concurrency,
			Backoff:     jobs.BackoffPolicy{Initial: d.Backoff.Initial, Maximum: d.Backoff.Maximum},
		}
		if err := r.opts.Jobs.Register(id, def); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) detachSubscriptions(s *slot) {
	for _, sub := range s.live {
		sub.Close()
	}
	s.live = nil
	for _, sub := range s.durable {
		sub.Close()
	}
	s.durable = nil
}

func (r *Registry) attachSubscriptions(ctx context.Context, s *slot, h host.Host, discard bool) error {
	r.detachSubscriptions(s)
	if r.opts.Events == nil {
		return nil
	}
	id := s.decl.manifest.ID
	for _, sub := range s.decl.subs {
		if sub.Durable != nil {
			name := durableName(id, sub.Durable.Name)
			cfg := events.DurableConfig{
				Name:    name,
				Pattern: sub.Pattern,
				Start:   events.CursorStart{Mode: events.FromNow},
				Retry:   events.DefaultRetry,
			}
			var handler events.TxHandler
			if discard || h == nil {
				handler = func(context.Context, storage.Tx, events.Event) error { return nil }
			} else {
				ph := sub.Durable.Handler
				pid := id
				handler = func(ctx context.Context, tx storage.Tx, e events.Event) error {
					if r.opts.Policy != nil {
						if err := r.opts.Policy.CheckWorkTx(ctx, tx, pid); err != nil {
							return nil
						}
					}
					return ph(ctx, &txAdapter{tx: tx, db: r.opts.DB, pluginID: pid}, mapHostEvent(e))
				}
			}
			got, err := r.opts.Events.SubscribeDurableTx(cfg, handler)
			if err != nil {
				return err
			}
			s.durable = append(s.durable, got)
			continue
		}
		if discard || h == nil {
			continue
		}
		ph := sub.Handler
		got, err := r.opts.Events.SubscribeLive(sub.Pattern, func(ctx context.Context, e events.Event) error {
			return ph(ctx, mapHostEvent(e))
		})
		if err != nil {
			return err
		}
		s.live = append(s.live, got)
	}
	return nil
}

func buildMux(routes []host.Route) (*http.ServeMux, error) {
	mux := http.NewServeMux()
	for _, rt := range routes {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	return mux, nil
}
