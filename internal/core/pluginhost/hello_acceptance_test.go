package pluginhost

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/browser"
	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
	"github.com/hbaldwin98/control-center/plugins/hello"
)

type helloFix struct {
	t     *testing.T
	ctx   context.Context
	store *storage.Store
	blobs *storage.BlobStore
	bus   *events.Log
	pol   *policy.Store
	q     *jobs.Queue
	ai    *ai.Service
	reg   *Registry
	plug  *hello.Plugin
	hold  *ai.HoldFake

	mu  sync.Mutex
	now time.Time
}

func newHelloFix(t *testing.T, hold bool) *helloFix {
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

	f := &helloFix{t: t, ctx: ctx, store: store, blobs: blobs, bus: bus, now: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)}
	pol, err := policy.New(store, store, bus, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.pol = pol

	q, err := jobs.New(store, store, bus, pol, jobs.Options{
		PollInterval: 20 * time.Millisecond, LeaseTTL: time.Second, Heartbeat: 40 * time.Millisecond,
		Now: f.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start(ctx)
	t.Cleanup(q.Stop)
	f.q = q

	key := make([]byte, 32)
	_, _ = rand.Read(key)
	creds, err := credentials.New(store, store, bus, credentials.Options{Keys: map[int][]byte{1: key}, Active: 1})
	if err != nil {
		t.Fatal(err)
	}
	actx := credentials.WithActor(ctx, "admin")
	if _, err := creds.CreateAPIKey(actx, credentials.APIKeyInput{
		ID: "fake-key", Provider: "fake", Secret: credentials.SecretInput{Value: "test-token"},
	}); err != nil {
		t.Fatal(err)
	}

	models := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(models, []byte(`
routes:
  cheap-chat:
    capabilities: [chat]
    maxInputTokens: 128
    maxOutputTokens: 64
    attempts:
      - provider: fake
        model: echo
        credential: fake-key
        inputMicroUSDPerMillion: 1000000
        outputMicroUSDPerMillion: 2000000
`), 0o600); err != nil {
		t.Fatal(err)
	}
	routes, err := ai.LoadRoutes(models)
	if err != nil {
		t.Fatal(err)
	}
	var providers []ai.Provider
	if hold {
		f.hold = ai.NewHoldFake()
		providers = []ai.Provider{f.hold}
	} else {
		providers = []ai.Provider{ai.Fake{}}
	}
	svc, err := ai.New(store, store, bus, pol, creds, ai.Options{
		Routes: routes, Providers: providers, Refs: creds, Now: f.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.ai = svc

	br, err := browser.New(bus, pol, browser.Options{
		Engine: browser.NewFake(map[string]http.Handler{
			"hello.test": browser.HTMLHandler(`<!doctype html><article class="lot">hello</article>`),
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)

	plug := hello.New()
	f.plug = plug
	reg, err := New(ctx, store, Options{
		DB: store, Blobs: blobs, Events: bus, Policy: pol, Jobs: q, AI: svc, Browser: br,
		Now: f.clock, ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(plug); err != nil {
		t.Fatal(err)
	}
	if err := reg.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Stop(context.Background()) })
	f.reg = reg
	return f
}

func (f *helloFix) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *helloFix) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *helloFix) enable() {
	f.t.Helper()
	if err := f.pol.SetBudget(f.ctx, "hello", policy.Budget{Daily: 50_000_000, OnExceed: policy.ExceedReject}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.reg.Enable(f.ctx, "hello", "test", "go"); err != nil {
		f.t.Fatal(err)
	}
}

func (f *helloFix) waitIdle(d time.Duration) {
	f.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		list, err := f.q.List(f.ctx, jobs.Filter{PluginID: "hello", Name: "tick"})
		if err != nil {
			f.t.Fatal(err)
		}
		busy := false
		for _, j := range list {
			if j.State == jobs.StatePending || j.State == jobs.StateRunning || j.State == jobs.StateCancelRequested {
				busy = true
				break
			}
		}
		if !busy {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatal("jobs did not go idle")
}

func (f *helloFix) waitJob(id int64, want jobs.State, d time.Duration) *jobs.Job {
	f.t.Helper()
	deadline := time.Now().Add(d)
	var last *jobs.Job
	for time.Now().Before(deadline) {
		j, err := f.q.Get(f.ctx, id)
		if err == nil {
			last = j
			if j.State == want {
				return j
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if last == nil {
		f.t.Fatalf("job %d never appeared", id)
	}
	f.t.Fatalf("job %d state %s, want %s", id, last.State, want)
	return last
}

func (f *helloFix) serve(method, path string, body []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	f.reg.ServeHTTP(rec, req)
	return rec
}

func (f *helloFix) facade() host.Host {
	return f.reg.facadeFor(f.plug.Manifest(), &pluginConfig{value: json.RawMessage(`{"note":"hello"}`)})
}

func (f *helloFix) tickCount() int {
	f.t.Helper()
	var n int
	if err := f.store.QueryRow(f.ctx, `SELECT count(*) FROM hello_ticks`).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestHelloDisabledUntilEnabled(t *testing.T) {
	f := newHelloFix(t, false)
	rec := f.serve(http.MethodGet, "/api/plugins/hello/ticks", nil)
	if rec.Code != http.StatusServiceUnavailable || !bytes.Contains(rec.Body.Bytes(), []byte("plugin_disabled")) {
		t.Fatalf("disabled GET: %d %s", rec.Code, rec.Body.Bytes())
	}
}

func TestHelloKillSwitchAcceptance(t *testing.T) {
	f := newHelloFix(t, false)
	f.enable()
	f.waitIdle(3 * time.Second)

	rec := f.serve(http.MethodPost, "/api/plugins/hello/tick", []byte(`{"hold":true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("enqueue hold: %d %s", rec.Code, rec.Body.Bytes())
	}
	var posted struct {
		JobID int64 `json:"jobId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &posted); err != nil || posted.JobID == 0 {
		t.Fatalf("body %s", rec.Body.Bytes())
	}
	running := f.waitJob(posted.JobID, jobs.StateRunning, 2*time.Second)

	if err := f.reg.Disable(f.ctx, "hello", "test", "kill"); err != nil {
		t.Fatal(err)
	}
	j := f.waitJob(running.ID, jobs.StateCancelled, 2*time.Second)
	if j.CancelReason != jobs.ReasonPluginDisabled {
		t.Fatalf("cancel reason %q", j.CancelReason)
	}

	before := len(mustList(t, f))
	f.advance(time.Minute)
	time.Sleep(150 * time.Millisecond)
	afterDisable := len(mustList(t, f))
	if afterDisable != before {
		t.Fatalf("cron enqueued while disabled: %d -> %d", before, afterDisable)
	}

	if err := f.reg.Enable(f.ctx, "hello", "test", "back"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if n := len(mustList(t, f)); n != afterDisable {
		t.Fatalf("re-enable caught up skipped slot: %d -> %d", afterDisable, n)
	}
}

func TestHelloRejectsWorkAfterDisable(t *testing.T) {
	f := newHelloFix(t, true)
	f.enable()
	f.waitIdle(3 * time.Second)
	started := f.tickCount()
	if err := f.reg.Disable(f.ctx, "hello", "test", "off"); err != nil {
		t.Fatal(err)
	}

	_, err := ai.Scoped(f.ai, "hello").Chat(f.ctx, ai.ChatRequest{
		Model: "cheap-chat", Messages: []ai.Message{{Role: "user", Text: "x"}},
	})
	if !errors.Is(err, policy.ErrPluginDisabled) {
		t.Fatalf("chat after disable: %v", err)
	}
	if f.hold.Calls() != 0 {
		t.Fatalf("provider called %d times after disable", f.hold.Calls())
	}
	page, err := f.ai.Calls(f.ctx, ai.CallQuery{PluginID: "hello"})
	if err != nil || len(page.Calls) != 0 {
		t.Fatalf("usage rows %+v %v", page, err)
	}

	rec := f.serve(http.MethodGet, "/api/plugins/hello/ticks", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("HTTP after disable: %d", rec.Code)
	}

	h := f.facade()
	rows, err := h.Store().Query(f.ctx, `SELECT count(*) FROM hello_ticks`)
	if err != nil {
		t.Fatalf("ticks still readable: %v", err)
	}
	rows.Close()

	if err := h.Events().Publish(f.ctx, "ticked", "x", nil); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("publish: %v", err)
	}
	if _, err := h.Store().Exec(f.ctx, `INSERT INTO hello_ticks(at, note) VALUES ('x','x')`); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("sql: %v", err)
	}
	if _, err := h.Blobs().Put(f.ctx, "x.txt", bytes.NewReader([]byte("x")), "text/plain"); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("blob: %v", err)
	}
	if _, err := h.Browser().Open(f.ctx, hostbrowser.OpenOptions{AllowedHosts: []string{"hello.test"}}); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("browser: %v", err)
	}

	if _, err := f.bus.Publish(f.ctx, events.Input{Type: "hello.ticked", Source: "hello", Subject: "s", Payload: map[string]string{"note": "nope"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if f.tickCount() != started {
		t.Fatal("durable handler ran while disabled")
	}
}

func TestHelloAdmittedChatSettlesAfterDisable(t *testing.T) {
	f := newHelloFix(t, true)
	f.enable()
	f.waitIdle(3 * time.Second)

	done := make(chan error, 1)
	go func() {
		rec := f.serve(http.MethodPost, "/api/plugins/hello/chat", nil)
		if rec.Code != http.StatusOK {
			done <- errors.New(rec.Body.String())
			return
		}
		done <- nil
	}()
	select {
	case <-f.hold.Started():
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start")
	}
	if err := f.reg.Disable(f.ctx, "hello", "test", "mid-call"); err != nil {
		t.Fatal(err)
	}
	f.hold.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("chat did not finish")
	}
	page, err := f.ai.Calls(f.ctx, ai.CallQuery{PluginID: "hello"})
	if err != nil || len(page.Calls) == 0 {
		t.Fatalf("usage %+v %v", page, err)
	}
	if page.Calls[0].Status != "succeeded" || page.Calls[0].SettledMicroUSD <= 0 {
		t.Fatalf("call %+v", page.Calls[0])
	}
}

func TestHelloCancelsAdmittedHTTPContext(t *testing.T) {
	f := newHelloFix(t, false)
	f.enable()
	f.waitIdle(3 * time.Second)

	done := make(chan int, 1)
	go func() {
		rec := f.serve(http.MethodGet, "/api/plugins/hello/wait", nil)
		done <- rec.Code
	}()
	time.Sleep(50 * time.Millisecond)
	if err := f.reg.Disable(f.ctx, "hello", "test", "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("code %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait handler did not return")
	}
}

func TestHelloHappyPathTick(t *testing.T) {
	f := newHelloFix(t, false)
	f.enable()
	f.waitIdle(3 * time.Second)

	rec := f.serve(http.MethodPost, "/api/plugins/hello/tick", []byte(`{}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("tick: %d %s", rec.Code, rec.Body.Bytes())
	}
	var posted struct {
		JobID int64 `json:"jobId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &posted); err != nil {
		t.Fatal(err)
	}
	j := f.waitJob(posted.JobID, jobs.StateSucceeded, 3*time.Second)
	_ = j

	deadline := time.Now().Add(2 * time.Second)
	for f.tickCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if f.tickCount() == 0 {
		t.Fatal("durable handler did not record a tick")
	}

	rec = f.serve(http.MethodGet, "/api/plugins/hello/ticks", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.Bytes())
	}

	rc, _, err := f.blobs.Scoped("hello").Get(f.ctx, "latest.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()

	if err := f.reg.UpdateConfig(f.ctx, "hello", json.RawMessage(`{"note":"hi"}`), "test"); err != nil {
		t.Fatal(err)
	}
}

func mustList(t *testing.T, f *helloFix) []jobs.Job {
	t.Helper()
	list, err := f.q.List(f.ctx, jobs.Filter{PluginID: "hello", Name: "tick"})
	if err != nil {
		t.Fatal(err)
	}
	return list
}
