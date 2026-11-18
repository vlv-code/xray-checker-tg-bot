package telegram

import (
	"strings"
	"testing"
)

// Regression tests for the /nodeadd one-time registration reply: the
// operator-configured master report URL (REPORT_URL / MASTER_PUBLIC_URL) and
// the generated token are interpolated into a Telegram HTML message. A raw &
// in a URL query string (e.g. ?a=1&b=2) breaks Telegram HTML delivery, so
// every runtime interpolation must be escaped.
//
// Wave K-B2 register entry (user-ratified UX overhaul): the former pins on
// the giant <pre> dump (2 <pre> blocks, 3 escaped interpolations per value)
// are replaced — the values moved into inline copy-text buttons and the
// message became a compact instruction. The escape guarantees survive
// unchanged: one escaped interpolation per value in the text, raw values
// must never appear, and the copy buttons carry the RAW unescaped values so
// the copied text is usable (pinned in TestNodeAddCopyMarkup).

func TestBuildNodeRegisteredTextEscapesReportURL(t *testing.T) {
	const rawURL = "https://master.example/report?a=1&b=2"
	const escapedURL = "https://master.example/report?a=1&amp;b=2"
	text := buildNodeRegisteredText("node-1",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		rawURL)

	if !strings.Contains(text, escapedURL) {
		t.Errorf("report URL ampersand must be escaped (%q missing), got:\n%s", escapedURL, text)
	}
	// The URL is interpolated once: the verification <code> line (the values
	// themselves moved into the copy-text buttons, Wave K-B2).
	if got := strings.Count(text, escapedURL); got != 1 {
		t.Errorf("expected escaped URL once, got %d:\n%s", got, text)
	}
	if strings.Contains(text, rawURL) {
		t.Errorf("raw unescaped URL must not appear in message:\n%s", text)
	}
	// The giant <pre> dump is gone (Wave K-B2): the compact instruction text
	// carries no <pre> blocks at all.
	if got := strings.Count(text, "<pre>"); got != 0 {
		t.Errorf("expected no <pre> blocks, got %d:\n%s", got, text)
	}
}

func TestBuildNodeRegisteredTextEscapesToken(t *testing.T) {
	hostile := `<tok>&"'x`
	escaped := escapeHTML(hostile)
	text := buildNodeRegisteredText("node-1", hostile,
		"https://master.example/report")

	if strings.Contains(text, hostile) {
		t.Errorf("hostile token must not appear raw in message:\n%s", text)
	}
	// Token is interpolated once: the verification <code> line.
	if got := strings.Count(text, escaped); got != 1 {
		t.Errorf("expected escaped token once, got %d:\n%s", got, text)
	}
}
