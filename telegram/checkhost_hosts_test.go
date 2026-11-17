package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// Wave H6 (managed Check-Host host list, decision D3): the Check-Host submenu
// is the UNION of subscription hosts («авто», non-removable) and customs from
// cfg.CheckHostHosts (removable); customs join the background RU-block audit.

// TestCheckHostUnionList_DedupAutoCustom pins the union semantics: enabled
// snapshot addresses are auto entries; customs are deduped case-insensitively
// against them (auto wins), blank custom entries are dropped, disabled proxies
// never enter the list, and the result is sorted stably by the lowercased
// address.
func TestCheckHostUnionList_DedupAutoCustom(t *testing.T) {
	snapshot := []metrics.ProxyMetric{
		{Name: "A", Address: "AAA.com:443", StableID: "a1", Online: true},
		{Name: "A-dup", Address: "aaa.com:443", StableID: "a2", Online: true}, // case-insensitive dup of a1
		{Name: "B", Address: "bbb.com:8443", StableID: "b1", Online: true},
		{Name: "D", Address: "dis.com:443", StableID: "d1", Disabled: true},
	}
	customs := []string{"ccc.example.com", "AAA.COM:443", "ddd.host:443", "   ", "eee.io"}

	items := checkHostUnionList(snapshot, customs)

	var got []string
	for _, it := range items {
		got = append(got, it.Address)
	}
	want := []string{"AAA.com:443", "bbb.com:8443", "ccc.example.com", "ddd.host:443", "eee.io"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("union list mismatch:\n got  %v\n want %v", got, want)
	}
	for _, it := range items {
		switch it.Address {
		case "AAA.com:443", "bbb.com:8443":
			if !it.Auto {
				t.Errorf("subscription host %q must be marked auto", it.Address)
			}
		default:
			if it.Auto {
				t.Errorf("custom host %q must not be marked auto", it.Address)
			}
		}
	}
}

