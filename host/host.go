// Package host is the plugin-facing SDK. A plugin depends on this module alone.
//
// The packages here contain only interfaces, DTOs, options, and sentinel errors.
// They do not import internal/core. Core modules adapt their implementations to
// these contracts; pluginhost constructs one scoped Host per plugin.
package host

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/hbaldwin98/control-center/host/ai"
	"github.com/hbaldwin98/control-center/host/browser"
	"github.com/hbaldwin98/control-center/host/events"
	"github.com/hbaldwin98/control-center/host/jobs"
	"github.com/hbaldwin98/control-center/host/storage"
)

// Plugin is the one interface a plugin implements.
type Plugin interface {
	Manifest() Manifest

	// Jobs, Subscriptions, and Routes are pure declarations. The host reads them
	// during registration, before Migrate or Init, and must not invoke handlers.
	Jobs() []jobs.Def
	Subscriptions() []Subscription
	Routes() []Route

	// Migrate runs before Init, on every start, idempotently — including while disabled.
	Migrate(m Migrator) error

	// Init receives a Host already scoped to this plugin. It runs once per enabled
	// runtime generation.
	Init(ctx context.Context, h Host) error

	Shutdown(ctx context.Context) error
}

// Manifest is identity and runtime metadata. Frontend routes, navigation, and icons
// live in the plugin's UI module, not here.
type Manifest struct {
	ID          string
	Name        string
	Version     string
	Description string

	// Automated is true only when work can start without an explicit user action,
	// such as cron or an event handler. Such plugins require a daily budget.
	Automated bool

	Config ConfigSpec
}

// ConfigSpec is the closed JSON Schema 2020-12 subset the host validates and renders.
// Secrets are credential references, never config values.
type ConfigSpec struct {
	Schema   json.RawMessage
	Defaults json.RawMessage
}

// Route is one plugin HTTP endpoint, mounted below /api/plugins/<id>/.
// Pattern is a Go 1.22 relative pattern, for example "POST /scans".
type Route struct {
	Pattern string
	Handler http.Handler
}

// Subscription is one event subscription. Set Handler for live delivery or Durable
// for durable delivery, never both.
type Subscription struct {
	Pattern string
	Handler events.Handler
	Durable *DurableSubscription
}

// DurableSubscription is at-least-once, serial, named delivery. The host registers
// the name globally as plugin:<id>:<name>.
type DurableSubscription struct {
	Name    string
	Handler events.TxHandler
}

// Host is the entire surface available to plugin code.
type Host interface {
	PluginID() string

	AI() ai.AI
	Browser() browser.Browser // headless sessions; the host owns the engine
	Jobs() jobs.Jobs
	Events() Events
	Store() storage.DB
	Blobs() storage.Blobs
	Config() Config
	Log() Logger
	Clock() Clock
}

// Events publishes under this plugin's identity. Source is forced to the plugin ID
// and Type is prefixed with it.
type Events interface {
	Publish(ctx context.Context, eventType, subject string, payload any) error
	PublishTx(ctx context.Context, tx storage.Tx, eventType, subject string, payload any) error
}

// Config is the current non-secret settings document. Only the authenticated
// administration API writes it.
type Config interface {
	Decode(dst any) error
	Watch(fn func(context.Context, json.RawMessage)) (unwatch func())
}

// Logger is structured and already tagged with the plugin ID.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	With(args ...any) Logger
}

// Clock is injectable so scheduled logic is testable.
type Clock interface {
	Now() time.Time
}

// Migrator applies this plugin's migrations. Every table name must start with
// the plugin ID and an underscore.
type Migrator interface {
	Apply(migrations []Migration) error
}

// Migration is one forward-only schema step.
type Migration struct {
	Version int
	Name    string
	Up      string
}
