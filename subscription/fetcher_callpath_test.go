package subscription

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type errRoundTripper struct{ err error }

func (rt errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, rt.err }

// Regression (Guardian P0-1, 2026-11-16): the redaction seam refactor dropped
// the error guard between doSubscriptionRequest and `defer resp.Body.Close()`
// in fetchURLContent, so ANY transport failure (unreachable upstream — a
// routine graceful-degradation path) panicked the process via the periodic
// refetch job. The earlier seam-level test (fetcher_redact_test.go) exercised
// doSubscriptionRequest directly and masked the broken call site; this test
// drives the REAL fetchURLContent path. The shared client is swapped for an
// always-failing transport; the target is a public IP literal so pre-flight
// validation passes without DNS and nothing touches the network.
func TestFetchURLContentTransportFailureReturnsErrorNotPanic(t *testing.T) {
	old := subscriptionClient
	subscriptionClient = &http.Client{
		Transport: errRoundTripper{err: errors.New("dial failure: connection refused")},
		Timeout:   time.Second,
	}
	t.Cleanup(func() { subscriptionClient = old })

	p := NewParser()
	_, err := p.fetchURLContent("http://93.184.216.34/sub?token=SECRET")
	if err == nil {
		t.Fatal("expected a transport error, got nil")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("transport error must be redacted, got: %v", err)
	}
}