// TestCheckHostMenuMarkup_UnionRowsAndPagination (REPLACES the wave G4 pin
// TestCheckHostMenuMarkupPagination over the old proxy list — authorized H6
// list-source register, 2026-11-17): the union list renders auto rows as a
// single «· авто» check button and custom rows as a check button plus a «🗑»
// delete button, carries the «➕ Добавить хост» row, paginates over the union
// with menu:checkhostpg:, clamps out-of-range pages, and every callback is
// routable and within the 64-byte limit (long hosts hashed via
// HostCallbackKey).
func TestCheckHostMenuMarkup_UnionRowsAndPagination(t *testing.T) {
	var items []CheckHostListItem
	for i := 0; i < 20; i++ {
		items = append(items, CheckHostListItem{Address: fmt.Sprintf("auto-%02d.example.com:443", i), Auto: true})
	}
	for i := 0; i < 5; i++ {
		items = append(items, CheckHostListItem{Address: fmt.Sprintf("custom-%02d.example.com:443", i)})
	}
	// A max-length custom host: HostCallbackKey must hash it so the row's
	// callbacks stay within the 64-byte limit.
	items = append(items, CheckHostListItem{Address: strings.Repeat("z", 100) + ".example.com:443"})

	totalPages := menuTotalPages(len(items), menuListPageSize)
	if totalPages != 2 {
		t.Fatalf("expected 2 pages for %d union items at page size %d, got %d", len(items), menuListPageSize, totalPages)
	}

	prefixCount := func(markup *telego.InlineKeyboardMarkup, prefix string) int {
		n := 0
		for _, row := range markup.InlineKeyboard {
			for _, b := range row {
				if strings.HasPrefix(b.CallbackData, prefix) {
					n++
				}
			}
		}
		return n
	}

	mk1 := CheckHostMenuMarkup(items, 1, totalPages)
	requireAllCallbacksRoutable(t, "CheckHostMenuMarkup union page 1", mk1)
	if got := prefixCount(mk1, "menu:checkhosth:"); got != 20 {
		t.Errorf("expected 20 per-host check buttons on page 1, got %d", got)
	}
	// Page 1 holds the first 20 sorted items (auto-00..auto-19): all auto, so
	// no delete buttons may appear on this page.
	if got := prefixCount(mk1, "menu:checkhostdel:"); got != 0 {
		t.Errorf("page 1 (all auto rows) must carry no delete buttons, got %d", got)
	}
	if !hasCallback(mk1, "menu:checkhostpg:2") {
		t.Errorf("page 1 must carry the next-page button menu:checkhostpg:2")
	}
	if hasCallback(mk1, "menu:checkhostpg:0") {
		t.Errorf("page 1 must not carry a prev-page button")
	}
	if !hasButtonText(mk1, "Стр. 1/2") {
		t.Errorf("page 1 must carry the «Стр. 1/2» label")
	}
	if !hasButtonText(mk1, "➕ Добавить хост") || !hasCallback(mk1, "menu:checkhostadd") {
		t.Errorf("the add-host row («➕ Добавить хост» → menu:checkhostadd) must always be present")
	}
	foundAutoMarker := false
	for _, row := range mk1.InlineKeyboard {
		for _, b := range row {
			if strings.HasSuffix(b.Text, "· авто") {
				foundAutoMarker = true
			}
			if strings.Contains(b.Text, "и ещё") {
				t.Errorf("overflow stub must stay removed in favor of pagination, got %q", b.Text)
			}
		}
	}
	if !foundAutoMarker {
		t.Errorf("auto rows must be marked with an «· авто» suffix, got %v", markupCallbacks(mk1))
	}

	mk2 := CheckHostMenuMarkup(items, 2, totalPages)
	requireAllCallbacksRoutable(t, "CheckHostMenuMarkup union page 2", mk2)
	if got := prefixCount(mk2, "menu:checkhosth:"); got != 6 {
		t.Errorf("expected 6 per-host check buttons on page 2 (5 customs + long host), got %d", got)
	}
	// Custom rows carry one 🗑 delete button each (page 2 = customs only).
	if got := prefixCount(mk2, "menu:checkhostdel:"); got != 6 {
		t.Errorf("expected 6 delete buttons for the 6 custom rows on page 2, got %d", got)
	}
	if !hasCallback(mk2, "menu:checkhostpg:1") {
		t.Errorf("page 2 must carry the prev-page button menu:checkhostpg:1")
	}
	if hasCallback(mk2, "menu:checkhostpg:3") {
		t.Errorf("page 2 must not carry a next-page button")
	}
	if !hasButtonText(mk2, "Стр. 2/2") {
		t.Errorf("page 2 must carry the «Стр. 2/2» label")
	}

	// Out-of-range pages clamp into range: page 99 renders the last page.
	mkClamp := CheckHostMenuMarkup(items, 99, totalPages)
	if got := prefixCount(mkClamp, "menu:checkhosth:"); got != 6 {
		t.Errorf("page 99 must clamp to the last page (6 items), got %d", got)
	}

	// Control: a single page carries no pagination row.
	mk1p := CheckHostMenuMarkup(items[:1], 1, 1)
	if hasCallback(mk1p, "menu:checkhostpg:2") || hasButtonText(mk1p, "Стр. 1/1") {
		t.Errorf("a single page must not carry pagination buttons")
	}
}

