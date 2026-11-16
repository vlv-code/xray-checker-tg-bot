package telegram

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// cfgRecordingManager is a NodeManager test double with a real settings
// state model (per-node effective values + override marks) that records
// every SetNodeSetting/ResetNodeSetting call, so Wave-E tests can assert
// that the UI never writes settings by any other path (nodeMgr is the
// only settings writer).
type cfgRecordingManager struct {
	nodes    map[string]bool
	values   map[string]map[string]string // node -> key -> effective value
	overrid  map[string]map[string]bool   // node -> key -> overridden
	setCalls []string                     // "node|key|value"
	resets   []string                     // "node|key"
}

func newCfgRecordingManager(names ...string) *cfgRecordingManager {
	m := &cfgRecordingManager{
		nodes:   map[string]bool{},
		values:  map[string]map[string]string{},
		overrid: map[string]map[string]bool{},
	}
	for _, n := range names {
		m.nodes[n] = true
	}
	return m
}

func (m *cfgRecordingManager) Nodes() []NodeInfo {
	var out []NodeInfo
	for name := range m.nodes {
		out = append(out, NodeInfo{Name: name, Up: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *cfgRecordingManager) ManagedSubs(node string) ([]ManagedSubInfo, error) {
	if !m.nodes[node] {
		return nil, fmt.Errorf("неизвестная нода: %s", node)
	}
	return nil, nil
}

func (m *cfgRecordingManager) AddSub(node, url string) error                         { return nil }
func (m *cfgRecordingManager) RemoveSub(node, url string) error                      { return nil }
func (m *cfgRecordingManager) AddNode(name, token string) error                      { m.nodes[name] = true; return nil }
func (m *cfgRecordingManager) RemoveNode(name string) error                          { delete(m.nodes, name); return nil }
func (m *cfgRecordingManager) NodeSnapshot(node string) []metrics.ProxyMetric        { return nil }
func (m *cfgRecordingManager) NodeDiagReports(node string) []checker.ProxyDiagReport { return nil }

var cfgViewKeys = []string{"check_interval", "check_method", "check_concurrency", "download_url"}

func (m *cfgRecordingManager) cfgValue(node, key, def string) string {
	if vals, ok := m.values[node]; ok {
		if v, ok := vals[key]; ok {
			return v
		}
	}
	return def
}

func (m *cfgRecordingManager) NodeSettingsView(node string) ([]NodeSettingEntry, error) {
	if !m.nodes[node] {
		return nil, fmt.Errorf("неизвестная нода: %s", node)
	}
	defaults := map[string]string{
		"check_interval":    "300",
		"check_method":      "ip",
		"check_concurrency": "0",
		"download_url":      "https://dl.example/f",
	}
	out := make([]NodeSettingEntry, 0, len(cfgViewKeys))
	for _, k := range cfgViewKeys {
		e := NodeSettingEntry{Key: k, Value: m.cfgValue(node, k, defaults[k])}
		e.Overridden = m.overrid[node][k]
		out = append(out, e)
	}
	return out, nil
}

func (m *cfgRecordingManager) SetNodeSetting(node, key, value string) error {
	if !m.nodes[node] {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	m.setCalls = append(m.setCalls, node+"|"+key+"|"+value)
	if m.values[node] == nil {
		m.values[node] = map[string]string{}
	}
	if m.overrid[node] == nil {
		m.overrid[node] = map[string]bool{}
	}
	m.values[node][key] = value
	m.overrid[node][key] = true
	return nil
}

func (m *cfgRecordingManager) ResetNodeSetting(node, key string) error {
	if !m.nodes[node] {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	m.resets = append(m.resets, node+"|"+key)
	if key == "" {
		delete(m.values, node)
		delete(m.overrid, node)
		return nil
	}
	delete(m.values[node], key)
	delete(m.overrid[node], key)
	return nil
}

func newCfgTestBot(t *testing.T, api *telego.Bot, adminIDs []int64, mgr NodeManager) (*Bot, *ConfigManager) {
	t.Helper()
	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{CheckIntervalSec: 300})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	b := &Bot{
		api:            api,
		ctx:            context.Background(),
		targets:        []ChatTarget{{ChatID: 12345}},
		allowedChatIDs: map[int64]bool{12345: true},
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "Proxy-X", StableID: "proxy-x", Address: "1.1.1.1:443", Protocol: "vless", Online: true},
		}},
		configMgr:   cm,
		tracker:     NewAlertTracker(),
		eventBuffer: NewEventBuffer(),
		lastSeen:    make(map[string]bool),
		nodeMgr:     mgr,
	}
	b.SetAdminUserIDs(adminIDs)
	return b, cm
}

// markupCallbacks walks the inline keyboard and returns every callback_data.
func markupCallbacks(markup *telego.InlineKeyboardMarkup) []string {
	var out []string
	if markup == nil {
		return nil
	}
	for _, row := range markup.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.CallbackData)
		}
	}
	return out
}

