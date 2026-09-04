package pluginhost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostevents "github.com/hbaldwin98/control-center/host/events"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
	hostpush "github.com/hbaldwin98/control-center/host/push"
	hostsearch "github.com/hbaldwin98/control-center/host/search"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/browser"
	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/push"
	"github.com/hbaldwin98/control-center/internal/core/search"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type scopedHost struct {
	pluginID string
	ai       hostai.AI
	browser  hostbrowser.Browser
	search   hostsearch.Search
	jobs     hostjobs.Jobs
	push     hostpush.Push
	events   host.Events
	store    hoststorage.DB
	blobs    hoststorage.Blobs
	config   *pluginConfig
	log      host.Logger
	clock    host.Clock
}

func (h *scopedHost) PluginID() string             { return h.pluginID }
func (h *scopedHost) AI() hostai.AI                { return h.ai }
func (h *scopedHost) Browser() hostbrowser.Browser { return h.browser }
func (h *scopedHost) Search() hostsearch.Search    { return h.search }
func (h *scopedHost) Jobs() hostjobs.Jobs          { return h.jobs }
func (h *scopedHost) Push() hostpush.Push          { return h.push }
func (h *scopedHost) Events() host.Events          { return h.events }
func (h *scopedHost) Store() hoststorage.DB        { return h.store }
func (h *scopedHost) Blobs() hoststorage.Blobs     { return h.blobs }
func (h *scopedHost) Config() host.Config          { return h.config }
func (h *scopedHost) Log() host.Logger             { return h.log }
func (h *scopedHost) Clock() host.Clock            { return h.clock }

type browserAdapter struct {
	inner browser.Browser
	creds credentials.Runtime
}

func (a *browserAdapter) Open(ctx context.Context, opts hostbrowser.OpenOptions) (hostbrowser.Session, error) {
	sess, err := a.inner.Open(ctx, browser.OpenOptions{AllowedHosts: opts.AllowedHosts})
	if err != nil {
		return nil, mapBrowserErr(err)
	}
	return browserSessionAdapter{inner: sess, creds: a.creds}, nil
}

func (a *browserAdapter) Do(ctx context.Context, opts hostbrowser.OpenOptions, req hostbrowser.Request) (hostbrowser.Resource, error) {
	body := append([]byte(nil), req.Body...)
	if req.Credential != nil {
		if a.creds == nil {
			return hostbrowser.Resource{}, fmt.Errorf("%w: credentials not configured", hostbrowser.ErrEngine)
		}
		if strings.TrimSpace(req.Credential.ID) == "" || strings.TrimSpace(req.Credential.Field) == "" {
			return hostbrowser.Resource{}, fmt.Errorf("%w: invalid credential injection", hostbrowser.ErrEngine)
		}
		var object map[string]any
		if err := json.Unmarshal(body, &object); err != nil {
			return hostbrowser.Resource{}, fmt.Errorf("%w: credential body must be a JSON object", hostbrowser.ErrEngine)
		}
		secret, err := a.creds.Token(ctx, req.Credential.ID)
		if err != nil {
			return hostbrowser.Resource{}, err
		}
		object[req.Credential.Field] = secret
		body, err = json.Marshal(object)
		if err != nil {
			return hostbrowser.Resource{}, fmt.Errorf("%w: encode credential body", hostbrowser.ErrEngine)
		}
	}
	r, err := a.inner.Do(ctx, browser.OpenOptions{AllowedHosts: opts.AllowedHosts}, browser.Request{
		Method: req.Method, URL: req.URL, Headers: req.Headers, Body: body,
	})
	if err != nil {
		return hostbrowser.Resource{}, mapBrowserErr(err)
	}
	return hostbrowser.Resource{URL: r.URL, MIME: r.MIME, Body: r.Body, Status: r.Status}, nil
}

func (a *browserAdapter) Read(ctx context.Context, opts hostbrowser.OpenOptions, url string) (hostbrowser.Document, error) {
	doc, err := a.inner.Read(ctx, browser.OpenOptions{AllowedHosts: opts.AllowedHosts}, url)
	if err != nil {
		return hostbrowser.Document{}, mapBrowserErr(err)
	}
	return hostbrowser.Document{URL: doc.URL, Content: doc.Content}, nil
}

