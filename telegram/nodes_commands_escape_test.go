package telegram

import (
	"strings"
	"testing"
)

// Regression tests for the /nodeadd one-time registration reply: the
// operator-configured master report URL (REPORT_URL / MASTER_PUBLIC_URL) and
// the generated token are interpolated into a Telegram HTML message with
// <pre> blocks. A raw & in a URL query string (e.g. ?a=1&b=2) breaks
// Telegram HTML delivery, so every runtime interpolation must be escaped.

func TestBuildNodeRegisteredTextEscapesReportURL(t *testing.T) {
	const rawURL = "https://master.example/report?a=1&b=2"
	const escapedURL = "https://master.example/report?a=1&amp;b=2"
	text := buildNodeRegisteredText("node-1",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		rawURL)

	if !strings.Contains(text, escapedURL) {
		t.Errorf("report URL ampersand must be escaped (%q missing), got:\n%s", escapedURL, text)
	}
	// The URL is interpolated three times: <code> block, compose <pre>,
	// docker run <pre>.
	if got := strings.Count(text, escapedURL); got != 3 {
		t.Errorf("expected escaped URL 3 times, got %d:\n%s", got, text)
	}
	if strings.Contains(text, rawURL) {
		t.Errorf("raw unescaped URL must not appear in message:\n%s", text)
	}
	// Both <pre> blocks must survive as intact entities.
	if got := strings.Count(text, "<pre>"); got != 2 {
		t.Errorf("expected 2 <pre> blocks, got %d:\n%s", got, text)
	}
	if got := strings.Count(text, "</pre>"); got != 2 {
		t.Errorf("expected 2 </pre> closers, got %d:\n%s", got, text)
	}
	// No raw " & " may remain inside <pre> block content.
	for _, block := range []string{
		text[strings.Index(text, "<pre>"):strings.Index(text, "</pre>")],
		text[strings.LastIndex(text, "<pre>"):strings.LastIndex(text, "</pre>")],
	} {
		if strings.Contains(block, " & ") {
			t.Errorf("raw ampersand inside <pre> block content:\n%s", block)
		}
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
	// Token is interpolated three times: <code> block, compose <pre>,
	// docker run <pre>.
	if got := strings.Count(text, escaped); got != 3 {
		t.Errorf("expected escaped token 3 times, got %d:\n%s", got, text)
	}
}
