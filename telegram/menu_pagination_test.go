package telegram

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// cheburPageProxies builds 25 enabled proxies with unique hosts
// host-00.example.com ... host-24.example.com (zero-padded → the host list
// sorts in numeric order).
func cheburPageProxies() []metrics.ProxyMetric {
	var proxies []metrics.ProxyMetric
	for i := 0; i < 25; i++ {
		host := fmt.Sprintf("host-%02d.example.com", i)
		proxies = append(proxies, metrics.ProxyMetric{
			Name:     fmt.Sprintf("P%02d", i),
			StableID: fmt.Sprintf("sid%02d", i),
			Address:  host + ":443",
			Protocol: "vless",
		})
	}
	return proxies
}

// cheburHostButtonCallbacks returns the menu:cheburh: callbacks of a markup.
func cheburHostButtonCallbacks(markup *telego.InlineKeyboardMarkup) []string {
	var out []string
	for _, row := range markup.InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.CallbackData, "menu:cheburh:") {
				out = append(out, b.CallbackData)
			}
		}
	}
	return out
}

// hasButtonText reports whether a button with the given visible text exists.
func hasButtonText(markup *telego.InlineKeyboardMarkup, text string) bool {
	for _, row := range markup.InlineKeyboard {
		for _, b := range row {
			if b.Text == text {
				return true
			}
		}
	}
	return false
}

// TestCheburMenuMarkupPagination (wave G4, replaces the Wave-F B4 cap+stub
// pin TestCheburMenuMarkupCapOverflowAndEmptyState): 25 unique hosts paginate
// at 20 per page — page 1 shows 20 «🧀 <host>» buttons plus the «Стр. 1/2»
// row with next-only navigation, page 2 shows the remaining 5 with prev.
// Every emitted callback routes (no dead buttons) and the wave-F overflow
// stub row is gone.
func TestCheburMenuMarkupPagination(t *testing.T) {
	proxies := cheburPageProxies()
	hosts := cheburHostsFromSnapshot(proxies)
	if len(hosts) != 25 {
		t.Fatalf("expected 25 unique hosts, got %d", len(hosts))
	}
	totalPages := menuTotalPages(len(hosts), menuListPageSize)
	if totalPages != 2 {
		t.Fatalf("expected 2 pages for 25 hosts at page size 20, got %d", totalPages)
	}

	mk1 := CheburMenuMarkup(hosts, 1, totalPages)
	requireAllCallbacksRoutable(t, "CheburMenuMarkup page 1", mk1)
	btns1 := cheburHostButtonCallbacks(mk1)
	if len(btns1) != 20 {
		t.Errorf("expected 20 host buttons on page 1, got %d", len(btns1))
	}
	if !hasCallback(mk1, "menu:cheburh:host-00.example.com") {
		t.Errorf("page 1 must start at the first sorted host, got %v", btns1)
	}
	if hasCallback(mk1, "menu:cheburh:host-24.example.com") {
		t.Errorf("page 1 must not contain page-2 hosts")
	}
	if !hasCallback(mk1, "menu:cheburpg:2") {
		t.Errorf("page 1 must carry the next-page button menu:cheburpg:2")
	}
	if hasCallback(mk1, "menu:cheburpg:0") {
		t.Errorf("page 1 must not carry a prev-page button (next-only)")
	}
	if !hasButtonText(mk1, "Стр. 1/2") {
		t.Errorf("page 1 must carry the «Стр. 1/2» label, got %v", markupCallbacks(mk1))
	}
	for _, row := range mk1.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.Text, "и ещё") {
				t.Errorf("overflow stub must be removed in favor of pagination, got %q", b.Text)
			}
		}
	}

	mk2 := CheburMenuMarkup(hosts, 2, totalPages)
	requireAllCallbacksRoutable(t, "CheburMenuMarkup page 2", mk2)
	btns2 := cheburHostButtonCallbacks(mk2)
	if len(btns2) != 5 {
		t.Errorf("expected 5 host buttons on page 2, got %d", len(btns2))
	}
	if !hasCallback(mk2, "menu:cheburh:host-24.example.com") {
		t.Errorf("page 2 must contain the last host, got %v", btns2)
	}
	if !hasCallback(mk2, "menu:cheburpg:1") {
		t.Errorf("page 2 must carry the prev-page button menu:cheburpg:1")
	}
	if hasCallback(mk2, "menu:cheburpg:3") {
		t.Errorf("page 2 must not carry a next-page button")
	}
	if !hasButtonText(mk2, "Стр. 2/2") {
		t.Errorf("page 2 must carry the «Стр. 2/2» label, got %v", markupCallbacks(mk2))
	}

	// Control: exactly 20 hosts → single page, no pagination row.
	mk20 := CheburMenuMarkup(hosts[:20], 1, 1)
	if len(cheburHostButtonCallbacks(mk20)) != 20 {
		t.Errorf("expected 20 host buttons for a full single page, got %d", len(cheburHostButtonCallbacks(mk20)))
	}
	if hasCallback(mk20, "menu:cheburpg:2") {
		t.Errorf("a single page must not carry pagination buttons")
	}
	if hasButtonText(mk20, "Стр. 1/1") {
		t.Errorf("a single page must not carry the «Стр. N/M» row")
	}
}

