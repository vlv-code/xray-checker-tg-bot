package telegram

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// The bot sends every view with Telegram HTML parse mode: a single unescaped
// runtime value breaks delivery of the WHOLE message (HTTP 400 "can't parse
// entities"). These tests pin the escaping contract of escapeHTML and the
// view-level interpolation spots that were found unescaped in the audit.

// TestEscapeHTML locks the escaper contract: all five HTML-significant
// characters (& < > " ') must be escaped so that escapeHTML is safe in BOTH
// text contexts (<b>%s</b>) and attribute contexts (<a href="%s">).
func TestEscapeHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text unchanged", "plain text 192.168.1.1", "plain text 192.168.1.1"},
		{"ampersand", "a&b", "a&amp;b"},
		{"less than", "<", "&lt;"},
		{"greater than", ">", "&gt;"},
		{"double quote", `"`, "&#34;"},
		{"single quote", `'`, "&#39;"},
		{"hostile mix", `<b>x&"y'z`, `&lt;b&gt;x&amp;&#34;y&#39;z`},
		{"script injection", "<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"attribute breakout", `" onmouseover="alert(1)`, `&#34; onmouseover=&#34;alert(1)`},
		{"unicode markup", "日本語<Тег>&'\"", "日本語&lt;Тег&gt;&amp;&#39;&#34;"},
		{"closes markup context", "</i><b>", "&lt;/i&gt;&lt;b&gt;"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeHTML(tt.in); got != tt.want {
				t.Errorf("escapeHTML(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestEscapeHTML_SinglePassContract pins the SINGLE-PASS semantics the whole
// HTML view layer relies on: escaping a RAW value once yields the safe form,
// and feeding that result through escapeHTML AGAIN yields the double-escaped
// form — escapeHTML is deliberately NOT idempotent.
//
// Call-site contract: every caller passes RAW runtime values exactly once.
// A call site that feeds an already-escaped value through escapeHTML renders
// "&amp;lt;b&amp;gt;" garbage on screen — that is a CALLER bug. This test
// documents (does not bless) the double-escape outcome so audits can detect
// accidental double-escaping in rendered output.
func TestEscapeHTML_SinglePassContract(t *testing.T) {
	raw := "<b>"
	once := escapeHTML(raw)
	if once != "&lt;b&gt;" {
		t.Fatalf("single pass must escape raw markup: escapeHTML(%q) = %q, want &lt;b&gt;", raw, once)
	}
	twice := escapeHTML(once)
	if twice != "&amp;lt;b&amp;gt;" {
		t.Fatalf("second pass must double-escape (proving single-pass semantics): escapeHTML(%q) = %q, want &amp;lt;b&amp;gt;", once, twice)
	}
}

// newEscapeTestStatsStore builds a real (empty) stats store; several view
// builders dereference statsStore unconditionally.
func newEscapeTestStatsStore(t *testing.T) *StatsStore {
	t.Helper()
	store, err := NewStatsStore(filepath.Join(t.TempDir(), "stats.json"))
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}
	return store
}

// TestGetStatsOverviewText_EscapesProtocol pins views.go getStatsOverviewText:
// pm.Protocol is interpolated into <i>(%s)</i>. The value is normally
// validated upstream against a fixed set, but the escaping invariant must not
// rely on that.
func TestGetStatsOverviewText_EscapesProtocol(t *testing.T) {
	src := &mockSource{metrics: []metrics.ProxyMetric{
		{StableID: "p1", Name: "Proxy-1", Address: "1.1.1.1:443", Protocol: `</i><b>evil`, Online: true},
	}}
	b := &Bot{source: src, statsStore: newEscapeTestStatsStore(t)}

	text := b.getStatsOverviewText()

	if !strings.Contains(text, `(&lt;/I&gt;&lt;B&gt;EVIL)`) {
		t.Errorf("protocol must be rendered escaped inside <i>(...)</i>, got:\n%s", text)
	}
	if strings.Contains(text, `</I><B>`) {
		t.Errorf("raw protocol markup survived into the message (would break HTML delivery):\n%s", text)
	}
}

// TestGetIncidentsTextFiltered_EscapesNodeFilterEmpty pins views.go
// getIncidentsTextFiltered: in the "no incidents" branch the node filter label
// is interpolated raw, while the non-empty branch (filterHeader) escapes it.
func TestGetIncidentsTextFiltered_EscapesNodeFilterEmpty(t *testing.T) {
	b := &Bot{statsStore: newEscapeTestStatsStore(t)}

	text := b.getIncidentsTextFiltered(`</i><b>n1`)

	if !strings.Contains(text, `(🖥 &lt;/i&gt;&lt;b&gt;n1)`) {
		t.Errorf("empty-branch filter label must be escaped like filterHeader, got:\n%s", text)
	}
	if strings.Contains(text, `(🖥 </i><b>n1)`) {
		t.Errorf("raw node filter label survived into the empty-branch header:\n%s", text)
	}
}

// TestFormatSingleProxyDiag_EscapesResolvedIP pins diagnostics.go
// formatSingleProxyDiagWithStats: rep.NodeHealth.ResolvedIP goes into
// <code>%s</code> (both the with-latency and no-latency branches).
func TestFormatSingleProxyDiag_EscapesResolvedIP(t *testing.T) {
	tests := []struct {
		name       string
		dnsLatency time.Duration
	}{
		{name: "with DNS latency", dnsLatency: 15 * time.Millisecond},
		{name: "without DNS latency", dnsLatency: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := checker.ProxyDiagReport{
				ProxyName:  "Proxy-1",
				Protocol:   "vless",
				Status:     "online",
				StableID:   "p1",
				NodeHealth: checker.NodeHealth{ResolvedIP: `10.0.0.1</code><script>`, DNSLatency: tt.dnsLatency},
			}

			var sb strings.Builder
			formatSingleProxyDiag(&sb, rep)
			text := sb.String()

			if !strings.Contains(text, `<code>10.0.0.1&lt;/code&gt;&lt;script&gt;</code>`) {
				t.Errorf("resolved IP must be escaped inside <code>, got:\n%s", text)
			}
			if strings.Contains(text, `10.0.0.1</code>`) || strings.Contains(text, `<script>`) {
				t.Errorf("raw resolved IP markup survived (would break HTML delivery):\n%s", text)
			}
		})
	}
}

// TestFormatSingleProxyDiag_EscapesTargetSiteName pins diagnostics.go: the
// per-target endpoint line interpolates simplifyTargetName(tr.URL) raw.
// A target URL that fails to parse (or carries markup in the host) must not
// inject markup.
func TestFormatSingleProxyDiag_EscapesTargetSiteName(t *testing.T) {
	tests := []struct {
		name    string
		success bool
	}{
		{name: "successful target", success: true},
		{name: "failed target", success: false},
	}
	hostileURL := `"><img src=x onerror=alert(1)>`
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := checker.ProxyDiagReport{
				ProxyName: "Proxy-1",
				Protocol:  "vless",
				Status:    "online",
				StableID:  "p1",
				Targets: []checker.TargetDiagResult{
					{URL: hostileURL, Success: tt.success, Latency: 20 * time.Millisecond, Error: "boom"},
				},
			}

			var sb strings.Builder
			formatSingleProxyDiag(&sb, rep)
			text := sb.String()

			if strings.Contains(text, `<img src=x`) {
				t.Errorf("raw target site name markup survived (would break HTML delivery):\n%s", text)
			}
			if !strings.Contains(text, `&lt;img src=x onerror=alert(1)&gt;`) {
				t.Errorf("target site name must be rendered escaped, got:\n%s", text)
			}
		})
	}
}

