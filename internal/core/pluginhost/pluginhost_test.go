package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostevents "github.com/hbaldwin98/control-center/host/events"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type fixture struct {
	t     *testing.T
	ctx   context.Context
	store *storage.Store
	blobs *storage.BlobStore
	bus   *events.Log
	pol   *policy.Store
	q     *jobs.Queue
	reg   *Registry
}

func newFixture(t *testing.T, plugins ...host.Plugin) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(dir, "cc.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	blobs, err := storage.NewBlobStore(store, storage.BlobOptions{
		Dir: filepath.Join(dir, "blobs"), MaxObjectBytes: 1 << 20, MaxScopeBytes: 10 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.New(store, store, events.Options{PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	bus.Start(ctx)
	t.Cleanup(bus.Stop)

	pol, err := policy.New(store, store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	q, err := jobs.New(store, store, bus, pol, jobs.Options{
		PollInterval: 20 * time.Millisecond, LeaseTTL: time.Second, Heartbeat: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start(ctx)
	t.Cleanup(q.Stop)

	reg, err := New(ctx, store, Options{
		DB: store, Blobs: blobs, Events: bus, Policy: pol, Jobs: q,
		ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(plugins...); err != nil {
		t.Fatal(err)
	}
	if err := reg.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Stop(context.Background()) })
	return &fixture{t: t, ctx: ctx, store: store, blobs: blobs, bus: bus, pol: pol, q: q, reg: reg}
}

type probe struct {
	inited    atomic.Bool
	shutdowns atomic.Int32
	handlerN  atomic.Int32
	h         host.Host
	ping      http.Handler
}

func newProbe() *probe {
	p := &probe{}
	p.ping = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return p
}

func (p *probe) Manifest() host.Manifest {
	return host.Manifest{
		ID: "probe", Name: "Probe", Version: "0.0.1",
		Config: host.ConfigSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}`),
			Defaults: json.RawMessage(`{"label":"hi"}`),
		},
	}
}
func (p *probe) Jobs() []hostjobs.Def { return nil }
func (p *probe) Subscriptions() []host.Subscription {
	return []host.Subscription{{
		Pattern: "probe.**",
		Durable: &host.DurableSubscription{Name: "ticks", Handler: p.onTick},
	}}
}
func (p *probe) Routes() []host.Route {
	return []host.Route{{Pattern: "GET /ping", Handler: p.ping}}
}
func (p *probe) Migrate(m host.Migrator) error {
	return m.Apply([]host.Migration{{Version: 1, Name: "ticks", Up: `
		CREATE TABLE probe_ticks (id INTEGER PRIMARY KEY, note TEXT NOT NULL) STRICT;
	`}})
}
func (p *probe) Init(_ context.Context, h host.Host) error {
	p.h = h
	p.inited.Store(true)
	return nil
}
func (p *probe) Shutdown(context.Context) error {
	p.shutdowns.Add(1)
	return nil
}
func (p *probe) onTick(ctx context.Context, tx hoststorage.Tx, e hostevents.Event) error {
	p.handlerN.Add(1)
	_, err := tx.Exec(ctx, `INSERT INTO probe_ticks(note) VALUES (?)`, string(e.Type))
	return err
}

func TestRegisterAllRejectsInvalidIDAndDuplicates(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "cc.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reg, err := New(ctx, store, Options{DB: store})
	if err != nil {
		t.Fatal(err)
	}
	bad := &probe{}
	// duplicate ids
	if err := reg.RegisterAll(newProbe(), newProbe()); err == nil {
		t.Fatal("expected duplicate rejection")
	}
	_ = bad
}

func TestRegisterAllIsAtomic(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "cc.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reg, err := New(ctx, store, Options{DB: store})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(newProbe(), &invalidID{}); err == nil {
		t.Fatal("expected rejection")
	}
	if _, err := reg.Describe("probe"); !errors.Is(err, ErrUnknownPlugin) {
		t.Fatalf("partial register leaked: %v", err)
	}
}

type invalidID struct{ probe }

func (*invalidID) Manifest() host.Manifest {
	return host.Manifest{ID: "CORE", Name: "nope"}
}

type badModel struct{ probe }

func (*badModel) Manifest() host.Manifest {
	m := newProbe().Manifest()
	m.Models = []host.ModelNeed{{Name: "CheapVision", Capabilities: []string{"chat"}, Purpose: "nope"}}
	return m
}

func TestRegisterAllRejectsInvalidModelNeeds(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "cc.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reg, err := New(ctx, store, Options{DB: store})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(&badModel{}); err == nil {
		t.Fatal("expected invalid model name rejection")
	}
}

type badEvent struct {
	*probe
	events []host.EventSpec
}

func (p *badEvent) Manifest() host.Manifest {
	m := p.probe.Manifest()
	m.Events = p.events
	return m
}

func TestRegisterAllRejectsInvalidEventSpecs(t *testing.T) {
	cases := []struct {
		name string
		spec host.EventSpec
	}{
		{name: "uppercase type", spec: host.EventSpec{Type: "Ticked", Purpose: "a tick"}},
		{name: "wildcard type", spec: host.EventSpec{Type: "*.alert", Purpose: "any alert"}},
		{name: "empty purpose", spec: host.EventSpec{Type: "ticked"}},
		{name: "unknown field type", spec: host.EventSpec{
			Type: "ticked", Purpose: "a tick",
			Fields: []host.EventField{{Name: "at", Type: "datetime", Purpose: "when"}},
		}},
		{name: "uppercase field name", spec: host.EventSpec{
			Type: "ticked", Purpose: "a tick",
			Fields: []host.EventField{{Name: "At", Type: "string", Purpose: "when"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "cc.db")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			reg, err := New(ctx, store, Options{DB: store})
			if err != nil {
				t.Fatal(err)
			}
			p := &badEvent{probe: newProbe(), events: []host.EventSpec{tc.spec}}
			if err := reg.RegisterAll(p); err == nil {
				t.Fatal("expected invalid event spec rejection")
			}
		})
	}
}

func TestRegisterAllAcceptsDeclaredEvents(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "cc.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reg, err := New(ctx, store, Options{DB: store})
	if err != nil {
		t.Fatal(err)
	}
	p := &badEvent{probe: newProbe(), events: []host.EventSpec{{
		Type: "finding.created", Purpose: "A watchlist produced new lots.",
		Fields: []host.EventField{
			{Name: "watchlistId", Type: "string", Purpose: "The watchlist that matched."},
			{Name: "count", Type: "number", Purpose: "How many new findings."},
		},
	}}}
	if err := reg.RegisterAll(p); err != nil {
		t.Fatal(err)
	}
	d, err := reg.Describe("probe")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Manifest.Events) != 1 || d.Manifest.Events[0].Type != "finding.created" {
		t.Fatalf("events = %#v", d.Manifest.Events)
	}
}

func TestDisabledPluginEnforcementMatrix(t *testing.T) {
	p := newProbe()
	f := newFixture(t, p)

	d, err := f.reg.Describe("probe")
	if err != nil {
		t.Fatal(err)
	}
	if d.State.Enabled || d.Health.Runtime != runtimeDisabled {
		t.Fatalf("start enabled: %+v", d)
	}
	if p.inited.Load() {
		t.Fatal("Init must not run while disabled")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/probe/ping", nil)
	f.reg.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET ping: %d %s", rec.Code, rec.Body.Bytes())
	}
	if !strings.Contains(rec.Body.String(), "plugin_disabled") {
		t.Fatalf("body %s", rec.Body.String())
	}

	h := f.reg.facadeFor(p.Manifest(), &pluginConfig{value: json.RawMessage(`{}`)})
	if err := h.Events().Publish(f.ctx, "ticked", "1", map[string]string{"n": "1"}); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("publish: %v", err)
	}
	if _, err := h.Store().Exec(f.ctx, `INSERT INTO probe_ticks(note) VALUES ('x')`); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("exec: %v", err)
	}
	// Reads remain available.
	rows, err := h.Store().Query(f.ctx, `SELECT count(*) FROM probe_ticks`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("expected a count row")
	}

	// Durable discard/ack: a matching event must not invoke the handler.
	if _, err := f.bus.Publish(f.ctx, events.Input{Type: "probe.ticked", Source: "probe", Subject: "s", Payload: map[string]int{"n": 1}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if p.handlerN.Load() != 0 {
		t.Fatalf("discard invoked handler %d times", p.handlerN.Load())
	}
}

func TestEnableThenDisableHTTPAndMutations(t *testing.T) {
	p := newProbe()
	f := newFixture(t, p)
	if err := f.reg.Enable(f.ctx, "probe", "test", "go"); err != nil {
		t.Fatal(err)
	}
	if !p.inited.Load() {
		t.Fatal("Init did not run")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/probe/ping", nil)
	f.reg.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled ping: %d %s", rec.Code, rec.Body.Bytes())
	}

	if err := p.h.Events().Publish(f.ctx, "ticked", "1", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for p.handlerN.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if p.handlerN.Load() == 0 {
		t.Fatal("durable handler did not run")
	}

	if err := f.reg.Disable(f.ctx, "probe", "test", "stop"); err != nil {
		t.Fatal(err)
	}
	if p.shutdowns.Load() == 0 {
		t.Fatal("Shutdown did not run")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/plugins/probe/ping", nil)
	f.reg.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled ping: %d", rec.Code)
	}
	if err := p.h.Events().Publish(f.ctx, "ticked", "2", nil); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("publish after disable: %v", err)
	}
}

func TestDisableCancelsAdmittedRequestContext(t *testing.T) {
	started := make(chan struct{})
	var sawCancel atomic.Bool
	p := newProbe()
	p.ping = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		sawCancel.Store(true)
		writePluginDisabled(w)
	})
	f := newFixture(t, p)
	if err := f.reg.Enable(f.ctx, "probe", "test", "go"); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/plugins/probe/ping", nil)
		f.reg.ServeHTTP(rec, req)
		done <- rec.Code
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	if err := f.reg.Disable(f.ctx, "probe", "test", "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if !sawCancel.Load() {
			t.Fatal("handler returned without observing cancellation")
		}
		if code != http.StatusServiceUnavailable {
			t.Fatalf("code %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler context was not cancelled")
	}
}

func TestInvalidPersistedConfigDegrades(t *testing.T) {
	p := newProbe()
	f := newFixture(t, p)
	_, err := f.store.Exec(f.ctx,
		`UPDATE core_plugin_config SET value_json = '{"label":1}' WHERE plugin_id = 'probe'`)
	if err != nil {
		t.Fatal(err)
	}
	err = f.reg.Enable(f.ctx, "probe", "test", "go")
	if err == nil {
		t.Fatal("expected config error")
	}
	d, err := f.reg.Describe("probe")
	if err != nil {
		t.Fatal(err)
	}
	if d.Health.Runtime != runtimeDegraded || d.Health.ErrorKind != kindConfig {
		t.Fatalf("health %+v", d.Health)
	}
	rec := httptest.NewRecorder()
	f.reg.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/probe/ping", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded still served plugin code: %d", rec.Code)
	}
}

func TestConfigUpdateNotifiesEnabledGeneration(t *testing.T) {
	p := newProbe()
	f := newFixture(t, p)
	if err := f.reg.Enable(f.ctx, "probe", "test", "go"); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	unwatch := p.h.Config().Watch(func(_ context.Context, raw json.RawMessage) {
		got <- string(raw)
	})
	defer unwatch()
	if err := f.reg.UpdateConfig(f.ctx, "probe", json.RawMessage(`{"label":"bye"}`), "test"); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if !strings.Contains(v, "bye") {
			t.Fatalf("watch %s", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not fire")
	}
}

func TestSecretConfigRejected(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "cc.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reg, err := New(ctx, store, Options{DB: store})
	if err != nil {
		t.Fatal(err)
	}
	err = reg.RegisterAll(&secretPlugin{})
	if err == nil || !errors.Is(err, ErrSecretConfig) {
		t.Fatalf("got %v", err)
	}
}

type secretPlugin struct{ probe }

func (*secretPlugin) Manifest() host.Manifest {
	return host.Manifest{
		ID: "secr", Name: "S",
		Config: host.ConfigSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"api_key":{"type":"string"}}}`),
			Defaults: json.RawMessage(`{"api_key":"x"}`),
		},
	}
}

func TestSchemaRejectsRefs(t *testing.T) {
	err := validateConfigSpec(hostConfigSpec{
		Schema:   json.RawMessage(`{"$ref":"#/defs/x"}`),
		Defaults: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected reject")
	}
}
