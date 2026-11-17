package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// TestMainMenuHasCheburcheckButton pins the Wave-D main-menu entry:
// «🧀 Cheburcheck РФ» (wave G3: the flag emoji is replaced by the
// Cheburcheck-themed 🧀) with the exact callback menu:chebur must sit on its
// own row BETWEEN the Check-Host row and the Nodes row.
func TestMainMenuHasCheburcheckButton(t *testing.T) {
	markup := MainMenuMarkup()
	cheburRow, checkhostRow, nodesRow := -1, -1, -1
	for i, row := range markup.InlineKeyboard {
		for _, b := range row {
			switch b.CallbackData {
			case "menu:chebur":
				cheburRow = i
			case "menu:checkhost":
				checkhostRow = i
			case "menu:nodes":
				nodesRow = i
			}
		}
	}
	if cheburRow == -1 {
		t.Fatalf("expected MainMenuMarkup to contain a menu:chebur button")
	}
	label := markup.InlineKeyboard[cheburRow][0].Text
	if !strings.Contains(label, "Cheburcheck РФ") {
		t.Errorf("expected button labeled 'Cheburcheck РФ', got %q", label)
	}
	if !strings.Contains(label, "🧀") {
		t.Errorf("expected the 🧀 chebur icon on the main-menu button, got %q", label)
	}
	if strings.Contains(label, "🇷🇺") {
		t.Errorf("flag emoji must be replaced by 🧀 on the main-menu button, got %q", label)
	}
	if checkhostRow == -1 || nodesRow == -1 || checkhostRow >= cheburRow || cheburRow >= nodesRow {
		t.Errorf("expected menu:chebur row %d strictly between Check-Host row %d and Nodes row %d",
			cheburRow, checkhostRow, nodesRow)
	}
}

// TestMenuCheburOpensSubmenu: the exact callback menu:chebur must edit the
// message into the Cheburcheck host-selection submenu (RU text carrying the
// /cheburcheck usage hint and the 🧀 icon, wave G3); with zero hosts the
// hint-only submenu must still render without panicking.
func TestMenuCheburOpensSubmenu(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	b.cheburClient = checker.NewCheburcheckClient("", time.Second,
		[]checker.ScannerCred{{Name: "s", Token: "t"}})

	b.handleCallbackQuery(gateCallback(9999, "menu:chebur"))

	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected menu:chebur to edit the message into the Cheburcheck submenu, got 0 edits")
	}
	text, _ := edits[0].Params["text"].(string)
	if !strings.Contains(text, "/cheburcheck") {
		t.Errorf("expected submenu text to contain the /cheburcheck usage hint, got: %s", text)
	}
	if !strings.Contains(text, "🧀") || strings.Contains(text, "🇷🇺") {
		t.Errorf("expected the 🧀 submenu title (no flag emoji), got: %s", text)
	}

	// Zero hosts: hint-only submenu must still render (no panic, one edit).
	ftEmpty, apiEmpty := newFakeTelegramBot(t)
	bEmpty, _ := newGateTestBot(t, apiEmpty, nil)
	bEmpty.source = &mockSource{metrics: []metrics.ProxyMetric{}}
	bEmpty.cheburClient = b.cheburClient
	bEmpty.handleCallbackQuery(gateCallback(9999, "menu:chebur"))
	if emptyEdits := ftEmpty.callsOf("editMessageText"); len(emptyEdits) == 0 {
		t.Fatalf("expected the submenu to render with zero hosts (hint-only), got 0 edits")
	}
}

// TestMenuCheburSubmenuUnconfiguredNotice: with neither CHEBURCHECK_API_KEY
// nor per-scanner tokens configured (public-repo default), the submenu text
// must carry the explicit «Сканеры не настроены» notice.
func TestMenuCheburSubmenuUnconfiguredNotice(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	// cheburClient stays nil: resolveCheburClient() builds the default client
	// over the empty DefaultCheburScanners -> no credentials.

	b.handleCallbackQuery(gateCallback(9999, "menu:chebur"))

	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected the Cheburcheck submenu to open, got 0 edits")
	}
	text, _ := edits[0].Params["text"].(string)
	if !strings.Contains(text, "Сканеры не настроены") {
		t.Errorf("expected unconfigured-scanners notice in submenu text, got: %s", text)
	}
}

