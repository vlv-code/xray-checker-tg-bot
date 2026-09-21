package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBasicAuthMiddleware(t *testing.T) {
	username := "admin"
	password := "secret123"

	handler := BasicAuthMiddleware(username, password)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("authorized"))
	}))

	tests := []struct {
		name       string
		user       string
		pass       string
		setAuth    bool
		wantStatus int
	}{
		{
			name:       "valid credentials",
			user:       "admin",
			pass:       "secret123",
			setAuth:    true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "wrong username",
			user:       "wronguser",
			pass:       "secret123",
			setAuth:    true,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong password",
			user:       "admin",
			pass:       "wrongpass",
			setAuth:    true,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing auth header",
			setAuth:    false,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if tt.setAuth {
				req.SetBasicAuth(tt.user, tt.pass)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, rec.Code)
			}
		})
	}
}

func TestBasicAuthRateLimiter(t *testing.T) {
	username := "admin"
	password := "secret123"

	limiter := NewAuthLimiter(3, 100*time.Millisecond, 1*time.Second)
	SetAuthLimiter(limiter)
	defer SetAuthLimiter(NewAuthLimiter(5, 30*time.Second, 1*time.Minute))

	handler := BasicAuthMiddleware(username, password)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("authorized"))
	}))

	ip1 := "192.0.2.1:12345"
	ip2 := "192.0.2.2:12345"

	// 3 failed attempts from ip1
	for i := 1; i <= 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = ip1
		req.SetBasicAuth("admin", "wrong")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i, rec.Code)
		}
	}

	// 4th attempt from ip1 should be locked out (429 Too Many Requests)
	{
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = ip1
		req.SetBasicAuth("admin", "wrong")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 Too Many Requests, got %d", rec.Code)
		}
	}

	// ip2 should still be allowed to attempt auth (even with correct creds)
	{
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = ip2
		req.SetBasicAuth("admin", "secret123")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("ip2 expected 200 OK, got %d", rec.Code)
		}
	}

	// After lockDuration passes, ip1 should be able to attempt again
	time.Sleep(150 * time.Millisecond)
	{
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = ip1
		req.SetBasicAuth("admin", "secret123")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK after lockout expired, got %d", rec.Code)
		}
	}
}

