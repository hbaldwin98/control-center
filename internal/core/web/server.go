package web

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/config"
	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	harnesscore "github.com/hbaldwin98/control-center/internal/core/harness"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/notifications"
	"github.com/hbaldwin98/control-center/internal/core/pluginhost"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/push"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// sessionCookie uses the __Host- prefix, which browsers accept only with Secure, Path=/,
// and no Domain. Loopback origins count as secure contexts, so this works over plain HTTP
// on localhost and requires TLS everywhere else.
const sessionCookie = "__Host-cc_session"

// csrfHeader carries the synchronizer token on every mutation.
const csrfHeader = "X-CSRF-Token"

const maxAuthBody = 8 << 10

// Deps are the lower-layer modules the web layer serves. Later milestones add fields here
// rather than letting handlers reach into globals.
type Deps struct {
	DB     storage.DB
	Config config.Config

	// Events is the log the SSE stream tails and the Events screen reads.
	Events *events.Log

	// Blobs serves stored objects through an authenticated application route.
	Blobs *storage.BlobStore

	// Policy owns the kill switch, budgets, and spend counters.
	Policy *policy.Store

	// Jobs is the durable queue. The Jobs screen lists, inspects, and cancels.
	Jobs *jobs.Queue

	// Harness supervises configured coding-agent processes and retains bounded output.
	Harness *harnesscore.Service

	// Push is live delivery. The transport is host-owned; what travels is the
	// publishing plugin's business.
	Push *push.Service

	// Credentials is secret-free administration. Runtime Token is never exposed here.
	Credentials *credentials.Store

	// AI is host-managed model routing and usage. Query returns no credentials.
	AI *ai.Service

	// Notifications turns committed events into inbox rows and channel deliveries.
	Notifications *notifications.Service

	// Plugins reports the registered plugins the shell reconciles against.
	Plugins func(context.Context) []PluginDescriptor

	// PluginHost owns plugin lifecycle, scoped facades, and /api/plugins/ routes.
	PluginHost *pluginhost.Registry

	// StaticDir holds the built frontend. When it is missing, the server serves a small
	// built-in placeholder so the API is still usable.
	StaticDir string

	// BootstrapPassword, when set and no administrator exists yet, completes first-run
	// setup without the loopback token. The container entrypoint uses this so Docker
	// Desktop (where the browser is not a loopback peer) can start in one command.
	BootstrapPassword string

	// Now is injectable for tests.
	Now func() time.Time
}

// Server is the HTTP front end.
type Server struct {
	deps  Deps
	auth  *authStore
	mux   *http.ServeMux
	now   func() time.Time
	limit *attemptLimiter

	allowedOrigins map[string]struct{}
}

