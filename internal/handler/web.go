package handler

import (
	"bytes"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go-llm-proxy/internal/httputil"
)

// Frontend sources remain ordinary HTML, CSS and JavaScript files. Embedding
// keeps deployments self-contained and independent of the working directory.
//
//go:embed web/templates/*.html web/static/*.css web/static/*.js
var webFiles embed.FS

func webAsset(name string) string {
	b, err := webFiles.ReadFile("web/" + name)
	if err != nil {
		panic(err) // A missing embedded asset is a programming error.
	}
	return string(b)
}

var consoleTemplates = template.Must(template.New("console").ParseFS(webFiles,
	"web/templates/login.html", "web/templates/shell.html"))

// Inline handlers and server-provided setup data still require unsafe-inline.
// All executable asset files are served locally; there are no CDN dependencies.
const cspWeb = "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

type trustedHTML = template.HTML // Only embedded templates, never request input.

type shellData struct {
	Active, Title string
	Body, Confirm template.HTML
	Script        string
	Setup         bool
}

func renderWeb(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := consoleTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("render console", "template", name, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "could not render page")
		return
	}
	httputil.SetSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", cspWeb)
	_, _ = buf.WriteTo(w)
}

// WebAssets exposes static assets only, never templates or directory listings.
func WebAssets() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		if strings.ContainsAny(name, "/\\") || (!strings.HasSuffix(name, ".css") && !strings.HasSuffix(name, ".js")) {
			http.NotFound(w, r)
			return
		}
		b, err := webFiles.ReadFile("web/static/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		if strings.HasSuffix(name, ".css") {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
	})
}

// MountUsage puts statistics behind the same authentication as every admin API.
func (h *AdminHandler) MountUsage(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/usage", h.RequirePage(h.UsagePage))
	mux.HandleFunc("GET /admin/usage/data", h.RequireAPI(h.UsageData))
	mux.HandleFunc("GET /usage", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/usage", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /usage/data", func(w http.ResponseWriter, r *http.Request) {
		target := "/admin/usage/data"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	})
}