func hasCallback(markup *telego.InlineKeyboardMarkup, data string) bool {
	for _, d := range markupCallbacks(markup) {
		if d == data {
			return true
		}
	}
	return false
}

// TestMenuNodesCfgOpensNodeList: the exact callback menu:nodes:cfg opens the
// node picker (one «🖥 <name>» button per node → menu:nodes:cfg:<name>, back
// row to menu:nodes); an empty registry renders «Ноды не настроены».
func TestMenuNodesCfgOpensNodeList(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("m31a", "msk-1")
	b, _ := newCfgTestBot(t, api, []int64{1001}, mgr)

	// View route must stay open to a non-admin even with admins configured.
	b.handleCallbackQuery(gateCallback(9999, "menu:nodes:cfg"))

	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected menu:nodes:cfg to render the node picker, got 0 edits")
	}
	text, _ := edits[0].Params["text"].(string)
	if !strings.Contains(text, "Выберите ноду") {
		t.Errorf("expected picker prompt in text, got: %s", text)
	}
	if strings.Contains(text, "Настройки ноды m31a") {
		t.Errorf("picker must not render a per-node screen, got: %s", text)
	}

	// Unit: picker markup carries one button per node and a back row.
	markup := NodesCfgListMarkup([]string{"m31a", "msk-1"})
	if !hasCallback(markup, "menu:nodes:cfg:m31a") || !hasCallback(markup, "menu:nodes:cfg:msk-1") {
		t.Errorf("expected per-node buttons in picker markup, got %v", markupCallbacks(markup))
	}
	if markup.InlineKeyboard[0][0].Text != "🖥 m31a" {
		t.Errorf("expected node button text «🖥 m31a», got %q", markup.InlineKeyboard[0][0].Text)
	}
	lastRow := markup.InlineKeyboard[len(markup.InlineKeyboard)-1]
	backFound := false
	for _, b := range lastRow {
		if b.CallbackData == "menu:nodes" {
			backFound = true
		}
	}
	if !backFound {
		t.Errorf("expected a back button to menu:nodes in the last row, got %v", lastRow)
	}

	// Empty registry: «Ноды не настроены».
	ftEmpty, apiEmpty := newFakeTelegramBot(t)
	bEmpty, _ := newCfgTestBot(t, apiEmpty, nil, newCfgRecordingManager())
	bEmpty.handleCallbackQuery(gateCallback(9999, "menu:nodes:cfg"))
	emptyEdits := ftEmpty.callsOf("editMessageText")
	if len(emptyEdits) == 0 {
		t.Fatalf("expected empty-registry picker to render")
	}
	emptyText, _ := emptyEdits[0].Params["text"].(string)
	if !strings.Contains(emptyText, "Ноды не настроены") {
		t.Errorf("expected «Ноды не настроены» for empty registry, got: %s", emptyText)
	}
}

