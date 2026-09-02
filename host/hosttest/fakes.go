package hosttest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostsearch "github.com/hbaldwin98/control-center/host/search"
)

// AIFake answers Chat, ChatStream, and Embed from canned replies the test programs.
//
// It never reaches a provider and never guesses: an unprogrammed route returns
// ErrUnknownRoute, the same error a plugin gets when the operator has not assigned a
// model. That is the failure a plugin most needs to handle, so the double defaults to
// it rather than to a friendly empty response.
type AIFake struct {
	mu       sync.Mutex
	chats    map[string][]chatReply
	chatFns  map[string]ChatFunc
	embeds   map[string][]embedReply
	embedFns map[string]EmbedFunc
	calls    []AICall
	gate     func(mutating bool) error
	clock    *Clock
}

// AICall is one recorded request, so a test can assert on what the plugin asked for
// -- the prompt, the schema, the image resolution -- and not only on what it did with
// the answer.
type AICall struct {
	Kind    string // "chat", "stream", or "embed"
	Request hostai.ChatRequest
	Embed   hostai.EmbedRequest
}

type chatReply struct {
	resp *hostai.ChatResponse
	err  error
}

type embedReply struct {
	resp *hostai.EmbedResponse
	err  error
}

// ChatFunc answers a chat request. Use it when the answer has to depend on what was
// asked -- a schema the plugin sent, a value it put in the prompt -- rather than being
// a fixed string. It is how a plugin models its own provider's behaviour in its own
// tests, instead of that knowledge having to live in the host.
type ChatFunc func(req hostai.ChatRequest) (*hostai.ChatResponse, error)

// EmbedFunc answers an embedding request the same way.
type EmbedFunc func(req hostai.EmbedRequest) (*hostai.EmbedResponse, error)

// AnswerChat installs a function to answer every call on a route. It takes precedence
// over queued replies.
func (f *AIFake) AnswerChat(model string, fn ChatFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatFns[model] = fn
}

// AnswerEmbed installs a function to answer every embedding call on a route.
func (f *AIFake) AnswerEmbed(model string, fn EmbedFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.embedFns[model] = fn
}

// Reply queues a plain text answer for the next call on a logical model route.
// Queued replies are consumed in order; the last one repeats once the queue empties,
// so a test that does not care how many times a route is called need only set one.
func (f *AIFake) Reply(model, text string) {
	f.ReplyWith(model, &hostai.ChatResponse{
		Text:   text,
		Usage:  hostai.Usage{InputTokens: 1, OutputTokens: int64(len(text)), Attempts: 1},
		Finish: "stop",
	})
}

// ReplyJSON queues a structured answer, for a route the plugin calls with a Schema.
func (f *AIFake) ReplyJSON(model string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f.ReplyWith(model, &hostai.ChatResponse{
		Text:   string(raw),
		Parsed: raw,
		Usage:  hostai.Usage{InputTokens: 1, OutputTokens: int64(len(raw)), Attempts: 1},
		Finish: "stop",
	})
	return nil
}

// ReplyWith queues a fully specified response, for tests that assert on usage, cost,
// citations, or a finish reason other than "stop".
func (f *AIFake) ReplyWith(model string, resp *hostai.ChatResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chats[model] = append(f.chats[model], chatReply{resp: resp})
}

// ReplyError queues a failure, for the retry and degradation paths.
func (f *AIFake) ReplyError(model string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chats[model] = append(f.chats[model], chatReply{err: err})
}

// Vectors queues an embedding response.
func (f *AIFake) Vectors(model string, vectors [][]float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.embeds[model] = append(f.embeds[model], embedReply{resp: &hostai.EmbedResponse{
		Vectors: vectors,
		Usage:   hostai.Usage{InputTokens: int64(len(vectors)), Attempts: 1},
	}})
}

// Calls returns every request the plugin made, in order.
func (f *AIFake) Calls() []AICall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AICall(nil), f.calls...)
}

