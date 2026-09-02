package hosttest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostevents "github.com/hbaldwin98/control-center/host/events"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// This file is the answer to the question a test double always raises: how do you know
// it behaves like the real thing?
//
// Conformance is one suite of assertions about the host contract, run twice -- once
// against this package's double, once against the control center's real facade. Both
// runs drive the same Probe plugin through the same Driver interface. A behaviour the
// double gets wrong fails in core's test run, where someone will see it, instead of
// quietly making a plugin's own tests lie.
//
// The suite covers only what both implementations can be held to. Anything the double
// deliberately does differently -- synchronous delivery, a clock that does not tick --
// is documented at the point of difference and left out of here.

// ProbeID is the plugin ID the conformance probe registers under. A host running the
// suite must accept it and must isolate its tables under the "conf_" prefix.
const ProbeID = "conf"

// Driver is what a host implementation supplies so the conformance suite can drive it.
// Harness satisfies it directly; core implements it over its registry.
type Driver interface {
	// Start registers, migrates, initializes, and enables the probe. The probe's Host
	// is available from the Probe once this returns.
	Start(ctx context.Context) error

	// Enable and Disable move the kill switch, the way an operator would.
	Enable(ctx context.Context) error
	Disable(ctx context.Context) error

	// PublishExternal publishes an event from somewhere other than the probe, so the
	// suite can test what the probe's subscriptions receive. The source is the event
	// type's first segment: the host requires a publisher to own the namespace it
	// publishes into, so "external.one" can only come from source "external".
	PublishExternal(ctx context.Context, eventType, subject string, payload any) error

	// Events returns the log, oldest first, as the rest of the system sees it.
	Events(ctx context.Context) ([]hostevents.Event, error)

	// Settle blocks until work already started has landed. The real host delivers
	// events asynchronously; the double delivers them before Publish returns, so this
	// is where that difference is absorbed rather than asserted on.
	Settle(ctx context.Context)
}

// Probe is the plugin the conformance suite drives. It holds no logic of its own: it
// records what the host gave it so the suite can make assertions about the contract.
type Probe struct {
	mu   sync.Mutex
	h    host.Host
	deep []hostevents.Event // received on the "**" subscription
	one  []hostevents.Event // received on the single-segment subscription
}

// NewProbe returns a fresh probe. One probe drives one Driver.
func NewProbe() *Probe { return &Probe{} }

// Host is the handle the host gave the probe in Init.
func (p *Probe) Host() host.Host {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h
}

func (p *Probe) Manifest() host.Manifest {
	return host.Manifest{
		ID: ProbeID, Name: "Conformance Probe", Version: "1",
		Description: "Drives the host contract for the shared conformance suite.",
		Config: host.ConfigSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}}}`),
			Defaults: json.RawMessage(`{"label":"from-manifest"}`),
		},
	}
}

func (p *Probe) Jobs() []hostjobs.Def {
	return []hostjobs.Def{{
		Name: "noop", MaxAttempts: 1,
		Handler: func(jc hostjobs.Context) error { return nil },
	}}
}

func (p *Probe) Subscriptions() []host.Subscription {
	return []host.Subscription{
		{
			Pattern: "external.**",
			Handler: func(ctx context.Context, e hostevents.Event) error {
				p.mu.Lock()
				defer p.mu.Unlock()
				p.deep = append(p.deep, e)
				return nil
			},
		},
		{
			Pattern: "external.*",
			Handler: func(ctx context.Context, e hostevents.Event) error {
				p.mu.Lock()
				defer p.mu.Unlock()
				p.one = append(p.one, e)
				return nil
			},
		},
	}
}

func (p *Probe) Routes() []host.Route {
	return []host.Route{{
		Pattern: "GET /ping",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true}`))
		}),
	}}
}

func (p *Probe) Migrate(m host.Migrator) error {
	return m.Apply([]host.Migration{
		{Version: 1, Name: "rows", Up: `CREATE TABLE conf_rows (k TEXT PRIMARY KEY, v TEXT NOT NULL) STRICT;`},
	})
}

func (p *Probe) Init(ctx context.Context, h host.Host) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.h = h
	return nil
}

func (p *Probe) Shutdown(context.Context) error { return nil }

// received returns what each subscription has seen so far.
func (p *Probe) received() (deep, one []hostevents.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]hostevents.Event(nil), p.deep...), append([]hostevents.Event(nil), p.one...)
}

var _ host.Plugin = (*Probe)(nil)