// TestMenuNodesCfgPrefixOrdering is the Wave-E routing-order regression pin:
// menu:nodes:cfg:<name> must reach the per-node screen prefix branch and
// must NOT be swallowed by the exact menu:nodes:cfg picker rule, and the bare
// menu:nodes:cfg must open the picker, never a per-node screen.
func TestMenuNodesCfgPrefixOrdering(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("m31a")
	b, _ := newCfgTestBot(t, api, nil, mgr)

	// 1. Prefixed data routes to the per-node screen.
	b.handleCallbackQuery(gateCallback(9999, "menu:nodes:cfg:m31a"))
	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected menu:nodes:cfg:m31a to render the node screen")
	}
	text, _ := edits[0].Params["text"].(string)
	if !strings.Contains(text, "Настройки ноды m31a") {
		t.Errorf("expected per-node screen for menu:nodes:cfg:m31a, got: %s", text)
	}
	if strings.Contains(text, "Выберите ноду") {
		t.Errorf("per-node callback must not open the picker, got: %s", text)
	}

	// 2. Bare data opens the picker, never a node screen.
	ft2, api2 := newFakeTelegramBot(t)
	b2, _ := newCfgTestBot(t, api2, nil, mgr)
	b2.handleCallbackQuery(gateCallback(9999, "menu:nodes:cfg"))
	edits2 := ft2.callsOf("editMessageText")
	if len(edits2) == 0 {
		t.Fatalf("expected bare menu:nodes:cfg to render the picker")
	}
	text2, _ := edits2[0].Params["text"].(string)
	if !strings.Contains(text2, "Выберите ноду") {
		t.Errorf("expected picker for bare menu:nodes:cfg, got: %s", text2)
	}
	if strings.Contains(text2, "Настройки ноды") {
		t.Errorf("bare menu:nodes:cfg must not render a per-node screen, got: %s", text2)
	}
}

// TestMenuNodesCfgNodeScreenRenders: menu:nodes:cfg:m31a renders the
// /nodeset-style screen (header, entries, ✏️ override mark, exclusions count
// line, /nodeset hint) with subs/excl/cycle/reset/back buttons.
func TestMenuNodesCfgNodeScreenRenders(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("m31a")
	_ = mgr.SetNodeSetting("m31a", "check_method", "status") // override → ✏️
	b, cm := newCfgTestBot(t, api, []int64{1001}, mgr)
	_ = cm.Update(func(c *BotConfig) {
		c.DisabledProxies = []string{"m31a/idx1", "other/zzz"}
	})

	// View route must stay open to a non-admin even with admins configured.
	b.handleCallbackQuery(gateCallback(9999, "menu:nodes:cfg:m31a"))

	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected node screen render, got 0 edits")
	}
	text, _ := edits[0].Params["text"].(string)
	for _, want := range []string{
		"Настройки ноды m31a",
		"check_interval: <code>300</code>",
		"check_method: <code>status</code> ✏️",
		"🚫 Исключено прокси: <b>1</b>",
		"переопределено на ноде",
		"Точные значения: /nodeset",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected node screen text to contain %q, got: %s", want, text)
		}
	}

	// Screen markup: subs link, exclusions view, cycles, reset, back.
	_, markup := b.getNodeCfgScreen("m31a")
	for _, want := range []string{
		"menu:nodes:subnode:m31a",
		"menu:nodes:excl:m31a",
		"menu:nodes:ncfg:m31a:check_method:download", // current status → next download
		"menu:nodes:ncfg:m31a:check_interval:900",    // current 300 → next 900
		"menu:nodes:ncfg:m31a:check_concurrency:2",   // current 0 → next 2
		"menu:nodes:nreset:m31a",
		"menu:nodes:cfg",
	} {
		if !hasCallback(markup, want) {
			t.Errorf("expected node screen markup to contain callback %q, got %v", want, markupCallbacks(markup))
		}
	}
}