type browserSessionAdapter struct {
	inner browser.Session
	creds credentials.Runtime
}

func (s browserSessionAdapter) NewPage(ctx context.Context) (hostbrowser.Page, error) {
	p, err := s.inner.NewPage(ctx)
	if err != nil {
		return nil, mapBrowserErr(err)
	}
	return browserPageAdapter{inner: p, creds: s.creds}, nil
}

func (s browserSessionAdapter) Subscribe(ctx context.Context, url string, opts hostbrowser.SubscribeOptions) (hostbrowser.Subscription, error) {
	inner := browser.SubscribeOptions{
		Handshake: make([][]byte, 0, len(opts.Handshake)),
		KeepAlive: make([]browser.KeepAliveRule, 0, len(opts.KeepAlive)),
	}
	for _, frame := range opts.Handshake {
		inner.Handshake = append(inner.Handshake, []byte(frame))
	}
	for _, rule := range opts.KeepAlive {
		inner.KeepAlive = append(inner.KeepAlive, browser.KeepAliveRule{Event: rule.Event, Reply: []byte(rule.Reply)})
	}
	sub, err := s.inner.Subscribe(ctx, url, inner)
	if err != nil {
		return nil, mapBrowserErr(err)
	}
	return newSubscriptionAdapter(sub), nil
}

func (s browserSessionAdapter) Close(ctx context.Context) error {
	return mapBrowserErr(s.inner.Close(ctx))
}

type browserPageAdapter struct {
	inner browser.Page
	creds credentials.Runtime
}

func (p browserPageAdapter) Goto(ctx context.Context, url string) error {
	return mapBrowserErr(p.inner.Goto(ctx, url))
}

func (p browserPageAdapter) WaitFor(ctx context.Context, selector string, d time.Duration) error {
	return mapBrowserErr(p.inner.WaitFor(ctx, selector, d))
}

func (p browserPageAdapter) Content(ctx context.Context) (string, error) {
	s, err := p.inner.Content(ctx)
	return s, mapBrowserErr(err)
}

func (p browserPageAdapter) Get(ctx context.Context, url string) (hostbrowser.Resource, error) {
	r, err := p.inner.Get(ctx, url)
	if err != nil {
		return hostbrowser.Resource{}, mapBrowserErr(err)
	}
	return hostbrowser.Resource{URL: r.URL, MIME: r.MIME, Body: r.Body, Status: r.Status}, nil
}

func (p browserPageAdapter) Post(ctx context.Context, raw string, form url.Values) (hostbrowser.Resource, error) {
	r, err := p.inner.Post(ctx, raw, form)
	if err != nil {
		return hostbrowser.Resource{}, mapBrowserErr(err)
	}
	return hostbrowser.Resource{URL: r.URL, MIME: r.MIME, Body: r.Body, Status: r.Status}, nil
}

func (p browserPageAdapter) Responses(ctx context.Context) ([]hostbrowser.Resource, error) {
	rs, err := p.inner.Responses(ctx)
	if err != nil {
		return nil, mapBrowserErr(err)
	}
	if len(rs) == 0 {
		return nil, nil
	}
	out := make([]hostbrowser.Resource, len(rs))
	for i, r := range rs {
		out[i] = hostbrowser.Resource{URL: r.URL, MIME: r.MIME, Body: r.Body, Status: r.Status}
	}
	return out, nil
}

func (p browserPageAdapter) Fill(ctx context.Context, selector, value string) error {
	return mapBrowserErr(p.inner.Fill(ctx, selector, value))
}

func (p browserPageAdapter) Click(ctx context.Context, selector string) error {
	return mapBrowserErr(p.inner.Click(ctx, selector))
}

func (p browserPageAdapter) FillCredential(ctx context.Context, selector, credentialID string) error {
	if strings.TrimSpace(credentialID) == "" {
		return fmt.Errorf("%w: empty credential id", hostbrowser.ErrEngine)
	}
	if p.creds == nil {
		return fmt.Errorf("%w: credentials not configured", hostbrowser.ErrEngine)
	}
	secret, err := p.creds.Token(ctx, credentialID)
	if err != nil {
		return err
	}
	return mapBrowserErr(p.inner.Fill(ctx, selector, secret))
}

