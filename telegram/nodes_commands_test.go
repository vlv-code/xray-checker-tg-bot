package telegram

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
	"xray-checker/metrics"
)

func TestNodeLineFor(t *testing.T) {
	if got := nodeLineFor(metrics.ProxyMetric{Name: "p"}); got != "" {
		t.Errorf("local proxy must have no node line, got %q", got)
	}
	got := nodeLineFor(metrics.ProxyMetric{NodeName: "n1"})
	if !strings.Contains(got, "n1") {
		t.Errorf("node line missing node: %q", got)
	}
	got = nodeLineFor(metrics.ProxyMetric{NodeName: "n1", NodeASN: "AS9009 M247"})
	if !strings.Contains(got, "AS9009 M247") {
		t.Errorf("node line missing ASN: %q", got)
	}
}

func TestCommandArgs(t *testing.T) {
	if got := commandArgs("/nodeaddsub n1 https://x/y"); len(got) != 2 || got[0] != "n1" || got[1] != "https://x/y" {
		t.Errorf("commandArgs: %v", got)
	}
	if got := commandArgs("/nodes"); len(got) != 0 {
		t.Errorf("commandArgs no-arg: %v", got)
	}
}

func TestFormatAge(t *testing.T) {
	cases := map[string]struct {
		d    time.Duration
		want string
	}{
		"12s":   {12 * time.Second, "12s"},
		"3m12s": {3*time.Minute + 12*time.Second, "3m12s"},
		"2h05m": {2*time.Hour + 5*time.Minute, "2h05m"},
	}
	for name, c := range cases {
		if got := formatAge(c.d); got != c.want {
			t.Errorf("%s: formatAge = %q, want %q", name, got, c.want)
		}
	}
}

type fakeNodeManager struct {
	nodes        map[string]string    // name -> token
	subs         map[string][]string  // name -> URLs
	overridden   map[string]bool      // settings override marks
	disabled     map[string]bool      // G5b: name -> disabled state
	renames      []string             // G5a: "old->new" records
	enabledCalls []string             // G5b: "name=true|false" records
	lastReport   map[string]time.Time // K-B2: name -> last report time (age column)
}

func (f *fakeNodeManager) Nodes() []NodeInfo {
	var out []NodeInfo
	for name := range f.nodes {
		n := NodeInfo{Name: name, Up: true, Disabled: f.disabled[name]}
		if lr, ok := f.lastReport[name]; ok && !lr.IsZero() {
			n.EverReported = true
			n.LastReport = lr
		}
		out = append(out, n)
	}
	return out
}

func (f *fakeNodeManager) ManagedSubs(node string) ([]ManagedSubInfo, error) {
	if f.subs == nil {
		return nil, nil
	}
	var out []ManagedSubInfo
	for _, u := range f.subs[node] {
		out = append(out, ManagedSubInfo{URL: u, ProxyCount: 10})
	}
	return out, nil
}

func (f *fakeNodeManager) AddSub(node, url string) error {
	if f.subs == nil {
		f.subs = make(map[string][]string)
	}
	f.subs[node] = append(f.subs[node], url)
	return nil
}

