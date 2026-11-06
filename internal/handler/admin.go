package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/httputil"
	"go-llm-proxy/internal/ratelimit"
	"go-llm-proxy/internal/usage"
)

const adminCookieName = "admin_auth"

// AdminHandler serves the /admin/* pages. It shares the usage-dashboard
// password for authentication and uses its own session store scoped to
// Path=/admin.
type AdminHandler struct {
	cs       *config.ConfigStore
	rl       *ratelimit.RateLimiter
	health   *config.HealthStore
	ul       *usage.UsageLogger // may be nil when metrics logging is off
	sessions *sessionStore

	// SetupEnabled reflects the CLI override for the optional setup generator.
	SetupEnabled bool
}

func NewAdminHandler(cs *config.ConfigStore, rl *ratelimit.RateLimiter, hs *config.HealthStore, ul *usage.UsageLogger) *AdminHandler {
	return &AdminHandler{cs: cs, rl: rl, health: hs, ul: ul, sessions: newSessionStore()}
}

// enabled reports whether the admin UI is switched on in the current config.
// Routes are always mounted; a 404 is served until a password exists, so the
// UI can be enabled by config reload without restarting.
func (h *AdminHandler) enabled() bool {
	return h.cs.Get().EffectiveAdminPassword() != ""
}

// Root redirects /admin to the unified overview.
func (h *AdminHandler) Root(w http.ResponseWriter, r *http.Request) {
	if !h.enabled() {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/admin/usage", http.StatusSeeOther)
}

// LoginPage renders the login form (or redirects to /admin/usage when the
// caller already has a valid session cookie).
func (h *AdminHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	if !h.enabled() {
		http.NotFound(w, r)
		return
	}
	if h.authed(r) {
		http.Redirect(w, r, "/admin/usage", http.StatusSeeOther)
		return
	}
	h.renderLogin(w, "")
}

// HandleLogin verifies the submitted password and issues a session cookie.
func (h *AdminHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if !h.enabled() {
		http.NotFound(w, r)
		return
	}
	ip := ratelimit.ClientIP(h.rl, r)
	if !h.rl.Check(ip) {
		httputil.WriteError(w, http.StatusTooManyRequests, "too many attempts")
		return
	}
	if err := r.ParseForm(); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "bad request")
		return
	}
	password := r.FormValue("password")
	cfg := h.cs.Get()
	adminPassword := cfg.EffectiveAdminPassword()
	if adminPassword == "" || !constantTimeEqual(password, adminPassword) {
		h.rl.RecordFailure(ip)
		h.renderLogin(w, "Incorrect password")
		return
	}
	token := h.sessions.create()
	if token == "" {
		httputil.WriteError(w, http.StatusInternalServerError, "session creation failed")
		return
	}
	h.setCookie(w, r, token)
	http.Redirect(w, r, "/admin/usage", http.StatusSeeOther)
}

// HandleLogout revokes the caller's session and clears the cookie.
func (h *AdminHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminCookieName); err == nil {
		h.sessions.revoke(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsTLS(r, h.rl),
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *AdminHandler) authed(r *http.Request) bool {
	cookie, err := r.Cookie(adminCookieName)
	if err != nil {
		return false
	}
	return h.sessions.validate(cookie.Value)
}

func (h *AdminHandler) setCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsTLS(r, h.rl),
		MaxAge:   int(dashboardSessionTTL.Seconds()),
	})
}

// RequirePage wraps a handler that renders an HTML page. Unauthenticated
// requests are redirected to /admin/login.
func (h *AdminHandler) RequirePage(inner http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.enabled() {
			http.NotFound(w, r)
			return
		}
		if !h.authed(r) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		inner(w, r)
	}
}

// RequireAPI wraps a JSON endpoint. Unauthenticated requests get a 401 JSON
// error. For POST mutations it also verifies the Origin/Referer header
// matches the server's own host — defense in depth beside SameSite=Strict.
func (h *AdminHandler) RequireAPI(inner http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.enabled() {
			http.NotFound(w, r)
			return
		}
		if !h.authed(r) {
			httputil.WriteError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if r.Method == http.MethodPost && !originMatches(r) {
			httputil.WriteError(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		inner(w, r)
	}
}

// originMatches returns true when the request's Origin or Referer header's
// host matches the request Host. A request with neither header set (e.g.
// curl) is rejected.
func originMatches(r *http.Request) bool {
	raw := r.Header.Get("Origin")
	if raw == "" {
		raw = r.Header.Get("Referer")
	}
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Host == "" {
		return false
	}
	return u.Host == r.Host
}

// decodeJSONBody decodes a small JSON body into dst. Bodies larger than
// limit bytes are rejected to prevent memory exhaustion.
func decodeJSONBody(r *http.Request, dst any, limit int64) error {
	r.Body = http.MaxBytesReader(nil, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// Ensure only one JSON object in the body.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return err
	}
	return nil
}

// writeJSON emits a JSON response with security headers applied.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	httputil.SetSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// ─── HTML rendering ──────────────────────────────────────────────────────────

func (h *AdminHandler) renderLogin(w http.ResponseWriter, errMsg string) {
	renderWeb(w, "login.html", map[string]any{"Error": errMsg})
}

func (h *AdminHandler) renderShell(w http.ResponseWriter, activeTab, title, bodyHTML, scriptFile string) {
	renderWeb(w, "shell.html", shellData{
		Active: activeTab, Title: title, Body: trustedHTML(bodyHTML),
		Confirm: trustedHTML(adminConfirmModalHTML), Script: scriptFile,
		Setup: h.SetupEnabled || h.cs.Get().ServeConfigGenerator,
	})
}

// adminConfirmModalHTML is a shared confirmation dialog available on every
// admin tab via openConfirm()/closeConfirm() in adminClientJS.
var adminConfirmModalHTML = webAsset("templates/confirm.html")