// Conformance runs the shared suite against one host implementation. newDriver builds a
// Driver around the probe it is given; the suite starts it.
//
// Core calls this with a driver over its real facade. This package calls it with a
// Harness. Both must pass, unchanged.
func Conformance(t *testing.T, newDriver func(t *testing.T, probe *Probe) Driver) {
	t.Helper()
	ctx := context.Background()

	// Each subtest gets its own host: the kill switch and the event log are global to
	// a plugin, so sharing one would make the results order-dependent.
	start := func(t *testing.T) (*Probe, Driver) {
		t.Helper()
		probe := NewProbe()
		d := newDriver(t, probe)
		if err := d.Start(ctx); err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		if probe.Host() == nil {
			t.Fatal("Start() returned before Init gave the plugin a Host")
		}
		return probe, d
	}

	t.Run("PluginIdentity", func(t *testing.T) {
		probe, _ := start(t)
		if got := probe.Host().PluginID(); got != ProbeID {
			t.Fatalf("PluginID() = %q, want %q", got, ProbeID)
		}
	})

	t.Run("StoreRoundTrip", func(t *testing.T) {
		probe, _ := start(t)
		h := probe.Host()
		if _, err := h.Store().Exec(ctx, `INSERT INTO conf_rows (k, v) VALUES (?, ?)`, "a", "1"); err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
		var v string
		if err := h.Store().QueryRow(ctx, `SELECT v FROM conf_rows WHERE k = ?`, "a").Scan(&v); err != nil {
			t.Fatalf("QueryRow() error = %v", err)
		}
		if v != "1" {
			t.Fatalf("v = %q, want 1", v)
		}
	})

	t.Run("TransactionRollsBackWrites", func(t *testing.T) {
		probe, _ := start(t)
		h := probe.Host()
		sentinel := errors.New("abandon")
		err := h.Store().Tx(ctx, func(tx hoststorage.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO conf_rows (k, v) VALUES (?, ?)`, "rolled", "back"); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("Tx() error = %v, want the handler's own error", err)
		}
		var n int
		if err := h.Store().QueryRow(ctx, `SELECT count(*) FROM conf_rows WHERE k = 'rolled'`).Scan(&n); err != nil {
			t.Fatalf("QueryRow() error = %v", err)
		}
		if n != 0 {
			t.Fatalf("rows after rollback = %d, want 0", n)
		}
	})

	t.Run("PublishedEventsCarryPluginIdentity", func(t *testing.T) {
		probe, d := start(t)
		if err := probe.Host().Events().Publish(ctx, "thing.happened", "subj-1", map[string]int{"n": 1}); err != nil {
			t.Fatalf("Publish() error = %v", err)
		}
		d.Settle(ctx)

		events, err := d.Events(ctx)
		if err != nil {
			t.Fatalf("Events() error = %v", err)
		}
		found := false
		for _, e := range events {
			if e.Source != ProbeID {
				continue
			}
			found = true
			// The host owns the namespace: a plugin cannot publish under another
			// plugin's name, and its own type is always prefixed with its ID.
			if e.Type != ProbeID+".thing.happened" {
				t.Fatalf("type = %q, want %q", e.Type, ProbeID+".thing.happened")
			}
			if e.Subject != "subj-1" {
				t.Fatalf("subject = %q, want subj-1", e.Subject)
			}
		}
		if !found {
			t.Fatalf("no event from %q in %d events", ProbeID, len(events))
		}
	})

	t.Run("TransactionalPublishIsVisibleOnlyAfterCommit", func(t *testing.T) {
		probe, d := start(t)
		h := probe.Host()

		sentinel := errors.New("abandon")
		err := h.Store().Tx(ctx, func(tx hoststorage.Tx) error {
			if err := h.Events().PublishTx(ctx, tx, "rolled.back", "s", nil); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("Tx() error = %v", err)
		}
		if err := h.Store().Tx(ctx, func(tx hoststorage.Tx) error {
			return h.Events().PublishTx(ctx, tx, "committed", "s", nil)
		}); err != nil {
			t.Fatalf("Tx() error = %v", err)
		}
		d.Settle(ctx)

		events, err := d.Events(ctx)
		if err != nil {
			t.Fatalf("Events() error = %v", err)
		}
		for _, e := range events {
			if e.Type == ProbeID+".rolled.back" {
				t.Fatal("an event published in a rolled-back transaction reached the log")
			}
		}
		committed := false
		for _, e := range events {
			if e.Type == ProbeID+".committed" {
				committed = true
			}
		}
		if !committed {
			t.Fatal("an event published in a committed transaction did not reach the log")
		}
	})

	t.Run("SubscriptionPatternsSelectBySegment", func(t *testing.T) {
		probe, d := start(t)
		// "external.**" takes both; "external.*" takes only the single-segment one.
		if err := d.PublishExternal(ctx, "external.one", "s-1", nil); err != nil {
			t.Fatalf("PublishExternal() error = %v", err)
		}
		if err := d.PublishExternal(ctx, "external.one.two", "s-2", nil); err != nil {
			t.Fatalf("PublishExternal() error = %v", err)
		}
		// A type outside both patterns, published from its own namespace.
		if err := d.PublishExternal(ctx, "unrelated.one", "s-3", nil); err != nil {
			t.Fatalf("PublishExternal() error = %v", err)
		}
		d.Settle(ctx)

		deep, one := probe.received()
		if len(deep) != 2 {
			t.Fatalf(`"external.**" received %d events, want 2: %+v`, len(deep), deep)
		}
		if len(one) != 1 {
			t.Fatalf(`"external.*" received %d events, want 1: %+v`, len(one), one)
		}
		if one[0].Type != "external.one" {
			t.Fatalf(`"external.*" received %q, want external.one`, one[0].Type)
		}
	})

	t.Run("BlobRoundTrip", func(t *testing.T) {
		probe, _ := start(t)
		h := probe.Host()
		ref, err := h.Blobs().Put(ctx, "note.txt", strings.NewReader("hello blobs"), "text/plain")
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		if ref.Size != int64(len("hello blobs")) {
			t.Fatalf("size = %d, want %d", ref.Size, len("hello blobs"))
		}
		rc, meta, err := h.Blobs().Get(ctx, "note.txt")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read blob: %v", err)
		}
		if string(body) != "hello blobs" {
			t.Fatalf("body = %q, want %q", body, "hello blobs")
		}
		if meta.SHA256 != ref.SHA256 {
			t.Fatalf("digest changed between Put and Get: %q then %q", ref.SHA256, meta.SHA256)
		}
	})

	t.Run("ConfigDecodesManifestDefaults", func(t *testing.T) {
		probe, _ := start(t)
		var cfg struct {
			Label string `json:"label"`
		}
		if err := probe.Host().Config().Decode(&cfg); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if cfg.Label != "from-manifest" {
			t.Fatalf("label = %q, want the manifest default", cfg.Label)
		}
	})

	t.Run("EnqueueRejectsUnknownJob", func(t *testing.T) {
		probe, _ := start(t)
		_, err := probe.Host().Jobs().Enqueue(ctx, "no-such-job", nil)
		if !errors.Is(err, hostjobs.ErrUnknownDef) {
			t.Fatalf("Enqueue() error = %v, want ErrUnknownDef", err)
		}
	})

	t.Run("DisableRejectsWritesAndAllowsReads", func(t *testing.T) {
		probe, d := start(t)
		h := probe.Host()

		if _, err := h.Store().Exec(ctx, `INSERT INTO conf_rows (k, v) VALUES ('before', 'x')`); err != nil {
			t.Fatalf("Exec() before disable: %v", err)
		}
		if err := d.Disable(ctx); err != nil {
			t.Fatalf("Disable() error = %v", err)
		}

		// Every write path reports the same sentinel, because a plugin is expected to
		// treat all of them the same way: stop cleanly, do not retry.
		if _, err := h.Store().Exec(ctx, `INSERT INTO conf_rows (k, v) VALUES ('after', 'x')`); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
			t.Errorf("Exec() while disabled = %v, want ErrPluginDisabled", err)
		}
		if err := h.Events().Publish(ctx, "nope", "s", nil); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
			t.Errorf("Publish() while disabled = %v, want ErrPluginDisabled", err)
		}
		if _, err := h.Jobs().Enqueue(ctx, "noop", nil); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
			t.Errorf("Enqueue() while disabled = %v, want ErrPluginDisabled", err)
		}
		// AI is checked only for refusal, not for a particular sentinel. The probe
		// declares no models, so a host is equally right to answer ErrUnknownRoute
		// (no such route) or ErrPluginDisabled (not while switched off) -- pinning
		// either one would be asserting an ordering the contract does not promise.
		if _, err := h.AI().Chat(ctx, hostai.ChatRequest{Model: "anything"}); err == nil {
			t.Error("Chat() while disabled succeeded, want a refusal")
		}

		// Reads stay available so the host UI can still show retained data.
		var v string
		if err := h.Store().QueryRow(ctx, `SELECT v FROM conf_rows WHERE k = 'before'`).Scan(&v); err != nil {
			t.Errorf("QueryRow() while disabled = %v, want the row", err)
		}
		// Diagnostic logging keeps working too.
		h.Log().Info("still logging while disabled")

		if err := d.Enable(ctx); err != nil {
			t.Fatalf("Enable() error = %v", err)
		}
		if _, err := h.Store().Exec(ctx, `INSERT INTO conf_rows (k, v) VALUES ('after', 'x')`); err != nil {
			t.Fatalf("Exec() after re-enable = %v", err)
		}
	})
}