// TestMenuCheburPrefixOrdering is the regression pin for the routing order
// invariant (wave G host-based rework): menu:cheburh:<hostKey> must reach the
// per-host probe handler (the "menu:cheburh:" prefix branch) and must NOT be
// swallowed by the submenu-open rule, while the bare menu:chebur must open the
// submenu and must NEVER invoke the host probe.
func TestMenuCheburPrefixOrdering(t *testing.T) {
	var mu sync.Mutex
	var checkTargets []string

	chebMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.URL.Path == "/check" {
			checkTargets = append(checkTargets, r.URL.Query().Get("target"))
		}
		mu.Unlock()
		switch {
		case r.URL.Path == "/check":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "job77"})
		case r.URL.Path == "/probe/job77":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"event: started\ndata: {\"id\":\"job77\",\"target\":\"1.1.1.1\",\"online_probes\":1}\n\n" +
					"event: result\ndata: {\"job_id\":\"j\",\"probe_id\":\"p1\",\"cached\":false,\"verdicts\":[\"ok\"]}\n\n" +
					"event: done\ndata: {\"id\":\"job77\",\"status\":\"finished\",\"response_count\":1,\"online_probes\":1}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer chebMock.Close()

	chebClient := checker.NewCheburcheckClient(chebMock.URL, 5*time.Second,
		[]checker.ScannerCred{{Name: "test-scanner", Token: "tok-1"}})

	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil) // snapshot: proxy-x -> 1.1.1.1:443
	b.cheburClient = chebClient

	waitChecks := func(want int) []string {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := len(checkTargets)
			mu.Unlock()
			if n >= want {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), checkTargets...)
	}

	// 1. menu:cheburh:1.1.1.1 must route to the per-host probe handler.
	b.handleCallbackQuery(gateCallback(9999, "menu:cheburh:1.1.1.1"))
	got := waitChecks(1)
	if len(got) != 1 || got[0] != "1.1.1.1" {
		t.Fatalf("expected menu:cheburh:1.1.1.1 to reach the per-host chebur probe (/check target 1.1.1.1), got %v", got)
	}

	// 2. The bare menu:chebur must open the submenu, never the probe handler.
	b.handleCallbackQuery(gateCallback(9999, "menu:chebur"))
	var submenuEdit string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && submenuEdit == "" {
		for _, c := range ft.callsOf("editMessageText") {
			if txt, _ := c.Params["text"].(string); strings.Contains(txt, "Активный пробинг блокировок РФ") {
				submenuEdit = txt
			}
		}
		if submenuEdit == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if submenuEdit == "" {
		t.Fatalf("expected bare menu:chebur to open the Cheburcheck submenu")
	}
	if got := waitChecks(1); len(got) != 1 {
		t.Errorf("bare menu:chebur must not invoke the per-host probe handler, saw /check targets %v", got)
	}
}

