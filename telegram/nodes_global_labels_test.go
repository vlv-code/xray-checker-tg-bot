package telegram

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

// TestNodesMenu_GlobalRenamedLabels pins the Wave-E rename: the existing
// global node-settings screen keeps callback menu:nodes:settings everywhere,
// but every LABEL that opens it reads «Глобальные настройки нод» (or the
// back-button form «К глобальным настройкам»). A new «⚙️ Настройки нод»
// button (callback menu:nodes:cfg) must sit on its own row DIRECTLY BELOW
// the renamed global row. Callback values of existing routes stay unchanged.
func TestNodesMenu_GlobalRenamedLabels(t *testing.T) {
	// 1. NodesMainMenuMarkup: renamed global row + new per-node row below it.
	nodesMenu := NodesMainMenuMarkup()
	settingsRow, cfgRow := -1, -1
	for i, row := range nodesMenu.InlineKeyboard {
		for _, b := range row {
			switch b.CallbackData {
			case "menu:nodes:settings":
				settingsRow = i
				if b.Text != "⚙️ Глобальные настройки нод" {
					t.Errorf("global settings button label = %q, want %q", b.Text, "⚙️ Глобальные настройки нод")
				}
			case "menu:nodes:cfg":
				cfgRow = i
				if b.Text != "⚙️ Настройки нод" {
					t.Errorf("per-node settings button label = %q, want %q", b.Text, "⚙️ Настройки нод")
				}
			}
		}
	}
	if settingsRow == -1 {
		t.Fatalf("expected NodesMainMenuMarkup to keep a menu:nodes:settings button")
	}
	if cfgRow == -1 {
		t.Fatalf("expected NodesMainMenuMarkup to contain the new menu:nodes:cfg button")
	}
	if cfgRow != settingsRow+1 {
		t.Errorf("expected the menu:nodes:cfg row (%d) directly below the menu:nodes:settings row (%d)", cfgRow, settingsRow)
	}

	// 2. NodesSubsListMarkup: back button to the global screen renamed.
	listMarkup := NodesSubsListMarkup(nil)
	lastRow := listMarkup.InlineKeyboard[len(listMarkup.InlineKeyboard)-1]
	if lastRow[0].CallbackData != "menu:nodes:settings" {
		t.Errorf("expected back button callback menu:nodes:settings, got %s", lastRow[0].CallbackData)
	}
	if lastRow[0].Text != "🔙 К глобальным настройкам" {
		t.Errorf("expected back button label «🔙 К глобальным настройкам», got %q", lastRow[0].Text)
	}

	// 3. NodeSubsManageMarkup: the global-screen shortcut button renamed.
	manageMarkup := NodeSubsManageMarkup("m31a", nil)
	foundGlobal := false
	for _, row := range manageMarkup.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "menu:nodes:settings" {
				foundGlobal = true
				if b.Text != "⚙️ Глобальные настройки нод" {
					t.Errorf("manage-screen global button label = %q, want «⚙️ Глобальные настройки нод»", b.Text)
				}
			}
		}
	}
	if !foundGlobal {
		t.Fatalf("expected NodeSubsManageMarkup to keep a menu:nodes:settings button")
	}

	// 4. getNodesSettingsView header (views.go render path) renamed.
	cm, err := NewConfigManager(filepath.Join(t.TempDir(), "cfg.json"), BotConfig{})
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{configMgr: cm}
	header := b.getNodesSettingsView()
	if !strings.Contains(header, "Глобальные настройки нод") {
		t.Errorf("expected global header «Глобальные настройки нод» in settings view, got: %s", header)
	}
	if strings.Contains(header, "<b>Настройки нод</b>") {
		t.Errorf("old header literal «Настройки нод» must not remain in the global settings view, got: %s", header)
	}
}

// TestNodesMenu_GlobalRenamedLabels_AddSubFlow pins the rename for the
// markup built inline by handleNodeAddSubURL (nodes_commands.go): the
// menu:nodes:settings button there must read «Глобальные настройки нод».
func TestNodesMenu_GlobalRenamedLabels_AddSubFlow(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	fnm := &fakeNodeManager{nodes: map[string]string{"m31a": "tok1"}}
	b := &Bot{
		api:            api,
		ctx:            context.Background(),
		targets:        []ChatTarget{{ChatID: 12345}},
		allowedChatIDs: map[int64]bool{12345: true},
		nodeMgr:        fnm,
	}

	msg := &telego.Message{Chat: telego.Chat{ID: 12345}, Text: "https://example.com/sub"}
	b.handleNodeAddSubURL(msg, "m31a", "https://example.com/sub")

	sent := ft.callsOf("sendMessage")
	if len(sent) == 0 {
		t.Fatalf("expected handleNodeAddSubURL to send a message")
	}
	rm, _ := sent[len(sent)-1].Params["reply_markup"].(map[string]interface{})
	if rm == nil {
		t.Fatalf("expected reply_markup in sendMessage params, got %v", sent[len(sent)-1].Params)
	}
	rows, _ := rm["inline_keyboard"].([]interface{})
	if rows == nil {
		t.Fatalf("expected inline_keyboard in reply_markup, got %v", rm)
	}
	found := false
	for _, r := range rows {
		btns, _ := r.([]interface{})
		for _, btnAny := range btns {
			btnMap, _ := btnAny.(map[string]interface{})
			data, _ := btnMap["callback_data"].(string)
			text, _ := btnMap["text"].(string)
			if data == "menu:nodes:settings" {
				found = true
				if !strings.Contains(text, "Глобальные настройки нод") {
					t.Errorf("addsub-flow global button label = %q, want «Глобальные настройки нод»", text)
				}
			}
		}
	}
	if !found {
		t.Fatalf("expected a menu:nodes:settings button in the addsub-flow markup")
	}
}
