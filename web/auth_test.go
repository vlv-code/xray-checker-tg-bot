package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
