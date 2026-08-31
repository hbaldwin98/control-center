// Package pluginhost registers plugins, owns validated non-secret plugin config,
// reconciles persisted policy state, runs lifecycle, and constructs each scoped Host.
//
// Layer 4. It imports every core module below it. Enabled state and budgets live in
// policy; this package serializes lifecycle and converges runtime resources to that
// persisted desired state.
package pluginhost

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/host"
	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/browser"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// Errors returned across the pluginhost boundary.
var (
	ErrUnknownPlugin     = errors.New("pluginhost: unknown plugin")
	ErrInvalidPlugin     = errors.New("pluginhost: invalid plugin")
	ErrAlreadyRegistered = errors.New("pluginhost: plugins already registered")
	ErrNotStarted        = errors.New("pluginhost: registry has not started")
	ErrSecretConfig      = errors.New("pluginhost: config schema must not declare secrets")
	ErrInvalidConfig     = errors.New("pluginhost: invalid plugin config")
	ErrIncomplete        = errors.New("pluginhost: reconciliation incomplete")
)

const (
	runtimeEnabled  = "enabled"
	runtimeDisabled = "disabled"
	runtimeDegraded = "degraded"

	kindMigration    = "migration"
	kindInit         = "init"
	kindRoute        = "route"
	kindScheduler    = "scheduler"
	kindSubscription = "subscription"
	kindConfig       = "config"
	kindShutdown     = "shutdown"
)

// Options wires the lower-layer modules pluginhost composes.
type Options struct {
	DB     *storage.Store
	Blobs  *storage.BlobStore
	Events *events.Log
	Policy *policy.Store
	Jobs   *jobs.Queue
	AI     *ai.Service
	Browser *browser.Service
	Log    *slog.Logger
	Now    func() time.Time

	ShutdownTimeout time.Duration
}

func (o *Options) applyDefaults() {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.ShutdownTimeout <= 0 {
		o.ShutdownTimeout = 10 * time.Second
	}
}

// Registry is the administrative and lifecycle surface.
type Registry struct {
	opts Options

	mu      sync.Mutex
	byID    map[string]*slot
	order   []string
	started bool
	unwatch func()
}

// New applies pluginhost migrations and returns an empty registry. Call RegisterAll
// then Start.
func New(ctx context.Context, m storage.Migrator, opts Options) (*Registry, error) {
	opts.applyDefaults()
	if err := m.Apply("pluginhost", migrations); err != nil {
		return nil, err
	}
	return &Registry{
		opts: opts,
		byID: map[string]*slot{},
	}, nil
}

// Descriptor is the combined view of a registered plugin.
type Descriptor struct {
	Manifest host.Manifest     `json:"manifest"`
	State    policy.State      `json:"state"`
	Jobs     []string          `json:"jobs"`
	Routes   []RouteDescriptor `json:"routes"`
	Health   Health            `json:"health"`
}

// RouteDescriptor is a declared plugin HTTP pattern.
type RouteDescriptor struct {
	Pattern string `json:"pattern"`
}

// Health distinguishes policy's desired state from the reconciled runtime state.
type Health struct {
	DesiredEnabled bool   `json:"desiredEnabled"`
	Runtime        string `json:"runtime"`
	LastError      string `json:"lastError"`
	ErrorKind      string `json:"errorKind,omitempty"`
}

type slot struct {
	mu     sync.Mutex
	plugin host.Plugin
	decl   declaration

	health Health

	initialized bool
	host        host.Host
	mux         *http.ServeMux

	live    []events.Subscription
	durable []events.Subscription

	watches []func()

	gen    context.Context
	cancel context.CancelFunc
}

func (r *Registry) now() time.Time { return r.opts.Now().UTC() }

func (r *Registry) lookup(id string) (*slot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.byID[id]
	return s, ok
}

func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
