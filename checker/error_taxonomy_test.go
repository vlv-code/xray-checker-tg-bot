package checker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
)

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		httpStatus int
		expected   ErrorCategory
	}{
		{
			name:       "no error 200 OK",
			err:        nil,
			httpStatus: 200,
			expected:   CatNone,
		},
		{
			name:       "no error 204 No Content",
			err:        nil,
			httpStatus: 204,
			expected:   CatNone,
		},
		{
			name:       "context deadline exceeded",
			err:        context.DeadlineExceeded,
			httpStatus: 0,
			expected:   CatTimeout,
		},
		{
			name:       "connection refused syscall",
			err:        syscall.ECONNREFUSED,
			httpStatus: 0,
			expected:   CatConnRefused,
		},
		{
			name:       "connection reset syscall",
			err:        syscall.ECONNRESET,
			httpStatus: 0,
			expected:   CatConnReset,
		},
		{
			name:       "io EOF treated as reset",
			err:        io.EOF,
			httpStatus: 0,
			expected:   CatConnReset,
		},
		{
			name:       "tls record header error",
			err:        tls.RecordHeaderError{Msg: "bad record"},
			httpStatus: 0,
			expected:   CatTLSError,
		},
		{
			name:       "x509 certificate error",
			err:        x509.UnknownAuthorityError{},
			httpStatus: 0,
			expected:   CatTLSError,
		},
		{
			name:       "dns error",
			err:        &net.DNSError{Name: "example.com"},
			httpStatus: 0,
			expected:   CatDNSError,
		},
		{
			name:       "http 403 Forbidden",
			err:        nil,
			httpStatus: 403,
			expected:   CatHTTP4xx,
		},
		{
			name:       "http 429 Too Many Requests",
			err:        nil,
			httpStatus: 429,
			expected:   CatHTTP4xx,
		},
		{
			name:       "http 502 Bad Gateway",
			err:        nil,
			httpStatus: 502,
			expected:   CatHTTP5xx,
		},
		{
			name:       "http 502 Bad Gateway with err and status",
			err:        errors.New("HTTP 502 from https://cp.cloudflare.com/generate_204"),
			httpStatus: 502,
			expected:   CatHTTP5xx,
		},
		{
			name:       "http 503 from error string with status 0",
			err:        errors.New("HTTP 503 from https://cp.cloudflare.com/generate_204"),
			httpStatus: 0,
			expected:   CatHTTP5xx,
		},
		{
			name:       "http 403 from error string with status 0",
			err:        errors.New("HTTP 403 from https://cp.cloudflare.com/generate_204"),
			httpStatus: 0,
			expected:   CatHTTP4xx,
		},
		{
			name:       "general socks server failure",
			err:        errors.New("socks connect tcp 127.0.0.1:1080->target:443: general SOCKS server failure"),
			httpStatus: 0,
			expected:   CatTimeout,
		},
		{
			name:       "unknown error",
			err:        errors.New("some strange internal failure"),
			httpStatus: 0,
			expected:   CatUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyError(tc.err, tc.httpStatus)
			if got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestFormatSoftHint(t *testing.T) {
	// TCP context
	hintReset := FormatSoftHint(CatConnReset, false)
	if hintReset != "сброс соединения, возможна DPI-блокировка" {
		t.Errorf("unexpected hint for CatConnReset: %s", hintReset)
	}

	hintTimeout := FormatSoftHint(CatTimeout, false)
	if hintTimeout != "таймаут соединения" {
		t.Errorf("unexpected hint for CatTimeout: %s", hintTimeout)
	}

	// UDP context (Hysteria2/QUIC)
	hintUdpTimeout := FormatSoftHint(CatTimeout, true)
	if hintUdpTimeout != "таймаут UDP/QUIC (возможна блокировка или сброс пакетов)" {
		t.Errorf("unexpected hint for UDP CatTimeout: %s", hintUdpTimeout)
	}
}
