package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostevents "github.com/hbaldwin98/control-center/host/events"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// matrixPlugin is core's own plugin for testing the enforcement matrix.
//
// The matrix is a property of the host, not of any plugin: disabling must cancel a
// running job, suppress a cron slot without backlog, reject new AI, browser, search,
// event, and storage work, and answer HTTP with a host-owned 503. Proving that with a
// real plugin gets the dependency backwards -- core's suite then fails whenever a
// plugin changes, and the plugin ends up carrying scaffolding that exists only for
// core's tests. This fixture touches every capability the matrix names, and nothing
// else.
type matrixPlugin struct {
	mu sync.Mutex
	h  host.Host

	// released, when non-nil, is closed by the holding job to signal it has entered
	// its wait. Tests use it instead of sleeping.
	holding chan struct{}
	once    sync.Once
}

const matrixID = "matrix"

func newMatrixPlugin() *matrixPlugin {
	return &matrixPlugin{holding: make(chan struct{})}
}

func (p *matrixPlugin) Manifest() host.Manifest {
	return host.Manifest{
		ID:          matrixID,
		Name:        "Enforcement Matrix Fixture",
		Version:     "1",
		Description: "Exercises every host capability the kill switch governs.",
		Automated:   true,
		Models: []host.ModelNeed{{
			Name:         "cheap-chat",
			Capabilities: []string{"chat"},
			Purpose:      "One tiny chat call per tick.",
		}},
		Events: []host.EventSpec{{
			Type:    "ticked",
			Purpose: "The fixture tick finished.",
			Fields: []host.EventField{
				{Name: "at", Type: "string", Purpose: "RFC3339Nano time of the tick."},
				{Name: "note", Type: "string", Purpose: "Configured note."},
				{Name: "blobKey", Type: "string", Purpose: "Blob written during the tick."},
				{Name: "aiText", Type: "string", Purpose: "Reply from cheap-chat, empty if the call failed."},
			},
		}},
		Config: host.ConfigSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"note":{"type":"string"}}}`),
			Defaults: json.RawMessage(`{"note":"default"}`),
		},
	}
}

func (p *matrixPlugin) host() (host.Host, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h, p.h != nil
}

// waitHolding blocks until the holding job has actually entered its wait, so a test
// can disable the plugin at a moment when a job is provably mid-flight.
func (p *matrixPlugin) waitHolding(d time.Duration) bool {
	select {
	case <-p.holding:
		return true
	case <-time.After(d):
		return false
	}
}

func (p *matrixPlugin) Jobs() []hostjobs.Def {
	return []hostjobs.Def{{
		Name:        "tick",
		Schedule:    "* * * * *",
		TimeZone:    "UTC",
		Timeout:     time.Minute,
		MaxAttempts: 1,
		Handler:     p.tick,
	}}
}

type matrixTickArgs struct {
	// Hold makes the job wait until its context is cancelled. Cron never sets it; it
	// is how a test gets a job to be running when the kill switch is thrown.
	Hold bool `json:"hold"`
}

type matrixTicked struct {
	At      string `json:"at"`
	Note    string `json:"note"`
	BlobKey string `json:"blobKey"`
	AIText  string `json:"aiText"`
}

const matrixBlobKey = "latest.txt"

func (p *matrixPlugin) tick(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("matrix: host is not initialized")
	}
	var args matrixTickArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	if args.Hold {
		_ = jc.Logf("holding until cancelled")
		p.once.Do(func() { close(p.holding) })
		<-jc.Done()
		return jc.Err()
	}

	now := h.Clock().Now().UTC()
	var cfg struct {
		Note string `json:"note"`
	}
	_ = h.Config().Decode(&cfg)
	_ = jc.Logf("tick at %s note %s", now.Format(time.RFC3339), cfg.Note)

	// Each capability is called and its failure tolerated: the matrix cares that a
	// disabled plugin is refused, not that this fixture completes.
	aiText := ""
	if resp, err := h.AI().Chat(jc, hostai.ChatRequest{
		Model:    "cheap-chat",
		Messages: []hostai.Message{{Role: hostai.RoleUser, Text: "ping"}},
	}); err != nil {
		_ = jc.Logf("ai call failed: %v", err)
	} else {
		aiText = resp.Text
	}

	if err := p.fetch(jc, h); err != nil {
		_ = jc.Logf("browser call failed: %v", err)
	}

	payload := []byte(cfg.Note + " " + now.Format(time.RFC3339Nano))
	if _, err := h.Blobs().Put(jc, matrixBlobKey, bytes.NewReader(payload), "text/plain"); err != nil {
		return err
	}
	rc, _, err := h.Blobs().Get(jc, matrixBlobKey)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()

	if err := jc.Progress(0.9, "publishing"); err != nil {
		return err
	}
	return h.Events().Publish(jc, "ticked", now.Format(time.RFC3339Nano), matrixTicked{
		At: now.Format(time.RFC3339Nano), Note: cfg.Note, BlobKey: matrixBlobKey, AIText: aiText,
	})
}

func (p *matrixPlugin) fetch(jc hostjobs.Context, h host.Host) error {
	sess, err := h.Browser().Open(jc, hostbrowser.OpenOptions{AllowedHosts: []string{matrixSiteHost}})
	if err != nil {
		return err
	}
	defer sess.Close(jc)
	page, err := sess.NewPage(jc)
	if err != nil {
		return err
	}
	defer page.Close(jc)
	if err := page.Goto(jc, "https://"+matrixSiteHost+"/"); err != nil {
		return err
	}
	_, err = page.Content(jc)
	return err
}

const matrixSiteHost = "matrix.test"

func (p *matrixPlugin) Subscriptions() []host.Subscription {
	return []host.Subscription{{
		Pattern: matrixID + ".ticked",
		Durable: &host.DurableSubscription{Name: "ticks", Handler: p.onTicked},
	}}
}

// onTicked writes inside the cursor transaction, so a test can prove that durable
// delivery and its acknowledgement commit together.
func (p *matrixPlugin) onTicked(ctx context.Context, tx hoststorage.Tx, e hostevents.Event) error {
	var body matrixTicked
	if len(e.Payload) > 0 {
		_ = json.Unmarshal(e.Payload, &body)
	}
	if body.At == "" {
		body.At = e.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO matrix_ticks(at, note, blob_key, ai_text, event_id) VALUES (?, ?, ?, ?, ?)`,
		body.At, body.Note, body.BlobKey, body.AIText, e.ID)
	return err
}

