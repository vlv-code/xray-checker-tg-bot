package telegram

import (
	"fmt"
	"sort"
	"strings"

	"xray-checker/checker"
)

// cheburProbeIcon picks the per-scanner icon from raw verdict kinds (wave G
// semantics, mirroring the checker-side cheburKindIcon): real restrictions
// (sni_block/tspu_block/dns_spoofing) are ⛔, the informational CDN family
// (cdn_block/whitelist) is ℹ️ — never ⛔ (the old one-bucket ⛔ for cdn_block
// mislabeled CDN-exclusion info as a hard block), ok ✅, everything else ❓.
func cheburProbeIcon(verdicts []string) string {
	for _, v := range verdicts {
		switch v {
		case "sni_block", "dns_spoofing", "tspu_block":
			return "⛔"
		}
	}
	for _, v := range verdicts {
		if v == "cdn_block" || v == "whitelist" {
			return "ℹ️"
		}
	}
	for _, v := range verdicts {
		if v == "ok" {
			return "✅"
		}
	}
	return "❓"
}

// cheburProbeLabels renders RU verdict labels (unknown kinds pass through raw,
// so HTML contexts must escape the joined result).
func cheburProbeLabels(verdicts []string) string {
	labels := make([]string, 0, len(verdicts))
	for _, v := range verdicts {
		labels = append(labels, checker.CheburVerdictLabel(v))
	}
	return strings.Join(labels, ", ")
}

// cheburDiagCountsLine renders the per-kind scanner counts for the diag
// block (kind order mirrors the checker report: informational family first,
// then unclear, then real restrictions; unknown kinds appended sorted). All
// label text is escaped exactly once.
func cheburDiagCountsLine(kinds map[string]int) string {
	if len(kinds) == 0 {
		return ""
	}
	order := []string{"ok", "whitelist", "cdn_block", "uncertain", "dns_spoofing", "sni_block", "tspu_block"}
	parts := make([]string, 0, len(kinds))
	for _, k := range order {
		if n := kinds[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d", escapeHTML(checker.CheburVerdictLabel(k)), n))
		}
	}
	var unknown []string
	for k, n := range kinds {
		if n > 0 && !isCheburDiagKnownKind(k, order) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		parts = append(parts, fmt.Sprintf("%s: %d", escapeHTML(checker.CheburVerdictLabel(k)), kinds[k]))
	}
	return strings.Join(parts, " · ")
}

// isCheburDiagKnownKind reports whether the kind belongs to the display order.
func isCheburDiagKnownKind(kind string, order []string) bool {
	for _, k := range order {
		if k == kind {
			return true
		}
	}
	return false
}

// formatCheburDiagBlock renders a compact escaped Cheburcheck block (verdict
// line + answered/online scanner counts + per-kind scanner counts +
// per-scanner one-liners) for the HTML diagnostics views. Wave G: the counts
// line surfaces the frozen-contract fields (KindCounts/OnlineProbes/Answered)
// and the informational CDN family never renders as ⛔.
func formatCheburDiagBlock(cheb *checker.CheburcheckSummary) string {
	if cheb == nil {
		return ""
	}
	var sb strings.Builder
	verdict := cheb.Verdict
	if verdict == "" {
		verdict = "нет данных"
	}
	fmt.Fprintf(&sb, "  • Cheburcheck: %s\n", escapeHTML(verdict))
	switch {
	case cheb.OnlineProbes > 0:
		fmt.Fprintf(&sb, "      Ответы: %d из %d сканеров\n", cheb.Answered, cheb.OnlineProbes)
	case cheb.Answered > 0:
		fmt.Fprintf(&sb, "      Ответы: %d сканеров\n", cheb.Answered)
	}
	if line := cheburDiagCountsLine(cheb.KindCounts); line != "" {
		sb.WriteString("      • " + line + "\n")
	}
	for _, r := range cheb.Results {
		meta := ""
		switch {
		case r.Region != "" && r.Provider != "":
			meta = fmt.Sprintf(" (%s/%s)", escapeHTML(r.Region), escapeHTML(r.Provider))
		case r.Region != "":
			meta = fmt.Sprintf(" (%s)", escapeHTML(r.Region))
		case r.Provider != "":
			meta = fmt.Sprintf(" (%s)", escapeHTML(r.Provider))
		}
		cached := ""
		if r.Cached {
			cached = " (кеш)"
		}
		fmt.Fprintf(&sb, "      – %s%s %s %s%s\n",
			escapeHTML(r.Name), meta, cheburProbeIcon(r.Verdicts), escapeHTML(cheburProbeLabels(r.Verdicts)), cached)
	}
	return sb.String()
}