// TestCheckHostHostKey_ChecksResolvedTarget pins the per-host check route:
// menu:checkhosth:<HostCallbackKey> reverse-looks-up the key against the union
// sources (snapshot addresses + customs) and runs CheckTCP on the resolved
// value, with the :443 default for bare custom hosts. Unknown keys answer
// gracefully without touching the API.
func TestCheckHostHostKey_ChecksResolvedTarget(t *testing.T) {
	var mu sync.Mutex
	var tcpHosts []string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			tcpHosts = append(tcpHosts, r.URL.Query().Get("host"))
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":             1,
				"request_id":     "hostkey1",
				"permanent_link": "https://check-host.net/check-report/hostkey1",
				"nodes": map[string]interface{}{
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Frankfurt", "5.6.7.8", "AS2"},
				},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/check-result/hostkey1") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"de1.node.check-host.net": []interface{}{
					[]interface{}{[]interface{}{"OK", 0.02, "1.2.3.4"}},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	chClient := checker.NewCheckHostClient(ts.URL, 10*time.Millisecond)
	chClient.HTTPClient = ts.Client()

	ft, api := newFakeTelegramBot(t)
	b, cm := newGateTestBot(t, api, nil) // snapshot proxy: 1.1.1.1:443 (auto)
	_ = cm.Update(func(c *BotConfig) {
		c.CheckHostHosts = []string{"5.5.5.5"}
	})
	b.checkHostClient = chClient

	since := func(baseline int) []string {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := len(tcpHosts)
			mu.Unlock()
			if n > baseline {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), tcpHosts[baseline:]...)
	}

	// 1. A bare custom host is checked with the :443 default.
	base := 0
	b.handleCallbackQuery(gateCallback(9999, "menu:checkhosth:5.5.5.5"))
	if got := since(base); len(got) != 1 || got[0] != "5.5.5.5:443" {
		t.Fatalf("bare custom host must be checked as 5.5.5.5:443, got %v", got)
	}

	// 2. An auto subscription address is checked with its stored host:port.
	base = len(tcpHosts)
	b.handleCallbackQuery(gateCallback(9999, "menu:checkhosth:1.1.1.1:443"))
	if got := since(base); len(got) != 1 || got[0] != "1.1.1.1:443" {
		t.Fatalf("auto host must be checked as its stored address, got %v", got)
	}

	// 3. An unknown key answers gracefully (host not found) — no API call.
	base = len(tcpHosts)
	b.handleCallbackQuery(gateCallback(9999, "menu:checkhosth:unknown.example.com"))
	foundNotFound := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !foundNotFound {
		for _, c := range ft.callsOf("editMessageText") {
			if txt, _ := c.Params["text"].(string); strings.Contains(txt, "Хост не найден в текущей конфигурации") {
				foundNotFound = true
			}
		}
		if !foundNotFound {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if got := since(base); len(got) != 0 {
		t.Errorf("unknown key must not reach the Check-Host API, saw %v", got)
	}
	if !foundNotFound {
		t.Errorf("expected the graceful host-not-found message after an unknown key")
	}
}

// TestCheckHostAddFlow_ArmsPersistsAndRejects pins the «➕ Добавить хост» flow:
// the exact route menu:checkhostadd is admin-gated, arms the single-shot
// waiting state, the next plain text is validated and persisted via
// updateConfig (dedup), subscription hosts are refused with a RU notice, and
// navigation disarms the pending input.
func TestCheckHostAddFlow_ArmsPersistsAndRejects(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, cm := newGateTestBot(t, api, []int64{1001}) // snapshot proxy: 1.1.1.1:443 (auto)

	// 1. Admin taps «➕ Добавить хост»: waiting state armed, RU prompt shown.
	b.handleCallbackQuery(gateCallback(1001, "menu:checkhostadd"))
	if !b.waitingCheckHostHost[12345] {
		t.Fatalf("menu:checkhostadd must arm waitingCheckHostHost for the chat")
	}
	prompt, ok := ft.lastParamOf("editMessageText", "text")
	if !ok || !strings.Contains(prompt, "Добавление хоста") || !strings.Contains(prompt, "Отправьте") {
		t.Errorf("expected the RU add-host prompt, got: %q", prompt)
	}

	// 2. The next plain text is persisted as a custom host (bare form kept).
	b.handleMessage(&telego.Message{
		MessageID: 11,
		Chat:      telego.Chat{ID: 12345, Type: "private"},
		From:      &telego.User{ID: 1001, Username: "admin"},
		Text:      "5.6.7.8",
	})
	if b.waitingCheckHostHost[12345] {
		t.Fatalf("waiting state must be consumed after the input")
	}
	hosts := cm.Get().CheckHostHosts
	if len(hosts) != 1 || hosts[0] != "5.6.7.8" {
		t.Fatalf("expected the custom host 5.6.7.8 persisted via updateConfig, got %v", hosts)
	}
	items := checkHostUnionList(b.source.MetricsSnapshot(), cm.Get().CheckHostHosts)
	foundCustom := false
	for _, it := range items {
		if it.Address == "5.6.7.8" && !it.Auto {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Errorf("the added host must appear in the union list as a custom entry, got %+v", items)
	}
	confirm, ok := ft.lastParamOf("sendMessage", "text")
	if !ok || !strings.Contains(confirm, "5.6.7.8") {
		t.Errorf("expected a RU confirmation naming the host, got %q", confirm)
	}

	// 3. Adding a host that already exists as auto is refused (no write).
	b.handleCallbackQuery(gateCallback(1001, "menu:checkhostadd"))
	b.handleMessage(&telego.Message{
		MessageID: 12,
		Chat:      telego.Chat{ID: 12345, Type: "private"},
		From:      &telego.User{ID: 1001, Username: "admin"},
		Text:      "1.1.1.1", // normalized 1.1.1.1:443 == the snapshot auto host
	})
	hosts = cm.Get().CheckHostHosts
	if len(hosts) != 1 {
		t.Errorf("auto host must not be added to customs, got %v", hosts)
	}
	reject, ok := ft.lastParamOf("sendMessage", "text")
	if !ok || !strings.Contains(reject, "подписк") {
		t.Errorf("expected the RU «уже есть в подписках» rejection, got %q", reject)
	}

	// 4. Adding an exact custom duplicate is refused (no write).
	b.handleCallbackQuery(gateCallback(1001, "menu:checkhostadd"))
	b.handleMessage(&telego.Message{
		MessageID: 13,
		Chat:      telego.Chat{ID: 12345, Type: "private"},
		From:      &telego.User{ID: 1001, Username: "admin"},
		Text:      "5.6.7.8",
	})
	hosts = cm.Get().CheckHostHosts
	if len(hosts) != 1 {
		t.Errorf("duplicate custom must not be added twice, got %v", hosts)
	}

	// 5. Navigation to the main menu disarms the pending input.
	b.handleCallbackQuery(gateCallback(1001, "menu:checkhostadd"))
	if !b.waitingCheckHostHost[12345] {
		t.Fatalf("expected the armed state before navigation")
	}
	b.handleCallbackQuery(gateCallback(1001, "menu:main"))
	if b.waitingCheckHostHost[12345] {
		t.Fatalf("menu:main must disarm waitingCheckHostHost")
	}
	b.handleMessage(&telego.Message{
		MessageID: 14,
		Chat:      telego.Chat{ID: 12345, Type: "private"},
		From:      &telego.User{ID: 1001, Username: "admin"},
		Text:      "8.8.8.8",
	})
	if got := cm.Get().CheckHostHosts; len(got) != 1 {
		t.Errorf("disarmed input must not write config, got %v", got)
	}

	// 6. Re-entering the submenu (menu:checkhost) disarms the pending input too.
	b.handleCallbackQuery(gateCallback(1001, "menu:checkhostadd"))
	if !b.waitingCheckHostHost[12345] {
		t.Fatalf("expected the armed state before re-entry")
	}
	b.handleCallbackQuery(gateCallback(1001, "menu:checkhost"))
	if b.waitingCheckHostHost[12345] {
		t.Fatalf("menu:checkhost re-entry must disarm waitingCheckHostHost")
	}
}

// TestCheckHostAddFlow_NonAdminDenied pins the admin gate on the arm route and
// on the armed text consumption.
func TestCheckHostAddFlow_NonAdminDenied(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, cm := newGateTestBot(t, api, []int64{1001})

	b.handleCallbackQuery(gateCallback(9999, "menu:checkhostadd"))
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "только администраторам") {
		t.Errorf("non-admin arm attempt must get the denial toast, got ok=%v toast=%q", ok, toast)
	}
	if b.waitingCheckHostHost[12345] {
		t.Fatalf("non-admin must not arm the waiting state")
	}

	// Armed-by-admin state + non-admin text: denied, no write, state consumed.
	b.handleCallbackQuery(gateCallback(1001, "menu:checkhostadd"))
	b.handleMessage(&telego.Message{
		Chat: telego.Chat{ID: 12345, Type: "private"},
		From: &telego.User{ID: 9999, Username: "intruder"},
		Text: "evil.example.com",
	})
	if b.waitingCheckHostHost[12345] {
		t.Fatalf("waiting state must be consumed even when denied")
	}
	if got := cm.Get().CheckHostHosts; len(got) != 0 {
		t.Errorf("non-admin text must not write config, got %v", got)
	}
	for _, c := range ft.callsOf("sendMessage") {
		if txt, _ := c.Params["text"].(string); strings.Contains(txt, "evil.example.com") {
			t.Errorf("non-admin must not receive an add confirmation, got: %s", txt)
		}
	}
}

// TestCheckHostDelFlow_RemovesCustomRejectsAuto pins the «🗑» flow:
// menu:checkhostdel:<HostCallbackKey> removes the custom via updateConfig with
// a «Удалён: <host>» toast and re-renders the list; subscription hosts are
// refused with the «есть в подписках» notice; non-admins are denied.
func TestCheckHostDelFlow_RemovesCustomRejectsAuto(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, cm := newGateTestBot(t, api, []int64{1001}) // snapshot proxy: 1.1.1.1:443 (auto)
	if err := cm.Update(func(c *BotConfig) {
		// 1.1.1.1:443 is a shadowed custom: stored in cfg but covered by the
		// auto snapshot entry (the union list never renders it).
		c.CheckHostHosts = []string{"9.9.9.9:443", "7.7.7.7", "1.1.1.1:443"}
	}); err != nil {
		t.Fatalf("failed to seed customs: %v", err)
	}

	// 1. Admin removes a custom host.
	b.handleCallbackQuery(gateCallback(1001, "menu:checkhostdel:9.9.9.9:443"))
	hosts := cm.Get().CheckHostHosts
	if len(hosts) != 2 || hosts[0] != "7.7.7.7" || hosts[1] != "1.1.1.1:443" {
		t.Fatalf("expected 9.9.9.9:443 removed and the rest kept, got %v", hosts)
	}
	toast, ok := ft.lastParamOf("answerCallbackQuery", "text")
	if !ok || !strings.HasPrefix(toast, "Удалён: 9.9.9.9:443") {
		t.Errorf("expected the «Удалён: 9.9.9.9:443» toast, got ok=%v toast=%q", ok, toast)
	}
	// The list re-render names the remaining host.
	edit, ok := ft.lastParamOf("editMessageText", "text")
	if !ok {
		t.Fatalf("expected the list re-render after deletion")
	}
	_ = edit

	// 2. Non-admin delete is denied, config untouched.
	b.handleCallbackQuery(gateCallback(9999, "menu:checkhostdel:7.7.7.7"))
	if got := cm.Get().CheckHostHosts; len(got) != 2 || got[0] != "7.7.7.7" || got[1] != "1.1.1.1:443" {
		t.Fatalf("non-admin delete must not write config, got %v", got)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "только администраторам") {
		t.Errorf("non-admin delete must get the denial toast, got ok=%v toast=%q", ok, toast)
	}

	// 3. Deleting a host that also exists as auto is refused (the shadowed
	//    custom stays in cfg; the auto entry is untouched).
	b.handleCallbackQuery(gateCallback(1001, "menu:checkhostdel:1.1.1.1:443"))
	if got := cm.Get().CheckHostHosts; len(got) != 2 {
		t.Fatalf("a custom covered by an auto host must not be deletable, got %v", got)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "есть в подписках") {
		t.Errorf("expected the «есть в подписках» refusal toast, got ok=%v toast=%q", ok, toast)
	}

	// 4. An unknown key answers gracefully without writes.
	b.handleCallbackQuery(gateCallback(1001, "menu:checkhostdel:not-added.example.com"))
	if got := cm.Get().CheckHostHosts; len(got) != 2 {
		t.Fatalf("unknown key must not write config, got %v", got)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "не найден") {
		t.Errorf("unknown key must get the not-found toast, got ok=%v toast=%q", ok, toast)
	}
}

// TestRunAuditNow_IncludesCustomHosts (decision D3): runAuditNow audits the
// subscription hosts PLUS the custom hosts — bare customs are audited as
// host:443, customs matching an auto host are deduped, and the alert keys keep
// the checkhost:<addr> form. Customs are audited even when the snapshot is
// empty.
func TestRunAuditNow_IncludesCustomHosts(t *testing.T) {
	var mu sync.Mutex
	var tcpHosts []string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			tcpHosts = append(tcpHosts, r.URL.Query().Get("host"))
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":             1,
				"request_id":     "customaud",
				"permanent_link": "https://check-host.net/check-report/customaud",
				"nodes": map[string]interface{}{
					"ru2.node.check-host.net": []interface{}{"ru", "Russia", "Moscow", "1.2.3.4", "AS1"},
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Frankfurt", "5.6.7.8", "AS2"},
				},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/check-result/customaud") {
			// RU down + world up: the RU-block signature fires alerts.
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ru2.node.check-host.net": []interface{}{map[string]interface{}{"error": "Connection timed out"}},
				"de1.node.check-host.net": []interface{}{map[string]interface{}{"time": 0.02, "address": "1.1.1.1"}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)

	chClient := checker.NewCheckHostClient(ts.URL, 10*time.Millisecond)
	chClient.HTTPClient = ts.Client()

	newAuditBot := func(snapshot []metrics.ProxyMetric, customs []string) (*Bot, *AlertTracker) {
		tmpDir := t.TempDir()
		cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{
			CheckHostBgEnabled:    true,
			CheckHostAlertEnabled: true,
			AlertMode:             AlertModeClean,
			CheckHostHosts:        customs,
		})
		if err != nil {
			t.Fatalf("failed to create config manager: %v", err)
		}
		tracker := NewAlertTracker()
		return &Bot{
			source:          &mockSource{metrics: snapshot},
			configMgr:       cm,
			tracker:         tracker,
			targets:         []ChatTarget{{ChatID: 12345}},
			checkHostClient: chClient,
			checkHostNodes:  []string{"ru2.node.check-host.net", "de1.node.check-host.net"},
			ctx:             context.Background(),
		}, tracker
	}

	// 1. Customs join the audit; a custom matching the auto host is deduped.
	bot, tracker := newAuditBot([]metrics.ProxyMetric{
		{Name: "P", Address: "1.1.1.1:443", StableID: "p1", Protocol: "vless", Online: true},
	}, []string{"5.5.5.5", "1.1.1.1"})
	bot.runAuditNow()

	mu.Lock()
	got := append([]string(nil), tcpHosts...)
	mu.Unlock()
	want := []string{"1.1.1.1:443", "5.5.5.5:443"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("audit targets mismatch (bare custom must become :443, auto dup dropped):\n got  %v\n want %v", got, want)
	}
	for _, key := range []string{"checkhost:1.1.1.1:443", "checkhost:5.5.5.5:443"} {
		if !tracker.HasAlert(12345, 0, key) {
			t.Errorf("expected the RU-block alert tracked under %q", key)
		}
	}

	// 2. Empty snapshot: customs are still audited.
	mu.Lock()
	tcpHosts = nil
	mu.Unlock()
	bot2, tracker2 := newAuditBot(nil, []string{"8.8.8.8"})
	bot2.runAuditNow()
	mu.Lock()
	got2 := append([]string(nil), tcpHosts...)
	mu.Unlock()
	if len(got2) != 1 || got2[0] != "8.8.8.8:443" {
		t.Fatalf("customs must be audited even with an empty snapshot, got %v", got2)
	}
	if !tracker2.HasAlert(12345, 0, "checkhost:8.8.8.8:443") {
		t.Errorf("expected the RU-block alert for the custom host under checkhost:8.8.8.8:443")
	}
}

