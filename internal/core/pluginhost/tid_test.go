package pluginhost

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/browser"
	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
	"github.com/hbaldwin98/control-center/plugins/tid"
)

func TestTIDCollectsUsageFromPortal(t *testing.T) {
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

	key := make([]byte, 32)
	_, _ = rand.Read(key)
	creds, err := credentials.New(store, store, bus, credentials.Options{Keys: map[int][]byte{1: key}, Active: 1})
	if err != nil {
		t.Fatal(err)
	}
	actx := credentials.WithActor(ctx, "admin")
	if _, err := creds.CreateAPIKey(actx, credentials.APIKeyInput{
		ID: "tid-pass", Provider: "tid", Secret: credentials.SecretInput{Value: "secret"},
	}); err != nil {
		t.Fatal(err)
	}
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
    maxInputTokens: 512
    maxOutputTokens: 256
    attempts:
      - provider: fake
        model: echo
        credential: fake-key
        inputMicroUSDPerMillion: 1000000
        outputMicroUSDPerMillion: 2000000
`), 0o600); err != nil {
		t.Fatal(err)
	}
	seed, err := ai.LoadSeed(models)
	if err != nil {
		t.Fatal(err)
	}
	aisvc, err := ai.New(store, store, bus, pol, creds, ai.Options{Seed: seed, Refs: creds})
	if err != nil {
		t.Fatal(err)
	}

	d1 := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	d2 := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	mux := http.NewServeMux()
	loginHTML := `<!doctype html><app-root>
<form method="post" action="/authentication/login">
<input type="email" formControlName="email" autocomplete="email">
<input type="password" formControlName="password" autocomplete="current-password">
<button mat-flat-button color="primary" class="w-100" type="submit">Sign in</button>
</form>
</app-root>`
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, loginHTML)
	})
	mux.HandleFunc("GET /authentication/login", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, loginHTML)
	})
	mux.HandleFunc("POST /authentication/login", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("email") != "me@tid.test" || r.Form.Get("password") != "secret" {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "ok", Path: "/"})
		http.Redirect(w, r, "https://my.tid.org/usage/graphs", http.StatusFound)
	})
	mux.HandleFunc("GET /usage/graphs", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("sid"); err != nil {
			http.Error(w, "no session", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<app-root><table class="usage"><tr><th>Date</th><th>kWh</th></tr>
<tr><td>%s</td><td>12.5</td></tr>
<tr><td>%s</td><td>8</td></tr></table></app-root>`, d1, d2)
	})

	br, err := browser.New(bus, pol, browser.Options{
		Engine: browser.NewFake(map[string]http.Handler{"my.tid.org": mux, "www.tid.org": mux, "tid.org": mux}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)

	reg, err := New(ctx, store, Options{
		DB: store, Blobs: blobs, Events: bus, Policy: pol, Jobs: q, AI: aisvc, Browser: br,
		Creds: creds, Refs: creds, ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(tid.New()); err != nil {
		t.Fatal(err)
	}
	if err := reg.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Stop(context.Background()) })

	if err := pol.SetBudget(ctx, "tid", policy.Budget{Daily: 50_000_000, OnExceed: policy.ExceedReject}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Enable(ctx, "tid", "test", "go"); err != nil {
		t.Fatal(err)
	}
	if err := reg.UpdateConfig(ctx, "tid", json.RawMessage(`{"username":"me@tid.test","credential_id":"tid-pass","cents_per_kwh":0,"usage_url":""}`), "test"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/tid/sync", bytes.NewReader(nil))
	reg.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync enqueue: %d %s", rec.Code, rec.Body.Bytes())
	}
	var posted struct {
		JobID int64 `json:"jobId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &posted); err != nil || posted.JobID == 0 {
		t.Fatalf("body %s", rec.Body.Bytes())
	}

	deadline := time.Now().Add(5 * time.Second)
	var job *jobs.Job
	for time.Now().Before(deadline) {
		j, err := q.Get(ctx, posted.JobID)
		if err == nil && (j.State == jobs.StateSucceeded || j.State == jobs.StateFailed || j.State == jobs.StateDead) {
			job = j
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if job == nil {
		t.Fatal("sync job did not finish")
	}
	if job.State != jobs.StateSucceeded {
		t.Fatalf("sync %s: %s", job.State, job.LastError)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/plugins/tid/summary", nil)
	reg.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body.Bytes())
	}
	var page struct {
		Days []struct {
			Day string  `json:"day"`
			KWh float64 `json:"kwh"`
		} `json:"days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Days) != 2 {
		t.Fatalf("summary days %+v", page.Days)
	}
	byDay := map[string]float64{}
	for _, d := range page.Days {
		byDay[d.Day] = d.KWh
	}
	if byDay[d1] != 12.5 || byDay[d2] != 8 {
		t.Fatalf("summary days %+v want %s=12.5 %s=8", page.Days, d1, d2)
	}
}

func TestTIDUploadCSV(t *testing.T) {
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
		DB: store, Blobs: blobs, Events: bus, Policy: pol, Jobs: q, ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(tid.New()); err != nil {
		t.Fatal(err)
	}
	if err := reg.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Stop(context.Background()) })
	if err := pol.SetBudget(ctx, "tid", policy.Budget{Daily: 50_000_000, OnExceed: policy.ExceedReject}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Enable(ctx, "tid", "test", "go"); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"csv": "Date,kWh\n2026-08-10,4.2\n2026-08-11,5\n"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/tid/upload", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	reg.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.Bytes())
	}
	var posted struct {
		JobID int64 `json:"jobId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &posted); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := q.Get(ctx, posted.JobID)
		if err == nil && j.State == jobs.StateSucceeded {
			return
		}
		if err == nil && (j.State == jobs.StateFailed || j.State == jobs.StateDead) {
			t.Fatalf("upload job %s: %s", j.State, j.LastError)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("upload job did not finish")
}