func (f *AIFake) Chat(ctx context.Context, req hostai.ChatRequest) (*hostai.ChatResponse, error) {
	if err := f.gate(true); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, AICall{Kind: "chat", Request: req})
	return f.nextChat(req)
}

func (f *AIFake) ChatStream(ctx context.Context, req hostai.ChatRequest) (hostai.Stream, error) {
	if err := f.gate(true); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, AICall{Kind: "stream", Request: req})
	resp, err := f.nextChat(req)
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// One chunk per word, so a test can watch a partial response arrive without the
	// double having to model tokenization.
	var chunks []hostai.Chunk
	for _, word := range strings.Fields(resp.Text) {
		chunks = append(chunks, hostai.Chunk{Text: word + " "})
	}
	usage := resp.Usage
	chunks = append(chunks, hostai.Chunk{Finish: resp.Finish, Usage: &usage})
	return &sliceStream{chunks: chunks}, nil
}

func (f *AIFake) Embed(ctx context.Context, req hostai.EmbedRequest) (*hostai.EmbedResponse, error) {
	if err := f.gate(true); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, AICall{Kind: "embed", Embed: req})
	if fn, ok := f.embedFns[req.Model]; ok {
		return fn(req)
	}
	queue := f.embeds[req.Model]
	if len(queue) == 0 {
		return nil, fmt.Errorf("%w: %s", hostai.ErrUnknownRoute, req.Model)
	}
	next := queue[0]
	if len(queue) > 1 {
		f.embeds[req.Model] = queue[1:]
	}
	return next.resp, next.err
}

// nextChat consumes one queued reply. The caller holds the lock.
func (f *AIFake) nextChat(req hostai.ChatRequest) (*hostai.ChatResponse, error) {
	model := req.Model
	if fn, ok := f.chatFns[model]; ok {
		return fn(req)
	}
	queue := f.chats[model]
	if len(queue) == 0 {
		return nil, fmt.Errorf("%w: %s", hostai.ErrUnknownRoute, model)
	}
	next := queue[0]
	if len(queue) > 1 {
		f.chats[model] = queue[1:]
	}
	return next.resp, next.err
}

var _ hostai.AI = (*AIFake)(nil)

type sliceStream struct {
	chunks []hostai.Chunk
	i      int
}

func (s *sliceStream) Recv() (hostai.Chunk, error) {
	if s.i >= len(s.chunks) {
		return hostai.Chunk{}, io.EOF
	}
	c := s.chunks[s.i]
	s.i++
	return c, nil
}

func (s *sliceStream) Close() error { return nil }

// SearchFake answers web lookups from canned hits, and applies the host's own
// filtering rules to them: HTTPS only, and AllowedDomains honoured including
// subdomains. A plugin that forgets to allowlist a domain fails here, not in
// production.
type SearchFake struct {
	mu    sync.Mutex
	hits  map[string][]hostsearch.Hit
	def   []hostsearch.Hit
	fn    SearchFunc
	err   error
	calls []hostsearch.Request
	gate  func(mutating bool) error
}

// SearchFunc answers a lookup. Use it when the plugin composes its queries at runtime,
// so no fixed query string would match. Its hits are still filtered by the request's
// allowlist, exactly as programmed ones are.
type SearchFunc func(req hostsearch.Request) ([]hostsearch.Hit, error)

// Answer installs a function to answer every query. It takes precedence over hits
// programmed for an exact query and over the default.
func (f *SearchFake) Answer(fn SearchFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fn = fn
}

// Results programs the hits returned for an exact query string.
func (f *SearchFake) Results(query string, hits ...hostsearch.Hit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits[query] = hits
}

// Default programs the hits returned for any query with no exact match.
func (f *SearchFake) Default(hits ...hostsearch.Hit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.def = hits
}

// Fail makes every subsequent query return err.
func (f *SearchFake) Fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Queries returns every lookup the plugin made, in order.
func (f *SearchFake) Queries() []hostsearch.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hostsearch.Request(nil), f.calls...)
}

