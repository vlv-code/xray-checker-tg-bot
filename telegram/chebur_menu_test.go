package telegram

import (
	"encoding/json"
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

// TestMainMenuHasCheburcheckButton pins the Wave-D main-menu entry with the
// exact callback menu:chebur on its own row BETWEEN the Check-Host row and the
// Nodes row. Wave H3 register (authorized label rework 2026-11-17): the
// former «🧀 Cheburcheck РФ» label is replaced by the plain «Cheburcheck» —
// no emoji, no «РФ». Wave M-D (M8): the label gains the plain 🛡️ shield
// fallback «🛡️ Cheburcheck» (IconChebur — no clean pack shield, CustomID "";
// still no 🧀/«РФ»).
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
	if label != "🛡️ Cheburcheck" {
		t.Errorf("expected plain '🛡️ Cheburcheck' button (wave H3: no 🧀/РФ; wave M-D: 🛡️ shield fallback), got %q", label)
	}
	if strings.Contains(label, "🧀") || strings.Contains(label, "🇷🇺") || strings.Contains(label, "РФ") {
		t.Errorf("wave H3: main-menu button must be free of 🧀/🇷🇺/РФ, got %q", label)
	}
	if checkhostRow == -1 || nodesRow == -1 || checkhostRow >= cheburRow || cheburRow >= nodesRow {
		t.Errorf("expected menu:chebur row %d strictly between Check-Host row %d and Nodes row %d",
			cheburRow, checkhostRow, nodesRow)
	}
}