// TestCheburMenuMarkupCallbackLength pins the hard Telegram 64-byte
// callback_data limit for every button of the Cheburcheck submenu even with
// the longest possible host (wave G1: buttons are hosts keyed via
// HostCallbackKey — hosts longer than 36 bytes hash to a short h:<hex> key),
// plus the 2-per-row layout.
func TestCheburMenuMarkupCallbackLength(t *testing.T) {
	longHost := strings.Repeat("a", 60) + ".example.com" // > 36 bytes → hashed key
	hosts := []string{"1.1.1.1", longHost}
	markup := CheburMenuMarkup(hosts, 1, 1)
	if markup == nil {
		t.Fatal("nil markup")
	}
	if len(markup.InlineKeyboard) != 2 {
		t.Errorf("expected 2 rows (one pair row + back row), got %d rows", len(markup.InlineKeyboard))
	}
	foundPair, foundShort, foundLong := false, false, false
	for _, row := range markup.InlineKeyboard {
		if len(row) == 2 {
			foundPair = true
		}
		for _, b := range row {
			if len(b.CallbackData) > 64 {
				t.Errorf("callback_data exceeds 64 bytes: %q (%d bytes)", b.CallbackData, len(b.CallbackData))
			}
			if b.CallbackData == "menu:cheburh:1.1.1.1" {
				foundShort = true
			}
			if b.CallbackData == "menu:cheburh:"+HostCallbackKey(longHost) {
				foundLong = true
			}
		}
	}
	if !foundPair {
		t.Errorf("expected a 2-per-row pair in CheburMenuMarkup")
	}
	if !foundShort {
		t.Errorf("expected a menu:cheburh:1.1.1.1 button in CheburMenuMarkup")
	}
	if !foundLong {
		t.Errorf("expected a menu:cheburh:%s button in CheburMenuMarkup", HostCallbackKey(longHost))
	}
}

// TestCheburMenuMarkupUniqueHostsDedup (wave G1): Cheburcheck probes HOSTS,
// not proxy connections — two proxies sharing the same bare host (any
// protocol/port) must yield exactly ONE «🧀 <host>» button; the host list is
// sorted; disabled proxies are excluded.
func TestCheburMenuMarkupUniqueHostsDedup(t *testing.T) {
	proxies := []metrics.ProxyMetric{
		{Name: "A-vless", StableID: "a1", Address: "shared.example.com:443", Protocol: "vless"},
		{Name: "B-hy2", StableID: "b1", Address: "shared.example.com:8443", Protocol: "hysteria2"},
		{Name: "C-tuic", StableID: "c1", Address: "10.0.0.5:7777", Protocol: "tuic"},
		{Name: "D-off", StableID: "d1", Address: "off.example.com:443", Protocol: "vless", Disabled: true},
	}
	hosts := cheburHostsFromSnapshot(proxies)
	if len(hosts) != 2 {
		t.Fatalf("expected 2 unique hosts (shared dedup, disabled skipped), got %v", hosts)
	}
	if hosts[0] != "10.0.0.5" || hosts[1] != "shared.example.com" {
		t.Errorf("expected sorted hosts [10.0.0.5 shared.example.com], got %v", hosts)
	}

	markup := CheburMenuMarkup(hosts, 1, 1)
	cheburBtns := 0
	foundShared := false
	for _, row := range markup.InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.CallbackData, "menu:cheburh:") {
				cheburBtns++
				if !strings.HasPrefix(b.Text, "🧀") {
					t.Errorf("expected «🧀 <host>» button text, got %q", b.Text)
				}
			}
			if b.CallbackData == "menu:cheburh:shared.example.com" {
				foundShared = true
			}
		}
	}
	if cheburBtns != 2 {
		t.Errorf("expected 2 host buttons, got %d", cheburBtns)
	}
	if !foundShared {
		t.Errorf("expected a menu:cheburh:shared.example.com button (dedup across protocols)")
	}
}

// TestCheburMenuTextEmptyState (replaces the Wave-F B4 empty-state pin, wave
// G host-based rework): the submenu flags zero hosts in Russian and speaks of
// hosts (not proxies) — the list is host-based since wave G.
func TestCheburMenuTextEmptyState(t *testing.T) {
	b := &Bot{}
	if txt := b.getCheburMenuText(0); !strings.Contains(txt, "Нет активных хостов для проверки") {
		t.Errorf("expected empty-state note for 0 hosts, got: %s", txt)
	}
	if txt := b.getCheburMenuText(3); strings.Contains(txt, "Нет активных хостов") {
		t.Errorf("empty-state note must not appear with hosts present, got: %s", txt)
	}
}
