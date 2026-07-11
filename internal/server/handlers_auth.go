package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/elian/nixbox/internal/auth"
	"github.com/elian/nixbox/internal/store"
)

const (
	sessionCookieName = "nixbox-session"
	sessionTTL        = 7 * 24 * time.Hour
	// sessionTouchAfter throttles sliding-expiry writes: the deadline is
	// refreshed only once a session is at least this much older than a
	// fresh one, so busy pages don't rewrite the row on every request.
	sessionTouchAfter = time.Hour
)

type ctxKey int

const userKey ctxKey = 0

// username returns the logged-in user recorded by requireAuth, or ""
// when auth is disabled.
func username(r *http.Request) string {
	u, _ := r.Context().Value(userKey).(string)
	return u
}

// newSessionToken mints the browser-held secret and the hash stored in
// the database (a stolen database must not yield usable cookies).
func newSessionToken() (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// requireAuth gates every route except the public few behind a valid
// session. Auth off (s.authn == nil) means the whole surface is open —
// the dry-run dev default and the reverse-proxy escape hatch.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authn == nil || publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(sessionCookieName)
		if err != nil {
			s.denyUnauthenticated(w, r)
			return
		}
		now := s.now()
		sess, err := s.store.ValidSession(hashToken(c.Value), now)
		if errors.Is(err, store.ErrNotFound) {
			s.denyUnauthenticated(w, r)
			return
		}
		if err != nil {
			// Fail closed but loudly: a broken session store is a 500,
			// not a silent logout (and certainly not an open door).
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		// Sliding expiry; best-effort, a failed refresh never blocks the
		// request (the session is still valid until its old deadline).
		if sess.ExpiresAt.Sub(now) < sessionTTL-sessionTouchAfter {
			if err := s.store.TouchSession(sess.TokenHash, now.Add(sessionTTL)); err != nil {
				slog.Warn("refreshing session", "err", err)
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, sess.Username)))
	})
}

// publicPath lists what an unauthenticated browser may reach: the login
// page itself, the static assets it needs, and the language picker so it
// is readable in the user's language.
func publicPath(path string) bool {
	return path == "/login" || path == "/lang" || strings.HasPrefix(path, "/static/")
}

// denyUnauthenticated speaks the dialect of whoever asked: full page
// loads are redirected to the login form (remembering the destination),
// htmx swaps get an HX-Redirect so the whole page navigates instead of
// swapping a login form into a fragment, and EventSource gets a plain
// 401 — the spec stops reconnecting on it.
func (s *Server) denyUnauthenticated(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Header.Get("HX-Request") == "true":
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
	case strings.Contains(r.Header.Get("Accept"), "text/event-stream"):
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	default:
		// A POST from a page whose session died: the form data is lost
		// either way, so just go log in.
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// loginData feeds the standalone login template (no baseData: the
// sidebar's workload list has no business on an unauthenticated page).
type loginData struct {
	Title string
	Error string
	Next  string
}

// renderLogin draws the login form; msg is an already-translated error
// line ("" for none — callers translate so the catalog scanner sees the
// keys at the call sites).
func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, msg, next string) {
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	s.render(w, r, "login", "login-page", loginData{
		Title: s.t(r, "login.title"),
		Error: msg,
		Next:  next,
	})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if s.authn == nil || s.loggedIn(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderLogin(w, r, http.StatusOK, "", safeNext(r.URL.Query().Get("next")))
}

// loggedIn reports whether the request carries a live session (only used
// to bounce already-authenticated visitors off the login page, so store
// errors just read as "not logged in" here).
func (s *Server) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	_, err = s.store.ValidSession(hashToken(c.Value), s.now())
	return err == nil
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if s.authn == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	ip := clientIP(r)
	next := safeNext(r.FormValue("next"))
	if s.limiter.blocked(ip) {
		s.renderLogin(w, r, http.StatusTooManyRequests, s.t(r, "login.throttled"), next)
		return
	}

	user := r.FormValue("username")
	pass := r.FormValue("password")
	if user == "" || pass == "" {
		s.failLogin(w, r, ip, next, "empty credentials")
		return
	}
	if err := s.authn.Authenticate(user, pass); err != nil {
		if errors.Is(err, auth.ErrBadCredentials) {
			s.failLogin(w, r, ip, next, err.Error())
			return
		}
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.authz.Authorize(user); err != nil {
		if errors.Is(err, auth.ErrNotAuthorized) {
			slog.Warn("login denied", "user", user, "ip", ip, "err", err)
			s.renderLogin(w, r, http.StatusForbidden, s.t(r, "login.denied"), next)
			return
		}
		httpError(w, err, http.StatusInternalServerError)
		return
	}

	token, hash, err := newSessionToken()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	now := s.now()
	if err := s.store.CreateSession(hash, user, now.Add(sessionTTL)); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	s.limiter.clear(ip)
	// Opportunistic housekeeping; an error here must not fail a login
	// that already has its session row.
	if err := s.store.DeleteExpiredSessions(now); err != nil {
		slog.Warn("sweeping expired sessions", "err", err)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	slog.Info("login", "user", user, "ip", ip)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// failLogin is the shared bad-credentials path: count it against the
// client, apply the failure delay, answer 401 with the same message for
// every cause (unknown user, wrong password, empty fields).
func (s *Server) failLogin(w http.ResponseWriter, r *http.Request, ip, next, reason string) {
	s.limiter.fail(ip)
	slog.Warn("login failed", "user", r.FormValue("username"), "ip", ip, "reason", reason)
	if s.loginFailDelay > 0 {
		select {
		case <-time.After(s.loginFailDelay):
		case <-r.Context().Done():
		}
	}
	s.renderLogin(w, r, http.StatusUnauthorized, s.t(r, "login.failed"), next)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.authn == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if err := s.store.DeleteSession(hashToken(c.Value)); err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext keeps post-login redirects on this site: an absolute-path
// target is honored, anything else (external URLs, scheme-relative
// //host tricks, garbage) falls back to the dashboard. A leading "/"
// alone is not enough: browsers parse "\" as "/" (so "/\host" is
// scheme-relative //host) and strip tabs and newlines before parsing
// (so "/\t/host" collapses to //host too), hence the second-character
// and control-character checks.
func safeNext(next string) string {
	if strings.ContainsFunc(next, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "/"
	}
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") && !strings.HasPrefix(next, "/\\") {
		return next
	}
	return "/"
}

// clientIP is the rate-limiter key. RemoteAddr, not X-Forwarded-For:
// nixbox has no trusted-proxy notion yet, and an attacker-controlled
// header must not let them reset their own limiter bucket.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
