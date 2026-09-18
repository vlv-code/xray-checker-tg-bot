package telegram

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

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
	nodes map[string]string   // name -> token
	subs  map[string][]string // name -> URLs
}

func (f *fakeNodeManager) Nodes() []NodeInfo {
	var out []NodeInfo
	for name := range f.nodes {
		out = append(out, NodeInfo{Name: name, Up: true})
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

func TestNodesSubsViews(t *testing.T) {
	fnm := &fakeNodeManager{
		nodes: map[string]string{"m31a": "tok1", "msk-1": "tok2"},
		subs: map[string][]string{
			"m31a": {"https://sub.url/1", "https://sub.url/2"},
		},
	}
	b := &Bot{nodeMgr: fnm}
	text, markup := b.getNodesSubsListView()
	if !strings.Contains(text, "Управление подписками нод") {
		t.Errorf("expected header in list view text: %s", text)
	}
	if markup == nil || len(markup.InlineKeyboard) < 2 {
		t.Fatalf("expected markup with node buttons, got %v", markup)
	}

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
		waitingNodeSub: map[int64]string{123: "m31a"},
	}

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

	// Verify that waiting state was cleared
	if b.waitingNodeSub[123] != "" {
		t.Errorf("expected waitingNodeSub to be cleared, got %q", b.waitingNodeSub[123])
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
		waitingNodeSub: make(map[int64]string),
	}

	cbMsg := &telego.Message{
		Chat:      telego.Chat{ID: 123},
		MessageID: 456,
	}

	// 1. menu:nodes:addsub:m31a sets waitingNodeSub
	b.handleCallbackQuery(&telego.CallbackQuery{
		ID:      "q1",
		Data:    "menu:nodes:addsub:m31a",
		Message: cbMsg,
	})
	if b.waitingNodeSub[123] != "m31a" {
		t.Errorf("expected waitingNodeSub to be m31a, got %q", b.waitingNodeSub[123])
	}

	// 2. menu:nodes:subs clears waitingNodeSub
	b.handleCallbackQuery(&telego.CallbackQuery{
		ID:      "q2",
		Data:    "menu:nodes:subs",
		Message: cbMsg,
	})
	if b.waitingNodeSub[123] != "" {
		t.Errorf("expected waitingNodeSub to be cleared on menu:nodes:subs, got %q", b.waitingNodeSub[123])
	}

	// 3. menu:nodes:delsub:m31a:0 removes the subscription
	b.handleCallbackQuery(&telego.CallbackQuery{
		ID:      "q3",
		Data:    "menu:nodes:delsub:m31a:0",
		Message: cbMsg,
	})
	subs, err := fnm.ManagedSubs("m31a")
	if err != nil {
		t.Fatalf("ManagedSubs failed: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("expected 0 subs after deletion, got %d", len(subs))
	}
}
