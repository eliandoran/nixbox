package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/elian/nixbox/internal/auth"
	"github.com/elian/nixbox/internal/config"
	"github.com/elian/nixbox/internal/store"
)

type fakeAuthn struct{ err error }

func (f fakeAuthn) Authenticate(username, password string) error { return f.err }

type fakeAuthz struct{ err error }

func (f fakeAuthz) Authorize(username string) error { return f.err }

// enableAuth switches an already-built test server to authenticated mode
// with scripted backends and no failure delay.
func enableAuth(t *testing.T, s *Server, authn auth.Authenticator, authz auth.Authorizer) {
	t.Helper()
	s.authn, s.authz = authn, authz
	s.loginFailDelay = 0
}

// login posts valid credentials and returns the session cookie.
func login(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	w := post(t, s, "/login", url.Values{"username": {"alice"}, "password": {"pw"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d (body: %s)", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("no session cookie on successful login")
	return nil
}

func getCookie(t *testing.T, s *Server, path string, c *http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func postCookie(t *testing.T, s *Server, path string, form url.Values, c *http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c != nil {
		req.AddCookie(c)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func TestNewWiresAuthBackends(t *testing.T) {
	// The default test server runs with auth off.
	if s := newTestServer(t); s.authn != nil || s.authz != nil {
		t.Error("auth backends wired without NIXBOX_AUTH=pam")
	}
	// AuthPAM wires the real PAM authenticator and group gate.
	s := newTestServerWith(t, func(c *config.Config) {
		c.Auth = config.AuthPAM
		c.AllowedGroups = []string{"wheel"}
	})
	if s.authn == nil || s.authz == nil {
		t.Fatal("AuthPAM should wire authn and authz")
	}
}

func TestAuthDisabledEverythingOpen(t *testing.T) {
	s := newTestServer(t)
	if w := get(t, s, "/"); w.Code != http.StatusOK {
		t.Errorf("GET / = %d, want 200", w.Code)
	}
	// No logout button when there is no session to end.
	if body := get(t, s, "/").Body.String(); strings.Contains(body, `action="/logout"`) {
		t.Error("logout form rendered with auth disabled")
	}
	// The login page has no purpose: bounce home.
	if w := get(t, s, "/login"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("GET /login = %d → %q, want 303 → /", w.Code, w.Header().Get("Location"))
	}
	if w := post(t, s, "/logout", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("POST /logout = %d → %q, want 303 → /", w.Code, w.Header().Get("Location"))
	}
	if w := post(t, s, "/login", url.Values{"username": {"x"}, "password": {"y"}}); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("POST /login = %d → %q, want 303 → /", w.Code, w.Header().Get("Location"))
	}
}

func TestRequireAuth(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, fakeAuthn{}, fakeAuthz{})

	// Browser page loads redirect to the login form, remembering where
	// the user was headed.
	w := get(t, s, "/")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login?next=%2F" {
		t.Errorf("GET / = %d → %q", w.Code, w.Header().Get("Location"))
	}
	w = get(t, s, "/workloads/new")
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || loc != "/login?next=%2Fworkloads%2Fnew" {
		t.Errorf("GET /workloads/new → %q", loc)
	}
	// Non-GET requests don't try to resume: just go log in.
	w = post(t, s, "/system/rebuild", nil)
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || loc != "/login" {
		t.Errorf("POST /system/rebuild = %d → %q", w.Code, loc)
	}
	// HTMX partial swaps can't use a 303 (it would swap the login page
	// into a fragment); HX-Redirect makes htmx do a full navigation.
	w = getCookie(t, s, "/partials/workloads", nil, map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusUnauthorized || w.Header().Get("HX-Redirect") != "/login" {
		t.Errorf("htmx request = %d, HX-Redirect %q", w.Code, w.Header().Get("HX-Redirect"))
	}
	// EventSource gets a plain 401 — the spec stops reconnecting on it.
	w = getCookie(t, s, "/events/metrics", nil, map[string]string{"Accept": "text/event-stream"})
	if w.Code != http.StatusUnauthorized || w.Header().Get("HX-Redirect") != "" {
		t.Errorf("sse request = %d", w.Code)
	}
	// Static assets stay public: the login page needs its stylesheet.
	if w := get(t, s, "/static/style.css"); w.Code != http.StatusOK {
		t.Errorf("GET /static/style.css = %d", w.Code)
	}
	// So does the language picker (usable from the login page).
	if w := post(t, s, "/lang", url.Values{"lang": {"en"}}); w.Code != http.StatusSeeOther {
		t.Errorf("POST /lang = %d", w.Code)
	}
	// The login form itself renders, carrying the next target.
	w = get(t, s, "/login?next=%2Fworkloads%2Fnew")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="username"`) ||
		!strings.Contains(w.Body.String(), `value="/workloads/new"`) {
		t.Errorf("GET /login = %d (body: %.200s)", w.Code, w.Body.String())
	}
}

func TestLoginLogoutFlow(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, fakeAuthn{}, fakeAuthz{})

	w := post(t, s, "/login", url.Values{
		"username": {"alice"}, "password": {"pw"}, "next": {"/workloads/new"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/workloads/new" {
		t.Fatalf("login = %d → %q", w.Code, w.Header().Get("Location"))
	}
	var c *http.Cookie
	for _, got := range w.Result().Cookies() {
		if got.Name == sessionCookieName {
			c = got
		}
	}
	if c == nil {
		t.Fatal("no session cookie")
	}
	if !c.HttpOnly || c.Path != "/" || c.SameSite != http.SameSiteLaxMode || c.MaxAge <= 0 {
		t.Errorf("cookie flags: %+v", c)
	}

	// The session row records who logged in, keyed by the token's hash
	// (never the token itself).
	sess, err := s.store.ValidSession(hashToken(c.Value), time.Now())
	if err != nil {
		t.Fatalf("session row: %v", err)
	}
	if sess.Username != "alice" {
		t.Errorf("session username = %q", sess.Username)
	}

	// Authenticated pages render, addressed to the user.
	w = getCookie(t, s, "/", c, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "alice") ||
		!strings.Contains(w.Body.String(), `action="/logout"`) {
		t.Errorf("authenticated GET / = %d", w.Code)
	}
	// Logged-in users have no business on the login page.
	if w := getCookie(t, s, "/login", c, nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("GET /login logged in = %d → %q", w.Code, w.Header().Get("Location"))
	}

	// Logout drops the row and expires the cookie.
	w = postCookie(t, s, "/logout", nil, c, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Errorf("logout = %d → %q", w.Code, w.Header().Get("Location"))
	}
	var cleared bool
	for _, got := range w.Result().Cookies() {
		if got.Name == sessionCookieName && got.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not clear the cookie")
	}
	if _, err := s.store.ValidSession(hashToken(c.Value), time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("session survives logout: %v", err)
	}
	if w := getCookie(t, s, "/", c, nil); w.Code != http.StatusSeeOther {
		t.Errorf("old cookie after logout = %d, want redirect", w.Code)
	}
	// Logging out again (stale cookie, double click) stays graceful.
	if w := postCookie(t, s, "/logout", nil, c, nil); w.Code != http.StatusSeeOther {
		t.Errorf("second logout = %d", w.Code)
	}
}

func TestLoginNextSanitized(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, fakeAuthn{}, fakeAuthz{})

	for next, want := range map[string]string{
		"/workloads/new?tab=1": "/workloads/new?tab=1",
		"":                     "/",
		"//evil.example":       "/",
		"https://evil.example": "/",
		"workloads":            "/",
	} {
		w := post(t, s, "/login", url.Values{
			"username": {"alice"}, "password": {"pw"}, "next": {next}})
		if loc := w.Header().Get("Location"); loc != want {
			t.Errorf("next=%q → %q, want %q", next, loc, want)
		}
	}
}

func TestLoginFailures(t *testing.T) {
	s := newTestServer(t)

	// Wrong password: 401, the translated message, no cookie.
	enableAuth(t, s, fakeAuthn{err: auth.ErrBadCredentials}, fakeAuthz{})
	w := post(t, s, "/login", url.Values{"username": {"alice"}, "password": {"nope"}})
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Wrong username or password") {
		t.Errorf("bad creds = %d (body: %.200s)", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Error("cookie set on failed login")
	}

	// PAM backend broken: a 500, not a policy message.
	enableAuth(t, s, fakeAuthn{err: errors.New("pam exploded")}, fakeAuthz{})
	if w := post(t, s, "/login", url.Values{"username": {"alice"}, "password": {"pw"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("backend failure = %d, want 500", w.Code)
	}

	// Valid password, but not in an allowed group.
	enableAuth(t, s, fakeAuthn{}, fakeAuthz{err: auth.ErrNotAuthorized})
	w = post(t, s, "/login", url.Values{"username": {"bob"}, "password": {"pw"}})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "not allowed to administer") {
		t.Errorf("unauthorized = %d (body: %.200s)", w.Code, w.Body.String())
	}

	// Group lookup broken: also a 500.
	enableAuth(t, s, fakeAuthn{}, fakeAuthz{err: errors.New("nss exploded")})
	if w := post(t, s, "/login", url.Values{"username": {"alice"}, "password": {"pw"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("authz failure = %d, want 500", w.Code)
	}

	// Empty credentials never reach the backend (the scripted backend
	// would accept them — the guard must reject first).
	enableAuth(t, s, fakeAuthn{}, fakeAuthz{})
	if w := post(t, s, "/login", url.Values{"username": {""}, "password": {"pw"}}); w.Code != http.StatusUnauthorized {
		t.Errorf("empty username = %d, want 401", w.Code)
	}
	if w := post(t, s, "/login", url.Values{"username": {"alice"}, "password": {""}}); w.Code != http.StatusUnauthorized {
		t.Errorf("empty password = %d, want 401", w.Code)
	}
}

func TestLoginThrottled(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, fakeAuthn{err: auth.ErrBadCredentials}, fakeAuthz{})
	now := time.Unix(1_700_000_000, 0)
	s.limiter = newLoginLimiter(2, time.Minute)
	s.limiter.now = func() time.Time { return now }

	for range 2 {
		if w := post(t, s, "/login", url.Values{"username": {"alice"}, "password": {"x"}}); w.Code != http.StatusUnauthorized {
			t.Fatalf("failed login = %d", w.Code)
		}
	}
	// Once blocked, even the right password is refused — the check runs
	// before the backend.
	enableAuth(t, s, fakeAuthn{}, fakeAuthz{})
	w := post(t, s, "/login", url.Values{"username": {"alice"}, "password": {"pw"}})
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "Too many attempts") {
		t.Errorf("throttled = %d (body: %.200s)", w.Code, w.Body.String())
	}
	// The window slides: waiting it out unblocks.
	now = now.Add(2 * time.Minute)
	if w := post(t, s, "/login", url.Values{"username": {"alice"}, "password": {"pw"}}); w.Code != http.StatusSeeOther {
		t.Errorf("after window = %d, want 303", w.Code)
	}
	// ...and the successful login cleared the failure history.
	if s.limiter.blocked("192.0.2.1") {
		t.Error("success should clear the counter")
	}
}

func TestSessionExpiryAndTouch(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, fakeAuthn{}, fakeAuthz{})
	base := time.Now()
	s.now = func() time.Time { return base }

	c := login(t, s)
	hash := hashToken(c.Value)

	// Two hours later the session is still good, and using it slid the
	// deadline forward (sliding expiry, refreshed at most hourly).
	base = base.Add(2 * time.Hour)
	if w := getCookie(t, s, "/", c, nil); w.Code != http.StatusOK {
		t.Fatalf("2h later = %d", w.Code)
	}
	sess, err := s.store.ValidSession(hash, base)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ExpiresAt.Before(base.Add(sessionTTL - time.Minute)) {
		t.Errorf("session not touched: expires %v, now %v", sess.ExpiresAt, base)
	}

	// The touch is best-effort: a read-only database still serves pages.
	denyWrites(t, s, "sessions")
	base = base.Add(2 * time.Hour)
	if w := getCookie(t, s, "/", c, nil); w.Code != http.StatusOK {
		t.Errorf("touch failure should not break the request: %d", w.Code)
	}

	// Past the deadline the cookie is dead and the browser is sent to
	// log in again.
	base = base.Add(sessionTTL)
	if w := getCookie(t, s, "/", c, nil); w.Code != http.StatusSeeOther {
		t.Errorf("expired session = %d, want redirect", w.Code)
	}
	// A cookie that matches nothing behaves the same.
	junk := &http.Cookie{Name: sessionCookieName, Value: "junk"}
	if w := getCookie(t, s, "/", junk, nil); w.Code != http.StatusSeeOther {
		t.Errorf("junk cookie = %d, want redirect", w.Code)
	}
}

func TestSessionStoreFaults(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, fakeAuthn{}, fakeAuthz{})
	c := login(t, s)

	// Logout that cannot delete its row reports failure instead of
	// pretending the session is gone.
	denyWrites(t, s, "sessions")
	if w := postCookie(t, s, "/logout", nil, c, nil); w.Code != http.StatusInternalServerError {
		t.Errorf("logout with denied deletes = %d, want 500", w.Code)
	}
	// New logins can't record a session: 500, not a silent pass.
	if w := post(t, s, "/login", url.Values{"username": {"alice"}, "password": {"pw"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("login with denied writes = %d, want 500", w.Code)
	}

	// A broken store fails closed — pages error rather than serving
	// unauthenticated.
	dropTable(t, s, "sessions")
	if w := getCookie(t, s, "/", c, nil); w.Code != http.StatusInternalServerError {
		t.Errorf("lookup with dropped table = %d, want 500", w.Code)
	}
}

func TestLoginFailureDelay(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, fakeAuthn{err: auth.ErrBadCredentials}, fakeAuthz{})

	// A tiny real delay exercises the sleep branch.
	s.loginFailDelay = time.Millisecond
	if w := post(t, s, "/login", url.Values{"username": {"a"}, "password": {"b"}}); w.Code != http.StatusUnauthorized {
		t.Errorf("delayed failure = %d", w.Code)
	}

	// A canceled request must skip the delay instead of parking a worker
	// for the full duration.
	s.loginFailDelay = time.Minute
	form := url.Values{"username": {"a"}, "password": {"b"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	start := time.Now()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req.WithContext(ctx))
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("canceled request still waited %v", elapsed)
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("canceled failure = %d", w.Code)
	}
}

func TestLoginExpiredSweepBestEffort(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, fakeAuthn{}, fakeAuthz{})
	// Only deletes fail: the session row itself is written fine, so the
	// opportunistic expired-session sweep failing must not fail the login.
	execSQL(t, s, `CREATE TRIGGER deny_del_sessions BEFORE DELETE ON sessions
		BEGIN SELECT RAISE(ABORT, 'denied'); END`)
	login(t, s)
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.0.2.7:4444"
	if got := clientIP(r); got != "192.0.2.7" {
		t.Errorf("clientIP = %q", got)
	}
	// No port (unix socket peers, exotic listeners): used verbatim.
	r.RemoteAddr = "peer-without-port"
	if got := clientIP(r); got != "peer-without-port" {
		t.Errorf("clientIP fallback = %q", got)
	}
}

func TestCrossOriginBlocked(t *testing.T) {
	// CSRF protection is active even with auth off (it also shields
	// no-auth reverse-proxy setups).
	s := newTestServer(t)

	// Same-origin and non-browser (headerless) requests pass.
	if w := post(t, s, "/lang", url.Values{"lang": {"en"}}); w.Code != http.StatusSeeOther {
		t.Errorf("headerless POST = %d", w.Code)
	}
	if w := postCookie(t, s, "/lang", url.Values{"lang": {"en"}}, nil,
		map[string]string{"Sec-Fetch-Site": "same-origin"}); w.Code != http.StatusSeeOther {
		t.Errorf("same-origin POST = %d", w.Code)
	}
	// Cross-site browser requests are refused.
	if w := postCookie(t, s, "/lang", url.Values{"lang": {"en"}}, nil,
		map[string]string{"Sec-Fetch-Site": "cross-site"}); w.Code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", w.Code)
	}
	// Older browsers without Sec-Fetch-Site still send Origin.
	if w := postCookie(t, s, "/lang", url.Values{"lang": {"en"}}, nil,
		map[string]string{"Origin": "http://evil.example"}); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d, want 403", w.Code)
	}
}
