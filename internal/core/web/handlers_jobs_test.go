package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/policy"
)

func newJobsHarness(t *testing.T) (*harness, *jobs.Queue, *policy.Store) {
	t.Helper()
	h := newHarness(t)
	bus, err := events.New(h.store, h.store, events.Options{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	p, err := policy.New(h.store, h.store, bus, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	q, err := jobs.New(h.store, h.store, bus, p, jobs.Options{})
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	h.server.deps.Events = bus
	h.server.deps.Policy = p
	h.server.deps.Jobs = q
	return h, q, p
}

func TestJobListGetAndCancel(t *testing.T) {
	h, q, p := newJobsHarness(t)
	h.bootstrapAdmin()
	ctx := context.Background()
	if err := p.Register(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}
	if err := p.Enable(ctx, "hello", "test", "setup"); err != nil {
		t.Fatal(err)
	}
	if err := q.Register("hello", jobs.Def{
		Name:    "work",
		Handler: func(jobs.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	id, err := q.Enqueue(ctx, "hello", "work", map[string]string{"k": "v"},
		jobs.WithRunAt(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodGet, "/api/jobs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var snap struct {
		Data        []jobs.Job `json:"data"`
		AsOfEventID string     `json:"asOfEventId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.AsOfEventID == "" || len(snap.Data) != 1 || snap.Data[0].ID != id {
		t.Fatalf("list = %+v", snap)
	}

	rec = h.do(http.MethodGet, "/api/jobs/"+strconv.FormatInt(id, 10), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}

	if rec := h.do(http.MethodPost, "/api/jobs/"+strconv.FormatInt(id, 10)+"/cancel", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	j, err := q.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != jobs.StateCancelled {
		t.Fatalf("state = %s", j.State)
	}

	if rec := h.do(http.MethodGet, "/api/jobs/999999", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", rec.Code)
	}
}
