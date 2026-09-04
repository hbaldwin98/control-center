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
	if !s.limit.allow("bootstrap", peerIP(r), remoteIsLoopback(r)) {
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

	s.limit.reset("bootstrap", peerIP(r))
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
	if !s.limit.allow("login", peerIP(r), remoteIsLoopback(r)) {
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
	s.limit.reset("login", peerIP(r))
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
	if !s.limit.allow("reauth", peerIP(r), remoteIsLoopback(r)) {
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
	s.limit.reset("reauth", peerIP(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"reauthenticatedUntil": s.now().UTC().Add(s.deps.Config.Session.ReauthWindow),
	})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// handleChangePassword replaces the administrator password. It takes the current password
// in the same request rather than leaning on the reauth window, so the change is always
// bound to a fresh proof of knowledge. Every other session is signed out.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !s.limit.allow("password", peerIP(r), remoteIsLoopback(r)) {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many attempts")
		return
	}

	var req changePasswordRequest
	if !decodeJSON(w, r, maxAuthBody, &req) {
		return
	}
	if len(req.NewPassword) < minPasswordLen {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"password must be at least 12 characters")
		return
	}
	if req.NewPassword == req.CurrentPassword {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"new password must differ from the current one")
		return
	}

	switch err := s.auth.checkPassword(r.Context(), req.CurrentPassword); {
	case err == nil:
	case errors.Is(err, errBadCredentials), errors.Is(err, errNoAdmin):
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid credentials")
		return
	default:
		s.fail(w, "change password", err)
		return
	}

	if err := s.auth.setPassword(r.Context(), req.NewPassword); err != nil {
		s.fail(w, "change password", err)
		return
	}
	s.limit.reset("password", peerIP(r))

	// A password change also revokes every other device's session.
	sess := sessionFrom(r.Context())
	if err := s.auth.deleteOthers(r.Context(), sess.ID); err != nil {
		s.fail(w, "change password", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// attemptLimiter is a fixed-window limiter for the unauthenticated auth endpoints.
//
// It counts per endpoint *and per peer*, which is the whole point: a counter keyed on the
// endpoint alone is a lockout weapon once the listener is reachable from anywhere. Ten
// requests a minute from a stranger would pin the shared counter above the ceiling
// forever and the administrator could never log in again.
//
// A per-peer counter alone is not enough either, because a flood from many addresses
// still costs a password hash each, so there is a second, looser ceiling on the endpoint
// as a whole. Loopback is exempt from that one only: a peer on this machine is the
// operator at the console, and no flood from the outside should be able to shut them out.
// It still gets its own per-peer counter, so local brute force is bounded like any other.
type attemptLimiter struct {
	mu      sync.Mutex
	perPeer int
	global  int
	window  time.Duration
	now     func() time.Time

	peers  map[string]*limiterEntry // endpoint + peer
	totals map[string]*limiterEntry // endpoint
}

type limiterEntry struct {
	count int
	reset time.Time
}

func newAttemptLimiter(perPeer, global int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{
		perPeer: perPeer,
		global:  global,
		window:  window,
		now:     time.Now,
		peers:   map[string]*limiterEntry{},
		totals:  map[string]*limiterEntry{},
	}
}

// allow records an attempt on key from peer and reports whether it may proceed.
//
// A peer already being tracked is charged against its own counter and nothing else, so a
// single address hammering the endpoint can never drain the shared budget and lock out
// anybody else -- which is the failure this limiter exists to prevent, and which keying
// the shared ceiling on requests would have quietly reintroduced.
//
// The shared ceiling is therefore charged only when admitting a peer that is not yet
// tracked, because that is what actually costs something: a new entry in the map and a
// fresh budget of password hashes. It bounds distinct attacking sources per window, and
// with them the size of the map, which is why no eviction policy is needed beyond the
// sweep below.
func (l *attemptLimiter) allow(key, peer string, loopback bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	pk := key + " " + peer

	if e, ok := l.peers[pk]; ok && !now.After(e.reset) {
		e.count++
		return e.count <= l.perPeer
	}
	if !loopback && !l.charge(l.totals, key, now, l.global) {
		return false
	}
	l.sweep(now)
	l.peers[pk] = &limiterEntry{count: 1, reset: now.Add(l.window)}
	return true
}

// charge increments one bucket and reports whether it is still under max. An expired
// window starts a fresh count.
func (l *attemptLimiter) charge(buckets map[string]*limiterEntry, key string, now time.Time, max int) bool {
	e, ok := buckets[key]
	if !ok || now.After(e.reset) {
		e = &limiterEntry{reset: now.Add(l.window)}
		buckets[key] = e
	}
	e.count++
	return e.count <= max
}

// sweep drops entries whose window has closed. It runs only when a new peer is admitted,
// which the shared ceiling already rate limits, so the cost is bounded and amortized.
func (l *attemptLimiter) sweep(now time.Time) {
	if len(l.peers) < 4*l.global {
		return
	}
	for k, e := range l.peers {
		if now.After(e.reset) {
			delete(l.peers, k)
		}
	}
	for k, e := range l.totals {
		if now.After(e.reset) {
			delete(l.totals, k)
		}
	}
}

// reset clears a peer's counter after it proves it is the administrator. The entry stays
// so the peer is still a known one, and the shared ceiling is deliberately left alone:
// one success does not mean the flood behind it stopped, and clearing it would hand an
// attacker a way to keep the gate open.
func (l *attemptLimiter) reset(key, peer string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.peers[key+" "+peer]; ok {
		e.count = 0
	}
}