func (f *SearchFake) Query(ctx context.Context, req hostsearch.Request) ([]hostsearch.Hit, error) {
	if err := f.gate(true); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, hostsearch.ErrInvalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	var hits []hostsearch.Hit
	switch {
	case f.fn != nil:
		answered, err := f.fn(req)
		if err != nil {
			return nil, err
		}
		hits = answered
	default:
		var ok bool
		if hits, ok = f.hits[req.Query]; !ok {
			hits = f.def
		}
	}
	out := make([]hostsearch.Hit, 0, len(hits))
	for _, h := range hits {
		if hostAllowed(h.URL, req.AllowedDomains) {
			out = append(out, h)
		}
	}
	if req.MaxResults > 0 && len(out) > req.MaxResults {
		out = out[:req.MaxResults]
	}
	return out, nil
}

var _ hostsearch.Search = (*SearchFake)(nil)

// BrowserFake serves pages from a routing table the test fills in. Every navigation,
// fetch, and subscription is checked against the session allowlist first, so the
// allowlist mistakes the real host rejects are rejected here too.
type BrowserFake struct {
	mu       sync.Mutex
	pages    map[string]string               // URL -> HTML
	resource map[string]hostbrowser.Resource // URL -> non-HTML fetch
	frames   map[string][][]byte             // wss URL -> frames to deliver
	handlers map[string]http.Handler         // DNS name -> handler serving the whole site
	creds    map[string]string               // credential ID -> secret the host injects
	visited  []string
	posts    []Post
	gate     func(mutating bool) error
	clock    *Clock
}

// Handle serves every request for a DNS name through an http.Handler, instead of
// through individually programmed URLs. Use it when the plugin talks to an API rather
// than reading pages: a ServeMux here is the portal, with its routing, status codes,
// and header checks intact.
func (f *BrowserFake) Handle(dnsName string, h http.Handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[strings.ToLower(dnsName)] = h
}

// Credential registers a secret the host will inject on the plugin's behalf. The
// plugin names the credential and never sees the value, so a test can prove the secret
// reached the far end without the plugin ever holding it.
func (f *BrowserFake) Credential(id, secret string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds[id] = secret
}

// serve dispatches a request through a registered site handler, if one covers the
// URL's host.
func (f *BrowserFake) serve(req *http.Request) (hostbrowser.Resource, bool) {
	f.mu.Lock()
	h, ok := f.handlers[strings.ToLower(req.URL.Hostname())]
	if ok {
		f.visited = append(f.visited, req.URL.String())
	}
	f.mu.Unlock()
	if !ok {
		return hostbrowser.Resource{}, false
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return hostbrowser.Resource{
		URL:    req.URL.String(),
		MIME:   res.Header.Get("Content-Type"),
		Body:   body,
		Status: res.StatusCode,
	}, true
}

// injectCredential reproduces the host's rule: the secret is merged into the top-level
// JSON object of the request body, and the plugin never receives it.
func (f *BrowserFake) injectCredential(body []byte, c *hostbrowser.JSONCredential) ([]byte, error) {
	if c == nil {
		return body, nil
	}
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Field) == "" {
		return nil, fmt.Errorf("%w: invalid credential injection", hostbrowser.ErrEngine)
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, fmt.Errorf("%w: credential body must be a JSON object", hostbrowser.ErrEngine)
	}
	f.mu.Lock()
	secret, ok := f.creds[c.ID]
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: no credential %q registered", hostbrowser.ErrEngine, c.ID)
	}
	object[c.Field] = secret
	return json.Marshal(object)
}

// Post is one recorded form submission.
type Post struct {
	URL  string
	Form url.Values
}

// Page programs the HTML served for a URL.
func (f *BrowserFake) Page(rawURL, html string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages[rawURL] = html
}

// Resource programs a non-HTML fetch: an image, a JSON API response, a CSV export.
func (f *BrowserFake) Resource(rawURL, mime string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resource[rawURL] = hostbrowser.Resource{URL: rawURL, MIME: mime, Body: body, Status: 200}
}

