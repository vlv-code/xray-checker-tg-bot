package nodes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// reportTimeout bounds a single push attempt; the next check cycle retries.
const reportTimeout = 15 * time.Second

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

// Send posts the payload and returns the managedSubs list. Errors are logged
// by the caller; a failed report is retried by the next check cycle.
func (r *Reporter) Send(p ReportPayload) ([]string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encoding report: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, r.url, bytes.NewReader(body))
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
		return nil, fmt.Errorf("master answered HTTP %d", resp.StatusCode)
	}

	var out IngestResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding ingest response: %w", err)
	}
	return out.ManagedSubs, nil
}
