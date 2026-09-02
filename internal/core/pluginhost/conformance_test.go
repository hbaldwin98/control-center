package pluginhost_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostevents "github.com/hbaldwin98/control-center/host/events"
	"github.com/hbaldwin98/control-center/host/hosttest"
	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/browser"
	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/pluginhost"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/search"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// TestHostConformance runs the shared contract suite from host/hosttest against the
// real facade.
//
// This is what makes the double trustworthy. Plugin authors test against
// host/hosttest and never link core; the only thing standing between that convenience
// and a plugin whose tests pass against a fiction is this test. When it fails, the
// double is wrong -- fix the double, not the suite.
func TestHostConformance(t *testing.T) {
	hosttest.Conformance(t, func(t *testing.T, probe *hosttest.Probe) hosttest.Driver {
		return newCoreDriver(t, probe)
	})
}

// coreDriver builds a real control center around the probe: real SQLite, real event
// log, real policy gate, real job queue.
type coreDriver struct {
	t     *testing.T
	probe *hosttest.Probe
	store *storage.Store
	bus   *events.Log
	reg   *pluginhost.Registry
}

func newCoreDriver(t *testing.T, probe *hosttest.Probe) hosttest.Driver {
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

	bus, err := events.New(store, store, events.Options{PollInterval: 10 * time.Millisecond})
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
		PollInterval: 10 * time.Millisecond, LeaseTTL: time.Second, Heartbeat: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start(ctx)
	t.Cleanup(q.Stop)

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	creds, err := credentials.New(store, store, bus, credentials.Options{
		Keys: map[int][]byte{1: key}, Active: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	aisvc, err := ai.New(store, store, bus, pol, creds, ai.Options{Refs: creds})
	if err != nil {
		t.Fatal(err)
	}

	br, err := browser.New(bus, pol, browser.Options{Engine: browser.NewFake(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)

	reg, err := pluginhost.New(ctx, store, pluginhost.Options{
		DB: store, Blobs: blobs, Events: bus, Policy: pol, Jobs: q, AI: aisvc,
		Browser: br, Search: search.New(pol, search.Options{Engine: search.Fake{}}),
		Creds: creds, Refs: creds,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(probe); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Stop(context.Background()) })

	return &coreDriver{t: t, probe: probe, store: store, bus: bus, reg: reg}
}

func (d *coreDriver) Start(ctx context.Context) error {
	if err := d.reg.Start(ctx); err != nil {
		return err
	}
	return d.reg.Enable(ctx, hosttest.ProbeID, "conformance", "suite")
}

func (d *coreDriver) Enable(ctx context.Context) error {
	return d.reg.Enable(ctx, hosttest.ProbeID, "conformance", "suite")
}

func (d *coreDriver) Disable(ctx context.Context) error {
	return d.reg.Disable(ctx, hosttest.ProbeID, "conformance", "suite")
}

func (d *coreDriver) PublishExternal(ctx context.Context, eventType, subject string, payload any) error {
	// The bus rejects a publisher that claims a namespace it does not own, so the
	// source is the type's first segment.
	source, _, _ := strings.Cut(eventType, ".")
	_, err := d.bus.Publish(ctx, events.Input{
		Type: eventType, Source: source, Subject: subject, Payload: payload,
	})
	return err
}

func (d *coreDriver) Events(ctx context.Context) ([]hostevents.Event, error) {
	rows, err := d.store.Query(ctx,
		`SELECT id, type, source, subject, payload, created_at FROM core_events ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []hostevents.Event
	for rows.Next() {
		var e hostevents.Event
		var payload, createdAt string
		if err := rows.Scan(&e.ID, &e.Type, &e.Source, &e.Subject, &payload, &createdAt); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Settle waits for the real bus to finish dispatching. Delivery here is asynchronous
// and driven by a poller, so the suite cannot read subscription results until it
// quiesces. Waiting for the event count to stop moving is enough: the suite only
// publishes a fixed, small number of events and then settles.
func (d *coreDriver) Settle(ctx context.Context) {
	d.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	stable := 0
	last := -1
	for time.Now().Before(deadline) {
		var n int
		if err := d.store.QueryRow(ctx, `SELECT count(*) FROM core_events`).Scan(&n); err != nil {
			d.t.Fatalf("settle: %v", err)
		}
		if n == last {
			stable++
			// Three quiet samples in a row means the poller has caught up and
			// handlers that were going to run have run.
			if stable >= 3 {
				return
			}
		} else {
			stable = 0
			last = n
		}
		time.Sleep(20 * time.Millisecond)
	}
	d.t.Fatal("settle: the event log never stopped changing")
}
