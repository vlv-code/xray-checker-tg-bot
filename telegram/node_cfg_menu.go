package telegram

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"xray-checker/metrics"
)

// cbNodeCfg composes a namespaced node callback `prefix + name + suffix` and
// reports whether it fits Telegram's hard 64-byte callback_data limit. Node
// names are user-defined, so callers must OMIT the button when ok is false
// and keep the long-name hint («длинное имя ноды — используйте /nodeset;
// часть кнопок недоступна») in the text. Wave-F B1/B2 reuse the same helper
// for the diag and node-subs surfaces (full callback incl. page suffix).
// (Wave-E, test-pinned by TestMenuNodesCfgCallback64ByteGuard).
func cbNodeCfg(prefix, name string, suffix ...string) (string, bool) {
	data := prefix + name
	if len(suffix) > 0 {
		data += suffix[0]
	}
	if len(data) > 64 {
		return "", false
	}
	return data, true
}

// Cycle ladders for the quick-action buttons on the per-node settings screen.
var (
	cfgMethodCycle      = []string{"ip", "status", "download"}
	cfgIntervalCycle    = []int{60, 300, 900, 3600}
	cfgConcurrencyCycle = []int{0, 2, 4, 8} // 0 = без лимита
)

func nextInStringCycle(cycle []string, cur string) string {
	for i, v := range cycle {
		if v == cur {
			return cycle[(i+1)%len(cycle)]
		}
	}
	return cycle[0]
}

func nextInIntCycle(cycle []int, cur string) int {
	n, err := strconv.Atoi(strings.TrimSpace(cur))
	if err == nil {
		for i, v := range cycle {
			if v == n {
				return cycle[(i+1)%len(cycle)]
			}
		}
	}
	return cycle[0]
}

func nextCheckMethod(cur string) string   { return nextInStringCycle(cfgMethodCycle, cur) }
func nextCheckInterval(cur string) string { return strconv.Itoa(nextInIntCycle(cfgIntervalCycle, cur)) }
func nextCheckConcurrency(cur string) string {
	return strconv.Itoa(nextInIntCycle(cfgConcurrencyCycle, cur))
}

// nodeCfgEntryValues maps NodeSettingsView entries to key → effective value.
func nodeCfgEntryValues(entries []NodeSettingEntry) map[string]string {
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		out[e.Key] = e.Value
	}
	return out
}

// NodesCfgListMarkup returns the node picker keyboard for the per-node
// settings section (mirrors NodesSubsListMarkup): one «🖥 <name>» button per
// node → menu:nodes:cfg:<name>, navigation row last.
func NodesCfgListMarkup(nodes []string) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	for _, name := range nodes {
		cb, ok := cbNodeCfg("menu:nodes:cfg:", name)
		if !ok {
			continue
		}
		rows = append(rows, tu.InlineKeyboardRow(
			btn("🖥 "+name, cb),
		))
	}
	rows = append(rows,
		tu.InlineKeyboardRow(
			btn("🔙 Назад", "menu:nodes"),
			btn("🏠 Главное меню", "menu:main"),
		),
	)
	return tu.InlineKeyboard(rows...)
}