// New wires the HTTP server, applies the web module's migrations, and issues a first-run
// bootstrap token when no administrator exists.
func New(ctx context.Context, m storage.Migrator, deps Deps) (*Server, error) {
	if err := m.Apply("web", migrations); err != nil {
		return nil, err
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}

	s := &Server{
		deps:           deps,
		auth:           &authStore{db: deps.DB, now: now},
		mux:            http.NewServeMux(),
		now:            now,
		limit:          newAttemptLimiter(10, time.Minute),
		allowedOrigins: allowedOrigins(deps.Config),
	}
	s.routes()

	if err := s.auth.purgeExpired(ctx); err != nil {
		return nil, err
	}
	if deps.BootstrapPassword != "" {
		created, err := s.auth.createAdminIfAbsent(ctx, deps.BootstrapPassword)
		if err != nil {
			return nil, err
		}
		if created {
			slog.Info("administrator created from CC_BOOTSTRAP_PASSWORD")
		}
		return s, nil
	}
	token, err := s.auth.ensureBootstrapToken(ctx)
	if err != nil {
		return nil, err
	}
	if token != "" {
		slog.Warn("first-run bootstrap required; POST this token with a new password to /api/auth/bootstrap from loopback",
			"token", token)
	}
	return s, nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("POST /api/auth/bootstrap", s.handleBootstrapAdmin)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.authenticated(s.handleLogout))
	s.mux.HandleFunc("POST /api/auth/reauth", s.authenticated(s.handleReauth))
	s.mux.HandleFunc("POST /api/auth/password", s.authenticated(s.handleChangePassword))

	s.mux.HandleFunc("GET /api/bootstrap", s.authenticated(s.handleShellBootstrap))
	s.mux.HandleFunc("GET /api/stream", s.authenticated(s.handleStream))
	s.mux.HandleFunc("GET /api/push/{plugin}", s.authenticated(s.handlePush))

	s.mux.HandleFunc("GET /api/events", s.authenticated(s.handleEventsQuery))
	s.mux.HandleFunc("GET /api/events/subscribers", s.authenticated(s.handleSubscribers))
	s.mux.HandleFunc("POST /api/events/subscribers/{name}/{action}", s.authenticated(s.handleSubscriberAction))

	s.mux.HandleFunc("GET /api/admin/plugins", s.authenticated(s.handlePluginList))
	s.mux.HandleFunc("POST /api/admin/plugins/{id}/enable", s.authenticated(s.handlePluginEnable))
	s.mux.HandleFunc("POST /api/admin/plugins/{id}/disable", s.authenticated(s.handlePluginDisable))
	s.mux.HandleFunc("PUT /api/admin/plugins/{id}/budget", s.authenticated(s.handlePluginBudget))
	s.mux.HandleFunc("GET /api/admin/plugins/{id}/config", s.authenticated(s.handlePluginConfigGet))
	s.mux.HandleFunc("PUT /api/admin/plugins/{id}/config", s.authenticated(s.handlePluginConfigPut))

	s.mux.Handle("/api/plugins/", s.authenticated(s.handlePluginAPI))

	s.mux.HandleFunc("GET /api/jobs", s.authenticated(s.handleJobList))
	s.mux.HandleFunc("GET /api/jobs/{id}", s.authenticated(s.handleJobGet))
	s.mux.HandleFunc("POST /api/jobs/{id}/cancel", s.authenticated(s.handleJobCancel))

	s.mux.HandleFunc("GET /api/harness", s.authenticated(s.handleHarnessList))
	s.mux.HandleFunc("POST /api/harness", s.authenticated(s.handleHarnessCreate))
	s.mux.HandleFunc("GET /api/harness/{id}", s.authenticated(s.handleHarnessGet))
	s.mux.HandleFunc("POST /api/harness/{id}/stop", s.authenticated(s.handleHarnessStop))

	s.mux.HandleFunc("GET /api/blobs/{scope}/{key...}", s.authenticated(s.handleBlob))
	s.mux.HandleFunc("HEAD /api/blobs/{scope}/{key...}", s.authenticated(s.handleBlob))

	s.mux.HandleFunc("GET /api/admin/credentials", s.authenticated(s.handleCredentialList))
	s.mux.HandleFunc("GET /api/admin/credentials/oauth/providers", s.authenticated(s.handleOAuthProviders))
	s.mux.HandleFunc("GET /api/admin/credentials/{id}/references", s.authenticated(s.handleCredentialReferences))
	s.mux.HandleFunc("POST /api/admin/credentials", s.requireReauth(s.handleCredentialCreate))
	s.mux.HandleFunc("PUT /api/admin/credentials/{id}", s.requireReauth(s.handleCredentialReplace))
	s.mux.HandleFunc("POST /api/admin/credentials/{id}/rotate", s.requireReauth(s.handleCredentialRotate))
	s.mux.HandleFunc("DELETE /api/admin/credentials/{id}", s.requireReauth(s.handleCredentialDelete))
	s.mux.HandleFunc("POST /api/admin/credentials/oauth/{provider}/begin", s.requireReauth(s.handleOAuthBegin))
	s.mux.HandleFunc("POST /api/admin/credentials/oauth/{provider}/import", s.requireReauth(s.handleOAuthImport))
	// Completing a flow is gated the same way whether the callback is served or pasted:
	// a session, and the one-time state that only the browser which began it holds.
	// Reauthentication is spent at the beginning, because the sign-in in between is a
	// trip through the provider's own login and can outlast the five-minute window.
	s.mux.HandleFunc("POST /api/admin/credentials/oauth/{provider}/manual", s.authenticated(s.withActor(s.handleOAuthManual)))
	s.mux.HandleFunc("GET /api/admin/credentials/oauth/callback", s.authenticated(s.withActor(s.handleOAuthCallback)))
	// The address a pinned provider redirects to. It completes the flow only when this
	// server is what answers there; otherwise it is an ordinary frontend path.
	s.mux.HandleFunc("GET /auth/callback", s.handlePinnedOAuthCallback)

	// Providers and routes are configuration, not secrets: they name a credential but
	// never carry one, so they need a session and CSRF rather than reauthentication.
	s.mux.HandleFunc("GET /api/admin/ai/providers", s.authenticated(s.handleAIProviderList))
	s.mux.HandleFunc("PUT /api/admin/ai/providers/{id}", s.authenticated(s.handleAIProviderPut))
	s.mux.HandleFunc("DELETE /api/admin/ai/providers/{id}", s.authenticated(s.handleAIProviderDelete))
	s.mux.HandleFunc("GET /api/admin/ai/providers/{id}/models", s.authenticated(s.handleAIModels))
	s.mux.HandleFunc("GET /api/admin/ai/routes", s.authenticated(s.handleAIRoutes))
	s.mux.HandleFunc("PUT /api/admin/ai/routes/{name}", s.authenticated(s.handleAIRoutePut))
	s.mux.HandleFunc("PUT /api/admin/ai/routes/{name}/assign", s.authenticated(s.handleAIAssign))
	s.mux.HandleFunc("DELETE /api/admin/ai/routes/{name}", s.authenticated(s.handleAIRouteDelete))
	s.mux.HandleFunc("GET /api/ai/calls", s.authenticated(s.handleAICalls))

	s.mux.HandleFunc("GET /api/notifications", s.authenticated(s.handleInboxList))
	s.mux.HandleFunc("GET /api/notifications/{id}", s.authenticated(s.handleInboxGet))
	s.mux.HandleFunc("POST /api/notifications/{id}/read", s.authenticated(s.handleInboxRead))

	s.mux.HandleFunc("GET /api/admin/notifications/rules", s.authenticated(s.handleNotifRules))
	s.mux.HandleFunc("PUT /api/admin/notifications/rules/{id}", s.authenticated(s.handleNotifRulePut))
	s.mux.HandleFunc("DELETE /api/admin/notifications/rules/{id}", s.authenticated(s.handleNotifRuleDelete))
	s.mux.HandleFunc("GET /api/admin/notifications/channels", s.authenticated(s.handleNotifChannels))
	s.mux.HandleFunc("PUT /api/admin/notifications/channels/{id}", s.authenticated(s.handleNotifChannelPut))
	s.mux.HandleFunc("DELETE /api/admin/notifications/channels/{id}", s.authenticated(s.handleNotifChannelDelete))
	s.mux.HandleFunc("GET /api/admin/notifications/health", s.authenticated(s.handleNotifHealth))
	s.mux.HandleFunc("GET /api/admin/notifications/catalog", s.authenticated(s.handleNotifCatalog))

	s.mux.HandleFunc("/", s.serveStatic)
}

