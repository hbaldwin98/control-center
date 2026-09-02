package hosttest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostevents "github.com/hbaldwin98/control-center/host/events"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hostsearch "github.com/hbaldwin98/control-center/host/search"

	_ "modernc.org/sqlite"
)

// TB is the part of *testing.T the harness uses. Taking an interface rather than
// *testing.T keeps this module free of a dependency on the test binary's shape, and
// lets a benchmark or a fuzz target drive a plugin too.
type TB interface {
	Helper()
	Cleanup(func())
	Logf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// Harness drives one plugin against a Host double.
//
// The programmable capabilities are exported fields: program AI replies, browser
// pages, and search hits directly, then drive the plugin through its declared
// surface -- HTTP routes, jobs, and event subscriptions -- and assert on what it
// stored, published, and logged.
type Harness struct {
	// AI, Browser, and Search are the capability fakes. Program them before the code
	// under test runs.
	AI      *AIFake
	Browser *BrowserFake
	Search  *SearchFake

	// Clock is the plugin's clock. It does not advance on its own.
	Clock *Clock

	// Log records everything the plugin logged.
	Log *Logger

	tb       TB
	plugin   host.Plugin
	manifest host.Manifest
	impl     *hostImpl
	bus      *bus
	mux      *http.ServeMux
	sqlDB    *sql.DB

	mu      sync.Mutex
	enabled bool
	started bool
	routes  []host.Route
}

// New builds a Harness for a plugin. It validates the plugin's declarations the way
// the registry does, but does not call Migrate or Init -- Run does that, so a test can
// program config or fakes first.
//
// The plugin starts enabled. Everything is torn down when the test ends.
func New(tb TB, plugin host.Plugin) *Harness {
	tb.Helper()
	if plugin == nil {
		tb.Fatalf("hosttest: nil plugin")
		return nil
	}
	m := plugin.Manifest()
	if m.ID == "" {
		tb.Fatalf("hosttest: manifest has no ID")
		return nil
	}

	// Each plugin gets its own private database, which is what makes a cross-plugin
	// read impossible rather than merely rejected. The shared cache keeps every
	// connection in the pool looking at the same in-memory database.
	sqlDB, err := sql.Open("sqlite",
		"file:"+m.ID+"?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		tb.Fatalf("hosttest: open database: %v", err)
		return nil
	}
	// More than one connection, because plugin code legitimately opens a second query
	// while the first is still being scanned -- an HTTP handler reading a list and then
	// looking something up per row. The real host serves those from a read pool; a
	// single-connection pool here would deadlock on a pattern that works in production.
	sqlDB.SetMaxOpenConns(4)
	// An in-memory SQLite database lives only as long as a connection to it does, so
	// the pool must never drop to zero while the test is running.
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxLifetime(0)

	h := &Harness{tb: tb, plugin: plugin, manifest: m, sqlDB: sqlDB, enabled: true}
	h.Clock = &Clock{now: DefaultNow}
	lines := new([]LogLine)
	h.Log = &Logger{tb: tb, attrs: []any{"plugin", m.ID}, lines: lines}

	gate := h.gate
	h.AI = &AIFake{
		chats: map[string][]chatReply{}, chatFns: map[string]ChatFunc{},
		embeds: map[string][]embedReply{}, embedFns: map[string]EmbedFunc{},
		gate: gate, clock: h.Clock,
	}
	h.Search = &SearchFake{hits: map[string][]hostsearch.Hit{}, gate: gate}
	h.Browser = &BrowserFake{
		pages:    map[string]string{},
		resource: map[string]hostbrowser.Resource{},
		frames:   map[string][][]byte{},
		handlers: map[string]http.Handler{},
		creds:    map[string]string{},
		gate:     gate,
		clock:    h.Clock,
	}

	store := &db{sqlDB: sqlDB, prefix: m.ID + "_", gate: gate}
	h.bus = &bus{store: store, clock: h.Clock, gate: gate}
	h.impl = &hostImpl{
		id:     m.ID,
		ai:     h.AI,
		brow:   h.Browser,
		search: h.Search,
		jobs: &jobsImpl{
			id: m.ID, defs: map[string]hostjobs.Def{}, jobs: map[int64]*hostjobs.Job{},
			clock: h.Clock, gate: gate, logs: map[int64][]hostjobs.LogLine{},
			idemp: map[string]int64{},
		},
		events: &eventsImpl{id: m.ID, bus: h.bus},
		store:  store,
		blobs:  &blobs{items: map[string]*blob{}, gate: gate, now: h.Clock.Now, id: m.ID},
		config: &configImpl{watchers: map[int]func(context.Context, json.RawMessage){}},
		log:    h.Log,
		clock:  h.Clock,
	}
	// Defaults are the plugin's own, so a test that never touches config sees what a
	// fresh install would.
	if len(m.Config.Defaults) > 0 {
		h.impl.config.doc = m.Config.Defaults
	}

	if err := h.declare(); err != nil {
		tb.Fatalf("hosttest: %v", err)
		return nil
	}
	tb.Cleanup(func() { _ = sqlDB.Close() })
	return h
}

// declare reads the plugin's pure declarations, the way the registry does before
// Migrate or Init. Handlers are recorded, never invoked.
func (h *Harness) declare() error {
	jobDefs := h.plugin.Jobs()
	for _, d := range jobDefs {
		if d.Name == "" {
			return fmt.Errorf("job definition has no name")
		}
		if d.Handler == nil {
			return fmt.Errorf("job %q has no handler", d.Name)
		}
		if _, dup := h.impl.jobs.defs[d.Name]; dup {
			return fmt.Errorf("duplicate job %q", d.Name)
		}
		if d.Schedule != "" && d.TimeZone == "" {
			return fmt.Errorf("job %q has a schedule but no time zone", d.Name)
		}
		h.impl.jobs.defs[d.Name] = d
	}

	h.routes = h.plugin.Routes()
	h.mux = http.NewServeMux()
	seen := map[string]struct{}{}
	for _, r := range h.routes {
		if r.Handler == nil {
			return fmt.Errorf("route %q has no handler", r.Pattern)
		}
		if _, dup := seen[r.Pattern]; dup {
			return fmt.Errorf("duplicate route %q", r.Pattern)
		}
		seen[r.Pattern] = struct{}{}
		h.mux.Handle(r.Pattern, r.Handler)
	}

	return h.bus.register(h.manifest.ID, h.plugin.Subscriptions())
}

// Run applies the plugin's migrations and initializes it, in that order, and registers
// Shutdown for the end of the test. It is the ordinary way to start a plugin; a test
// that wants to assert on migration alone can call Migrate on its own.
func (h *Harness) Run(ctx context.Context) {
	h.tb.Helper()
	h.Migrate()
	if err := h.plugin.Init(ctx, h.impl); err != nil {
		h.tb.Fatalf("hosttest: Init: %v", err)
		return
	}
	h.mu.Lock()
	h.started = true
	h.mu.Unlock()
	h.tb.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.plugin.Shutdown(shutdownCtx); err != nil {
			h.tb.Logf("hosttest: Shutdown: %v", err)
		}
	})
}

