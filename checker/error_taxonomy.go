package checker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

// ErrorCategory categorizes network and protocol failures internally.
type ErrorCategory int

const (
	CatNone        ErrorCategory = iota // No error
	CatTimeout                          // context.DeadlineExceeded or net.Error with Timeout() == true
	CatConnRefused                      // syscall.ECONNREFUSED or ICMP Port Unreachable
	CatConnReset                        // syscall.ECONNRESET, io.EOF, TCP RST
	CatTLSError                         // tls.RecordHeaderError, x509 cert errors
	CatDNSError                         // net.DNSError
	CatHTTP4xx                          // 403, 429 - target rejected request
	CatHTTP5xx                          // 500, 502, 503, 504 - target server error
	CatUnknown                          // Uncategorized error
)

func (c ErrorCategory) String() string {
	switch c {
	case CatNone:
		return "none"
	case CatTimeout:
		return "timeout"
	case CatConnRefused:
		return "conn_refused"
	case CatConnReset:
		return "conn_reset"
	case CatTLSError:
		return "tls_error"
	case CatDNSError:
		return "dns_error"
	case CatHTTP4xx:
		return "http_4xx"
	case CatHTTP5xx:
		return "http_5xx"
	default:
		return "unknown"
	}
}

// ClassifyError inspects an error and HTTP status code to determine its category.
func ClassifyError(err error, httpStatus int) ErrorCategory {
	if httpStatus >= 400 && httpStatus < 500 {
		return CatHTTP4xx
	}
	if httpStatus >= 500 && httpStatus < 600 {
		return CatHTTP5xx
	}

	if err == nil {
		return CatNone
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return CatTimeout
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return CatConnRefused
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF) {
		return CatConnReset
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return CatTimeout
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return CatDNSError
	}

	var recErr tls.RecordHeaderError
	if errors.As(err, &recErr) {
		return CatTLSError
	}

	var certErr x509.CertificateInvalidError
	if errors.As(err, &certErr) {
		return CatTLSError
	}

	var unkAuthErr x509.UnknownAuthorityError
	if errors.As(err, &unkAuthErr) {
		return CatTLSError
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "general socks server failure") || strings.Contains(msg, "socks server failure") {
		return CatTimeout
	}
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") {
		return CatTimeout
	}
	if strings.Contains(msg, "connection refused") || strings.Contains(msg, "refused") {
		return CatConnRefused
	}
	if strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe") || strings.Contains(msg, "reset by peer") || strings.Contains(msg, "eof") {
		return CatConnReset
	}
	// Explicit HTTP status text is a stronger signal than words embedded in a
	// URL, so check it before the TLS/DNS phrases below.
	if strings.Contains(msg, "http 4") || strings.Contains(msg, "http status: 4") {
		return CatHTTP4xx
	}
	if strings.Contains(msg, "http 5") || strings.Contains(msg, "http status: 5") {
		return CatHTTP5xx
	}
	// Phrase-based matching: error strings embed full URLs, and bare word
	// substrings like "tls"/"dns" match hostname segments ("tls.example.com",
	// "dns.google"), corrupting the category.
	if strings.Contains(msg, "tls handshake") || strings.Contains(msg, "tls:") || strings.Contains(msg, "certificate") || strings.Contains(msg, "handshake failure") || strings.Contains(msg, "x509") {
		return CatTLSError
	}
	if strings.Contains(msg, "no such host") || strings.Contains(msg, "server misbehaving") || strings.Contains(msg, "dns lookup") || strings.Contains(msg, "lookup ") {
		return CatDNSError
	}

	return CatUnknown
}

// FormatSoftHint returns a user-friendly soft hint string with "вероятно:".
// Note: never present as a definitive diagnostic; always a hint.
func FormatSoftHint(cat ErrorCategory, isUDP bool) string {
	switch cat {
	case CatTimeout:
		if isUDP {
			return "таймаут UDP/QUIC (возможна блокировка или сброс пакетов)"
		}
		return "таймаут соединения"
	case CatConnReset:
		return "сброс соединения, возможна DPI-блокировка"
	case CatConnRefused:
		return "соединение отклонено узлом"
	case CatTLSError:
		return "ошибка TLS-рукопожатия"
	case CatDNSError:
		return "ошибка DNS-резолва"
	case CatHTTP4xx:
		return "целевой сервер отклонил запрос"
	case CatHTTP5xx:
		return "целевой сервер недоступен"
	default:
		return "ошибка сети"
	}
}