// TestMenuNodesCfgCycleAppliesSetting: tapping a cycle callback applies the
// next value through nodeMgr.SetNodeSetting (the only settings writer) and
// re-renders the screen with the new effective value.
func TestMenuNodesCfgCycleAppliesSetting(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("m31a") // effective check_method = ip
	b, _ := newCfgTestBot(t, api, nil, mgr)

	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:ncfg:m31a:check_method:status"))

	want := "m31a|check_method|status"
	found := false
	for _, c := range mgr.setCalls {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected SetNodeSetting(%q), got calls %v", want, mgr.setCalls)
	}

	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected the node screen to re-render after the cycle")
	}
	text, _ := edits[len(edits)-1].Params["text"].(string)
	if !strings.Contains(text, "check_method: <code>status</code> ✏️") {
		t.Errorf("expected re-rendered screen to show the new value status, got: %s", text)
	}

	// Interval and concurrency cycles advance through their ladders.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:ncfg:m31a:check_interval:900"))
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:ncfg:m31a:check_concurrency:4"))
	for _, want := range []string{"m31a|check_interval|900", "m31a|check_concurrency|4"} {
		found := false
		for _, c := range mgr.setCalls {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected SetNodeSetting(%q), got calls %v", want, mgr.setCalls)
		}
	}
}

// TestMenuNodesCfgResetGatedByAdmin: menu:nodes:nreset:<name> is a mutating
// route — a non-admin is denied when admins are configured (settings stay
// untouched), while an authorized admin resets through nodeMgr.
func TestMenuNodesCfgResetGatedByAdmin(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("x")
	_ = mgr.SetNodeSetting("x", "check_method", "status")
	b, _ := newCfgTestBot(t, api, []int64{1001}, mgr)

	// 1. Non-admin: denied, nothing reset, no screen edit.
	b.handleCallbackQuery(gateCallback(9999, "menu:nodes:nreset:x"))
	if len(mgr.resets) != 0 {
		t.Fatalf("non-admin reset must be denied, got resets %v", mgr.resets)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "только администраторам") {
		t.Errorf("expected admin-denial toast, got ok=%v toast=%q", ok, toast)
	}
	if edits := ft.callsOf("editMessageText"); len(edits) != 0 {
		t.Errorf("denied reset must not edit any message, got %d edits", len(edits))
	}

	// 2. Admin: reset runs through nodeMgr and the screen re-renders.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:nreset:x"))
	if len(mgr.resets) != 1 || mgr.resets[0] != "x|" {
		t.Fatalf("expected ResetNodeSetting(x, \"\"), got resets %v", mgr.resets)
	}
	if mgr.overrid["x"]["check_method"] {
		t.Errorf("override mark must be cleared after reset")
	}
	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected the node screen to re-render after reset")
	}
}

