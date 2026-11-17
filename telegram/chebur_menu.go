package telegram

import (
	"net"
	"sort"
	"strings"

	"xray-checker/metrics"
)

// cheburHostsFromSnapshot extracts the UNIQUE bare hosts of enabled proxies
// from the metrics snapshot: the port is stripped (net.SplitHostPort) and
// entries are deduped by host regardless of protocol — Cheburcheck probes
// hosts and has no port concept (wave G1). The result is sorted.
func cheburHostsFromSnapshot(proxies []metrics.ProxyMetric) []string {
	seen := make(map[string]bool)
	hosts := make([]string, 0, len(proxies))
	for _, pm := range proxies {
		if pm.Disabled || pm.Address == "" {
			continue
		}
		host, _, err := net.SplitHostPort(pm.Address)
		if err != nil || host == "" {
			host = pm.Address
		}
		// DNS names are case-insensitive: dedup on the lowercased form while
		// keeping the first-seen spelling for display.
		key := strings.ToLower(host)
		if seen[key] {
			continue
		}
		seen[key] = true
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

// getCheburMenuText renders the Cheburcheck submenu header: a one-line
// description of the active RF probing, the /cheburcheck usage hint, an
// empty-state note when no host exists (the list is host-based since wave G1)
// and — when neither the CHEBURCHECK_API_KEY nor per-scanner tokens are
// configured — an explicit unconfigured notice. Static RU text only (no
// external strings are interpolated), so no escapeHTML call sites are needed.
func (b *Bot) getCheburMenuText(hostCount int) string {
	text := "🧀 <b>Активный пробинг блокировок РФ (Cheburcheck)</b>\n\n" +
		"Сканеры из России отправляют реальные пробы к хосту и показывают тип блокировки " +
		"(SNI, ТСПУ, подмена DNS, CDN).\n\n" +
		"Выберите хост из списка ниже или отправьте команду с любым хостом:\n" +
		"<code>/cheburcheck &lt;хост&gt;</code>"
	if hostCount == 0 {
		text += "\n\n📭 Нет активных хостов для проверки."
	}
	if !b.resolveCheburClient().HasCredentials() {
		text += "\n\n⚠️ Сканеры не настроены (CHEBURCHECK_API_KEY пуст)"
	}
	return text
}
