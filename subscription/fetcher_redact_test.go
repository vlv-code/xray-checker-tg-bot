package subscription

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Regression (P0): a failed client.Do returned the raw *url.Error whose
// message embeds the full request URL — including ?token=SECRET query
// parameters — leaking the subscription token into logs and Telegram alerts.
// The pre-flight SSRF check rejects loopback targets before the request is
// issued, so this exercises doSubscriptionRequest (the fetchURLContent Do
// site) directly with a closed-listener target.
func TestSubscriptionClientTransportErrorRedactsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	target := srv.URL + "/sub?token=SECRET"
	srv.Close() // closed listener: the request fails fast, no network needed

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	_, err = doSubscriptionRequest(subscriptionClient, req)
	if err == nil {
		t.Fatal("expected an error from a closed listener, got nil")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("transport error leaks the token: %v", err)
	}
	if want := RedactURL(target); !strings.Contains(err.Error(), want) {
		t.Errorf("error should contain the redacted URL %q, got: %v", want, err)
	}
}

// Credentials in userinfo must never surface in transport errors.
func TestSubscriptionClientTransportErrorRedactsUserinfo(t *testing.T) {
	target := "http://user:pw@127.0.0.1:1/secret-endpoint"
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	_, err = doSubscriptionRequest(subscriptionClient, req)
	if err == nil {
		t.Fatal("expected a dial error, got nil")
	}
	if strings.Contains(err.Error(), "pw") {
		t.Fatalf("transport error leaks userinfo password: %v", err)
	}
}

// The url.Parse failure path in validateSubscriptionTarget must not echo the
// raw URL either: parse errors embed the full input string verbatim.
func TestValidateSubscriptionTargetParseErrorRedactsSecrets(t *testing.T) {
	raw := "http://user:pw@127.0.0.1:notaport/sub?token=SECRET"
	err := validateSubscriptionTarget(raw)
	if err == nil {
		t.Fatal("expected a parse error for the invalid port, got nil")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("parse error leaks token: %v", err)
	}
	if strings.Contains(err.Error(), "pw") {
		t.Fatalf("parse error leaks userinfo password: %v", err)
	}
}

func TestRedactURLError(t *testing.T) {
	inner := errors.New("connection refused")
	ue := &url.Error{Op: "Get", URL: "https://panel.example.com/sub?token=SECRET", Err: inner}

	got := redactURLError(ue)
	if strings.Contains(got.Error(), "SECRET") {
		t.Fatalf("redactURLError leaks the token: %v", got)
	}
	if want := "https://panel.example.com/sub?<redacted>"; !strings.Contains(got.Error(), want) {
		t.Errorf("expected redacted URL %q in %q", want, got.Error())
	}
	if !errors.Is(got, inner) {
		t.Errorf("inner cause must stay unwrappable, got %v", got)
	}
	var asURL *url.Error
	if !errors.As(got, &asURL) || asURL.Op != "Get" {
		t.Errorf("operation name must be preserved, got %v", got)
	}

	// Errors that are not *url.Error pass through unchanged.
	boom := errors.New("boom")
	if pass := redactURLError(boom); pass != boom {
		t.Errorf("expected pass-through of %v, got %v", boom, pass)
	}
}
