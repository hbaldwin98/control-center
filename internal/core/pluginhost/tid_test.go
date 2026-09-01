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
	"net/url"
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
	const tenant = "tenant-test"
	step := 0
	nextStep := func(w http.ResponseWriter, want int) bool {
		step++
		if step != want {
			http.Error(w, fmt.Sprintf("request step %d, want %d", step, want), http.StatusConflict)
			return false
		}
		return true
	}
	checkHeaders := func(w http.ResponseWriter, r *http.Request, auth bool) bool {
		if r.Header.Get("ocx-tenant-id") != tenant {
			http.Error(w, "tenant header", http.StatusBadRequest)
			return false
		}
		if auth && r.Header.Get("Authorization") != "Bearer access-test" {
			http.Error(w, "authorization header", http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		if !nextStep(w, 1) || !checkHeaders(w, r, false) {
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["email"] != "person@example.test" || body["password"] != "secret" {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"accessToken":"access-test","email":"person@example.test","username":"internal-test","firstName":"Test","lastName":"Person"}}`)
	})
	mux.HandleFunc("GET /user/user-details", func(w http.ResponseWriter, r *http.Request) {
		if !nextStep(w, 2) || !checkHeaders(w, r, true) {
			return
		}
		_, _ = io.WriteString(w, `{"userDetails":{"accounts":[{"accountId":"account-test"}]}}`)
	})
	mux.HandleFunc("POST /ouaf/get-active-services", func(w http.ResponseWriter, r *http.Request) {
		if !nextStep(w, 3) || !checkHeaders(w, r, true) {
			return
		}
		var body struct {
			Payload  map[string]string `json:"payload"`
			Username string            `json:"username"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Payload["accountId"] != "account-test" || body.Payload["action"] != "READ" || body.Username != "internal-test" {
			http.Error(w, "active services request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"premiseList":{"serviceAgreements":{"saId":"service-test","serviceType":"E","saRateSchedule":{"rateSchedule":"TEST"},"ServicePoints":{"meterId":"meter-test"}}}}}`)
	})
	mux.HandleFunc("POST /ouaf/get-bill-data-extract", func(w http.ResponseWriter, r *http.Request) {
		if !nextStep(w, 4) || !checkHeaders(w, r, true) {
			return
		}
		var body struct {
			Payload                  map[string]string `json:"payload"`
			SelectedServiceAgreement map[string]any    `json:"selectedServiceAgreement"`
			Username                 string            `json:"username"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Payload["accountId"] != "account-test" || body.Payload["action"] != "READ" || body.Payload["saId"] != "service-test" || body.Username != "internal-test" {
			http.Error(w, "bill data request", http.StatusBadRequest)
			return
		}
		if body.SelectedServiceAgreement["saId"] != "service-test" || body.SelectedServiceAgreement["ServicePoints"] == nil {
			http.Error(w, "bill service agreement", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"data":{"billHistoryList":[{"usagePeriodStartDateTime":"%sT00:00:00-07:00","usagePeriodEndDateTime":"%sT23:59:59-07:00"}]}}`, d1, d2)
	})
	mux.HandleFunc("POST /ouaf/retrieve-usage-for-sa", func(w http.ResponseWriter, r *http.Request) {
		if !nextStep(w, 5) || !checkHeaders(w, r, true) {
			return
		}
		var body struct {
			Payload                  map[string]string `json:"payload"`
			SelectedServiceAgreement map[string]any    `json:"selectedServiceAgreement"`
			Username                 string            `json:"username"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Payload["action"] != "READ" || body.Payload["username"] != "internal-test" || body.Payload["firstname"] != "Test" || body.Payload["lastname"] != "Person" || body.Payload["emailAddress"] != "person@example.test" || body.Payload["accountId"] != "account-test" || body.Payload["personId"] != "" || body.Payload["saId"] != "service-test" || body.Payload["viewModeFlg"] != "D2BB" || body.Payload["usagePeriodStartDateTime"] != d1+"T00:00:00-07:00" || body.Payload["usagePeriodEndDateTime"] != d2+"T23:59:59-07:00" || body.Username != "internal-test" {
			http.Error(w, "usage request", http.StatusBadRequest)
			return
		}
		if body.SelectedServiceAgreement["saId"] != "service-test" || body.SelectedServiceAgreement["saRateSchedule"] == nil {
			http.Error(w, "selected service agreement", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"status":"OK","data":{"usageList":[{"costDate":"%s","usage":"12.5"},{"costDate":"%s","usage":"8"}]}}`, d1, d2)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	transport := client.Transport
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = serverURL.Scheme
		clone.URL.Host = serverURL.Host
		return transport.RoundTrip(clone)
	})

	br, err := browser.New(bus, pol, browser.Options{
		Engine: browser.NewFake(map[string]http.Handler{"unused.test": mux}), Client: client,
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
	if err := reg.UpdateConfig(ctx, "tid", json.RawMessage(`{"tenant_id":"tenant-test","username":"person@example.test","credential_id":"tid-pass","cents_per_kwh":0}`), "test"); err != nil {
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

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