func (p browserPageAdapter) Close(ctx context.Context) error {
	return mapBrowserErr(p.inner.Close(ctx))
}

// subscriptionAdapter republishes core frames on a plugin-facing channel. The pump
// exists only to translate the frame type; it inherits the core channel's lifetime, so
// the plugin sees the feed close exactly when the socket does. Its buffer matches the
// core's and it drops rather than blocking, so a plugin that abandons a subscription
// without closing it cannot wedge this goroutine.
type subscriptionAdapter struct {
	inner  browser.Subscription
	frames chan hostbrowser.Frame
}

func newSubscriptionAdapter(inner browser.Subscription) *subscriptionAdapter {
	a := &subscriptionAdapter{inner: inner, frames: make(chan hostbrowser.Frame, 256)}
	go a.pump()
	return a
}

func (a *subscriptionAdapter) pump() {
	defer close(a.frames)
	for f := range a.inner.Frames() {
		select {
		case a.frames <- hostbrowser.Frame{At: f.At, Data: f.Data}:
		default:
		}
	}
}

func (a *subscriptionAdapter) Frames() <-chan hostbrowser.Frame { return a.frames }

func (a *subscriptionAdapter) Err() error { return mapBrowserErr(a.inner.Err()) }

func (a *subscriptionAdapter) Close(ctx context.Context) error {
	return mapBrowserErr(a.inner.Close(ctx))
}

func mapBrowserErr(err error) error {
	if err == nil {
		return nil
	}
	err = mapPolicyErr(err)
	switch {
	case errors.Is(err, browser.ErrDenied):
		return errors.Join(hostbrowser.ErrDenied, err)
	case errors.Is(err, browser.ErrInvalidAllowlist):
		return errors.Join(hostbrowser.ErrInvalidAllowlist, err)
	case errors.Is(err, browser.ErrLimit):
		return errors.Join(hostbrowser.ErrLimit, err)
	case errors.Is(err, browser.ErrEngine), errors.Is(err, browser.ErrClosed):
		return errors.Join(hostbrowser.ErrEngine, err)
	case errors.Is(err, browser.ErrReader):
		return errors.Join(hostbrowser.ErrReader, err)
	}
	return err
}

type disabledBrowser struct{}

func (disabledBrowser) Open(context.Context, hostbrowser.OpenOptions) (hostbrowser.Session, error) {
	return nil, hostpolicy.ErrPluginDisabled
}
func (disabledBrowser) Do(context.Context, hostbrowser.OpenOptions, hostbrowser.Request) (hostbrowser.Resource, error) {
	return hostbrowser.Resource{}, hostpolicy.ErrPluginDisabled
}
func (disabledBrowser) Read(context.Context, hostbrowser.OpenOptions, string) (hostbrowser.Document, error) {
	return hostbrowser.Document{}, hostpolicy.ErrPluginDisabled
}

type searchAdapter struct {
	inner search.Search
}

func (a *searchAdapter) Query(ctx context.Context, req hostsearch.Request) ([]hostsearch.Hit, error) {
	hits, err := a.inner.Query(ctx, search.Request{
		Query: req.Query, MaxResults: req.MaxResults, AllowedDomains: req.AllowedDomains,
	})
	if err != nil {
		return nil, mapSearchErr(err)
	}
	out := make([]hostsearch.Hit, len(hits))
	for i, h := range hits {
		out[i] = hostsearch.Hit{URL: h.URL, Title: h.Title, Snippet: h.Snippet}
	}
	return out, nil
}

func mapSearchErr(err error) error {
	if err == nil {
		return nil
	}
	err = mapPolicyErr(err)
	switch {
	case errors.Is(err, search.ErrUnavailable):
		return errors.Join(hostsearch.ErrUnavailable, err)
	case errors.Is(err, search.ErrInvalid):
		return errors.Join(hostsearch.ErrInvalid, err)
	case errors.Is(err, search.ErrDenied):
		return errors.Join(hostsearch.ErrDenied, err)
	}
	return err
}

type disabledSearch struct{}

func (disabledSearch) Query(context.Context, hostsearch.Request) ([]hostsearch.Hit, error) {
	return nil, hostpolicy.ErrPluginDisabled
}