// Migrate runs the plugin's migrations. It is idempotent and runs even while the
// plugin is disabled, matching the real host, so calling it twice is a legitimate
// test of a plugin's schema steps.
func (h *Harness) Migrate() {
	h.tb.Helper()
	if err := h.plugin.Migrate(&migrator{h: h}); err != nil {
		h.tb.Fatalf("hosttest: Migrate: %v", err)
	}
}

// migrator applies a plugin's forward-only schema steps and records what it applied,
// so a second Migrate is a no-op.
type migrator struct{ h *Harness }

func (m *migrator) Apply(migrations []host.Migration) error {
	h := m.h
	prefix := h.manifest.ID + "_"
	if _, err := h.sqlDB.Exec(`CREATE TABLE IF NOT EXISTS hosttest_schema_version (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	ordered := append([]host.Migration(nil), migrations...)
	sort.Slice(ordered, func(a, b int) bool { return ordered[a].Version < ordered[b].Version })

	seen := map[int]struct{}{}
	for _, mig := range ordered {
		if mig.Version <= 0 {
			return fmt.Errorf("hosttest: migration %q has no version", mig.Name)
		}
		if _, dup := seen[mig.Version]; dup {
			return fmt.Errorf("hosttest: duplicate migration version %d", mig.Version)
		}
		seen[mig.Version] = struct{}{}
		// The real host enforces the prefix at runtime with a SQLite authorizer. The
		// double enforces it here, where a plugin author can actually see it.
		if err := checkMigrationPrefix(prefix, mig.Up); err != nil {
			return err
		}
		var applied int
		err := h.sqlDB.QueryRow(`SELECT COUNT(*) FROM hosttest_schema_version WHERE version = ?`, mig.Version).Scan(&applied)
		if err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		tx, err := h.sqlDB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(mig.Up); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("hosttest: migration %d (%s): %w", mig.Version, mig.Name, err)
		}
		if _, err := tx.Exec(`INSERT INTO hosttest_schema_version (version, name, applied_at) VALUES (?, ?, ?)`,
			mig.Version, mig.Name, h.Clock.Now().Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

var _ host.Migrator = (*migrator)(nil)

// Host returns the host.Host the plugin sees, for code that takes one directly rather
// than going through the plugin's own lifecycle.
func (h *Harness) Host() host.Host { return h.impl }

// DB is the plugin's database, for arranging fixtures and asserting on rows. It is
// the same database the plugin writes through, without the gate.
func (h *Harness) DB() *sql.DB { return h.sqlDB }

// SetConfig replaces the plugin's settings document and notifies its watchers
// synchronously, standing in for the administration API.
func (h *Harness) SetConfig(ctx context.Context, v any) {
	h.tb.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		h.tb.Fatalf("hosttest: SetConfig: %v", err)
		return
	}
	h.impl.config.set(ctx, raw)
}

// Disable turns the kill switch off, the way an operator would. Every gated
// capability then returns policy.ErrPluginDisabled: new AI calls, browser sessions,
// search queries, event publications, storage mutations, and job enqueues. Reads and
// logging keep working, and HTTP routes answer a host-owned 503 without reaching
// plugin code.
func (h *Harness) Disable() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enabled = false
}

// Enable turns the plugin back on.
func (h *Harness) Enable() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enabled = true
}

// gate is the single enforcement point every capability consults. mutating is false
// for reads, which a disabled plugin is still allowed to make.
func (h *Harness) gate(mutating bool) error {
	if !mutating {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.enabled {
		return disabledError()
	}
	return nil
}

// Events returns every event the plugin published, oldest first, with the types and
// source the rest of the system would see.
func (h *Harness) Events() []hostevents.Event { return h.bus.events() }

// Publish delivers an event to the plugin's subscriptions as if another part of the
// system had published it. Delivery is synchronous: when Publish returns, every
// matching handler has run.
func (h *Harness) Publish(ctx context.Context, source, eventType, subject string, payload any) {
	h.tb.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		h.tb.Fatalf("hosttest: Publish: %v", err)
		return
	}
	e := hostevents.Event{
		Type: eventType, Source: source, Subject: subject,
		Payload: raw, CreatedAt: h.Clock.Now(),
	}
	if err := h.bus.commit(ctx, e); err != nil {
		h.tb.Fatalf("hosttest: subscription handler: %v", err)
	}
}

// Enqueue queues a job by name without running it, for asserting on what a route or
// handler scheduled.
func (h *Harness) Enqueue(ctx context.Context, name string, args any) int64 {
	h.tb.Helper()
	id, err := h.impl.jobs.Enqueue(ctx, name, args)
	if err != nil {
		h.tb.Fatalf("hosttest: Enqueue %s: %v", name, err)
	}
	return id
}

// RunJob runs one job to completion, including retries, and returns the error the
// final attempt produced. A failing job is not a failing test: a test of the retry
// path wants that error.
func (h *Harness) RunJob(ctx context.Context, id int64) error {
	return h.impl.jobs.run(ctx, id)
}

// RunJobNow enqueues a job and runs it in one step, which is what most tests want.
func (h *Harness) RunJobNow(ctx context.Context, name string, args any) error {
	h.tb.Helper()
	return h.RunJob(ctx, h.Enqueue(ctx, name, args))
}

// Drain runs every queued job, including jobs enqueued by the jobs it runs, until the
// queue is empty. It returns the first error any job ended with.
//
// The cap exists so a plugin that re-enqueues itself fails the test instead of hanging
// it.
func (h *Harness) Drain(ctx context.Context) error {
	h.tb.Helper()
	var first error
	for i := 0; i < 1000; i++ {
		pending := h.impl.jobs.pending()
		if len(pending) == 0 {
			return first
		}
		for _, id := range pending {
			if err := h.RunJob(ctx, id); err != nil && first == nil {
				first = err
			}
		}
	}
	h.tb.Fatalf("hosttest: Drain did not settle after 1000 rounds; a job is re-enqueueing itself")
	return first
}

// Job returns one job's current row, including its logs.
func (h *Harness) Job(ctx context.Context, id int64) *hostjobs.Job {
	h.tb.Helper()
	job, err := h.impl.jobs.Get(ctx, id)
	if err != nil {
		h.tb.Fatalf("hosttest: Job %d: %v", id, err)
	}
	return job
}

// Do dispatches an HTTP request to the plugin's routes, applying the same rules the
// real mount does: the path is relative to /api/plugins/<id>, a disabled plugin gets a
// host-owned 503 without reaching plugin code, a request body is capped at 1 MiB, and
// a handler panic becomes a 500 rather than taking the process down.
// The result is named so a recovered panic still returns the recorder holding the 500,
// the way the real mount still writes a response after a handler dies.
func (h *Harness) Do(req *http.Request) (rec *httptest.ResponseRecorder) {
	rec = httptest.NewRecorder()
	if err := h.gate(true); err != nil {
		rec.Header().Set("Content-Type", "application/json; charset=utf-8")
		rec.WriteHeader(http.StatusServiceUnavailable)
		_, _ = rec.Body.WriteString(`{"error":{"code":"plugin_disabled","message":"plugin disabled"}}`)
		return rec
	}
	defer func() {
		if r := recover(); r != nil {
			h.tb.Logf("hosttest: plugin handler panic: %v", r)
			rec.Code = http.StatusInternalServerError
			rec.Body.Reset()
			_, _ = rec.Body.WriteString(`{"error":{"code":"internal","message":"internal error"}}`)
		}
	}()
	if req.Body != nil {
		req.Body = http.MaxBytesReader(rec, req.Body, 1<<20)
	}
	h.mux.ServeHTTP(rec, req)
	return rec
}

// GET dispatches a GET to a plugin route path, relative to the plugin's mount point.
func (h *Harness) GET(path string) *httptest.ResponseRecorder {
	return h.Do(httptest.NewRequest(http.MethodGet, path, nil))
}

// POST dispatches a JSON POST to a plugin route path.
func (h *Harness) POST(path string, body any) *httptest.ResponseRecorder {
	h.tb.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.tb.Fatalf("hosttest: POST %s: %v", path, err)
			return nil
		}
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(http.MethodPost, path, reader)
	req.Header.Set("Content-Type", "application/json")
	return h.Do(req)
}

// DecodeJSON decodes a recorded response body, failing the test if the status is not
// the one expected or the body is not the shape asked for.
func (h *Harness) DecodeJSON(rec *httptest.ResponseRecorder, wantStatus int, dst any) {
	h.tb.Helper()
	if rec.Code != wantStatus {
		h.tb.Fatalf("hosttest: status = %d, want %d: %s", rec.Code, wantStatus, rec.Body.String())
		return
	}
	if dst == nil {
		return
	}
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		h.tb.Fatalf("hosttest: decode response: %v: %s", err, rec.Body.String())
	}
}

func errInvalid(format string, args ...any) error {
	return fmt.Errorf("hosttest: "+format, args...)
}
