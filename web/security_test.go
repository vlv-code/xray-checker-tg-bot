package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersMiddleware(t *testing.T) {
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	handler := SecurityHeadersMiddleware(dummyHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	headers := rec.Header()

	if got := headers.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}

	if got := headers.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want %q", got, "DENY")
	}

	if got := headers.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Errorf("Referrer-Policy = %q, want %q", got, "strict-origin-when-cross-origin")
	}

	csp := headers.Get("Content-Security-Policy")
	if csp == "" || !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("Content-Security-Policy missing or invalid: %q", csp)
	}

	if perm := headers.Get("Permissions-Policy"); perm == "" {
		t.Errorf("Permissions-Policy header missing")
	}
}