func (p *matrixPlugin) Routes() []host.Route {
	return []host.Route{
		{Pattern: "GET /ticks", Handler: http.HandlerFunc(p.handleGetTicks)},
		{Pattern: "POST /tick", Handler: http.HandlerFunc(p.handlePostTick)},
		{Pattern: "POST /chat", Handler: http.HandlerFunc(p.handlePostChat)},
		{Pattern: "GET /wait", Handler: http.HandlerFunc(p.handleWait)},
	}
}

// handlePostChat spends money inside a request, so a test can disable the plugin
// mid-call and check that an already-admitted call still settles its reservation.
func (p *matrixPlugin) handlePostChat(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		http.Error(w, `{"error":{"code":"internal"}}`, http.StatusInternalServerError)
		return
	}
	resp, err := h.AI().Chat(r.Context(), hostai.ChatRequest{
		Model:    "cheap-chat",
		Messages: []hostai.Message{{Role: hostai.RoleUser, Text: "ping"}},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSONBody(w, http.StatusOK, map[string]any{
		"text":         resp.Text,
		"costMicroUsd": resp.Usage.CostMicroUSD,
	})
}

func (p *matrixPlugin) handleGetTicks(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		http.Error(w, `{"error":{"code":"internal"}}`, http.StatusInternalServerError)
		return
	}
	rows, err := h.Store().Query(r.Context(), `SELECT at, note FROM matrix_ticks ORDER BY id DESC LIMIT 50`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()
	out := []matrixTicked{}
	for rows.Next() {
		var t matrixTicked
		if err := rows.Scan(&t.At, &t.Note); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, t)
	}
	writeJSONBody(w, http.StatusOK, map[string]any{"data": out})
}

func (p *matrixPlugin) handlePostTick(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		http.Error(w, `{"error":{"code":"internal"}}`, http.StatusInternalServerError)
		return
	}
	var args matrixTickArgs
	if r.Body != nil {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				http.Error(w, `{"error":{"code":"bad_request"}}`, http.StatusBadRequest)
				return
			}
		}
	}
	id, err := h.Jobs().Enqueue(r.Context(), "tick", args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSONBody(w, http.StatusOK, map[string]any{"jobId": id})
}

// handleWait blocks until its request context is cancelled, which is how a test
// observes that disabling cancels an admitted request rather than only rejecting new
// ones.
func (p *matrixPlugin) handleWait(w http.ResponseWriter, r *http.Request) {
	<-r.Context().Done()
	writeJSONBody(w, http.StatusServiceUnavailable, map[string]any{
		"error": map[string]string{"code": "plugin_disabled"},
	})
}

func writeJSONBody(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (p *matrixPlugin) Migrate(m host.Migrator) error {
	return m.Apply([]host.Migration{{
		Version: 1, Name: "ticks", Up: `
CREATE TABLE matrix_ticks (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    at        TEXT    NOT NULL,
    note      TEXT    NOT NULL,
    blob_key  TEXT    NOT NULL,
    ai_text   TEXT    NOT NULL,
    event_id  INTEGER NOT NULL
) STRICT;`,
	}})
}

func (p *matrixPlugin) Init(ctx context.Context, h host.Host) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.h = h
	return nil
}

func (p *matrixPlugin) Shutdown(context.Context) error { return nil }

var _ host.Plugin = (*matrixPlugin)(nil)