// Frames programs the text frames a websocket subscription will receive before the
// feed closes.
func (f *BrowserFake) Frames(wssURL string, frames ...[]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames[wssURL] = frames
}

// Visited returns every URL the plugin navigated to or fetched, in order.
func (f *BrowserFake) Visited() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.visited...)
}

// Posts returns every form submission the plugin made, in order.
func (f *BrowserFake) Posts() []Post {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Post(nil), f.posts...)
}

func (f *BrowserFake) Open(ctx context.Context, opts hostbrowser.OpenOptions) (hostbrowser.Session, error) {
	if err := f.gate(true); err != nil {
		return nil, err
	}
	if len(opts.AllowedHosts) == 0 {
		return nil, hostbrowser.ErrInvalidAllowlist
	}
	return &fakeSession{owner: f, allowed: opts.AllowedHosts}, nil
}

func (f *BrowserFake) Do(ctx context.Context, opts hostbrowser.OpenOptions, req hostbrowser.Request) (hostbrowser.Resource, error) {
	if err := f.gate(true); err != nil {
		return hostbrowser.Resource{}, err
	}
	if len(opts.AllowedHosts) == 0 {
		return hostbrowser.Resource{}, hostbrowser.ErrInvalidAllowlist
	}
	if !hostAllowed(req.URL, opts.AllowedHosts) {
		return hostbrowser.Resource{}, hostbrowser.ErrDenied
	}
	// The host injects the secret; the plugin supplied only a credential name.
	body, err := f.injectCredential(req.Body, req.Credential)
	if err != nil {
		return hostbrowser.Resource{}, err
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	hreq := httptest.NewRequest(method, req.URL, bytes.NewReader(body))
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}
	if res, ok := f.serve(hreq); ok {
		return res, nil
	}
	return f.fetch(req.URL)
}

