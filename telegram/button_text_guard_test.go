package telegram

// Guardian re-gate 2026-11-18 (K+L hotfix wave): structural pin for the
// "raw <tg-emoji> HTML inside InlineKeyboardButton.Text" defect class.
//
// Contract: button text has NO parse_mode — it renders verbatim, so a
// renderIcon(...) output (raw <tg-emoji> when the ✨ layer is on) is shown to
// users as literal markup. The correct shape is PLAIN Icon.Fallback in Text,
// with the animated icon attached ONLY via WithIconCustomEmojiID (gated
// enabled && CustomID != "") — exactly what btnIcon does.
//
// Two complementary guards live here:
//  1. TestNoRawTgEmojiInAnyButtonText — runtime: walks the FULL markup
//     inventory with the ✨ layer FORCED ON; every button Text must be free of
//     raw tg-emoji tags and HTML entities, and every IconCustomEmojiID must be
//     digit-only-or-empty.
//  2. TestNoRenderIconInButtonConstructionSource — source-level: menu.go and
//     callbacks.go construct ONLY buttons (zero message-text renderIcon uses),
//     so the enforceable form there is a TOTAL textual ban of `renderIcon(` —
//     any occurrence (including comments, which currently carry none) means
//     the class regressed.

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// guardInventoryButtons constructs the full markup inventory (every exported
// markup builder plus the shared row builders and Bot-method screens) and
// returns all buttons flattened. NEW markup builders MUST be registered here —
// the sub-100 sanity floor makes a silent inventory shrink loud.
func guardInventoryButtons(t *testing.T) []telego.InlineKeyboardButton {
	t.Helper()

	var buttons []telego.InlineKeyboardButton
	add := func(m *telego.InlineKeyboardMarkup) {
		if m == nil {
			t.Errorf("nil markup in inventory")
			return
		}
		for _, row := range m.InlineKeyboard {
			buttons = append(buttons, row...)
		}
	}
	addRow := func(row []telego.InlineKeyboardButton) {
		buttons = append(buttons, row...)
	}

	cfg := BotConfig{CheckIntervalSec: 300}
	cfgBgOn := cfg
	cfgBgOn.CheckHostBgEnabled = true
	cfgQuietOn := cfg
	cfgQuietOn.QuietHoursEnabled = true
	cfgSnoozed := cfg
	cfgSnoozed.QuietSnoozeUntil = time.Now().Add(time.Hour).Unix()
	cfgLive := cfg
	cfgLive.AlertMode = AlertModeLive

	// Bot-method screens need only a metrics source (GetConfig degrades to
	// DefaultBotConfig with a nil configMgr — graceful-degradation invariant).
	b := &Bot{source: &mockSource{metrics: []metrics.ProxyMetric{
		{Name: "P1", StableID: "m31a/p1", Address: "1.1.1.1:443", NodeName: "m31a"},
		{Name: "P2", StableID: "m31a/p2", Address: "2.2.2.2:443", NodeName: "m31a"},
	}}}

	reports := []checker.ProxyDiagReport{
		{ProxyName: "OK-proxy", StableID: "ok", Status: "online"},
		{ProxyName: "Down-proxy", StableID: "down", Status: "offline"},
		{ProxyName: "Degraded-proxy", StableID: "degr", Status: "degraded"},
		{ProxyName: "Disabled-proxy", StableID: "dis", Status: "disabled"},
	}
	tabs := []NodeTabItem{
		{ID: "local", Label: "🏠 Мастер", IsActive: true},
		{ID: "m31a", Label: "🖥 m31a", Status: "1/2"},
	}
	entries := []NodeSettingEntry{
		{Key: "check_method", Value: "ip"},
		{Key: "check_interval", Value: "300"},
		{Key: "check_concurrency", Value: "0"},
	}

	// --- menus cluster (menu.go) ---
	add(MainMenuMarkup())
	add(NodesMainMenuMarkup())
	add(NodesInstallMarkup())
	add(NodesHealthMarkup())
	add(NodesSettingsMarkup(cfg))
	add(NodeSubsManageMarkup("m31a", []ManagedSubInfo{{URL: "https://s/x", ProxyCount: 3}}))
	add(NodeSubsManageMarkup("very-long-node-name-that-overflows-cbNodeCfg-limit", nil))
	add(SettingsMenuMarkup(cfg, true))
	add(SettingsMenuMarkupWithCfg(cfg, false))
	add(CheckHostSettingsMarkup(cfg))
	add(CheckHostSettingsMarkup(cfgBgOn))
	add(DisabledProxiesMarkup([]ProxyToggleItem{
		{StableID: "p1", Name: "Enabled-proxy", Protocol: "vless", Address: "1.1.1.1:443"},
		{StableID: "p2", Name: "Disabled-proxy", Protocol: "vless", Address: "2.2.2.2:443", Disabled: true},
	}, 2, 3))
	add(DisabledHostsMarkup([]string{"host-a.example.com", "host-b.example.com"},
		map[string]bool{"host-b.example.com": true}, 1, 2))
	add(StatusMenuMarkup())
	add(StatsMenuMarkup())
	add(QuietHoursMarkup(cfg))
	add(QuietHoursMarkup(cfgQuietOn))
	add(QuietHoursMarkup(cfgSnoozed))
	add(AlertModeMarkup(cfg))
	add(AlertModeMarkup(cfgLive))
	add(TargetsMenuMarkup())
	add(BackToMenuMarkup())
	add(BackToSettingsMarkup())
	add(BackToStatsMarkup())
	add(SubsMenuMarkup())
	add(TimezoneMarkup("Europe/Moscow"))
	add(IntervalMenuMarkup(60))
	add(CheckHostMenuMarkup([]CheckHostListItem{
		{Address: "auto.example.com:443", Auto: true},
		{Address: "custom.example.com:443"},
	}, 1, 1))
	add(CheburMenuMarkup([]string{"chebur-a.example.com", "chebur-b.example.com"}, 1, 1))
	add(CheburResultMarkup(2))
	add(DiagPaginationMarkup(2, 3))
	add(DeepDiagnosticsMarkup("local", 1))
	add(DeepDiagnosticsMarkup("m31a", 1))
	add(PickDeepDiagnosticsMarkup(reports, 1))
	add(PickDeepDiagnosticsMarkupWithTabs("local", tabs, reports, 1))
	add(PickDeepDiagnosticsMarkupWithTabs("m31a", tabs, reports, 2))
	add(CheckHostResultMarkup())
	add(RichReportMarkup(DiagDeepLink{Name: "Bad-proxy", StableID: "bad"}))
	add(RichReportMarkupWithTabs("local", tabs, DiagDeepLink{Name: "Bad-proxy", StableID: "bad"}))
	add(DiagnosticsPaginationMarkupWithTabs("local", 2, 3, tabs))
	add(DiagnosticsPaginationMarkupWithTabs("m31a", 2, 3, tabs))
	add(IncidentsFilterMarkup("m31a", tabs))

	// --- node cfg cluster (node_cfg_menu.go) ---
	add(NodesCfgListMarkup([]string{"m31a"}))
	add(NodeCfgMarkup("m31a", true, entries))
	add(NodeCfgMarkup("m31a", false, entries))
	add(nodeSubsConfirmMarkup("m31a", 0))
	add(nodeResetConfirmMarkup("m31a"))

	// --- node commands (nodes_commands.go) ---
	add(nodeAddCopyMarkup("m31a", "tok-1234567890abcdef", "https://master.example.com:2112/report"))

	// --- views cluster (views.go) ---
	add(StatusPageMarkup(2, 3))
	_, exclView := b.getNodeExclusionsView("m31a", 1, "")
	add(exclView)

	// --- shared row builders (both files) ---
	addRow(backRow("Главное меню", "menu:main"))
	addRow(backRow("К настройкам", "menu:settings"))
	addRow(backRow("К нодам", "menu:nodes"))
	addRow(backRow("К статистике", "menu:stats"))
	addRow(menuPaginationRow("menu:guardpg:", 2, 3))
	addRow(viewPagerRow("menu:statuspg:", 2, 3))
	add(withPagerRow(IncidentsFilterMarkup("m31a", tabs), incidentsPagerPrefix("m31a"), 2, 3))

	return buttons
}

