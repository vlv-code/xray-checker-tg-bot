package telegram

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
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

// cbNodeKeyMaxRawLen caps the node-name length that stays raw inside
// exclusion callbacks: the widest raw composition
// («menu:nodes:nx:<name>:<3d>:<3d>») must stay under the 64-byte limit.
const cbNodeKeyMaxRawLen = 32

// cbNodeKey maps a node name to the callback key used by the per-node
// exclusions screen (Wave H1): short names (≤ cbNodeKeyMaxRawLen, the
// validNodeName maximum) are used raw so callbacks stay human-debuggable;
// longer names (possible via unvalidated NODES env) hash deterministically
// to a fixed "n:"+12-hex key so the composed toggle/pager callbacks always
// fit Telegram's hard 64-byte callback_data limit. nodeByKey resolves keys
// back to node names, so hashing is transparent end-to-end.
func cbNodeKey(node string) string {
	if len(node) <= cbNodeKeyMaxRawLen {
		return node
	}
	sum := sha256.Sum256([]byte(node))
	return "n:" + hex.EncodeToString(sum[:6])
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
// settings section (Wave H2/D4: the primary node-picker surface since the
// global «Подписки нод» screen was removed): one «🖥 <name>» button per
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

// nodeExclProxies returns the node's own proxies from the merged metrics
// snapshot (pm.NodeName == node), sorted by StableID. This fixed order is the
// row order of the exclusions screen and the index basis of the
// menu:nodes:nx: toggle callbacks (Wave H1).
func (b *Bot) nodeExclProxies(node string) []metrics.ProxyMetric {
	var list []metrics.ProxyMetric
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.NodeName == node {
				list = append(list, pm)
			}
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].StableID < list[j].StableID })
	return list
}

// nodeExclEntry normalizes the cfg.DisabledProxies entry for a node proxy:
// in the merged snapshot a node proxy's StableID is already namespaced
// `node/localID` (nodes/report.go) — exactly the per-node exclusion format;
// the defensive composition covers snapshots that report a bare local ID.
func nodeExclEntry(node, stableID string) string {
	if strings.HasPrefix(stableID, node+"/") {
		return stableID
	}
	return node + "/" + stableID
}

// nodeByKey resolves a cbNodeKey callback key back to the node's display
// name (raw names match directly; hashed keys are matched by re-deriving
// cbNodeKey over the registered node names). Returns "" for unknown keys.
func (b *Bot) nodeByKey(key string) string {
	if b.nodeMgr == nil {
		return ""
	}
	for _, n := range b.nodeMgr.Nodes() {
		if n.Name == key || cbNodeKey(n.Name) == key {
			return n.Name
		}
	}
	return ""
}

// getNodeExclusionsView renders the FUNCTIONAL per-proxy exclusions screen
// for one node (Wave H1, decision D1): every proxy the node reported (merged
// snapshot, sorted by StableID) becomes a «⏸️/▶️» toggle row reflecting the
// current prefixed cfg.DisabledProxies state (`node/stableID` — the entry is
// synced to the node, which stops checking that proxy). menuListPageSize rows
// per page, pager route menu:nodes:exclpg:<key>:<page>, toggle route
// menu:nodes:nx:<key>:<idx>:<page>. The notice argument carries an optional
// state warning («Список изменился» after a stale-index tap).
func (b *Bot) getNodeExclusionsView(node string, page int, notice string) (string, *telego.InlineKeyboardMarkup) {
	list := b.nodeExclProxies(node)
	excludedTotal := b.nodeExcludedProxyCount(node)
	totalPages := menuTotalPages(len(list), menuListPageSize)
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	pageProxies := menuPageSlice(list, page, totalPages)

	var sb strings.Builder
	fmt.Fprintf(&sb, "🚫 <b>Исключения ноды %s</b>\n\n", escapeHTML(node))
	if notice != "" {
		fmt.Fprintf(&sb, "%s\n\n", notice)
	}
	if len(list) == 0 {
		sb.WriteString("У этой ноды пока нет прокси: нода ещё не отчиталась или её подписки пусты.\n")
	} else {
		fmt.Fprintf(&sb, "Всего прокси: <b>%d</b> · Исключено: <b>%d</b>\n", len(list), excludedTotal)
		if totalPages > 1 {
			fmt.Fprintf(&sb, "Стр. %d/%d\n", page, totalPages)
		}
		sb.WriteString("\nНажмите ⏸️/▶️, чтобы исключить прокси из проверки на ноде или вернуть его " +
			"(изменение применится при следующем отчёте ноды).\n")
	}

	disabled := make(map[string]bool, len(b.GetConfig().DisabledProxies))
	for _, id := range b.GetConfig().DisabledProxies {
		disabled[id] = true
	}

	var rows [][]telego.InlineKeyboardButton
	hintNeeded := false
	for i, pm := range pageProxies {
		idx := (page-1)*menuListPageSize + i
		entry := nodeExclEntry(node, pm.StableID)
		name := pm.Name
		if name == "" {
			name = pm.Address
		}
		label := "▶️ " + truncateButtonText(name, 28)
		if disabled[entry] {
			label = "⏸️ " + truncateButtonText(name, 28)
		}
		cb, ok := cbNodeCfg("menu:nodes:nx:", cbNodeKey(node), ":"+strconv.Itoa(idx)+":"+strconv.Itoa(page))
		if !ok {
			// Defensive: cbNodeKey guarantees the fit; omit rather than emit a
			// dead button if the composition ever overflows.
			hintNeeded = true
			continue
		}
		rows = append(rows, tu.InlineKeyboardRow(btn(label, cb)))
	}

	key := cbNodeKey(node)
	if totalPages > 1 {
		if _, ok := cbNodeCfg("menu:nodes:exclpg:", key, ":99"); ok {
			rows = append(rows, menuPaginationRow("menu:nodes:exclpg:"+key+":", page, totalPages))
		} else {
			hintNeeded = true
		}
	}

	var navRow []telego.InlineKeyboardButton
	if cb, ok := cbNodeCfg("menu:nodes:cfg:", node); ok {
		navRow = append(navRow, btn("🔙 К настройкам ноды", cb))
	}
	navRow = append(navRow, btn("🏠 Главное меню", "menu:main"))
	rows = append(rows, tu.InlineKeyboardRow(navRow...))

	if hintNeeded {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("ℹ️ длинное имя ноды — часть кнопок недоступна", "menu:noop"),
		))
	}

	return sb.String(), tu.InlineKeyboard(rows...)
}