func (f *BrowserFake) fetch(rawURL string) (hostbrowser.Resource, error) {
	// A registered site handler wins over individually programmed URLs, so a test can
	// mount a whole API and still override one path if it needs to.
	if res, ok := f.serve(httptest.NewRequest(http.MethodGet, rawURL, nil)); ok {
		return res, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visited = append(f.visited, rawURL)
	if res, ok := f.resource[rawURL]; ok {
		return res, nil
	}
	if html, ok := f.pages[rawURL]; ok {
		return hostbrowser.Resource{URL: rawURL, MIME: "text/html", Body: []byte(html), Status: 200}, nil
	}
	return hostbrowser.Resource{URL: rawURL, Status: 404}, nil
}

var _ hostbrowser.Browser = (*BrowserFake)(nil)

type fakeSession struct {
	owner   *BrowserFake
	allowed []string
	closed  bool
}

func (s *fakeSession) NewPage(ctx context.Context) (hostbrowser.Page, error) {
	if s.closed {
		return nil, hostbrowser.ErrEngine
	}
	if err := s.owner.gate(true); err != nil {
		return nil, err
	}
	return &fakePage{session: s}, nil
}

func (s *fakeSession) Subscribe(ctx context.Context, rawURL string, opts hostbrowser.SubscribeOptions) (hostbrowser.Subscription, error) {
	if err := s.owner.gate(true); err != nil {
		return nil, err
	}
	if !hostAllowed(rawURL, s.allowed) {
		return nil, hostbrowser.ErrDenied
	}
	s.owner.mu.Lock()
	frames := s.owner.frames[rawURL]
	now := s.owner.clock.Now()
	s.owner.mu.Unlock()

	// Every programmed frame is buffered before the channel is handed over, so a
	// reader sees the whole feed and then a closed channel. No goroutine, no timing.
	ch := make(chan hostbrowser.Frame, len(frames))
	for _, data := range frames {
		ch <- hostbrowser.Frame{At: now, Data: data}
	}
	close(ch)
	return &fakeSubscription{frames: ch}, nil
}

func (s *fakeSession) Close(ctx context.Context) error {
	s.closed = true
	return nil
}

type fakeSubscription struct {
	frames chan hostbrowser.Frame
	err    error
}

func (s *fakeSubscription) Frames() <-chan hostbrowser.Frame { return s.frames }
func (s *fakeSubscription) Err() error                       { return s.err }
func (s *fakeSubscription) Close(ctx context.Context) error  { return nil }

type fakePage struct {
	session *fakeSession
	url     string
	html    string
}

func (p *fakePage) check(rawURL string) error {
	if err := p.session.owner.gate(true); err != nil {
		return err
	}
	if !hostAllowed(rawURL, p.session.allowed) {
		return hostbrowser.ErrDenied
	}
	return nil
}

func (p *fakePage) Goto(ctx context.Context, rawURL string) error {
	if err := p.check(rawURL); err != nil {
		return err
	}
	res, err := p.session.owner.fetch(rawURL)
	if err != nil {
		return err
	}
	// Navigating somewhere the test never programmed is a mistake in the test, not a
	// blank page: the real engine fails to resolve or connect. Reporting ErrEngine
	// says so, instead of handing the plugin empty content to misinterpret.
	if res.Status == http.StatusNotFound {
		return fmt.Errorf("%w: no page programmed for %s", hostbrowser.ErrEngine, rawURL)
	}
	p.url, p.html = rawURL, string(res.Body)
	return nil
}

func (p *fakePage) WaitFor(ctx context.Context, selector string, d time.Duration) error {
	// The double has no renderer and no timing: content is whatever Goto loaded. A
	// selector that is present succeeds immediately, and one that is absent fails
	// immediately rather than after d.
	if strings.Contains(p.html, strings.Trim(selector, ".#[]")) {
		return nil
	}
	return fmt.Errorf("hosttest: selector %q not found on %s", selector, p.url)
}

func (p *fakePage) Content(ctx context.Context) (string, error) { return p.html, nil }

func (p *fakePage) Get(ctx context.Context, rawURL string) (hostbrowser.Resource, error) {
	if err := p.check(rawURL); err != nil {
		return hostbrowser.Resource{}, err
	}
	return p.session.owner.fetch(rawURL)
}

func (p *fakePage) Post(ctx context.Context, rawURL string, form url.Values) (hostbrowser.Resource, error) {
	if err := p.check(rawURL); err != nil {
		return hostbrowser.Resource{}, err
	}
	p.session.owner.mu.Lock()
	p.session.owner.posts = append(p.session.owner.posts, Post{URL: rawURL, Form: form})
	p.session.owner.mu.Unlock()

	// A registered site handler must see this as the POST it is, with the form body
	// the page submitted. Serving it as a GET would make a handler that routes on
	// method answer 405 to a request the real engine would have delivered correctly.
	req := httptest.NewRequest(http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if res, ok := p.session.owner.serve(req); ok {
		return res, nil
	}
	return p.session.owner.fetch(rawURL)
}

func (p *fakePage) Responses(ctx context.Context) ([]hostbrowser.Resource, error) {
	return nil, nil
}

func (p *fakePage) Fill(ctx context.Context, selector, value string) error { return nil }
func (p *fakePage) Click(ctx context.Context, selector string) error       { return nil }

func (p *fakePage) FillCredential(ctx context.Context, selector, credentialID string) error {
	return nil
}

func (p *fakePage) Close(ctx context.Context) error { return nil }

// hostAllowed applies the host's allowlist rule: HTTPS (or wss) only, and the URL's
// host must equal an allowed name or be a subdomain of one. An empty list allows any
// public HTTPS host, which is what Search does; Browser rejects an empty list before
// reaching here.
func hostAllowed(rawURL string, allowed []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "https" && u.Scheme != "wss" {
		return false
	}
	name := strings.ToLower(u.Hostname())
	if name == "" {
		return false
	}
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		a = strings.ToLower(a)
		if name == a || strings.HasSuffix(name, "."+a) {
			return true
		}
	}
	return false
}