// NodeCfgMarkup returns the keyboard for one node's settings screen. Cycle
// buttons carry the NEXT value of each ladder; each button whose callback
// would exceed the 64-byte limit is omitted individually (the text carries
// the /nodeset hint). Wave G5b/G5c adds the enable/disable toggle (action
// matching the current state) and the rename prompt button, both composed
// through cbNodeCfg with the same omit-on-overflow discipline.
func NodeCfgMarkup(node string, enabled bool, entries []NodeSettingEntry) *telego.InlineKeyboardMarkup {
	vals := nodeCfgEntryValues(entries)

	var rows [][]telego.InlineKeyboardButton
	var firstRow []telego.InlineKeyboardButton
	if cb, ok := cbNodeCfg("menu:nodes:subnode:", node); ok {
		firstRow = append(firstRow, btn("📋 Подписки ноды", cb))
	}
	if cb, ok := cbNodeCfg("menu:nodes:excl:", node); ok {
		firstRow = append(firstRow, btn("🚫 Исключения", cb))
	}
	if len(firstRow) > 0 {
		rows = append(rows, tu.InlineKeyboardRow(firstRow...))
	}

	if enabled {
		if cb, ok := cbNodeCfg("menu:nodes:tg:", node, ":off"); ok {
			rows = append(rows, tu.InlineKeyboardRow(
				btn("⏸️ Отключить ноду", cb),
			))
		}
	} else {
		if cb, ok := cbNodeCfg("menu:nodes:tg:", node, ":on"); ok {
			rows = append(rows, tu.InlineKeyboardRow(
				btn("▶️ Включить ноду", cb),
			))
		}
	}
	if cb, ok := cbNodeCfg("menu:nodes:ren:", node); ok {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("✏️ Переименовать", cb),
		))
	}

	if cb, ok := cbNodeCfg("menu:nodes:ncfg:", node, ":check_method:"+nextCheckMethod(vals["check_method"])); ok {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("♻️ Метод: "+vals["check_method"], cb),
		))
	}
	if cb, ok := cbNodeCfg("menu:nodes:ncfg:", node, ":check_interval:"+nextCheckInterval(vals["check_interval"])); ok {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("⏱ Интервал: "+vals["check_interval"]+"с", cb),
		))
	}
	if cb, ok := cbNodeCfg("menu:nodes:ncfg:", node, ":check_concurrency:"+nextCheckConcurrency(vals["check_concurrency"])); ok {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("🧵 Параллелизм: "+concurrencyLabel(vals["check_concurrency"]), cb),
		))
	}

	if cb, ok := cbNodeCfg("menu:nodes:nreset:", node); ok {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("↩️ Сбросить переопределения", cb),
		))
	}
	rows = append(rows, tu.InlineKeyboardRow(
		btn("🔙 К списку нод", "menu:nodes:cfg"),
		btn("🏠 Главное меню", "menu:main"),
	))
	return tu.InlineKeyboard(rows...)
}

// concurrencyLabel renders the RU label for the concurrency value
// (0 = без лимита).
func concurrencyLabel(cur string) string {
	if n, err := strconv.Atoi(strings.TrimSpace(cur)); err == nil && n == 0 {
		return "без лимита"
	}
	return cur
}

// nodeExcludedProxyCount counts the master's disabled-proxy entries that are
// namespaced to this node (`node/` prefix in cfg.DisabledProxies). Disabled
// hosts are global-only (no per-node prefixing), so proxies only.
func (b *Bot) nodeExcludedProxyCount(node string) int {
	prefix := node + "/"
	n := 0
	for _, id := range b.GetConfig().DisabledProxies {
		if strings.HasPrefix(id, prefix) {
			n++
		}
	}
	return n
}

// getNodesCfgListView renders the node picker for the per-node settings
// section (callback menu:nodes:cfg, exact-match route).
func (b *Bot) getNodesCfgListView() (string, *telego.InlineKeyboardMarkup) {
	if b.nodeMgr == nil {
		return "⚙️ <b>Настройки нод</b>\n\nНоды не настроены.", NodesCfgListMarkup(nil)
	}
	nodes := b.nodeMgr.Nodes()
	if len(nodes) == 0 {
		return "⚙️ <b>Настройки нод</b>\n\nНоды не настроены.\nСначала подключите ноду через меню нод (<code>/nodeadd</code>).", NodesCfgListMarkup(nil)
	}
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	text := "⚙️ <b>Настройки нод</b>\n\n" +
		"Выберите ноду, чтобы посмотреть и изменить её локальные настройки " +
		"(переопределения мастера, подписки, исключения):"
	return text, NodesCfgListMarkup(names)
}

// nodeCfgCyclesFit reports whether all three quick-action cycle callbacks
// stay within the 64-byte limit for this node name.
func nodeCfgCyclesFit(node string, entries []NodeSettingEntry) bool {
	vals := nodeCfgEntryValues(entries)
	_, methodOK := cbNodeCfg("menu:nodes:ncfg:", node, ":check_method:"+nextCheckMethod(vals["check_method"]))
	_, intervalOK := cbNodeCfg("menu:nodes:ncfg:", node, ":check_interval:"+nextCheckInterval(vals["check_interval"]))
	_, concOK := cbNodeCfg("menu:nodes:ncfg:", node, ":check_concurrency:"+nextCheckConcurrency(vals["check_concurrency"]))
	return methodOK && intervalOK && concOK
}

// nodeEnabledByName looks up the node's enabled state from the manager's
// node list; unknown managers/nodes default to enabled (zero-value-safe).
func (b *Bot) nodeEnabledByName(node string) bool {
	if b.nodeMgr == nil {
		return true
	}
	for _, n := range b.nodeMgr.Nodes() {
		if n.Name == node {
			return !n.Disabled
		}
	}
	return true
}

