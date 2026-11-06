package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"go-llm-proxy/internal/usage"
)

func TestUnifiedUsageSession(t *testing.T) {
	h, _, _ := newAdminTestHandler(t)
	ul, err := usage.NewUsageLogger(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ul.Close() })
	h.ul = ul
	mux := http.NewServeMux()
	h.MountUsage(mux)
	mux.HandleFunc("POST /admin/login", h.HandleLogin)
	mux.HandleFunc("POST /admin/logout", h.HandleLogout)
	mux.HandleFunc("GET /admin", h.Root)
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/admin/usage", 303}, {"/admin/usage/data", 401}, {"/usage", 303}, {"/usage/data?days=7", 307},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.code {
			t.Fatalf("%s: got %d, want %d", tc.path, w.Code, tc.code)
		}
	}
	form := url.Values{"password": {"hunter2"}}
	r := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Header().Get("Location") != "/admin/usage" {
		t.Fatal("login must open overview")
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no session cookie")
	}
	cookie := cookies[0]
	for _, path := range []string{"/admin/usage", "/admin/usage/data?days=7"} {
		r = httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if path == "/admin/usage" && !strings.Contains(w.Body.String(), `/assets/usage.js`) {
			t.Fatal("overview script missing")
		}
	}
	r = httptest.NewRequest("POST", "/admin/logout", nil)
	r.AddCookie(cookie)
	mux.ServeHTTP(httptest.NewRecorder(), r)
	r = httptest.NewRequest("GET", "/admin/usage/data", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("logout must revoke usage access")
	}
}

func TestUsageWithoutMetrics(t *testing.T) {
	h, _, _ := newAdminTestHandler(t)
	w := httptest.NewRecorder()
	h.UsagePage(w, httptest.NewRequest("GET", "/admin/usage", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Usage logging is off") {
		t.Fatal("missing disabled state")
	}
	w = httptest.NewRecorder()
	h.UsageData(w, httptest.NewRequest("GET", "/admin/usage/data", nil))
	if w.Code != 503 {
		t.Fatal("disabled metrics must not panic")
	}
}

func TestLegacyProcessorsRedirect(t *testing.T) {
	h, _, _ := newAdminTestHandler(t)
	w := httptest.NewRecorder()
	h.ProcessorsPage(w, httptest.NewRequest("GET", "/admin/processors", nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/chat?tab=helpers" {
		t.Fatal("old processors bookmark should open the shared helpers editor")
	}
}

func TestWebAssets(t *testing.T) {
	for _, tc := range []struct {
		path, contentType string
		code              int
	}{
		{"/assets/base.css", "text/css", 200}, {"/assets/models.js", "text/javascript", 200},
		{"/assets/", "", 404}, {"/assets/../templates/login.html", "", 404}, {"/assets/missing.js", "", 404},
	} {
		w := httptest.NewRecorder()
		WebAssets().ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.code {
			t.Errorf("%s: %d", tc.path, w.Code)
		}
		if tc.code == 200 && !strings.HasPrefix(w.Header().Get("Content-Type"), tc.contentType) {
			t.Errorf("%s: wrong MIME type", tc.path)
		}
	}
}