func (r *Registry) facadeFor(m host.Manifest, cfg *pluginConfig) host.Host {
	id := m.ID
	var aiHandle hostai.AI
	if r.opts.AI != nil {
		aiHandle = &aiAdapter{inner: ai.Scoped(r.opts.AI, id)}
	} else {
		aiHandle = disabledAI{}
	}
	var browserHandle hostbrowser.Browser
	if r.opts.Browser != nil {
		browserHandle = &browserAdapter{inner: browser.Scoped(r.opts.Browser, id), creds: r.opts.Creds}
	} else {
		browserHandle = disabledBrowser{}
	}
	var searchHandle hostsearch.Search
	if r.opts.Search != nil {
		searchHandle = &searchAdapter{inner: search.Scoped(r.opts.Search, id)}
	} else {
		searchHandle = disabledSearch{}
	}
	var pushHandle hostpush.Push
	if r.opts.Push != nil {
		pushHandle = &pushAdapter{inner: push.Scoped(r.opts.Push, id)}
	} else {
		pushHandle = disabledPush{}
	}
	return &scopedHost{
		pluginID: id,
		ai:       aiHandle,
		browser:  browserHandle,
		search:   searchHandle,
		jobs:     &jobsAdapter{inner: jobs.Scoped(r.opts.Jobs, id), gate: r.opts.Policy, pluginID: id},
		push:     pushHandle,
		events:   &gatedEvents{inner: events.Scoped(r.opts.Events, id), db: r.opts.DB, gate: r.opts.Policy, pluginID: id},
		store:    &gatedStore{db: r.opts.DB, inner: storage.Prefixed(r.opts.DB, id), gate: r.opts.Policy, pluginID: id},
		blobs: &blobAdapter{inner: storage.WithMutationAdmission(r.opts.Blobs.Scoped(id), func(ctx context.Context, tx storage.Tx) error {
			return r.opts.Policy.CheckWorkTx(ctx, tx, id)
		})},
		config: cfg,
		log:    slogLogger{l: r.opts.Log.With("plugin", id)},
		clock:  clockFunc(r.opts.Now),
	}
}

type clockFunc func() time.Time

func (c clockFunc) Now() time.Time { return c().UTC() }

type slogLogger struct{ l *slog.Logger }

func (s slogLogger) Debug(msg string, args ...any) { s.l.Debug(msg, args...) }
func (s slogLogger) Info(msg string, args ...any)  { s.l.Info(msg, args...) }
func (s slogLogger) Warn(msg string, args ...any)  { s.l.Warn(msg, args...) }
func (s slogLogger) Error(msg string, args ...any) { s.l.Error(msg, args...) }
func (s slogLogger) With(args ...any) host.Logger  { return slogLogger{l: s.l.With(args...)} }

func mapPolicyErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, policy.ErrPluginDisabled) {
		return errors.Join(hostpolicy.ErrPluginDisabled, err)
	}
	if errors.Is(err, policy.ErrBudgetExceeded) {
		return errors.Join(hostpolicy.ErrBudgetExceeded, err)
	}
	return err
}

type gatedEvents struct {
	inner    *events.ScopedBus
	db       *storage.Store
	gate     policy.Gate
	pluginID string
}

func (g *gatedEvents) Publish(ctx context.Context, eventType, subject string, payload any) error {
	return g.db.Tx(ctx, func(tx storage.Tx) error {
		return g.PublishTx(ctx, &txAdapter{tx: tx, db: g.db, pluginID: g.pluginID}, eventType, subject, payload)
	})
}

func (g *gatedEvents) PublishTx(ctx context.Context, tx hoststorage.Tx, eventType, subject string, payload any) error {
	itx, ok := unwrapTx(tx)
	if !ok {
		return fmt.Errorf("pluginhost: PublishTx needs a host transaction from Store().Tx")
	}
	if err := g.gate.CheckWorkTx(ctx, itx, g.pluginID); err != nil {
		return mapPolicyErr(err)
	}
	return g.inner.PublishTx(ctx, itx, eventType, subject, payload)
}

type gatedStore struct {
	db       *storage.Store
	inner    *storage.PrefixDB
	gate     policy.Gate
	pluginID string
}

func (g *gatedStore) Query(ctx context.Context, q string, args ...any) (hoststorage.Rows, error) {
	return g.inner.Query(ctx, q, args...)
}

