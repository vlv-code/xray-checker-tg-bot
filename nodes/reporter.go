package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// reportTimeout bounds a single push attempt; the next check cycle retries.
const reportTimeout = 15 * time.Second

// httpStatusError is a non-2xx status answered by the master's ingest
// endpoint. Classification uses errors.As, so a network error that merely
// mentions an HTTP status in its text is never mistaken for a master answer.
type httpStatusError struct {
	code int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("master answered HTTP %d", e.code)
}

// StatusCode returns the HTTP status the master answered with.
func (e *httpStatusError) StatusCode() int {
	return e.code
}

// Reporter pushes check snapshots to the master's ingest endpoint and returns
// the desired managed-subscription list from the response.
type Reporter struct {
	url    string
	token  string
	client *http.Client
}

// NewReporter builds a reporter for the given ingest URL and bearer token.
func NewReporter(url, token string) *Reporter {
	return &Reporter{url: url, token: token, client: &http.Client{Timeout: reportTimeout}}
}

// Send posts the payload and returns the IngestResponse. Errors are logged
// by the caller; a failed report is retried by the next check cycle.
func (r *Reporter) Send(p ReportPayload) (*IngestResponse, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encoding report: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building report request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("posting report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		return nil, &httpStatusError{code: resp.StatusCode}
	}

	var out IngestResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding ingest response: %w", err)
	}
	return &out, nil
}

// DefaultReportBackoffs are the default backoff intervals when retrying transient report failures.
var DefaultReportBackoffs = []time.Duration{2 * time.Second, 5 * time.Second}

// SendWithRetry posts the payload with retries on transient network and server errors.
// Permanent errors (such as HTTP 400 or 401) are not retried.
func (r *Reporter) SendWithRetry(p ReportPayload, backoffs []time.Duration) (*IngestResponse, error) {
	if len(backoffs) == 0 {
		return r.Send(p)
	}

	var resp *IngestResponse
	var lastErr error

	for attempt := 0; attempt <= len(backoffs); attempt++ {
		resp, lastErr = r.Send(p)
		if lastErr == nil {
			return resp, nil
		}

		// Do not retry non-transient master rejections (400 Bad Request,
		// 401 Unauthorized); only a typed status answer counts — any other
		// error (network failures, 5xx, other statuses) stays transient.
		var statusErr *httpStatusError
		if errors.As(lastErr, &statusErr) &&
			(statusErr.StatusCode() == http.StatusBadRequest || statusErr.StatusCode() == http.StatusUnauthorized) {
			return nil, lastErr
		}

		if attempt < len(backoffs) {
			time.Sleep(backoffs[attempt])
		}
	}

	return nil, fmt.Errorf("failed after %d attempts: %w", len(backoffs)+1, lastErr)
}
