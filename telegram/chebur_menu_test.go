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
// «🇷🇺 Cheburcheck РФ» with the exact callback menu:chebur must sit on its
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
	if !strings.Contains(markup.InlineKeyboard[cheburRow][0].Text, "Cheburcheck РФ") {
		t.Errorf("expected button labeled 'Cheburcheck РФ', got %q", markup.InlineKeyboard[cheburRow][0].Text)
	}
	if checkhostRow == -1 || nodesRow == -1 || checkhostRow >= cheburRow || cheburRow >= nodesRow {
		t.Errorf("expected menu:chebur row %d strictly between Check-Host row %d and Nodes row %d",
			cheburRow, checkhostRow, nodesRow)
	}
}

// TestMenuCheburOpensSubmenu: the exact callback menu:chebur must edit the
// message into the Cheburcheck proxy-selection submenu (RU text carrying the
// /cheburcheck usage hint); with zero proxies the hint-only submenu must
// still render without panicking.
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

	// Zero proxies: hint-only submenu must still render (no panic, one edit).
	ftEmpty, apiEmpty := newFakeTelegramBot(t)
	bEmpty, _ := newGateTestBot(t, apiEmpty, nil)
	bEmpty.source = &mockSource{metrics: []metrics.ProxyMetric{}}
	bEmpty.cheburClient = b.cheburClient
	bEmpty.handleCallbackQuery(gateCallback(9999, "menu:chebur"))
	if emptyEdits := ftEmpty.callsOf("editMessageText"); len(emptyEdits) == 0 {
		t.Fatalf("expected the submenu to render with zero proxies (hint-only), got 0 edits")
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
// invariant: menu:chebur:<stableID> must reach the per-proxy probe handler
// (the "menu:chebur:" prefix branch) and must NOT be swallowed by the
// submenu-open rule, while the bare menu:chebur must open the submenu and
// must NEVER invoke the proxy probe.
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

	// 1. menu:chebur:proxy-x must route to the per-proxy probe handler.
	b.handleCallbackQuery(gateCallback(9999, "menu:chebur:proxy-x"))
	got := waitChecks(1)
	if len(got) != 1 || got[0] != "1.1.1.1" {
		t.Fatalf("expected menu:chebur:proxy-x to reach the per-proxy chebur probe (/check target 1.1.1.1), got %v", got)
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
		t.Errorf("bare menu:chebur must not invoke the per-proxy probe handler, saw /check targets %v", got)
	}
}

// TestCheburMenuMarkupCallbackLength pins the hard Telegram 64-byte
// callback_data limit for every button of the Cheburcheck submenu even with
// the longest possible stableID, plus the 2-per-row layout and the
// skip-disabled rule.
func TestCheburMenuMarkupCallbackLength(t *testing.T) {
	longest := strings.Repeat("a", 16) // max models.GenerateStableID length
	proxies := []metrics.ProxyMetric{
		{Name: "Proxy One", StableID: longest, Address: "1.1.1.1:443", Protocol: "vless"},
		{Name: "Proxy Two", StableID: longest, Address: "2.2.2.2:443", Protocol: "vless"},
		{Name: "Off", StableID: "dis", Address: "3.3.3.3:443", Protocol: "vless", Disabled: true},
	}
	markup := CheburMenuMarkup(proxies)
	if markup == nil {
		t.Fatal("nil markup")
	}
	// One 2-per-row pair + back row; the disabled proxy is skipped.
	if len(markup.InlineKeyboard) != 2 {
		t.Errorf("expected 2 rows (one pair row + back row, disabled skipped), got %d rows", len(markup.InlineKeyboard))
	}
	foundPair, foundLongest := false, false
	for _, row := range markup.InlineKeyboard {
		if len(row) == 2 {
			foundPair = true
		}
		for _, b := range row {
			if len(b.CallbackData) > 64 {
				t.Errorf("callback_data exceeds 64 bytes: %q (%d bytes)", b.CallbackData, len(b.CallbackData))
			}
			if b.CallbackData == "menu:chebur:"+longest {
				foundLongest = true
			}
		}
	}
	if !foundPair {
		t.Errorf("expected a 2-per-row pair in CheburMenuMarkup")
	}
	if !foundLongest {
		t.Errorf("expected a menu:chebur:%s button in CheburMenuMarkup", longest)
	}
}