// TestFormatSingleProxyDiag_EscapesProtocol pins the same Protocol-into-
// <i>(%s)</i> pattern as views.go, present in the per-proxy diagnostics line.
func TestFormatSingleProxyDiag_EscapesProtocol(t *testing.T) {
	rep := checker.ProxyDiagReport{
		ProxyName: "Proxy-1",
		Protocol:  `</i><b>evil`,
		Status:    "online",
		StableID:  "p1",
	}

	var sb strings.Builder
	formatSingleProxyDiag(&sb, rep)
	text := sb.String()

	if !strings.Contains(text, `(&lt;/I&gt;&lt;B&gt;EVIL)`) {
		t.Errorf("protocol must be rendered escaped inside <i>(...)</i>, got:\n%s", text)
	}
	if strings.Contains(text, `</I><B>`) {
		t.Errorf("raw protocol markup survived:\n%s", text)
	}
}

// TestFormatDeepDiagnostics_EscapesResolvedIP pins diagnostics.go
// formatDeepDiagnostics: the DNS stage interpolates health.ResolvedIP raw.
func TestFormatDeepDiagnostics_EscapesResolvedIP(t *testing.T) {
	b := &Bot{}
	pm := metrics.ProxyMetric{
		StableID: "p1", Name: "Deep-Proxy", Address: "1.1.1.1:443",
		Protocol: "vless", Online: true,
	}
	health := checker.NodeHealth{ResolvedIP: `10.0.0.1</code><b>`}

	text := b.formatDeepDiagnostics(pm, health, nil)

	if !strings.Contains(text, `(10.0.0.1&lt;/code&gt;&lt;b&gt;)`) {
		t.Errorf("deep-diagnostics resolved IP must be escaped, got:\n%s", text)
	}
	if strings.Contains(text, `10.0.0.1</code>`) {
		t.Errorf("raw resolved IP markup survived (would break HTML delivery):\n%s", text)
	}
}

