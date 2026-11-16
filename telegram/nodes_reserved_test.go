package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

// TestIsReservedNodeName (Wave-F B6): the four diagnostic target tokens are
// reserved node names — they collide with static Telegram diag routes
// (menu:diag:details:<name>, menu:diag:refresh:<name>, target parsing in
// callbacks.go). Exact-match only: prefixed/suffixed/cased variants are free.
func TestIsReservedNodeName(t *testing.T) {
	for _, n := range []string{"local", "rich", "summary", "details"} {
		if !isReservedNodeName(n) {
			t.Errorf("expected isReservedNodeName(%q) = true", n)
		}
	}
	for _, n := range []string{"locals", "rich-2", "msk-1", "summary1", "details_x", "Local"} {
		if isReservedNodeName(n) {
			t.Errorf("expected isReservedNodeName(%q) = false", n)
		}
	}
}

// TestNodeAddRejectsReservedName (Wave-F B6): /nodeadd must refuse a reserved
// name with a Russian error and must not register the node.
func TestNodeAddRejectsReservedName(t *testing.T) {
	for _, name := range []string{"rich", "local", "summary", "details"} {
		ft, api := newFakeTelegramBot(t)
		fnm := &fakeNodeManager{nodes: map[string]string{}}
		b := &Bot{
			api:     api,
			ctx:     context.Background(),
			nodeMgr: fnm,
		}
		msg := &telego.Message{
			Chat:      telego.Chat{ID: 123, Type: "private"},
			MessageID: 1,
			Text:      "/nodeadd " + name,
		}
		b.handleNodeAdd(msg, name)

		if _, exists := fnm.nodes[name]; exists {
			t.Fatalf("reserved name %q must not be registered", name)
		}
		text, ok := ft.lastParamOf("sendMessage", "text")
		if !ok {
			t.Fatalf("expected a rejection reply for %q, got no sendMessage", name)
		}
		if !strings.Contains(text, "зарезервировано") {
			t.Errorf("expected RU reserved-name rejection for %q, got: %s", name, text)
		}
	}

	// Control: a non-reserved name still registers.
	ftCtl, apiCtl := newFakeTelegramBot(t)
	fnmCtl := &fakeNodeManager{nodes: map[string]string{}}
	bCtl := &Bot{api: apiCtl, ctx: context.Background(), nodeMgr: fnmCtl}
	msg := &telego.Message{
		Chat:      telego.Chat{ID: 123, Type: "private"},
		MessageID: 2,
		Text:      "/nodeadd msk-1",
	}
	bCtl.handleNodeAdd(msg, "msk-1")
	if _, exists := fnmCtl.nodes["msk-1"]; !exists {
		t.Fatalf("non-reserved name must still register (control failed)")
	}
	if text, ok := ftCtl.lastParamOf("sendMessage", "text"); ok && strings.Contains(text, "зарезервировано") {
		t.Errorf("non-reserved name must not hit the reserved rejection, got: %s", text)
	}
}