// getNodeCfgScreen renders one node's settings screen: the /nodeset-style
// effective values with override marks, a per-node exclusions count, the
// /nodeset hint, and quick-action cycle buttons (callback menu:nodes:cfg:<name>).
func (b *Bot) getNodeCfgScreen(node string) (string, *telego.InlineKeyboardMarkup) {
	if b.nodeMgr == nil {
		return "Ноды не настроены.", NodesCfgListMarkup(nil)
	}
	entries, err := b.nodeMgr.NodeSettingsView(node)
	if err != nil {
		return "❌ " + escapeHTML(err.Error()), NodesCfgListMarkup(nil)
	}
	enabled := b.nodeEnabledByName(node)

	var sb strings.Builder
	fmt.Fprintf(&sb, "⚙️ <b>Настройки ноды %s</b>\n\n", escapeHTML(node))
	for _, e := range entries {
		mark := ""
		if e.Overridden {
			mark = " ✏️"
		}
		fmt.Fprintf(&sb, "• %s: <code>%s</code>%s\n", escapeHTML(e.Key), escapeHTML(e.Value), mark)
	}
	if !enabled {
		// Wave G5b: disabled nodes are excluded from merged metrics and node
		// alerts — make that consequence explicit on the screen.
		sb.WriteString("\n⏸️ Нода отключена — её прокси исключены из метрик и алертов мастера.\n")
	}
	fmt.Fprintf(&sb, "\n🚫 Исключено прокси: <b>%d</b>\n", b.nodeExcludedProxyCount(node))
	sb.WriteString("\n✏️ — переопределено на ноде; «↩️ Сбросить» возвращает к мастеру.\n")
	sb.WriteString("Точные значения: /nodeset &lt;имя&gt; &lt;ключ&gt; &lt;значение&gt;\n")
	// Wave-F B10: one unified hint wording for ALL omission branches
	// (exclusions / cycle buttons / Wave-G5 toggle and rename buttons omitted).
	_, exclOK := cbNodeCfg("menu:nodes:excl:", node)
	_, toggleOK := cbNodeCfg("menu:nodes:tg:", node, ":off")
	_, renameOK := cbNodeCfg("menu:nodes:ren:", node)
	if !exclOK || !nodeCfgCyclesFit(node, entries) || !toggleOK || !renameOK {
		sb.WriteString("\n(длинное имя ноды — используйте /nodeset; часть кнопок недоступна)\n")
	}

	return sb.String(), NodeCfgMarkup(node, enabled, entries)
}

// getNodeExclusionsView renders the read-only list of proxy names excluded
// on this node (cfg.DisabledProxies entries namespaced `node/`), resolved to
// display names via the merged metrics snapshot. Toggling stays in the
// master exclusions screens (callback menu:nodes:excl:<name>).
func (b *Bot) getNodeExclusionsView(node string) (string, *telego.InlineKeyboardMarkup) {
	prefix := node + "/"
	cfg := b.GetConfig()

	var snapshot []metrics.ProxyMetric
	if b.source != nil {
		snapshot = b.source.MetricsSnapshot()
	}
	resolveName := func(id string) string {
		for _, pm := range snapshot {
			if pm.StableID == id {
				if pm.Name != "" {
					return pm.Name
				}
				return pm.Address
			}
		}
		return ""
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🚫 <b>Исключения ноды %s</b>\n\n", escapeHTML(node))

	count := 0
	for _, id := range cfg.DisabledProxies {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		count++
		localID := strings.TrimPrefix(id, prefix)
		if name := resolveName(id); name != "" {
			fmt.Fprintf(&sb, "• %s\n", escapeHTML(name))
		} else {
			fmt.Fprintf(&sb, "• %s <i>(имя недоступно — нода не отчитывалась)</i>\n", escapeHTML(localID))
		}
	}
	if count == 0 {
		sb.WriteString("У этой ноды нет исключённых прокси.\n")
	}

	sb.WriteString("\n<i>Переключение исключений доступно только в мастер-экранах: " +
		"команда /togglenode и «Управление прокси» в глобальных настройках.</i>\n")

	return sb.String(), NodeExclMarkup(node)
}

// NodeExclMarkup returns the navigation keyboard for the read-only
// exclusions view: back to the per-node settings screen. The back button is
// omitted if its callback would exceed the 64-byte limit (practically
// unreachable: the picker admits names that always fit here).
func NodeExclMarkup(node string) *telego.InlineKeyboardMarkup {
	var row []telego.InlineKeyboardButton
	if cb, ok := cbNodeCfg("menu:nodes:cfg:", node); ok {
		row = append(row, btn("🔙 К настройкам ноды", cb))
	}
	row = append(row, btn("🏠 Главное меню", "menu:main"))
	return tu.InlineKeyboard(tu.InlineKeyboardRow(row...))
}
