package web

import (
	"errors"
	"net/http"
	"sync"
	"time"
)

// minPasswordLen is the only password rule. There is one administrator, no reset flow, and
// no password expiry, so complexity theatre buys nothing.
const minPasswordLen = 12

type authStatus struct {
	BootstrapRequired bool `json:"bootstrapRequired"`
	// BootstrapAvailable is true only when this request also arrives from loopback.
	BootstrapAvailable bool `json:"bootstrapAvailable"`
	Authenticated      bool `json:"authenticated"`
}

// handleAuthStatus tells the shell whether to render first-run setup, the login form, or
// the application. It is the one unauthenticated read.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	exists, err := s.auth.adminExists(r.Context())
	if err != nil {
		s.fail(w, "auth status", err)
		return
	}
	st := authStatus{
		BootstrapRequired:  !exists,
		BootstrapAvailable: !exists && remoteIsLoopback(r),
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, err := s.auth.lookup(r.Context(), c.Value, s.deps.Config.Session.Idle); err == nil {
			st.Authenticated = true
		}
	}
	writeJSON(w, http.StatusOK, st)
}

type bootstrapAdminRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// handleBootstrapAdmin completes first-run setup. It is accepted only from a loopback peer
// and only while no administrator exists, and it consumes the one-time token.
func (s *Server) handleBootstrapAdmin(w http.ResponseWriter, r *http.Request) {
	if !remoteIsLoopback(r) {
		writeError(w, http.StatusForbidden, CodeForbidden, "first-run bootstrap is available on loopback only")
		return
	}
	if !s.originAllowed(r) {
		writeError(w, http.StatusForbidden, CodeForbidden, "origin not allowed")
		return
	}
	if !s.limit.allow("bootstrap") {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many attempts")
		return
	}

	var req bootstrapAdminRequest
	if !decodeJSON(w, r, maxAuthBody, &req) {
		return
	}
	if len(req.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"password must be at least 12 characters")
		return
	}

	switch err := s.auth.bootstrap(r.Context(), req.Token, req.Password); {
	case err == nil:
	case errors.Is(err, errBadToken):
		writeError(w, http.StatusForbidden, CodeForbidden, "invalid or already used bootstrap token")
		return
	default:
		s.fail(w, "bootstrap", err)
		return
	}

	s.limit.reset("bootstrap")
	s.startSession(w, r)
}

type loginRequest struct {
	Password string `json:"password"`
}

// handleLogin authenticates the administrator and rotates the session.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		writeError(w, http.StatusForbidden, CodeForbidden, "origin not allowed")
		return
	}
	if !s.limit.allow("login") {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many attempts")
		return
	}

	var req loginRequest
	if !decodeJSON(w, r, maxAuthBody, &req) {
		return
	}

	switch err := s.auth.checkPassword(r.Context(), req.Password); {
	case err == nil:
	case errors.Is(err, errBadCredentials), errors.Is(err, errNoAdmin):
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid credentials")
		return
	default:
		s.fail(w, "login", err)
		return
	}

	// Authentication rotates the session: discard whatever the client presented.
	if c, err := r.Cookie(sessionCookie); err == nil {
		if old, err := s.auth.lookup(r.Context(), c.Value, s.deps.Config.Session.Idle); err == nil {
			_ = s.auth.delete(r.Context(), old.ID)
		}
	}
	s.limit.reset("login")
	s.startSession(w, r)
}

type sessionResponse struct {
	CSRFToken string    `json:"csrfToken"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	cookie, csrf, err := s.auth.create(r.Context(), s.deps.Config.Session.Absolute)
	if err != nil {
		s.fail(w, "create session", err)
		return
	}
	setSessionCookie(w, cookie, s.deps.Config.Session.Absolute)
	writeJSON(w, http.StatusOK, sessionResponse{
		CSRFToken: csrf,
		ExpiresAt: s.now().UTC().Add(s.deps.Config.Session.Absolute),
	})
}

// handleLogout invalidates the session server side and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if err := s.auth.delete(r.Context(), sess.ID); err != nil {
		s.fail(w, "logout", err)
		return
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleReauth records a fresh password confirmation. Credential changes require one
// within the configured window.
func (s *Server) handleReauth(w http.ResponseWriter, r *http.Request) {
	if !s.limit.allow("reauth") {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many attempts")
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, maxAuthBody, &req) {
		return
	}
	if err := s.auth.checkPassword(r.Context(), req.Password); err != nil {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid credentials")
		return
	}
	sess := sessionFrom(r.Context())
	if err := s.auth.markReauth(r.Context(), sess.ID); err != nil {
		s.fail(w, "reauth", err)
		return
	}
	s.limit.reset("reauth")
	writeJSON(w, http.StatusOK, map[string]any{
		"reauthenticatedUntil": s.now().UTC().Add(s.deps.Config.Session.ReauthWindow),
	})
}

func setSessionCookie(w http.ResponseWriter, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/", // __Host- requires Path=/ and no Domain.
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// attemptLimiter is a small fixed-window limiter for the unauthenticated auth endpoints.
// There is one administrator, so a per-endpoint counter is enough.
type attemptLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	state  map[string]*limiterEntry
}

type limiterEntry struct {
	count int
	reset time.Time
}

func newAttemptLimiter(max int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{max: max, window: window, state: map[string]*limiterEntry{}}
}

func (l *attemptLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e, ok := l.state[key]
	if !ok || now.After(e.reset) {
		e = &limiterEntry{reset: now.Add(l.window)}
		l.state[key] = e
	}
	e.count++
	return e.count <= l.max
}

func (l *attemptLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.state, key)
}