func (g *gatedStore) QueryRow(ctx context.Context, q string, args ...any) hoststorage.Row {
	return g.inner.QueryRow(ctx, q, args...)
}

func (g *gatedStore) Exec(ctx context.Context, q string, args ...any) (hoststorage.Result, error) {
	var res sql.Result
	err := g.Tx(ctx, func(tx hoststorage.Tx) error {
		var err error
		res, err = tx.Exec(ctx, q, args...)
		return err
	})
	return res, err
}

func (g *gatedStore) Tx(ctx context.Context, fn func(hoststorage.Tx) error) error {
	return g.db.Tx(ctx, func(tx storage.Tx) error {
		if err := g.gate.CheckWorkTx(ctx, tx, g.pluginID); err != nil {
			return mapPolicyErr(err)
		}
		return fn(&txAdapter{tx: tx, db: g.db, pluginID: g.pluginID})
	})
}

type txAdapter struct {
	tx       storage.Tx
	db       *storage.Store
	pluginID string
}

func unwrapTx(tx hoststorage.Tx) (storage.Tx, bool) {
	a, ok := tx.(*txAdapter)
	if !ok {
		return nil, false
	}
	return a.tx, true
}

func (t *txAdapter) withAuth(fn func() error) error {
	prefix := t.pluginID + "_"
	if err := t.db.SetPluginAuthorizer(prefix); err != nil {
		return err
	}
	defer func() { _ = t.db.SetPluginAuthorizer("") }()
	return fn()
}

func (t *txAdapter) Query(ctx context.Context, q string, args ...any) (hoststorage.Rows, error) {
	var rows *sql.Rows
	err := t.withAuth(func() error {
		var err error
		rows, err = t.tx.Query(ctx, q, args...)
		return storageWrapDenied(err)
	})
	return rows, err
}

func (t *txAdapter) QueryRow(ctx context.Context, q string, args ...any) hoststorage.Row {
	_ = t.db.SetPluginAuthorizer(t.pluginID + "_")
	defer func() { _ = t.db.SetPluginAuthorizer("") }()
	return t.tx.QueryRow(ctx, q, args...)
}

func (t *txAdapter) Exec(ctx context.Context, q string, args ...any) (hoststorage.Result, error) {
	var res sql.Result
	err := t.withAuth(func() error {
		var err error
		res, err = t.tx.Exec(ctx, q, args...)
		return storageWrapDenied(err)
	})
	return res, err
}

func (t *txAdapter) AfterCommit(fn func()) { t.tx.AfterCommit(fn) }

func storageWrapDenied(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrSQLDenied) {
		return err
	}
	return err
}

type blobAdapter struct{ inner storage.Blobs }

func (b *blobAdapter) Put(ctx context.Context, key string, r io.Reader, mime string) (hoststorage.BlobRef, error) {
	ref, err := b.inner.Put(ctx, key, r, mime)
	if err != nil {
		return hoststorage.BlobRef{}, mapPolicyErr(err)
	}
	return hoststorage.BlobRef{Key: ref.Key, MIME: ref.MIME, Size: ref.Size, SHA256: ref.SHA256}, nil
}

func (b *blobAdapter) Get(ctx context.Context, key string) (io.ReadCloser, hoststorage.BlobMeta, error) {
	rc, meta, err := b.inner.Get(ctx, key)
	if err != nil {
		return nil, hoststorage.BlobMeta{}, err
	}
	return rc, hoststorage.BlobMeta{
		BlobRef:   hoststorage.BlobRef{Key: meta.Key, MIME: meta.MIME, Size: meta.Size, SHA256: meta.SHA256},
		CreatedAt: meta.CreatedAt,
		UpdatedAt: meta.UpdatedAt,
	}, nil
}

func (b *blobAdapter) Delete(ctx context.Context, key string) error {
	return mapPolicyErr(b.inner.Delete(ctx, key))
}

func (b *blobAdapter) URL(key string) string { return b.inner.URL(key) }

type jobsAdapter struct {
	inner    jobs.Jobs
	gate     policy.Gate
	pluginID string
}