// TestCheckHostHostKey_UDPProtoAddressStillTCPChecked (REPLACES the
// TestHandleCheckHostProxy_UDPProtoTCPWithWarning pin — authorized H6
// list-source register, 2026-11-17): the Check-Host request stays ALWAYS a
// TCP check of host:port from the subscription entry for UDP-protocol proxies
// (user decision 2026-11-16), now via the host-key route of the union list.
// The former per-proxy UDP warning text is gone together with the per-proxy
// surface: the union list is host-based.
func TestCheckHostHostKey_UDPProtoAddressStillTCPChecked(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var tcpHosts []string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			tcpHosts = append(tcpHosts, r.URL.Query().Get("host"))
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":             1,
				"request_id":     "udp5678",
				"permanent_link": "https://check-host.net/check-report/udp5678",
				"nodes": map[string]interface{}{
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Frankfurt", "5.6.7.8", "AS2"},
				},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/check-result/udp5678") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"de1.node.check-host.net": []interface{}{
					[]interface{}{[]interface{}{"OK", 0.02, "1.2.3.4"}},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	chClient := checker.NewCheckHostClient(ts.URL, 10*time.Millisecond)
	chClient.HTTPClient = ts.Client()

	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	_ = b.updateConfig(func(c *BotConfig) {
		c.CheckHostHosts = nil
	})
	b.checkHostClient = chClient
	b.source = &mockSource{metrics: []metrics.ProxyMetric{
		{Name: "Hys-1", StableID: "hys-1", Address: "1.2.3.4:5678", Protocol: "hysteria2", Online: true},
	}}

	b.handleCallbackQuery(gateCallback(9999, "menu:checkhosth:1.2.3.4:5678"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(tcpHosts)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()

	for _, p := range paths {
		if strings.HasPrefix(p, "/check-ping") {
			t.Errorf("UDP-protocol address must stay a TCP check, saw %s (all paths: %v)", p, paths)
		}
	}
	if len(tcpHosts) != 1 || tcpHosts[0] != "1.2.3.4:5678" {
		t.Errorf("expected exactly one /check-tcp with the subscription host:port 1.2.3.4:5678, got hosts %v (paths %v)", tcpHosts, paths)
	}
	reported := false
	for _, c := range ft.callsOf("editMessageText") {
		if txt, _ := c.Params["text"].(string); strings.Contains(txt, "<code>1.2.3.4:5678</code>") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("expected the report to name the checked host:port, got %v", ft.callsOf("editMessageText"))
	}
}

// TestValidateCheckHostHostInput pins the manual-input validation: bare hosts
// and numeric host:port pass; schemes/paths/whitespace, non-numeric ports and
// unparsable colon garbage are rejected with RU errors (the numeric-port rule
// — net.SplitHostPort alone does not validate the port).
func TestValidateCheckHostHostInput(t *testing.T) {
	ok := map[string]string{
		"example.com":       "example.com",
		"  Example.COM  ":   "Example.COM",
		"1.2.3.4":           "1.2.3.4",
		"example.com:443":   "example.com:443",
		"1.2.3.4:8443":      "1.2.3.4:8443",
		"[2001:db8::1]:443": "[2001:db8::1]:443",
	}
	for in, want := range ok {
		got, err := validateCheckHostHostInput(in)
		if err != nil || got != want {
			t.Errorf("validateCheckHostHostInput(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}

	bad := []string{
		"",
		"   ",
		"https://example.com",
		"example.com/path",
		"two hosts here",
		"example.com:abc",
		"example.com:",
		"example.com:443:555",
		"::1",
	}
	for _, in := range bad {
		got, err := validateCheckHostHostInput(in)
		if err == nil {
			t.Errorf("validateCheckHostHostInput(%q) = %q, nil; want a RU error", in, got)
			continue
		}
		if !strings.ContainsAny(err.Error(), "хостХостПустПортадресА") && !strings.Contains(err.Error(), " ") {
			t.Errorf("validation error for %q must be a RU sentence, got %q", in, err.Error())
		}
	}
}

// TestBotConfig_CheckHostHosts_LegacyLoadPersistAndCopy pins the storage
// contract: a legacy config file without the field loads as empty; the field
// persists additively through updateConfig; Get returns a defensive copy.
func TestBotConfig_CheckHostHosts_LegacyLoadPersistAndCopy(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bot_cfg.json")
	if err := os.WriteFile(p, []byte(`{"alert_mode":"clean","target_urls":["https://x/"]}`), 0600); err != nil {
		t.Fatalf("failed to write legacy config: %v", err)
	}

	cm, err := NewConfigManager(p, BotConfig{})
	if err != nil {
		t.Fatalf("legacy config must load cleanly, got: %v", err)
	}
	if got := cm.Get().CheckHostHosts; len(got) != 0 {
		t.Fatalf("legacy config without the field must yield empty CheckHostHosts, got %v", got)
	}

	if err := cm.Update(func(c *BotConfig) {
		c.CheckHostHosts = []string{"a.example.com:443", "B.example.com"}
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	// Get returns a defensive copy: mutating it must not leak into the store.
	got := cm.Get().CheckHostHosts
	got[0] = "mutated"
	if cm.Get().CheckHostHosts[0] != "a.example.com:443" {
		t.Fatalf("Get must return a copy of CheckHostHosts")
	}

	cm2, err := NewConfigManager(p, BotConfig{})
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	hosts := cm2.Get().CheckHostHosts
	if len(hosts) != 2 || hosts[0] != "a.example.com:443" || hosts[1] != "B.example.com" {
		t.Fatalf("customs must persist across reload, got %v", hosts)
	}

	data, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(data), `"checkhost_hosts"`) {
		t.Errorf("the persisted file must contain the checkhost_hosts key (err=%v, data=%s)", err, data)
	}
}