// Handler returns the fully wrapped handler: security headers, then routing.
func (s *Server) Handler() http.Handler {
	return securityHeaders(s.mux)
}

// ListenAndServe runs the server until ctx is cancelled, then drains connections.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.deps.Config.Server.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: /api/stream is a long-lived SSE response.
	}

	errc := make(chan error, 1)
	go func() {
		if s.deps.Config.TLSEnabled() {
			srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			slog.Info("listening", "addr", srv.Addr, "tls", true)
			errc <- srv.ListenAndServeTLS(s.deps.Config.Server.TLSCert, s.deps.Config.Server.TLSKey)
			return
		}
		slog.Info("listening", "addr", srv.Addr, "tls", false, "loopback", true)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// ---- middleware ----

// securityHeaders applies defence-in-depth headers to every response. The CSP is strict
// because the shell ships no inline script and loads no third-party origin.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; "+
				"form-action 'self'; object-src 'none'")
		next.ServeHTTP(w, r)
	})
}

type ctxKey int

const sessionKey ctxKey = 1

// sessionFrom returns the authenticated session attached by the authenticated wrapper.
func sessionFrom(ctx context.Context) *session {
	s, _ := ctx.Value(sessionKey).(*session)
	return s
}

// authenticated requires a valid session, and on mutations also a valid Origin and a
// synchronizer CSRF token bound to that session.
func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
			return
		}
		sess, err := s.auth.lookup(r.Context(), cookie.Value, s.deps.Config.Session.Idle)
		if err != nil {
			clearSessionCookie(w)
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
			return
		}
		if isMutation(r.Method) {
			if !s.originAllowed(r) {
				writeError(w, http.StatusForbidden, CodeForbidden, "origin not allowed")
				return
			}
			if !s.auth.checkCSRF(r.Context(), sess.ID, r.Header.Get(csrfHeader)) {
				writeError(w, http.StatusForbidden, CodeCSRF, "missing or invalid CSRF token")
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	}
}

// withActor stamps the single administrator principal for credential mutations.
func (s *Server) withActor(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, r.WithContext(credentials.WithActor(r.Context(), actorAdmin)))
	}
}

// requireReauth is authenticated plus a fresh password reauthentication. Credential
// changes go through here so a stolen session cookie is not enough.
func (s *Server) requireReauth(next http.HandlerFunc) http.HandlerFunc {
	return s.authenticated(s.withActor(func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		if sess == nil || !sess.reauthFresh(s.now().UTC(), s.deps.Config.Session.ReauthWindow) {
			writeError(w, http.StatusForbidden, CodeReauthRequired, "password reauthentication required")
			return
		}
		next(w, r)
	}))
}

func isMutation(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// originAllowed accepts a request whose Origin matches the host it was sent to, or one of
// the explicitly configured origins. A missing Origin on a same-origin non-CORS request is
// rejected, because every mutation the shell sends includes one.
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if _, ok := s.allowedOrigins[strings.ToLower(origin)]; ok {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func allowedOrigins(cfg config.Config) map[string]struct{} {
	out := make(map[string]struct{})
	add := func(o string) {
		if o != "" {
			out[strings.ToLower(o)] = struct{}{}
		}
	}
	scheme := "http"
	if cfg.TLSEnabled() {
		scheme = "https"
	}
	if host, port, err := net.SplitHostPort(cfg.Server.Addr); err == nil {
		add(scheme + "://" + net.JoinHostPort(host, port))
		if isLoopback(host) {
			// The same listener answers to every loopback spelling.
			for _, alias := range []string{"localhost", "127.0.0.1", "[::1]"} {
				add(scheme + "://" + alias + ":" + port)
			}
		}
	}
	for _, o := range cfg.Server.Origins {
		add(o)
	}
	return out
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// remoteIsLoopback reports whether the peer is on this machine. First-run bootstrap is
// offered only to such peers.
func remoteIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
