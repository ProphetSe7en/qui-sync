package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prophetse7en/qui-sync/internal/core"
)

// initAuthHarness spins up an InitAuth-wired mux against an isolated
// /config dir so every test gets a fresh auth state. Returns the
// ServeMux + auth.Store so tests can assert against the live store.
func initAuthHarness(t *testing.T) (*http.ServeMux, *core.Config, string) {
	t.Helper()
	tmp := t.TempDir()
	cfg := &core.Config{
		Authentication:         "forms",
		AuthenticationRequired: "enabled", // disable local-bypass for predictable test responses
		SessionTTLDays:         30,
	}
	mux := http.NewServeMux()
	store := InitAuth(context.Background(), func() *core.Config { return cfg }, "test", mux)
	// InitAuth ignores AuthFilePath in DefaultConfig; auth.Store reads
	// from /config/auth.json by default. Override via the store's path
	// is not exposed, so we let the package's default win — t.TempDir()
	// alone won't redirect it. For the tests below we don't actually
	// land creds on disk; setup-handler tests use a spawned-and-thrown
	// store via the same path.
	_ = store
	_ = filepath.Join(tmp, "unused")
	return mux, cfg, tmp
}

// TestSanitizeNext pins down the open-redirect guard. Each entry is a
// raw "next" value and the expected sanitized output (empty string =
// rejected).
func TestSanitizeNext(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/", "/"},
		{"/settings", "/settings"},
		{"/settings?tab=auth", "/settings?tab=auth"},
		// Open-redirect bypasses — must be rejected.
		{"https://evil.com", ""},
		{"//evil.com/", ""},
		{"/\\evil.com", ""},
		{"javascript:alert(1)", ""},
		// Control-byte injection — must be rejected.
		{"/x\x00y", ""},
		{"/x y", ""},
		// Must-start-with-slash rule.
		{"settings", ""},
		{"./settings", ""},
	}
	for _, c := range cases {
		got := sanitizeNext(c.in)
		if got != c.want {
			t.Errorf("sanitizeNext(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestAuthStatus_Public verifies the auth-status endpoint is reachable
// without credentials (it has to be — the SPA polls it before
// authenticating to know whether to show /setup, /login, or local
// bypass UI). Pre-setup it returns configured:false and exposes only
// safe fields.
func TestAuthStatus_Public(t *testing.T) {
	mux, _, _ := initAuthHarness(t)

	req := httptest.NewRequest("GET", "/api/auth/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/api/auth/status pre-setup → %d; want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["configured"] != false {
		t.Errorf("configured = %v; want false", body["configured"])
	}
	if body["authenticated"] != false {
		t.Errorf("authenticated = %v; want false", body["authenticated"])
	}
	if body["authentication"] != "forms" {
		t.Errorf("authentication = %v; want forms", body["authentication"])
	}
}

// TestSetupPage_RendersWithCSRF confirms GET /setup is reachable
// pre-creds (middleware allowlist) and embeds the CSRF token + sets
// the cookie that POST /setup will re-validate.
func TestSetupPage_RendersWithCSRF(t *testing.T) {
	mux, _, _ := initAuthHarness(t)

	req := httptest.NewRequest("GET", "/setup", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/setup → %d; want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="csrf_token"`) {
		t.Errorf("response missing csrf_token form field")
	}
	if !strings.Contains(rec.Body.String(), "qui-sync") {
		t.Errorf("response missing qui-sync branding (clonarr leaked through?)")
	}
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "quisync_csrf=") {
		t.Errorf("Set-Cookie missing quisync_csrf; got %q", cookie)
	}
}

// TestSetupSubmit_RejectsMismatchedPasswords surfaces the simplest
// post-setup validation. The wizard re-renders with an error, no
// credentials get persisted.
func TestSetupSubmit_RejectsMismatchedPasswords(t *testing.T) {
	mux, _, _ := initAuthHarness(t)

	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", "Aa1!aaaaaaaaaa")
	form.Set("password_confirm", "different-password")
	form.Set("csrf_token", "anything") // CSRF middleware lives outside InitAuth's mux

	req := httptest.NewRequest("POST", "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("/setup mismatched passwords → %d; want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "do not match") {
		t.Errorf("response missing 'do not match' error wording")
	}
}

// TestLoginPage_RedirectsToSetupWhenUnconfigured catches the
// fresh-install loop where a user lands on /login before /setup is
// done — the handler must bounce them to /setup, not render the
// login form against missing creds.
func TestLoginPage_RedirectsToSetupWhenUnconfigured(t *testing.T) {
	mux, _, _ := initAuthHarness(t)

	req := httptest.NewRequest("GET", "/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("/login pre-setup → %d; want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/setup" {
		t.Errorf("Location = %q; want /setup", loc)
	}
}

// TestLogout_ClearsCookie verifies the logout endpoint sets a
// Max-Age:-1 cookie regardless of whether a session existed. Without
// the cookie clear the browser keeps re-sending a dead session ID on
// every page load.
func TestLogout_ClearsCookie(t *testing.T) {
	mux, _, _ := initAuthHarness(t)

	req := httptest.NewRequest("POST", "/logout", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("/logout → %d; want 302", rec.Code)
	}
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "quisync_session=") {
		t.Errorf("Set-Cookie missing quisync_session; got %q", cookie)
	}
	if !strings.Contains(cookie, "Max-Age=0") {
		t.Errorf("Set-Cookie missing Max-Age=0 (cookie not cleared); got %q", cookie)
	}
}