// isAllDigits reports whether s is non-empty and consists of ASCII digits only
// (the Telegram custom-emoji id format).
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// TestNoRawTgEmojiInAnyButtonText walks the full markup inventory with the ✨
// custom-emoji layer FORCED ON: button text must stay plain (no raw <tg-emoji>
// tags — the button text has NO parse_mode — and no HTML entities), and the
// animated icon may ride ONLY in IconCustomEmojiID (digit-only-or-empty).
func TestNoRawTgEmojiInAnyButtonText(t *testing.T) {
	SetCustomEmojiEnabled(true)
	t.Cleanup(func() { SetCustomEmojiEnabled(false) })

	buttons := guardInventoryButtons(t)
	if len(buttons) < 100 {
		t.Fatalf("markup inventory shrank to %d buttons — register new markup builders in guardInventoryButtons", len(buttons))
	}
	for i, b := range buttons {
		if strings.Contains(b.Text, "<tg-emoji") {
			t.Errorf("button #%d (%q): raw <tg-emoji> tag in button Text — text has NO parse_mode, users see the literal tag; put Icon.Fallback in Text and the id in WithIconCustomEmojiID (btnIcon contract)", i, b.Text)
		}
		if strings.Contains(b.Text, "&lt;") {
			t.Errorf("button #%d (%q): escaped HTML entity &lt; in button Text — button labels are verbatim, not HTML", i, b.Text)
		}
		if b.IconCustomEmojiID != "" && !isAllDigits(b.IconCustomEmojiID) {
			t.Errorf("button #%d (%q): IconCustomEmojiID %q must be digit-only-or-empty", i, b.Text, b.IconCustomEmojiID)
		}
	}

	// Cross-check the correct dual-layer shape on a known btnIcon row: with the
	// layer on, the Check-Host button carries the plain fallback in Text AND
	// the pack id in IconCustomEmojiID.
	var checkhost *telego.InlineKeyboardButton
	for i := range buttons {
		if buttons[i].CallbackData == "menu:checkhost" {
			checkhost = &buttons[i]
			break
		}
	}
	if checkhost == nil {
		t.Fatalf("inventory lost the Check-Host main-menu button")
	}
	if checkhost.IconCustomEmojiID != IconCheckHost.CustomID {
		t.Errorf("enabled layer must attach IconCustomEmojiID %q to the Check-Host button, got %q", IconCheckHost.CustomID, checkhost.IconCustomEmojiID)
	}
	if !strings.HasPrefix(checkhost.Text, IconCheckHost.Fallback) {
		t.Errorf("enabled Check-Host label must carry the plain fallback %q, got %q", IconCheckHost.Fallback, checkhost.Text)
	}
}

