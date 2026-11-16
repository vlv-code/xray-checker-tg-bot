package subscription

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// redirectHop builds a redirect-hop request for policy tests.
func redirectHop(t *testing.T, rawURL string, hdr map[string]string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", rawURL, err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return req
}

// Regression (P1): without CheckRedirect Go follows up to 10 redirects with NO
// SSRF re-validation and forwards every original header, letting a malicious
// redirect turn the checker into an SSRF probe and leak panel credentials.
func TestSubscriptionClientHasRedirectPolicy(t *testing.T) {
	if subscriptionClient.CheckRedirect == nil {
		t.Fatal("subscriptionClient must validate redirect targets (CheckRedirect is nil)")
	}
}

func TestSubscriptionRedirectPolicy_BlocksLoopback(t *testing.T) {
	req := redirectHop(t, "http://127.0.0.1:9/x", nil)
	via := []*http.Request{redirectHop(t, "https://panel.example.com/sub", nil)}

	err := subscriptionRedirectPolicy(req, via)
	if err == nil {
		t.Fatal("expected loopback redirect to be blocked, got nil")
	}
	if !strings.Contains(err.Error(), "SSRF") && !strings.Contains(err.Error(), "blocked") {
		t.Errorf("expected SSRF/blocked in error, got: %v", err)
	}
}

func TestSubscriptionRedirectPolicy_BlocksIPv6Loopback(t *testing.T) {
	req := redirectHop(t, "http://[::1]/x", nil)
	via := []*http.Request{redirectHop(t, "https://panel.example.com/sub", nil)}

	err := subscriptionRedirectPolicy(req, via)
	if err == nil {
		t.Fatal("expected IPv6 loopback redirect to be blocked, got nil")
	}
	if !strings.Contains(err.Error(), "SSRF") && !strings.Contains(err.Error(), "blocked") {
		t.Errorf("expected SSRF/blocked in error, got: %v", err)
	}
}

// Same-host redirect: valid target passes and identifying headers are kept.
// The request carries the hop headers because net/http's copier has already
// applied the original headers before CheckRedirect runs.
func TestSubscriptionRedirectPolicy_SameHostKeepsHeaders(t *testing.T) {
	// IP literal: validateSubscriptionTarget passes without any DNS lookup.
	via := []*http.Request{redirectHop(t, "http://93.184.216.34/sub?token=S", map[string]string{
		"X-Hwid": "hwid",
	})}
	req := redirectHop(t, "http://93.184.216.34/follow", map[string]string{
		"X-Hwid": "hwid",
	})

	if err := subscriptionRedirectPolicy(req, via); err != nil {
		t.Fatalf("same-host redirect must be allowed, got: %v", err)
	}
	if req.Header.Get("X-Hwid") != "hwid" {
		t.Errorf("same-host redirect must keep X-Hwid, got %q", req.Header.Get("X-Hwid"))
	}
}

// The example.com variant from the spec: same-host redirect on a public
// hostname resolves DNS through validateSubscriptionTarget.
func TestSubscriptionRedirectPolicy_SameHostExampleCom(t *testing.T) {
	via := []*http.Request{redirectHop(t, "https://example.com/sub", nil)}
	req := redirectHop(t, "https://example.com/follow", nil)

	if err := subscriptionRedirectPolicy(req, via); err != nil {
		t.Fatalf("same-host example.com redirect must be allowed, got: %v", err)
	}
}

// Cross-host redirect: identifying/credentials headers must be stripped, safe
// transport headers kept.
func TestSubscriptionRedirectPolicy_CrossHostStripsHeaders(t *testing.T) {
	via := []*http.Request{redirectHop(t, "http://93.184.216.34/sub", map[string]string{
		"User-Agent":      "Xray-Checker",
		"Accept":          "*/*",
		"Accept-Encoding": "gzip",
		"X-Hwid":          "hwid",
		"X-Test":          "leak-me",
		"Cookie":          "session=abc",
		"Authorization":   "Bearer abc",
	})}
	req := redirectHop(t, "http://8.8.8.8/follow", map[string]string{
		"User-Agent":      "Xray-Checker",
		"Accept":          "*/*",
		"Accept-Encoding": "gzip",
		"X-Hwid":          "hwid",
		"X-Test":          "leak-me",
		"Cookie":          "session=abc",
		"Authorization":   "Bearer abc",
	})

	if err := subscriptionRedirectPolicy(req, via); err != nil {
		t.Fatalf("public cross-host redirect must be allowed (headers stripped, not aborted), got: %v", err)
	}
	for _, h := range []string{"X-Hwid", "X-Test", "Cookie", "Authorization"} {
		if got := req.Header.Get(h); got != "" {
			t.Errorf("cross-host redirect must strip %s, got %q", h, got)
		}
	}
	for _, h := range []string{"User-Agent", "Accept", "Accept-Encoding"} {
		if req.Header.Get(h) == "" {
			t.Errorf("cross-host redirect must keep %s", h)
		}
	}
}

// Go's built-in limit is replaced when CheckRedirect is set, so the policy
// must re-implement it.
func TestSubscriptionRedirectPolicy_StopsAfter10Redirects(t *testing.T) {
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = redirectHop(t, "http://93.184.216.34/hop", nil)
	}
	req := redirectHop(t, "http://93.184.216.34/hop", nil)

	err := subscriptionRedirectPolicy(req, via)
	if err == nil {
		t.Fatal("expected the 10-redirect limit to apply, got nil")
	}
	if !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("expected Go's own limit message, got: %v", err)
	}
}

func TestSubscriptionRedirectPolicy_ValidTargetUnderLimitPasses(t *testing.T) {
	via := []*http.Request{redirectHop(t, "http://93.184.216.34/hop", nil)}
	req := redirectHop(t, "http://93.184.216.34/hop", nil)

	if err := subscriptionRedirectPolicy(req, via); err != nil {
		t.Fatalf("valid public target under the hop limit must pass, got: %v", err)
	}
}

// Integration through a real redirect flow: the initial request reaches the
// httptest server (redirect targets are the only thing the policy governs),
// and a loopback Location must abort the redirect with an SSRF error.
func TestSubscriptionClientRedirectToLoopbackAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:9/loopback?token=SECRET", http.StatusFound)
	}))
	defer srv.Close()

	client := &http.Client{CheckRedirect: subscriptionRedirectPolicy}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// Route through the production Do site so the FIX 1 redaction also covers
	// the outer *url.Error whose URL Go overrides with the raw Location.
	_, err = doSubscriptionRequest(client, req)
	if err == nil {
		t.Fatal("expected the redirect to a loopback target to be blocked, got nil")
	}
	if !strings.Contains(err.Error(), "SSRF") && !strings.Contains(err.Error(), "blocked") {
		t.Errorf("expected SSRF/blocked in error, got: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("redirect error must not leak the Location query secret: %v", err)
	}
}