func (a *jobsAdapter) Enqueue(ctx context.Context, name string, args any, opts ...hostjobs.Opt) (int64, error) {
	// The gate comes first. A disabled plugin has its job definitions unmounted, so
	// without this the queue reports the enqueue as an unknown definition -- an
	// implementation detail that tells the plugin to fix its code when the real answer
	// is that it is switched off.
	if err := a.gate.CheckWork(ctx, a.pluginID); err != nil {
		return 0, mapPolicyErr(err)
	}
	o := hostjobs.ApplyOpts(opts)
	var jopts []jobs.Opt
	if o.HasRunAt {
		jopts = append(jopts, jobs.WithRunAt(o.RunAt))
	}
	if o.IdempotencyKey != "" {
		jopts = append(jopts, jobs.WithIdempotencyKey(o.IdempotencyKey))
	}
	if o.Priority != 0 {
		jopts = append(jopts, jobs.WithPriority(o.Priority))
	}
	id, err := a.inner.Enqueue(ctx, name, args, jopts...)
	return id, mapJobsErr(err)
}

func (a *jobsAdapter) Cancel(ctx context.Context, id int64) error {
	return mapJobsErr(a.inner.Cancel(ctx, id))
}

func (a *jobsAdapter) Get(ctx context.Context, id int64) (*hostjobs.Job, error) {
	j, err := a.inner.Get(ctx, id)
	if err != nil {
		return nil, mapJobsErr(err)
	}
	return mapJob(j), nil
}

// jobSentinels pairs each core jobs error with the plugin-facing one that means the
// same thing. The two sets are declared separately -- core's in internal/core/jobs,
// the plugin's in host/jobs -- so a plugin's errors.Is check against the host sentinel
// only works if the adapter joins them here.
var jobSentinels = []struct{ inner, outer error }{
	{jobs.ErrUnknownJob, hostjobs.ErrUnknownJob},
	{jobs.ErrUnknownDef, hostjobs.ErrUnknownDef},
	{jobs.ErrInvalidName, hostjobs.ErrInvalidName},
	{jobs.ErrInvalidSchedule, hostjobs.ErrInvalidSchedule},
	{jobs.ErrLostLease, hostjobs.ErrLostLease},
	{jobs.ErrAlreadyTerminal, hostjobs.ErrAlreadyTerminal},
	{jobs.ErrNotCancellable, hostjobs.ErrNotCancellable},
	{jobs.ErrPluginMismatch, hostjobs.ErrPluginMismatch},
}

// mapJobsErr translates a queue error into the sentinel a plugin is documented to
// check for, keeping the original as context. Policy errors are mapped too, so one
// call covers everything that crosses this boundary.
func mapJobsErr(err error) error {
	if err == nil {
		return nil
	}
	for _, s := range jobSentinels {
		if errors.Is(err, s.inner) {
			return errors.Join(s.outer, err)
		}
	}
	return mapPolicyErr(err)
}

func (a *jobsAdapter) List(ctx context.Context, f hostjobs.Filter) ([]hostjobs.Job, error) {
	list, err := a.inner.List(ctx, jobs.Filter{Name: f.Name, State: jobs.State(f.State), Limit: f.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]hostjobs.Job, 0, len(list))
	for i := range list {
		out = append(out, *mapJob(&list[i]))
	}
	return out, nil
}

func mapJob(j *jobs.Job) *hostjobs.Job {
	out := &hostjobs.Job{
		ID: j.ID, Name: j.Name, Args: j.Args, State: string(j.State),
		Attempt: j.Attempt, MaxAttempts: j.MaxAttempts, Progress: j.Progress,
		ProgressMessage: j.ProgressMessage, LastError: j.LastError,
		CancelReason: j.CancelReason, CreatedAt: j.CreatedAt,
		StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
	}
	for _, l := range j.Logs {
		out.Logs = append(out.Logs, hostjobs.LogLine{Attempt: l.Attempt, At: l.At, Line: l.Line})
	}
	return out
}

// hostJobCtx is the plugin-facing job context. It carries the core job handle
// plus a context stamped with the job ID so AI usage attributes to this run.
type hostJobCtx struct {
	ctx   context.Context
	inner jobs.Context
}