// TestButtonTextDisabledLayerPlain pins byte-identity of the DISABLED layer:
// with the ✨ layer off every label is the plain fallback text (identical to
// the pre-K+L rendering) and no button carries an icon id.
func TestButtonTextDisabledLayerPlain(t *testing.T) {
	SetCustomEmojiEnabled(false)
	t.Cleanup(func() { SetCustomEmojiEnabled(false) })

	buttons := guardInventoryButtons(t)
	for i, b := range buttons {
		if strings.Contains(b.Text, "tg-emoji") {
			t.Errorf("disabled layer must render plain fallback emojis, button #%d got %q", i, b.Text)
		}
		if b.IconCustomEmojiID != "" {
			t.Errorf("disabled layer must not attach IconCustomEmojiID, button #%d (%q) got %q", i, b.Text, b.IconCustomEmojiID)
		}
	}

	// Byte-exact pins for backRow — THE most frequent rows in the bot. The
	// plain path must render exactly the pre-fix bytes: "↩️ " + label.
	single := backRow("Главное меню", "menu:main")
	if len(single) != 1 || single[0].Text != "↩️ Главное меню" || single[0].CallbackData != "menu:main" {
		t.Errorf("backRow(main) disabled layer drifted: %+v", single)
	}
	pair := backRow("К настройкам", "menu:settings")
	want := []struct{ text, data string }{
		{"↩️ К настройкам", "menu:settings"},
		{mainMenuBtnText, "menu:main"},
	}
	if len(pair) != len(want) {
		t.Fatalf("backRow(parent) must render %d buttons, got %d", len(want), len(pair))
	}
	for i, w := range want {
		if pair[i].Text != w.text || pair[i].CallbackData != w.data {
			t.Errorf("backRow(parent)[%d] disabled layer drifted: got (%q, %q), want (%q, %q)", i, pair[i].Text, pair[i].CallbackData, w.text, w.data)
		}
	}
}

// TestNoRenderIconInButtonConstructionSource is the source-level guard:
// menu.go and callbacks.go construct ONLY inline-keyboard buttons (no
// message-text rendering), so ANY occurrence of `renderIcon(` in them — code
// or comment — is a regression of the raw-tg-emoji-in-button-text defect
// class. Message-text renderIcon uses live in views.go / diagnostics.go /
// checkhost.go and stay legitimate.
func TestNoRenderIconInButtonConstructionSource(t *testing.T) {
	for _, name := range []string{"menu.go", "callbacks.go"} {
		src, err := os.ReadFile(name) // Go tests run with CWD = package dir
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "renderIcon(") {
				t.Errorf("%s:%d: renderIcon( is banned in button-construction files — its output is raw <tg-emoji> HTML and button text has NO parse_mode; use Icon.Fallback in Text or route through btnIcon", name, i+1)
			}
		}
	}
}
