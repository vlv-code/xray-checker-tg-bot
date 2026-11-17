package telegram

import (
	"strings"
	"testing"

	"xray-checker/metrics"
)

// Hotfix RED-first: proxies the user disabled manually must not turn node
// badges into «⚠️» — a failure is a non-online proxy among the ACTIVE ones
// (Total − Disabled). User follow-up (2026-11-17): the badge is BINARY —
// «просто есть падения / нет падений»; detailed state lives inside the tab.

// TestNodeHealthBadge_DisabledAreNotFailures pins the shared badge renderer.
func TestNodeHealthBadge_DisabledAreNotFailures(t *testing.T) {
	cases := []struct {
		name          string
		online, total int
		disabled      int
		want          string
		wantNoWarning bool
	}{
		{"3 online + 1 disabled", 3, 4, 1, "", true},
		{"2 online + 1 offline + 1 disabled", 2, 4, 1, "⚠️", false},
		{"all proxies disabled", 0, 4, 4, "", true},
		{"all online", 3, 3, 0, "", true},
		{"no proxies", 0, 0, 0, "", true},
	}
	for _, tc := range cases {
		got := nodeHealthBadge(tc.online, tc.total, tc.disabled)
		if got != tc.want {
			t.Errorf("%s: want %q, got %q", tc.name, tc.want, got)
		}
		if tc.wantNoWarning && strings.Contains(got, "⚠️") {
			t.Errorf("%s: badge must not warn, got %q", tc.name, got)
		}
	}
}

// TestGetNodeTabs_DisabledProxiesNoWarning: node tab badges follow the
// active-based semantics; a node whose only non-online proxy is user-disabled
// carries NO badge, not «⚠️ 3/4».
func TestGetNodeTabs_DisabledProxiesNoWarning(t *testing.T) {
	mgr := &viewsTestNodeManager{nodes: []NodeInfo{
		{Name: "ok-paused", Up: true, Online: 3, Total: 4, DisabledProxies: 1},
		{Name: "failing-paused", Up: true, Online: 2, Total: 4, DisabledProxies: 1},
		{Name: "all-paused", Up: true, Online: 0, Total: 2, DisabledProxies: 2},
		{Name: "down-node", Up: false, Online: 0, Total: 4, DisabledProxies: 1},
	}}
	b := &Bot{nodeMgr: mgr}

	tabs := b.getNodeTabs("")
	if len(tabs) != 5 {
		t.Fatalf("want master + 4 node tabs, got %d", len(tabs))
	}
	want := map[string]string{
		"ok-paused":      "",
		"failing-paused": "⚠️",
		"all-paused":     "",
		"down-node":      "🔴 оффлайн",
	}
	for _, tab := range tabs[1:] {
		if tab.Status != want[tab.ID] {
			t.Errorf("node %s: want badge %q, got %q", tab.ID, want[tab.ID], tab.Status)
		}
	}
}

// TestGetNodeTabs_MasterTabShowsPaused: the master tab counts locally disabled
// proxies the same way — disabled never produce a badge.
func TestGetNodeTabs_MasterTabShowsPaused(t *testing.T) {
	src := &mockSource{metrics: []metrics.ProxyMetric{
		{StableID: "a", Name: "p1", Address: "1.1.1.1:443", Online: true},
		{StableID: "b", Name: "p2", Address: "2.2.2.2:443", Online: true},
		{StableID: "c", Name: "p3", Address: "3.3.3.3:443", Online: true},
		{StableID: "d", Name: "p4", Address: "4.4.4.4:443", Disabled: true},
	}}
	mgr := &viewsTestNodeManager{nodes: []NodeInfo{{Name: "n1", Up: true}}}
	b := &Bot{source: src, nodeMgr: mgr}

	tabs := b.getNodeTabs("")
	if tabs[0].Status != "" {
		t.Errorf("master tab: want no badge (3 active online, 1 disabled), got %q", tabs[0].Status)
	}
}

// TestReplyNodes_ShowsDisabledProxiesSeparately: the /nodes row shows the
// binary badge only when there are failures (no fraction noise).
func TestReplyNodes_ShowsDisabledProxiesSeparately(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := &viewsTestNodeManager{nodes: []NodeInfo{
		{Name: "n1", Up: true, EverReported: true, Online: 3, Total: 4, DisabledProxies: 1},
	}}
	b, _ := newCfgTestBot(t, api, nil, mgr)

	b.replyNodes(ChatTarget{ChatID: 12345})

	sent := append(ft.callsOf("sendMessage"), ft.callsOf("editMessageText")...)
	if len(sent) == 0 {
		t.Fatal("expected /nodes to send a message")
	}
	text, _ := sent[0].Params["text"].(string)
	if strings.Contains(text, "⚠️") || strings.Contains(text, "3/4") || strings.Contains(text, "3/3") {
		t.Errorf("/nodes row must carry no fraction and no warning when only disabled proxies exist, got:\n%s", text)
	}
	if !strings.Contains(text, "отчёт") {
		t.Errorf("/nodes row must keep the report age, got:\n%s", text)
	}
}