// TestMenuNodesCfgCallback64ByteGuard: with a 40-char node name the cycle
// callbacks would exceed Telegram's hard 64-byte callback_data limit — the
// cycle buttons must be omitted, the text must carry the /nodeset hint, and
// every emitted callback must stay ≤64 bytes. A 49-char name is still
// admitted by the picker (15+49=64) and there pins the Guardian P2 fix:
// «menu:nodes:excl:» + name is 65 bytes, so the «🚫 Исключения» button must
// be omitted under cbNodeCfg and the hint must cover exclusions.
// Guardian-authorized register entry: the former expectation that the raw
// menu:nodes:excl:<name> callback stays present codified the 64-byte bypass
// defect (weak_spots §Waves D+E, P2) and is inverted here.
func TestMenuNodesCfgCallback64ByteGuard(t *testing.T) {
	long := strings.Repeat("n", 40)

	// Unit: the composer refuses over-limit callbacks.
	if data, ok := cbNodeCfg("menu:nodes:ncfg:", long, ":check_method:download"); ok || data != "" {
		t.Errorf("cbNodeCfg must refuse a >64-byte callback, got (%q, %v)", data, ok)
	}
	if data, ok := cbNodeCfg("menu:nodes:cfg:", long); !ok || data != "menu:nodes:cfg:"+long {
		t.Errorf("cbNodeCfg must pass a fitting callback through, got (%q, %v)", data, ok)
	}

	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager(long)
	b, _ := newCfgTestBot(t, api, nil, mgr)

	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:cfg:"+long))

	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected the long-name node screen to render")
	}
	text, _ := edits[0].Params["text"].(string)
	if !strings.Contains(text, "длинное имя ноды") {
		t.Errorf("expected the long-name /nodeset hint in text, got: %s", text)
	}

	_, markup := b.getNodeCfgScreen(long)
	for _, cb := range markupCallbacks(markup) {
		if len(cb) > 64 {
			t.Errorf("callback_data exceeds 64 bytes: %q (%d bytes)", cb, len(cb))
		}
		if strings.HasPrefix(cb, "menu:nodes:ncfg:") {
			t.Errorf("cycle buttons must be omitted for long node names, got %q", cb)
		}
	}
	// Non-cycle controls survive: reset, subs link, exclusions, back.
	for _, want := range []string{
		"menu:nodes:nreset:" + long,
		"menu:nodes:subnode:" + long,
		"menu:nodes:excl:" + long,
		"menu:nodes:cfg",
	} {
		if !hasCallback(markup, want) {
			t.Errorf("expected callback %q to remain under the guard, got %v", want, markupCallbacks(markup))
		}
	}

	// 49-char case: the picker still admits the name, but the exclusions
	// callback would be 65 bytes — the button must be omitted, the hint must
	// mention exclusions, and every emitted callback must stay ≤64 bytes.
	veryLong := strings.Repeat("n", 49)
	ftLong, apiLong := newFakeTelegramBot(t)
	mgrLong := newCfgRecordingManager(veryLong)
	bLong, _ := newCfgTestBot(t, apiLong, nil, mgrLong)

	bLong.handleCallbackQuery(gateCallback(1001, "menu:nodes:cfg:"+veryLong))

	longEdits := ftLong.callsOf("editMessageText")
	if len(longEdits) == 0 {
		t.Fatalf("expected the 49-char node screen to render")
	}
	longText, _ := longEdits[0].Params["text"].(string)
	if !strings.Contains(longText, "исключения недоступны") {
		t.Errorf("expected the long-name hint to cover exclusions, got: %s", longText)
	}

	_, longMarkup := bLong.getNodeCfgScreen(veryLong)
	for _, cb := range markupCallbacks(longMarkup) {
		if len(cb) > 64 {
			t.Errorf("callback_data exceeds 64 bytes: %q (%d bytes)", cb, len(cb))
		}
		if strings.HasPrefix(cb, "menu:nodes:ncfg:") {
			t.Errorf("cycle buttons must be omitted for 49-char node names, got %q", cb)
		}
		if strings.HasPrefix(cb, "menu:nodes:excl:") {
			t.Errorf("exclusions button must be omitted for 49-char node names, got %q", cb)
		}
	}
	// Static navigation (no node name inside) must survive the guard.
	if !hasCallback(longMarkup, "menu:nodes:cfg") || !hasCallback(longMarkup, "menu:main") {
		t.Errorf("static navigation callbacks must survive, got %v", markupCallbacks(longMarkup))
	}
}

// TestMenuNodesExclView: the read-only exclusions view lists the node's
// excluded proxy display names (resolved from the namespaced stable IDs) and
// links back to the per-node settings screen; foreign-node entries stay out.
func TestMenuNodesExclView(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("m31a")
	b, cm := newCfgTestBot(t, api, nil, mgr)
	_ = cm.Update(func(c *BotConfig) {
		c.DisabledProxies = []string{"m31a/idx1", "m31a/idx2", "other/zzz"}
	})
	b.source = &mockSource{metrics: []metrics.ProxyMetric{
		{Name: "Alpha", StableID: "m31a/idx1", Address: "1.1.1.1:443", Protocol: "vless"},
		{Name: "Beta", StableID: "m31a/idx2", Address: "2.2.2.2:443", Protocol: "vless"},
	}}

	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:excl:m31a"))

	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected exclusions view render, got 0 edits")
	}
	text, _ := edits[0].Params["text"].(string)
	for _, want := range []string{"Alpha", "Beta", "мастер"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected exclusions view to contain %q, got: %s", want, text)
		}
	}
	if strings.Contains(text, "zzz") {
		t.Errorf("foreign-node exclusion must not be listed, got: %s", text)
	}

	// Back button returns to the per-node settings screen.
	_, markup := b.getNodeExclusionsView("m31a")
	if !hasCallback(markup, "menu:nodes:cfg:m31a") {
		t.Errorf("expected back callback menu:nodes:cfg:m31a, got %v", markupCallbacks(markup))
	}
}