func (f *fakeNodeManager) RemoveSub(node, url string) error {
	if f.subs == nil {
		return nil
	}
	list := f.subs[node]
	for i, u := range list {
		if u == url {
			f.subs[node] = append(list[:i], list[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeNodeManager) AddNode(name, token string) error {
	if f.nodes == nil {
		f.nodes = make(map[string]string)
	}
	if _, exists := f.nodes[name]; exists {
		return fmt.Errorf("нода уже существует")
	}
	f.nodes[name] = token
	return nil
}

func (f *fakeNodeManager) RemoveNode(name string) error {
	if _, exists := f.nodes[name]; !exists {
		return fmt.Errorf("нода не найдена")
	}
	delete(f.nodes, name)
	return nil
}

func (f *fakeNodeManager) NodeSnapshot(node string) []metrics.ProxyMetric {
	return nil
}

func (f *fakeNodeManager) NodeDiagReports(node string) []checker.ProxyDiagReport {
	return nil
}

func TestGenerateNodeToken(t *testing.T) {
	tok1 := GenerateNodeToken()
	tok2 := GenerateNodeToken()
	if len(tok1) != 64 {
		t.Fatalf("expected 64 characters hex token, got %d (%s)", len(tok1), tok1)
	}
	if tok1 == tok2 {
		t.Fatalf("subsequent tokens must not match")
	}
}

// TestNodesSubsManageView (replaces TestNodesSubsViews — authorized register
// entry, decision D4: the global «Подписки нод» list screen was removed; the
// per-node manage view is the only subs surface and is reached from the
// per-node cfg screen).
func TestNodesSubsManageView(t *testing.T) {
	fnm := &fakeNodeManager{
		nodes: map[string]string{"m31a": "tok1", "msk-1": "tok2"},
		subs: map[string][]string{
			"m31a": {"https://sub.url/1", "https://sub.url/2"},
		},
	}
	b := &Bot{nodeMgr: fnm}
	manageText, manageMarkup := b.getNodeSubsManageView("m31a")
	if !strings.Contains(manageText, "m31a") || !strings.Contains(manageText, "https://sub.url/1") {
		t.Errorf("expected node details in manage view text: %s", manageText)
	}
	if manageMarkup == nil || len(manageMarkup.InlineKeyboard) < 3 {
		t.Fatalf("expected manage markup with delete & add buttons, got %v", manageMarkup)
	}
}

func TestHandleNodeAddSubURL(t *testing.T) {
	fnm := &fakeNodeManager{
		nodes: map[string]string{"m31a": "tok1"},
	}
	b := &Bot{
		nodeMgr:        fnm,
		allowedChatIDs: map[int64]bool{123: true},
	}
	b.armPending(123, pendingKindNodeSub, "m31a")

	// Message with raw URL
	msg := &telego.Message{
		Chat: telego.Chat{ID: 123},
		Text: "https://example.com/newsub",
	}

	b.handleMessage(msg)

	// Verify that sub was added to node manager
	subs, err := fnm.ManagedSubs("m31a")
	if err != nil {
		t.Fatalf("ManagedSubs failed: %v", err)
	}
	if len(subs) != 1 || subs[0].URL != "https://example.com/newsub" {
		t.Fatalf("expected sub to be added, got: %v", subs)
	}

	// Verify that the pending input was consumed
	if b.hasPending(123) {
		t.Errorf("expected the pending input to be cleared")
	}
}

func TestNodeSubsCallbacks(t *testing.T) {
	fnm := &fakeNodeManager{
		nodes: map[string]string{"m31a": "tok1"},
		subs: map[string][]string{
			"m31a": {"https://sub.url/1"},
		},
	}
	b := &Bot{
		nodeMgr:        fnm,
		allowedChatIDs: map[int64]bool{123: true},
	}

	cbMsg := &telego.Message{
		Chat:      telego.Chat{ID: 123},
		MessageID: 456,
	}

	// 1. menu:nodes:addsub:m31a arms the pending input with the node payload
	b.handleCallbackQuery(&telego.CallbackQuery{
		ID:      "q1",
		Data:    "menu:nodes:addsub:m31a",
		Message: cbMsg,
	})
	kind, payload, ok := b.takePending(123)
	if !ok || kind != pendingKindNodeSub || payload != "m31a" {
		t.Errorf("expected the armed nodesub input (m31a), got ok=%v kind=%q payload=%q", ok, kind, payload)
	}
	b.armPending(123, pendingKindNodeSub, "m31a")

	// 2. menu:nodes:subnode:m31a clears the pending input (Wave H2/D4
	// register: the former menu:nodes:subs disarm route was removed; the
	// manage screen re-entry keeps the same disarm semantics — wave L-1 hook)
	b.handleCallbackQuery(&telego.CallbackQuery{
		ID:      "q2",
		Data:    "menu:nodes:subnode:m31a",
		Message: cbMsg,
	})
	if b.hasPending(123) {
		t.Errorf("expected the pending input to be cleared on menu:nodes:subnode:m31a")
	}

	// 3. Wave K-B2 two-step delete (wired by K-B3): the first
	//    menu:nodes:delsub:m31a:0 tap renders the confirm screen and removes
	//    NOTHING; the explicit «:yes» callback performs the removal.
	b.handleCallbackQuery(&telego.CallbackQuery{
		ID:      "q3",
		Data:    "menu:nodes:delsub:m31a:0",
		Message: cbMsg,
	})
	subs, err := fnm.ManagedSubs("m31a")
	if err != nil {
		t.Fatalf("ManagedSubs failed: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("first delsub tap must only confirm (no removal), got %d subs", len(subs))
	}
	b.handleCallbackQuery(&telego.CallbackQuery{
		ID:      "q4",
		Data:    "menu:nodes:delsub:m31a:0:yes",
		Message: cbMsg,
	})
	subs, err = fnm.ManagedSubs("m31a")
	if err != nil {
		t.Fatalf("ManagedSubs failed: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("expected 0 subs after the confirming :yes tap, got %d", len(subs))
	}
}

func (f *fakeNodeManager) NodeSettingsView(node string) ([]NodeSettingEntry, error) {
	if _, ok := f.nodes[node]; !ok {
		return nil, fmt.Errorf("неизвестная нода: %s", node)
	}
	return []NodeSettingEntry{
		{Key: "check_interval", Value: "300"},
		{Key: "check_method", Value: "status", Overridden: f.overridden["check_method"]},
	}, nil
}

func (f *fakeNodeManager) SetNodeSetting(node, key, value string) error {
	if _, ok := f.nodes[node]; !ok {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	if key != "check_method" && key != "check_interval" {
		return fmt.Errorf("неизвестный ключ %q", key)
	}
	if f.overridden == nil {
		f.overridden = map[string]bool{}
	}
	f.overridden[key] = true
	return nil
}

func (f *fakeNodeManager) ResetNodeSetting(node, key string) error {
	if _, ok := f.nodes[node]; !ok {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	if key != "" {
		delete(f.overridden, key)
	} else {
		f.overridden = nil
	}
	return nil
}

// RenameNode implements the NodeManager extension (Wave G5a): test double
// that migrates name/token/disabled state and records the rename.
func (f *fakeNodeManager) RenameNode(oldName, newName string) error {
	if _, ok := f.nodes[oldName]; !ok {
		return fmt.Errorf("нода не найдена")
	}
	if _, exists := f.nodes[newName]; exists {
		return fmt.Errorf("нода уже существует")
	}
	f.nodes[newName] = f.nodes[oldName]
	delete(f.nodes, oldName)
	if f.disabled[oldName] {
		f.disabled[newName] = true
		delete(f.disabled, oldName)
	}
	f.renames = append(f.renames, oldName+"->"+newName)
	return nil
}

// SetNodeEnabled implements the NodeManager extension (Wave G5b).
func (f *fakeNodeManager) SetNodeEnabled(name string, enabled bool) error {
	if _, ok := f.nodes[name]; !ok {
		return fmt.Errorf("нода не найдена")
	}
	if f.disabled == nil {
		f.disabled = make(map[string]bool)
	}
	f.disabled[name] = !enabled
	state := "false"
	if enabled {
		state = "true"
	}
	f.enabledCalls = append(f.enabledCalls, name+"="+state)
	return nil
}

func TestProcessNodeCheckHostAudits(t *testing.T) {
	cm, err := NewConfigManager(filepath.Join(t.TempDir(), "cfg.json"), BotConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// Production defaults enable Check-Host alerts via env; the zero-value
	// config has them off, so turn them on explicitly for the test.
	_ = cm.Update(func(c *BotConfig) { c.CheckHostAlertEnabled = true })
	b := &Bot{
		configMgr:   cm,
		tracker:     NewAlertTracker(),
		eventBuffer: NewEventBuffer(),
		targets:     []ChatTarget{{ChatID: 1}},
	}
	// Fix "now" at noon so quiet hours never swallow the alert path.
	b.nowFunc = func() time.Time {
		return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	}

	key := "checkhost:h.example:443"
	down := map[string]checker.CheckHostSummary{
		"h.example:443": {RUAvailable: false, WorldAvailable: true, PermanentLink: "https://check-host.net/check-report/x"},
	}
	up := map[string]checker.CheckHostSummary{
		"h.example:443": {RUAvailable: true, WorldAvailable: true},
	}

	// Down: alert tracked for the chat.
	b.ProcessNodeCheckHostAudits("n1", down)
	if !b.tracker.HasAlert(1, 0, key) {
		t.Fatal("expected RU-block alert to be tracked")
	}

	// Same report again: idempotent, still exactly tracked.
	b.ProcessNodeCheckHostAudits("n1", down)
	if !b.tracker.HasAlert(1, 0, key) {
		t.Fatal("alert must remain tracked")
	}

	// Unknown node name / empty audits: no-ops.
	b.ProcessNodeCheckHostAudits("", down)
	b.ProcessNodeCheckHostAudits("n1", nil)

	// Recovery: alert resolved (clean mode deletes, message no-op with nil api).
	b.ProcessNodeCheckHostAudits("n1", up)
	if b.tracker.HasAlert(1, 0, key) {
		t.Fatal("alert must be resolved after RU recovery")
	}
}

// --- Wave K-B2: compact /nodeadd reply with copy-text rows ---

// nodeAddCopyTexts returns the copy-text payload of every button whose label
// contains the given substring (helper for the copy-row assertions).
func nodeAddCopyTexts(markup *telego.InlineKeyboardMarkup, labelPart string) []string {
	var out []string
	for _, row := range markup.InlineKeyboard {
		for _, b := range row {
			if b.CopyText != nil && strings.Contains(b.Text, labelPart) {
				out = append(out, b.CopyText.Text)
			}
		}
	}
	return out
}

// findButtonText returns the text of the first button whose label contains
// the given substring ("" for the first button overall).
func findButtonText(markup *telego.InlineKeyboardMarkup, labelPart string) string {
	for _, row := range markup.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.Text, labelPart) {
				return b.Text
			}
		}
	}
	return ""
}

// TestBuildNodeRegisteredTextCompact pins the Wave K-B2 text contract: the
// registration header and the copy-button guidance survive, and the giant
// <pre> dump is gone (the values moved into the copy rows; escape guarantees
// are pinned by nodes_commands_escape_test.go).
func TestBuildNodeRegisteredTextCompact(t *testing.T) {
	text := buildNodeRegisteredText("n1", strings.Repeat("t", 64), "https://m.example/r")
	if !strings.Contains(text, "успешно зарегистрирована") {
		t.Errorf("registration header must survive, got:\n%s", text)
	}
	if strings.Contains(text, "<pre>") {
		t.Errorf("giant <pre> dump must be gone, got:\n%s", text)
	}
	if !strings.Contains(text, "скопиру") {
		t.Errorf("copy-button guidance expected, got:\n%s", text)
	}
	if !strings.Contains(text, "Docker") {
		t.Errorf("docker launch instruction expected, got:\n%s", text)
	}
}

// TestNodeAddCopyMarkup pins the Wave K-B2 /nodeadd keyboard: copy-text rows
// for the token, the report URL and the docker run one-liner (RAW payloads —
// copy text must NOT be HTML-escaped), short-form value previews in the
// labels, the passive «🔄 Обновить» re-render row (was «🩺 Проверить связь»),
// «↩️» back wording, and every callback within the 64-byte limit.
func TestNodeAddCopyMarkup(t *testing.T) {
	token := strings.Repeat("a1b2", 16) // 64 hex chars
	rawURL := "https://master.example/report?a=1&b=2"
	m := nodeAddCopyMarkup("n1", token, rawURL)

	// Token row: label carries the value short form, payload is the raw token.
	tokTexts := nodeAddCopyTexts(m, "Токен ноды")
	if len(tokTexts) != 1 || tokTexts[0] != token {
		t.Fatalf("expected one raw-token copy row, got %v", tokTexts)
	}
	tokLabel := findButtonText(m, "Токен ноды")
	if !strings.Contains(tokLabel, token[:8]) || !strings.Contains(tokLabel, "…") {
		t.Errorf("token label must include the value short form, got %q", tokLabel)
	}

	// URL row: RAW URL (literal &, never &amp; in the copy payload).
	urlTexts := nodeAddCopyTexts(m, "URL отчёта")
	if len(urlTexts) != 1 || urlTexts[0] != rawURL {
		t.Fatalf("expected one raw-URL copy row, got %v", urlTexts)
	}
	if urlLabel := findButtonText(m, "URL отчёта"); !strings.Contains(urlLabel, "master.example") {
		t.Errorf("URL label must include the host short form, got %q", urlLabel)
	}

	// Command row: a single part while the one-liner fits the 256-char cap.
	cmdTexts := nodeAddCopyTexts(m, "Команда запуска")
	if len(cmdTexts) != 1 {
		t.Fatalf("expected a single command copy row, got %d", len(cmdTexts))
	}
	cmd := cmdTexts[0]
	if len(cmd) > 256 {
		t.Errorf("copy payload exceeds the 256-char cap: %d", len(cmd))
	}
	if !strings.HasPrefix(cmd, "docker run -d --name xray-node-n1 ") ||
		!strings.Contains(cmd, " -e REPORT_URL="+rawURL+" ") ||
		!strings.Contains(cmd, " -e REPORT_TOKEN="+token+" ") ||
		!strings.HasSuffix(cmd, " ghcr.io/vlv-code/xray-checker-tg-bot:latest") {
		t.Errorf("command copy payload malformed: %q", cmd)
	}
	if cmdLabel := findButtonText(m, "Команда запуска"); !strings.Contains(cmdLabel, "xray-node-n1") {
		t.Errorf("command label must include the container short form, got %q", cmdLabel)
	}

	// Passive re-render honesty (K-B2 deliverable 3): «🔄 Обновить», and the
	// diagnostic-sounding «Проверить связь» wording is gone.
	if findButtonText(m, "🔄 Обновить") == "" || !hasCallback(m, "menu:nodes:health") {
		t.Errorf("expected the «🔄 Обновить» row targeting menu:nodes:health, got %v", markupCallbacks(m))
	}
	if findButtonText(m, "Проверить связь") != "" {
		t.Errorf("dishonest «Проверить связь» wording must be gone")
	}

	// Back-row wording (K-B2 deliverable 4): «↩️ К списку нод», no «🔙».
	for _, row := range m.InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.Text, "🔙") {
				t.Errorf("«🔙» wording must be gone, got %q", b.Text)
			}
		}
	}
	if back := findButtonText(m, "К списку нод"); !strings.HasPrefix(back, "↩️") {
		t.Errorf("back row must use «↩️» wording, got %q", back)
	}
	if !hasCallback(m, "menu:nodes") || !hasCallback(m, "menu:main") {
		t.Errorf("navigation callbacks must survive, got %v", markupCallbacks(m))
	}
	for _, cb := range markupCallbacks(m) {
		if len(cb) > 64 {
			t.Errorf("callback_data exceeds 64 bytes: %q (%d)", cb, len(cb))
		}
	}
}

// TestNodeAddCopyMarkupSplitsLongCommand: a one-liner that exceeds the
// 256-char copy-text cap (max-length node name + long report URL) is split
// into paste-ordered continuation parts («часть 1/2», «часть 2/2»), each
// within the cap, whose reconstruction equals the full command; an
// over-cap URL omits its copy row and the command parts degrade to the
// hint row instead of a dead button.
func TestNodeAddCopyMarkupSplitsLongCommand(t *testing.T) {
	token := strings.Repeat("f", 64)
	longName := strings.Repeat("n", 32)
	longURL := "https://master.example.com/very/long/report/path?a=1&b=2&c=3&d=4&e=5&f=6"
	full := nodeAddDockerCommand(longName, longURL, token)
	if len(full) <= 256 {
		t.Fatalf("fixture must overflow the 256-char cap, got %d", len(full))
	}
	m := nodeAddCopyMarkup(longName, token, longURL)

	parts := nodeAddCopyTexts(m, "Команда запуска")
	if len(parts) < 2 {
		t.Fatalf("expected split command parts, got %d", len(parts))
	}
	var rebuilt string
	for i, p := range parts {
		if len(p) > 256 {
			t.Errorf("part %d exceeds the cap: %d", i+1, len(p))
		}
		if i < len(parts)-1 {
			if !strings.HasSuffix(p, " \\") {
				t.Fatalf("continuation part %d must end with a backslash continuation, got %q", i+1, p)
			}
			rebuilt += p[:len(p)-2] + " "
		} else {
			rebuilt += p
		}
		if want := fmt.Sprintf("часть %d/%d", i+1, len(parts)); !strings.Contains(findButtonText(m, want), want) {
			t.Errorf("part %d label must carry its ordinal %q", i+1, want)
		}
	}
	if rebuilt != full {
		t.Errorf("paste reconstruction mismatch:\n got %q\nwant %q", rebuilt, full)
	}

	// Pathological URL (>256 chars): the URL copy row is omitted and the
	// command rows degrade to the hint row (no dead buttons).
	hugeURL := "https://m.example/" + strings.Repeat("x", 260)
	mHuge := nodeAddCopyMarkup("n1", token, hugeURL)
	if got := nodeAddCopyTexts(mHuge, "URL отчёта"); len(got) != 0 {
		t.Errorf("over-cap URL copy row must be omitted, got %v", got)
	}
	if got := nodeAddCopyTexts(mHuge, "Команда запуска"); len(got) != 0 {
		t.Errorf("unsplittable command copy rows must be omitted, got %v", got)
	}
	if findButtonText(mHuge, "не поместилась") == "" {
		t.Errorf("expected the over-cap hint row, got %v", markupCallbacks(mHuge))
	}
}

// TestReplyNodesReportAgeIcon pins the Wave K-B2 legend on the /nodes list:
// the report-age column uses ⏱️ fmtAgeIcon (RU units), not the compact
// formatAge latin units.
func TestReplyNodesReportAgeIcon(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	fnm := &fakeNodeManager{
		nodes:      map[string]string{"m31a": "t1"},
		lastReport: map[string]time.Time{"m31a": time.Now().Add(-2 * time.Minute)},
	}
	b, _ := newCfgTestBot(t, api, nil, fnm)

	b.replyNodes(ChatTarget{ChatID: 12345})

	sent := append(ft.callsOf("sendMessage"), ft.callsOf("editMessageText")...)
	if len(sent) == 0 {
		t.Fatal("expected /nodes to send a message")
	}
	text, _ := sent[0].Params["text"].(string)
	if !strings.Contains(text, "отчёт ⏱️ 2 мин") {
		t.Errorf("expected the ⏱️ fmtAgeIcon age column («отчёт ⏱️ 2 мин»), got: %s", text)
	}
	if strings.Contains(text, "2m00s") {
		t.Errorf("latin formatAge units must be gone from the age column, got: %s", text)
	}
}