// TestFormatReleaseNotification_EscapesHTMLURLInHref pins releases.go
// formatReleaseNotification: rel.HTMLURL comes straight from the GitHub API
// response and is interpolated into an href="..." attribute. A quote in the
// URL must not terminate the attribute early (attribute-injection class).
func TestFormatReleaseNotification_EscapesHTMLURLInHref(t *testing.T) {
	rel := &ReleaseInfo{
		TagName: "v9.9.9",
		Name:    "Evil Release",
		HTMLURL: `https://github.com/vlv-code/xray-checker-tg-bot/releases/tag/v9.9.9" onmouseover="alert(1)`,
	}

	text := formatReleaseNotification("v9.9.8", rel)

	if strings.Contains(text, `" onmouseover="alert(1)`) {
		t.Errorf("raw quote in HTMLURL must not survive inside the href attribute:\n%s", text)
	}
	if !strings.Contains(text, `<a href="https://github.com/vlv-code/xray-checker-tg-bot/releases/tag/v9.9.9&#34;`) {
		t.Errorf("href attribute must stay intact with the URL escaped, got:\n%s", text)
	}
}

// TestFormatReleaseNotification_PlainURLKeepsHref verifies no behavior change
// for the normal (benign) GitHub URL: it must round-trip unescaped.
func TestFormatReleaseNotification_PlainURLKeepsHref(t *testing.T) {
	rel := &ReleaseInfo{
		TagName: "v2.4.0",
		Name:    "v2.4.0: Node Optimization",
		HTMLURL: "https://github.com/vlv-code/xray-checker-tg-bot/releases/tag/v2.4.0",
	}

	text := formatReleaseNotification("v2.3.5", rel)

	if !strings.Contains(text, `<a href="https://github.com/vlv-code/xray-checker-tg-bot/releases/tag/v2.4.0">`) {
		t.Errorf("benign URL must round-trip unescaped in the href attribute, got:\n%s", text)
	}
}

// richPreformattedText digs the joined detailLines text out of a built rich
// message: section headings are skipped, every details block contributes the
// text of its preformatted (plain) block.
func richPreformattedText(t *testing.T, blocks []telego.InputRichBlock) string {
	t.Helper()
	var out string
	for _, blk := range blocks {
		details, ok := blk.(*telego.InputRichBlockDetails)
		if !ok {
			continue
		}
		for _, sub := range details.Blocks {
			pre, ok := sub.(*telego.InputRichBlockPreformatted)
			if !ok {
				continue
			}
			if plain, ok := pre.Text.(*telego.RichTextPlain); ok {
				out += string(*plain)
			}
		}
	}
	return out
}

// TestBuildDiagnosticsDetailsRich_RawPlainTextContract pins the RAW contract
// of the rich-text detailLines path (diagnostics.go
// buildDiagnosticsDetailsRichMessageWithTarget): detailLines are joined into
// tu.RichBlockPreformatted(tu.RichTextPlain(...)) — a PLAIN text entity that
// Telegram renders verbatim, NOT html-parsed. The OPPOSITE of the
// escapeHTML call sites: a hostile runtime string ("<b>x&y") must pass
// through UNESCAPED, because escaping here is the double-escape bug (the
// user would literally see "&lt;b&gt;"), while a plain entity cannot break
// message delivery the way a raw interpolation into an HTML-parse-mode
// string would. If this pin starts failing, the rich builder started
// escaping or the entity switched to HTML parsing — re-audit both paths.
func TestBuildDiagnosticsDetailsRich_RawPlainTextContract(t *testing.T) {
	hostile := "<b>x&y"
	b := &Bot{}
	reports := []checker.ProxyDiagReport{
		{
			ProxyName:  "Proxy-1",
			Protocol:   "vless",
			Status:     "online",
			StableID:   "p1",
			NodeHealth: checker.NodeHealth{DNSErr: hostile},
		},
	}

	msg, _ := b.buildDiagnosticsDetailsRichMessageWithTarget(reports, "", 1)
	if msg == nil {
		t.Fatalf("expected a rich details message to be built")
	}

	pre := richPreformattedText(t, msg.Blocks)
	if !strings.Contains(pre, "DNS: ❌ "+hostile) {
		t.Errorf("hostile detail line must pass through RAW into the plain rich entity, got: %q", pre)
	}
	if strings.Contains(pre, "&lt;") || strings.Contains(pre, "&amp;") {
		t.Errorf("plain rich entity must NOT be html-escaped (that would double-escape the view), got: %q", pre)
	}
}