// TestMenuCheburOpensSubmenu: the exact callback menu:chebur must edit the
// message into the Cheburcheck host-selection submenu (RU text carrying the
// /cheburcheck usage hint; wave H3 register: the title is
// «Cheburcheck — активный пробинг блокировок», free of 🧀 and «РФ»); with zero
// hosts the hint-only submenu must still render without panicking.
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
	if !strings.Contains(text, "Cheburcheck — активный пробинг блокировок") {
		t.Errorf("expected the wave H3 submenu title, got: %s", text)
	}
	if strings.Contains(text, "🧀") || strings.Contains(text, "РФ") {
		t.Errorf("wave H3: submenu text must be free of 🧀/РФ, got: %s", text)
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
	// Wave H3 register: the detection string follows the reworked title
	// («Cheburcheck — активный пробинг блокировок», no РФ).
	b.handleCallbackQuery(gateCallback(9999, "menu:chebur"))
	var submenuEdit string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && submenuEdit == "" {
		for _, c := range ft.callsOf("editMessageText") {
			if txt, _ := c.Params["text"].(string); strings.Contains(txt, "активный пробинг блокировок") {
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
// plus the 2-per-row layout. Wave H4 register (authorized layout change
// 2026-11-17): the manual-input row «✍️ Проверить свой хост» adds one row.
func TestCheburMenuMarkupCallbackLength(t *testing.T) {
	longHost := strings.Repeat("a", 60) + ".example.com" // > 36 bytes → hashed key
	hosts := []string{"1.1.1.1", longHost}
	markup := CheburMenuMarkup(hosts, 1, 1)
	if markup == nil {
		t.Fatal("nil markup")
	}
	if len(markup.InlineKeyboard) != 3 {
		t.Errorf("expected 3 rows (one pair row + manual-input row + back row), got %d rows", len(markup.InlineKeyboard))
	}
	// Wave K-B3 register (back-row unification): the submenu back button is
	// the unified «↩️ Главное меню» (the former «⬅️ Назад» is replaced).
	foundUnifiedBack := false
	for _, row := range markup.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "menu:main" {
				if b.Text != "↩️ Главное меню" {
					t.Errorf("wave K-B3: chebur submenu back button must read «↩️ Главное меню», got %q", b.Text)
				}
				foundUnifiedBack = true
			}
		}
	}
	if !foundUnifiedBack {
		t.Errorf("expected a menu:main back button in CheburMenuMarkup, got %v", markupCallbacks(markup))
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
// protocol/port) must yield exactly ONE «<host>» button; the host list is
// sorted; disabled proxies are excluded. Wave H3 register (authorized label
// rework 2026-11-17): host buttons are PLAIN <host> — the former «🧀» prefix
// is removed.
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
				if strings.Contains(b.Text, "🧀") {
					t.Errorf("wave H3: host buttons must be plain <host>, got %q", b.Text)
				}
			}
			if b.CallbackData == "menu:cheburh:shared.example.com" {
				foundShared = true
				if b.Text != "shared.example.com" {
					t.Errorf("expected the plain host as button text, got %q", b.Text)
				}
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

// TestCheburMenuMarkupManualInputRow (Wave H4): the Cheburcheck submenu gains
// the «✍️ Проверить свой хост» row with the exact route menu:cheburin — the
// button must never be dead and must stay within the 64-byte callback limit.
func TestCheburMenuMarkupManualInputRow(t *testing.T) {
	markup := CheburMenuMarkup([]string{"1.1.1.1"}, 1, 1)
	requireAllCallbacksRoutable(t, "CheburMenuMarkup(manual input row)", markup)
	if !hasCallback(markup, "menu:cheburin") {
		t.Fatalf("expected the manual-input button with exact callback menu:cheburin, got %v", markupCallbacks(markup))
	}
	if !hasButtonText(markup, "✍️ Проверить свой хост") {
		t.Errorf("expected the «✍️ Проверить свой хост» button, got %v", markupCallbacks(markup))
	}
}

// TestMenuCheburinManualInputFlow (Wave H4): the manual host input flow —
// menu:cheburin arms the per-chat waiting state and prompts in RU; the next
// plain text from an ADMIN is consumed as host[:port] (port stripped) and
// probed via the injected chebur client; non-admins are denied; invalid input
// answers with a RU validation error; navigating to the main menu disarms.
func TestMenuCheburinManualInputFlow(t *testing.T) {
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
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "injob"})
		case r.URL.Path == "/probe/injob":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"event: started\ndata: {\"id\":\"injob\",\"target\":\"h\",\"online_probes\":1}\n\n" +
					"event: result\ndata: {\"job_id\":\"j\",\"probe_id\":\"p1\",\"verdicts\":[\"ok\"]}\n\n" +
					"event: done\ndata: {\"id\":\"injob\",\"status\":\"finished\",\"response_count\":1,\"online_probes\":1}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer chebMock.Close()

	chebClient := checker.NewCheburcheckClient(chebMock.URL, 5*time.Second,
		[]checker.ScannerCred{{Name: "test-scanner", Token: "tok-1"}})

	checkTargetsLen := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(checkTargets)
	}
	waitChecksGrew := func(baseline int) []string {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if checkTargetsLen() > baseline {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), checkTargets[baseline:]...)
	}
	newTargetsSince := func(baseline int) []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), checkTargets[baseline:]...)
	}

	// 1. menu:cheburin arms the waiting state and renders the RU prompt.
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	b.cheburClient = chebClient

	b.handleCallbackQuery(gateCallback(9999, "menu:cheburin"))
	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected menu:cheburin to render the input prompt, got 0 edits")
	}
	prompt, _ := edits[0].Params["text"].(string)
	if !strings.Contains(prompt, "Проверить свой хост") || !strings.Contains(prompt, "Отправьте хост") {
		t.Errorf("expected the RU manual-input prompt, got: %s", prompt)
	}
	if !b.hasPending(12345) {
		t.Fatalf("menu:cheburin must arm the pending input for the chat")
	}

	// 2. The next plain text is consumed as host:port — port stripped.
	baseline := checkTargetsLen()
	b.handleMessage(&telego.Message{
		MessageID: 11,
		Chat:      telego.Chat{ID: 12345, Type: "private"},
		From:      &telego.User{ID: 9999, Username: "admin"},
		Text:      "example.com:8443",
	})
	got := waitChecksGrew(baseline)
	if len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected the waiting text to probe the bare host example.com (port stripped), got %v", got)
	}
	if b.hasPending(12345) {
		t.Fatalf("pending input must be consumed after the input")
	}
	deadline := time.Now().Add(2 * time.Second)
	var report string
	for time.Now().Before(deadline) && report == "" {
		for _, c := range ft.callsOf("editMessageText") {
			if txt, _ := c.Params["text"].(string); strings.Contains(txt, "<b>Cheburcheck</b>: <code>example.com</code>") {
				report = txt
			}
		}
		if report == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if report == "" {
		t.Errorf("expected the FormatCheburcheckReport edit for the manual host, got %v", ft.callsOf("editMessageText"))
	}

	// 3. Non-admin text while armed: denied, no probe.
	baselineNA := checkTargetsLen()
	ftNA, apiNA := newFakeTelegramBot(t)
	bNA, _ := newGateTestBot(t, apiNA, []int64{1001})
	bNA.cheburClient = chebClient
	bNA.handleCallbackQuery(gateCallback(1001, "menu:cheburin"))
	bNA.handleMessage(&telego.Message{
		Chat: telego.Chat{ID: 12345, Type: "private"},
		From: &telego.User{ID: 9999, Username: "intruder"},
		Text: "evil.example.com",
	})
	time.Sleep(150 * time.Millisecond)
	if got := newTargetsSince(baselineNA); len(got) != 0 {
		t.Fatalf("non-admin waiting text must be denied, saw /check targets %v", got)
	}
	if bNA.hasPending(12345) {
		t.Fatalf("pending input must be consumed even when denied")
	}
	for _, c := range ftNA.callsOf("sendMessage") {
		if txt, _ := c.Params["text"].(string); strings.Contains(txt, "Cheburcheck") {
			t.Errorf("non-admin must never receive a probe report, got: %s", txt)
		}
	}

	// 4. Invalid input: RU validation error, no network.
	baselineBad := checkTargetsLen()
	ftBad, apiBad := newFakeTelegramBot(t)
	bBad, _ := newGateTestBot(t, apiBad, nil)
	bBad.cheburClient = chebClient
	bBad.handleCallbackQuery(gateCallback(9999, "menu:cheburin"))
	bBad.handleMessage(&telego.Message{
		Chat: telego.Chat{ID: 12345, Type: "private"},
		From: &telego.User{ID: 9999, Username: "admin"},
		Text: "https://example.com/path",
	})
	time.Sleep(150 * time.Millisecond)
	if got := newTargetsSince(baselineBad); len(got) != 0 {
		t.Fatalf("invalid input must not reach the probe, saw /check targets %v", got)
	}
	var errReply string
	for _, c := range ftBad.callsOf("sendMessage") {
		if txt, _ := c.Params["text"].(string); strings.HasPrefix(txt, "❌") {
			errReply = txt
		}
	}
	if !strings.Contains(errReply, "Отправьте") {
		t.Errorf("expected a RU validation error, got: %q", errReply)
	}

	// 5. Navigation to the main menu disarms the pending input.
	baselineNav := checkTargetsLen()
	ftNav, apiNav := newFakeTelegramBot(t)
	bNav, _ := newGateTestBot(t, apiNav, nil)
	bNav.cheburClient = chebClient
	bNav.handleCallbackQuery(gateCallback(9999, "menu:cheburin"))
	if !bNav.hasPending(12345) {
		t.Fatalf("expected the armed state before navigation")
	}
	bNav.handleCallbackQuery(gateCallback(9999, "menu:main"))
	if bNav.hasPending(12345) {
		t.Fatalf("menu:main must disarm the pending input")
	}
	if len(ftNav.callsOf("editMessageText")) == 0 {
		t.Fatalf("expected menu:main to render the main menu")
	}
	bNav.handleMessage(&telego.Message{
		Chat: telego.Chat{ID: 12345, Type: "private"},
		From: &telego.User{ID: 9999, Username: "admin"},
		Text: "example.com",
	})
	time.Sleep(150 * time.Millisecond)
	if got := newTargetsSince(baselineNav); len(got) != 0 {
		t.Fatalf("disarmed state must not probe, saw /check targets %v", got)
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

// TestMenuCheburinPromptUnifiedBackRow (wave K-B3): the manual-input prompt
// screen (menu:cheburin) carries the unified back row — «↩️ К Cheburcheck» and
// «☰ Главное меню» — with no 🏠 anywhere (the master-only collision rule).
func TestMenuCheburinPromptUnifiedBackRow(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)

	b.handleCallbackQuery(gateCallback(9999, "menu:cheburin"))

	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected the cheburin prompt edit, got 0 edits")
	}
	mk := decodedReplyMarkup(t, edits[len(edits)-1])
	if mk == nil {
		t.Fatalf("expected reply_markup on the cheburin prompt edit, got %v", edits)
	}
	requireAllCallbacksRoutable(t, "cheburin prompt", mk)
	if !hasCallback(mk, "menu:chebur") || !hasCallback(mk, "menu:main") {
		t.Fatalf("expected back-to-list and main-menu buttons, got %v", markupCallbacks(mk))
	}
	for _, row := range mk.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.Text, "🏠") || strings.Contains(btn.Text, "🌐") {
				t.Errorf("prompt buttons must be free of 🏠/🌐, got %q", btn.Text)
			}
			if btn.CallbackData == "menu:chebur" && btn.Text != "↩️ К Cheburcheck" {
				t.Errorf("expected «↩️ К Cheburcheck», got %q", btn.Text)
			}
			if btn.CallbackData == "menu:main" && btn.Text != "☰ Главное меню" {
				t.Errorf("expected «☰ Главное меню», got %q", btn.Text)
			}
		}
	}
}

// TestCheburMenuTextShieldHeader (wave N-A FIX 2b): the submenu header text
// carries the plain 🛡️ shield before the bold title — the message-text
// counterpart of the wave M8 «🛡️ Cheburcheck» button (chebur files are
// plain-by-construction: the fallback is rendered directly, no btnIcon
// wiring, no custom-emoji layer).
func TestCheburMenuTextShieldHeader(t *testing.T) {
	b := &Bot{}
	txt := b.getCheburMenuText(2)
	if !strings.Contains(txt, "🛡️ <b>Cheburcheck — активный пробинг блокировок</b>") {
		t.Errorf("expected the shield-prefixed header «🛡️ <b>Cheburcheck — …», got: %s", txt)
	}
	// The wave H3 pins survive: no 🧀/РФ anywhere in the submenu text.
	if strings.Contains(txt, "🧀") || strings.Contains(txt, "РФ") {
		t.Errorf("submenu text must stay free of 🧀/РФ, got: %s", txt)
	}
}