func (c hostJobCtx) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c hostJobCtx) Done() <-chan struct{}       { return c.ctx.Done() }
func (c hostJobCtx) Err() error                  { return c.ctx.Err() }
func (c hostJobCtx) Value(key any) any           { return c.ctx.Value(key) }
func (c hostJobCtx) JobID() int64                { return c.inner.JobID() }
func (c hostJobCtx) Attempt() int                { return c.inner.Attempt() }
func (c hostJobCtx) Args(into any) error         { return c.inner.Args(into) }
func (c hostJobCtx) Progress(fraction float64, message string) error {
	return c.inner.Progress(fraction, message)
}
func (c hostJobCtx) Logf(format string, args ...any) error {
	return c.inner.Logf(format, args...)
}

type aiAdapter struct{ inner ai.AI }

func (a *aiAdapter) Chat(ctx context.Context, req hostai.ChatRequest) (*hostai.ChatResponse, error) {
	resp, err := a.inner.Chat(ctx, mapChatReq(req))
	if err != nil {
		return nil, mapPolicyErr(err)
	}
	return mapChatResp(resp), nil
}

func (a *aiAdapter) ChatStream(ctx context.Context, req hostai.ChatRequest) (hostai.Stream, error) {
	s, err := a.inner.ChatStream(ctx, mapChatReq(req))
	if err != nil {
		return nil, mapPolicyErr(err)
	}
	return streamAdapter{s}, nil
}

func (a *aiAdapter) Embed(ctx context.Context, req hostai.EmbedRequest) (*hostai.EmbedResponse, error) {
	resp, err := a.inner.Embed(ctx, ai.EmbedRequest{Model: req.Model, Inputs: req.Inputs})
	if err != nil {
		return nil, mapPolicyErr(err)
	}
	return &hostai.EmbedResponse{
		Vectors: resp.Vectors,
		Usage: hostai.Usage{
			InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			CostMicroUSD: hostpolicy.MicroUSD(resp.Usage.CostMicroUSD),
			Latency:      resp.Usage.Latency, Attempts: resp.Usage.Attempts,
		},
	}, nil
}

func mapChatReq(req hostai.ChatRequest) ai.ChatRequest {
	out := ai.ChatRequest{Model: req.Model, MaxTokens: req.MaxTokens, Schema: req.Schema}
	if req.Grounding != nil {
		g := ai.GroundingOptions{
			MaxQueries: req.Grounding.MaxQueries, Freshness: req.Grounding.Freshness,
			AllowedDomains: append([]string{}, req.Grounding.AllowedDomains...),
		}
		out.Grounding = &g
	}
	for _, m := range req.Messages {
		msg := ai.Message{Role: m.Role, Text: m.Text}
		for _, img := range m.Images {
			msg.Images = append(msg.Images, ai.Image{
				Blob: img.Blob, MIME: img.MIME, Resolution: ai.Resolution(img.Resolution),
			})
		}
		out.Messages = append(out.Messages, msg)
	}
	return out
}

func mapChatResp(resp *ai.ChatResponse) *hostai.ChatResponse {
	if resp == nil {
		return nil
	}
	out := &hostai.ChatResponse{
		Text: resp.Text, Parsed: resp.Parsed, Finish: resp.Finish,
		Usage: hostai.Usage{
			InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			CostMicroUSD: hostpolicy.MicroUSD(resp.Usage.CostMicroUSD),
			Latency:      resp.Usage.Latency, Attempts: resp.Usage.Attempts,
		},
	}
	for _, c := range resp.Citations {
		out.Citations = append(out.Citations, hostai.Citation{Start: c.Start, End: c.End, Source: c.Source})
	}
	for _, s := range resp.Sources {
		src := hostai.Source{URL: s.URL, Title: s.Title, PublishedAt: s.PublishedAt}
		out.Sources = append(out.Sources, src)
	}
	return out
}

type streamAdapter struct{ inner ai.Stream }

func (s streamAdapter) Recv() (hostai.Chunk, error) {
	c, err := s.inner.Recv()
	if err != nil {
		return hostai.Chunk{}, err
	}
	out := hostai.Chunk{Text: c.Text, Finish: c.Finish}
	if c.Usage != nil {
		u := hostai.Usage{
			InputTokens: c.Usage.InputTokens, OutputTokens: c.Usage.OutputTokens,
			CostMicroUSD: hostpolicy.MicroUSD(c.Usage.CostMicroUSD),
			Latency:      c.Usage.Latency, Attempts: c.Usage.Attempts,
		}
		out.Usage = &u
	}
	return out, nil
}

