package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookieName  = "curator_session"
	csrfCookieName     = "curator_csrf"
	maxLoginBody       = 4096
	loginWindow        = time.Minute
	loginMaxFailures   = 5
	rememberSessionTTL = 30 * 24 * time.Hour
)

// AuthOptions controls browser session behavior. SecureCookies should be true
// when HTTPS terminates in front of this service.
type AuthOptions struct {
	SecureCookies bool
	SessionTTL    time.Duration
	// PublicOrigin, when set, must match Origin on browser cookie write requests.
	// An empty value still requires a syntactically valid Origin and CSRF token.
	PublicOrigin string
}

type authSession struct {
	csrf      string
	expiresAt time.Time
}

type loginAttempt struct {
	window   time.Time
	failures int
}

// Auth implements management bearer authentication and short-lived browser
// sessions. A nil or empty configured token deliberately disables all access.
type Auth struct {
	token    []byte
	options  AuthOptions
	mu       sync.Mutex
	sessions map[[32]byte]authSession
	attempts map[string]loginAttempt
}

// NewAuth constructs the management authenticator. The caller should pass the
// token read from its private configuration file; token bytes are copied.
func NewAuth(token []byte, options AuthOptions) *Auth {
	if options.SessionTTL <= 0 {
		options.SessionTTL = 12 * time.Hour
	}
	return &Auth{
		token: append([]byte(nil), token...), options: options,
		sessions: make(map[[32]byte]authSession), attempts: make(map[string]loginAttempt),
	}
}

// Register adds the unauthenticated-token-validated login and authenticated
// logout routes to mux. Protect management API routes with Middleware.
func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/login", a.login)
	mux.Handle("POST /admin/logout", a.Middleware(http.HandlerFunc(a.logout)))
}

// ManagementEntry sends browser navigation to the login page or existing session.
// It serves no management data and never changes authentication requirements.
func (a *Auth) ManagementEntry(w http.ResponseWriter, r *http.Request) {
	destination := "/admin/login"
	if a != nil && len(a.token) > 0 {
		_, session := a.sessionFromRequest(r)
		if session || a.validBearer(r.Header.Get("Authorization")) {
			destination = "/admin/"
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, destination, http.StatusFound)
}

// Middleware requires a valid Bearer token or session for every wrapped route.
// Browser session writes additionally require Origin and double-submit CSRF.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a == nil || len(a.token) == 0 {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if a.validBearer(r.Header.Get("Authorization")) {
			next.ServeHTTP(w, r)
			return
		}
		session, ok := a.sessionFromRequest(r)
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if isWriteMethod(r.Method) && !a.validCSRF(w, r, session) {
			writeAuthError(w, http.StatusForbidden, "csrf_failed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	if len(a.token) == 0 {
		writeAuthError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	key := clientKey(r)
	if !a.reserveAttempt(key) {
		w.Header().Set("Retry-After", "60")
		writeAuthError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	var input struct {
		Token    string `json:"token"`
		Remember bool   `json:"remember"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil || input.Token == "" || dec.Decode(new(any)) != io.EOF {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeAuthError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE")
			return
		}
		writeAuthError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !a.tokenMatches([]byte(input.Token)) {
		writeAuthError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	a.clearFailures(key)
	sessionRaw, err := randomToken()
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	csrf, err := randomToken()
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	ttl := a.options.SessionTTL
	if input.Remember {
		ttl = rememberSessionTTL
	}
	digest := sha256.Sum256([]byte(sessionRaw))
	now := time.Now()
	a.mu.Lock()
	for k, v := range a.sessions {
		if !now.Before(v.expiresAt) {
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) >= 256 {
		a.mu.Unlock()
		writeAuthError(w, http.StatusTooManyRequests, "SESSION_LIMIT")
		return
	}
	a.sessions[digest] = authSession{csrf: csrf, expiresAt: now.Add(ttl)}
	a.mu.Unlock()
	cookieTTL := time.Duration(0)
	if input.Remember {
		cookieTTL = ttl
	}
	setAuthCookie(w, sessionCookieName, sessionRaw, a.options, cookieTTL, true)
	setAuthCookie(w, csrfCookieName, csrf, a.options, cookieTTL, false)
	writeJSON(w, http.StatusOK, map[string]string{"status": "authenticated"})
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		digest := sha256.Sum256([]byte(c.Value))
		a.mu.Lock()
		delete(a.sessions, digest)
		a.mu.Unlock()
	}
	clearAuthCookie(w, sessionCookieName, a.options, true)
	clearAuthCookie(w, csrfCookieName, a.options, false)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (a *Auth) validBearer(header string) bool {
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	return a.tokenMatches([]byte(strings.TrimPrefix(header, "Bearer ")))
}

func (a *Auth) tokenMatches(candidate []byte) bool {
	expected := sha256.Sum256(a.token)
	got := sha256.Sum256(candidate)
	return subtle.ConstantTimeCompare(expected[:], got[:]) == 1 && len(candidate) > 0 && len(a.token) > 0
}

func (a *Auth) sessionFromRequest(r *http.Request) (authSession, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return authSession{}, false
	}
	digest := sha256.Sum256([]byte(c.Value))
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[digest]
	if !ok {
		return authSession{}, false
	}
	if !now.Before(session.expiresAt) {
		delete(a.sessions, digest)
		return authSession{}, false
	}
	return session, true
}

func (a *Auth) validCSRF(w http.ResponseWriter, r *http.Request, session authSession) bool {
	origin := r.Header.Get("Origin")
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if a.options.PublicOrigin == "" && u.Host != r.Host {
		return false
	}
	if origin == "" || (a.options.PublicOrigin != "" && origin != a.options.PublicOrigin) {
		return false
	}
	csrfCookie, err := r.Cookie(csrfCookieName)
	if err != nil {
		return false
	}
	csrfHeader := r.Header.Get("X-CSRF-Token")
	if csrfHeader == "" || csrfCookie.Value == "" {
		return false
	}
	return constantStringEqual(csrfHeader, session.csrf) && constantStringEqual(csrfCookie.Value, session.csrf)
}

func constantStringEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1 && len(a) > 0 && len(b) > 0
}

func (a *Auth) reserveAttempt(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range a.attempts {
		if now.Sub(v.window) >= loginWindow {
			delete(a.attempts, k)
		}
	}
	entry, ok := a.attempts[key]
	if ok && entry.failures >= loginMaxFailures {
		return false
	}
	if !ok && len(a.attempts) >= 4096 {
		return false
	}
	if !ok || now.Sub(entry.window) >= loginWindow {
		entry = loginAttempt{window: now}
	}
	entry.failures++
	a.attempts[key] = entry
	return true
}

func (a *Auth) clearFailures(key string) {
	a.mu.Lock()
	delete(a.attempts, key)
	a.mu.Unlock()
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func setAuthCookie(w http.ResponseWriter, name, value string, options AuthOptions, ttl time.Duration, httpOnly bool) {
	cookie := &http.Cookie{Name: name, Value: value, Path: "/admin", HttpOnly: httpOnly,
		Secure: options.SecureCookies, SameSite: http.SameSiteStrictMode}
	if ttl > 0 {
		cookie.MaxAge = int(ttl.Seconds())
		cookie.Expires = time.Now().Add(ttl)
	}
	http.SetCookie(w, cookie)
}

func clearAuthCookie(w http.ResponseWriter, name string, options AuthOptions, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/admin", HttpOnly: httpOnly,
		Secure: options.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func isWriteMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func writeAuthError(w http.ResponseWriter, status int, code string) {
	writeAPIError(w, status, strings.ToUpper(code))
}