// TestCheckHostMenuMarkupPagination (wave G4, replaces the CheckHostMenuMarkup
// overflow stub): 25 proxies paginate at 20 per page with menu:checkhostpg:
// navigation; the «...и ещё %d прокси-хостов» stub row is gone; every emitted
// callback routes.
func TestCheckHostMenuMarkupPagination(t *testing.T) {
	proxies := cheburPageProxies()
	totalPages := menuTotalPages(len(proxies), menuListPageSize)
	if totalPages != 2 {
		t.Fatalf("expected 2 pages for 25 proxies, got %d", totalPages)
	}

	runButtons := func(markup *telego.InlineKeyboardMarkup) int {
		n := 0
		for _, row := range markup.InlineKeyboard {
			for _, b := range row {
				if strings.HasPrefix(b.CallbackData, "menu:checkhost:run:") {
					n++
				}
			}
		}
		return n
	}

	mk1 := CheckHostMenuMarkup(proxies, 1, totalPages)
	requireAllCallbacksRoutable(t, "CheckHostMenuMarkup page 1", mk1)
	if got := runButtons(mk1); got != 20 {
		t.Errorf("expected 20 run buttons on page 1, got %d", got)
	}
	if !hasCallback(mk1, "menu:checkhostpg:2") {
		t.Errorf("page 1 must carry the next-page button menu:checkhostpg:2")
	}
	if hasCallback(mk1, "menu:checkhostpg:0") {
		t.Errorf("page 1 must not carry a prev-page button")
	}
	if !hasButtonText(mk1, "Стр. 1/2") {
		t.Errorf("page 1 must carry the «Стр. 1/2» label, got %v", markupCallbacks(mk1))
	}
	for _, row := range mk1.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.Text, "и ещё") {
				t.Errorf("overflow stub must be removed in favor of pagination, got %q", b.Text)
			}
		}
	}

	mk2 := CheckHostMenuMarkup(proxies, 2, totalPages)
	requireAllCallbacksRoutable(t, "CheckHostMenuMarkup page 2", mk2)
	if got := runButtons(mk2); got != 5 {
		t.Errorf("expected 5 run buttons on page 2, got %d", got)
	}
	if !hasCallback(mk2, "menu:checkhostpg:1") {
		t.Errorf("page 2 must carry the prev-page button menu:checkhostpg:1")
	}
	if hasCallback(mk2, "menu:checkhostpg:3") {
		t.Errorf("page 2 must not carry a next-page button")
	}

	// Control: 20 proxies → single page, no pagination row.
	mk20 := CheckHostMenuMarkup(proxies[:20], 1, 1)
	if got := runButtons(mk20); got != 20 {
		t.Errorf("expected 20 run buttons for a full single page, got %d", got)
	}
	if hasCallback(mk20, "menu:checkhostpg:2") {
		t.Errorf("a single page must not carry pagination buttons")
	}
}