// handleNodeExclToggle applies one menu:nodes:nx:<key>:<idx>:<page> tap
// (Wave H1). Mutating route (admin-gated via isMutatingCallback) and
// self-toasting (isToastCallbackData). The write goes ONLY through
// updateConfig (add/remove the `node/stableID` entry in cfg.DisabledProxies)
// — never nodeMgr settings. SAFETY ECHO against index shifts: the toast
// resolves the proxy name at tap time, and a stale (out-of-range) index
// re-renders the screen with a «Список изменился» notice without writing.
func (b *Bot) handleNodeExclToggle(chatID int64, msgID int, cb *telego.CallbackQuery, data string) {
	rest := strings.TrimPrefix(data, "menu:nodes:nx:")
	// Robust parse: idx and page sit after the LAST two colons, so raw node
	// keys containing ':' (NODES env names) round-trip — same discipline as
	// the delsub/tg parsers.
	sep := strings.LastIndex(rest, ":")
	sep2 := strings.LastIndex(rest[:sep], ":")
	if sep == -1 || sep2 == -1 {
		return
	}
	page, perr := strconv.Atoi(rest[sep+1:])
	idx, ierr := strconv.Atoi(rest[sep2+1 : sep])
	if perr != nil || ierr != nil || page < 1 || idx < 0 {
		return
	}
	node := b.nodeByKey(rest[:sep2])
	if node == "" {
		if b.api != nil {
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("❌ Нода не найдена"))
		}
		return
	}

	list := b.nodeExclProxies(node)
	if idx >= len(list) {
		// Stale index (subscription set changed between render and tap):
		// never write, never confirm — re-render with the shift notice.
		if b.api != nil {
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("⚠️ Список изменился — обновите экран"))
		}
		text, markup := b.getNodeExclusionsView(node, page, "⚠️ Список изменился — обновите экран.")
		b.editWithMarkup(chatID, msgID, text, markup)
		return
	}

	pm := list[idx]
	entry := nodeExclEntry(node, pm.StableID)
	name := pm.Name
	if name == "" {
		name = pm.Address
	}
	var excluded bool
	_ = b.updateConfig(func(c *BotConfig) {
		removed := false
		for i, id := range c.DisabledProxies {
			if id == entry {
				c.DisabledProxies = append(c.DisabledProxies[:i], c.DisabledProxies[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			c.DisabledProxies = append(c.DisabledProxies, entry)
		}
		excluded = !removed
	})
	toast := "✅ Возвращён: " + name
	if excluded {
		toast = "✅ Исключён: " + name
	}
	if b.api != nil {
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText(toast))
	}
	text, markup := b.getNodeExclusionsView(node, page, "")
	b.editWithMarkup(chatID, msgID, text, markup)
}