func (s streamAdapter) Close() error { return s.inner.Close() }

type disabledAI struct{}

func (disabledAI) Chat(context.Context, hostai.ChatRequest) (*hostai.ChatResponse, error) {
	return nil, hostpolicy.ErrPluginDisabled
}
func (disabledAI) ChatStream(context.Context, hostai.ChatRequest) (hostai.Stream, error) {
	return nil, hostpolicy.ErrPluginDisabled
}
func (disabledAI) Embed(context.Context, hostai.EmbedRequest) (*hostai.EmbedResponse, error) {
	return nil, hostpolicy.ErrPluginDisabled
}

type hostMigrator struct {
	inner    storage.Migrator
	pluginID string
}

func (m hostMigrator) Apply(ms []host.Migration) error {
	out := make([]storage.Migration, 0, len(ms))
	for _, x := range ms {
		out = append(out, storage.Migration{Version: x.Version, Name: x.Name, Up: x.Up})
	}
	return m.inner.Apply(m.pluginID, out)
}

func wrapJobHandler(h hostjobs.Handler) jobs.Handler {
	return func(jc jobs.Context) error {
		err := h(hostJobCtx{
			ctx:   browser.WithJob(ai.WithJob(jc, strconv.FormatInt(jc.JobID(), 10)), strconv.FormatInt(jc.JobID(), 10)),
			inner: jc,
		})
		if hostjobs.IsPermanent(err) {
			return jobs.Permanent(err)
		}
		if d, ok := hostjobs.RetryAfterDelay(err); ok {
			return jobs.RetryAfter(err, d)
		}
		return err
	}
}

func mapHostEvent(e events.Event) hostevents.Event {
	return hostevents.Event{
		ID: e.ID, Type: e.Type, Source: e.Source, Subject: e.Subject,
		Payload: e.Payload, CreatedAt: e.CreatedAt,
	}
}

// pushAdapter maps the core push capability onto the plugin SDK contract. The two
// Watcher types are identical by shape and deliberately separate by package: the SDK
// must not import core, and core must not import the SDK.
type pushAdapter struct{ inner push.Push }

func (a *pushAdapter) Publish(ctx context.Context, topic string, payload any) error {
	if err := a.inner.Publish(ctx, topic, payload); err != nil {
		return mapPushErr(err)
	}
	return nil
}

func (a *pushAdapter) Available(ctx context.Context, topic string) error {
	if err := a.inner.Available(ctx, topic); err != nil {
		return mapPushErr(err)
	}
	return nil
}

func (a *pushAdapter) Unavailable(ctx context.Context, topic string) error {
	if err := a.inner.Unavailable(ctx, topic); err != nil {
		return mapPushErr(err)
	}
	return nil
}

func (a *pushAdapter) Subscribers(topic string) int { return a.inner.Subscribers(topic) }

func (a *pushAdapter) Watch(w hostpush.Watcher) func() {
	if w == nil {
		return a.inner.SetWatcher(nil)
	}
	return a.inner.SetWatcher(coreWatcher{inner: w})
}

type coreWatcher struct{ inner hostpush.Watcher }

func (c coreWatcher) Join(ctx context.Context, topic string) error { return c.inner.Join(ctx, topic) }
func (c coreWatcher) Leave(topic string)                           { c.inner.Leave(topic) }

func mapPushErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, push.ErrInvalidTopic):
		return errors.Join(hostpush.ErrInvalidTopic, err)
	case errors.Is(err, push.ErrLimit):
		return errors.Join(hostpush.ErrLimit, err)
	case errors.Is(err, push.ErrPayload):
		return errors.Join(hostpush.ErrPayload, err)
	}
	return err
}

type disabledPush struct{}

func (disabledPush) Publish(context.Context, string, any) error { return hostpolicy.ErrPluginDisabled }
func (disabledPush) Available(context.Context, string) error    { return hostpolicy.ErrPluginDisabled }
func (disabledPush) Unavailable(context.Context, string) error  { return hostpolicy.ErrPluginDisabled }
func (disabledPush) Subscribers(string) int                     { return 0 }
func (disabledPush) Watch(hostpush.Watcher) func()              { return func() {} }