// TestCheburRoutesExactVsPrefix (wave G4): the new pager routes
// menu:cheburpg:<page> / menu:checkhostpg:<page> and the per-host probe
// menu:cheburh:<key> are prefix branches that can never collide with the
// exact submenu-open case menu:chebur; out-of-range pages clamp; the probe
// fires only from the cheburh route.
func TestCheburRoutesExactVsPrefix(t *testing.T) {
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

	checkCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(checkTargets)
	}

	// 1. Exact menu:chebur opens the submenu (page 1) — never a probe.
	before := checkCount()
	b.handleCallbackQuery(gateCallback(9999, "menu:chebur"))
	if edits := ft.callsOf("editMessageText"); len(edits) == 0 {
		t.Fatal("expected menu:chebur to render the submenu")
	}
	if got := checkCount(); got != before {
		t.Errorf("exact menu:chebur must not probe, saw /check count %d → %d", before, got)
	}

	// 2. menu:cheburpg:2 (out of range for a 1-host snapshot) clamps to the
	//    last page and renders — no probe.
	editsBefore := len(ft.callsOf("editMessageText"))
	b.handleCallbackQuery(gateCallback(9999, "menu:cheburpg:2"))
	if got := checkCount(); got != before {
		t.Errorf("menu:cheburpg must not probe, saw /check count %d → %d", before, got)
	}
	if got := len(ft.callsOf("editMessageText")); got != editsBefore+1 {
		t.Errorf("menu:cheburpg:2 must render a page edit, got edits %d → %d", editsBefore, got)
	}

	// 3. menu:checkhostpg:2 (out of range) clamps and renders — no probe.
	editsBefore = len(ft.callsOf("editMessageText"))
	b.handleCallbackQuery(gateCallback(9999, "menu:checkhostpg:2"))
	if got := checkCount(); got != before {
		t.Errorf("menu:checkhostpg must not probe, saw /check count %d → %d", before, got)
	}
	if got := len(ft.callsOf("editMessageText")); got != editsBefore+1 {
		t.Errorf("menu:checkhostpg:2 must render a page edit, got edits %d → %d", editsBefore, got)
	}

	// 4. menu:cheburh:1.1.1.1 reaches the per-host probe handler.
	b.handleCallbackQuery(gateCallback(9999, "menu:cheburh:1.1.1.1"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && checkCount() < before+1 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := checkCount(); got != before+1 {
		t.Fatalf("expected menu:cheburh:1.1.1.1 to fire exactly one /check, got %d → %d", before, got)
	}

	// 5. An unknown host key answers gracefully (host not found) — no probe.
	//    The route dispatches the handler in a goroutine, so poll for the edit.
	before = checkCount()
	b.handleCallbackQuery(gateCallback(9999, "menu:cheburh:not-in-snapshot.example.com"))
	foundNotFound := false
	notFoundDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(notFoundDeadline) && !foundNotFound {
		for _, c := range ft.callsOf("editMessageText") {
			if txt, _ := c.Params["text"].(string); strings.Contains(txt, "Хост не найден в текущей конфигурации") {
				foundNotFound = true
			}
		}
		if !foundNotFound {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if got := checkCount(); got != before {
		t.Errorf("unknown host key must not probe, saw /check count %d → %d", before, got)
	}
	if !foundNotFound {
		t.Errorf("expected the graceful host-not-found message after an unknown key")
	}

	// 6. Route inventory pin: both new pager routes and the per-host probe
	//    route are wired into callbacks.go (no dead buttons).
	for _, data := range []string{
		"menu:cheburpg:1",
		"menu:checkhostpg:1",
		"menu:cheburh:1.1.1.1",
	} {
		if !callbackRoutes(data) {
			t.Errorf("expected callbackRoutes(%q) = true (route must be wired in callbacks.go)", data)
		}
	}
	if callbackRoutes("menu:chebur:legacy-stable-id") {
		t.Errorf("the removed stableID route menu:chebur:<id> must no longer be wired")
	}
}
