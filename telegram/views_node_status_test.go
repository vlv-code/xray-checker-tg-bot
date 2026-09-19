package telegram

import (
	"strings"
	"testing"
	"time"

	"xray-checker/metrics"
)

type viewsTestNodeManager struct {
	nodes []NodeInfo
}

func (m *viewsTestNodeManager) Nodes() []NodeInfo {
	return m.nodes
}

func (m *viewsTestNodeManager) ManagedSubs(node string) ([]ManagedSubInfo, error) {
	return nil, nil
}

func (m *viewsTestNodeManager) AddSub(node, url string) error {
	return nil
}

func (m *viewsTestNodeManager) RemoveSub(node, url string) error {
	return nil
}

func (m *viewsTestNodeManager) AddNode(name, token string) error {
	return nil
}

func (m *viewsTestNodeManager) RemoveNode(name string) error {
	return nil
}

func (m *viewsTestNodeManager) NodeSnapshot(node string) []metrics.ProxyMetric {
	return nil
}

func TestGetMenuText_WithoutNodes(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "Proxy1", Address: "1.1.1.1:443", Online: true},
			{StableID: "p2", Name: "Proxy2", Address: "2.2.2.2:443", Online: true},
		},
	}
	b := &Bot{source: src}

	text := b.getMenuText()
	if !strings.Contains(text, "• Текущий статус: <b>2/2 онлайн</b>") {
		t.Errorf("expected standard status line, got:\n%s", text)
	}
	if strings.Contains(text, "Мастер:") || strings.Contains(text, "├") {
		t.Errorf("expected no tree branches without nodes, got:\n%s", text)
	}
}

func TestGetMenuText_WithNodes_AllOnline(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "MasterProxy1", Address: "1.1.1.1:443", Online: true},
			{StableID: "p2", Name: "MasterProxy2", Address: "2.2.2.2:443", Online: true},
			{StableID: "p3", Name: "NodeProxy1", Address: "3.3.3.3:443", Online: true, NodeName: "m31a", NodeASN: "AS60879 System Projects, LLC"},
			{StableID: "p4", Name: "NodeProxy2", Address: "4.4.4.4:443", Online: true, NodeName: "m31a", NodeASN: "AS60879 System Projects, LLC"},
		},
	}
	mgr := &viewsTestNodeManager{
		nodes: []NodeInfo{
			{Name: "m31a", Up: true, ASN: "AS60879 System Projects, LLC", Online: 2, Total: 2, EverReported: true, LastReport: time.Now()},
		},
	}
	b := &Bot{source: src, nodeMgr: mgr}

	text := b.getMenuText()

	// Should show total status
	if !strings.Contains(text, "• Статус прокси: <b>4/4 онлайн</b>") {
		t.Errorf("expected '• Статус прокси: <b>4/4 онлайн</b>', got:\n%s", text)
	}
	// Should show master branch
	if !strings.Contains(text, "├ 🏠 Мастер: <b>2/2 онлайн</b>") {
		t.Errorf("expected master branch '├ 🏠 Мастер: <b>2/2 онлайн</b>', got:\n%s", text)
	}
	// Should show node branch with online status
	if !strings.Contains(text, "└ 🖥 m31a: 🟢 <b>2/2 онлайн</b>") {
		t.Errorf("expected node branch '└ 🖥 m31a: 🟢 <b>2/2 онлайн</b>', got:\n%s", text)
	}
	// Should show ASN info
	if !strings.Contains(text, "AS60879") {
		t.Errorf("expected node ASN in text, got:\n%s", text)
	}
	// All healthy message
	if !strings.Contains(text, "🟢 <i>Все активные прокси-хосты доступны и работают стабильно.</i>") {
		t.Errorf("expected all healthy message, got:\n%s", text)
	}
}

func TestGetMenuText_WithMasterASN(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "MasterProxy1", Address: "1.1.1.1:443", Online: true},
			{StableID: "p2", Name: "NodeProxy1", Address: "2.2.2.2:443", Online: true, NodeName: "m31a"},
		},
	}
	mgr := &viewsTestNodeManager{
		nodes: []NodeInfo{
			{Name: "m31a", Up: true, ASN: "AS60879 System Projects, LLC", Online: 1, Total: 1, EverReported: true, LastReport: time.Now()},
		},
	}
	b := &Bot{
		source:     src,
		nodeMgr:    mgr,
		diagSource: &mockDiagSource{},
	}
	b.SetASNLookup(func(ip string) string {
		if ip == "1.2.3.4" {
			return "AS24940 Hetzner Online GmbH"
		}
		return ""
	})

	text := b.getMenuText()
	if !strings.Contains(text, "├ 🏠 Мастер: <b>1/1 онлайн</b> (AS24940 Hetzner Online GmbH)") {
		t.Errorf("expected master branch with ASN, got:\n%s", text)
	}
}

func TestGetMenuText_WithNodes_ProxyDown(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "MasterProxy1", Address: "1.1.1.1:443", Online: true},
			{StableID: "p2", Name: "NodeProxy1", Address: "2.2.2.2:443", Online: true, NodeName: "m31a"},
			{StableID: "p3", Name: "NodeProxy2", Address: "3.3.3.3:443", Online: false, NodeName: "m31a"},
		},
	}
	mgr := &viewsTestNodeManager{
		nodes: []NodeInfo{
			{Name: "m31a", Up: true, Online: 1, Total: 2, EverReported: true, LastReport: time.Now()},
		},
	}
	b := &Bot{source: src, nodeMgr: mgr}

	text := b.getMenuText()

	if !strings.Contains(text, "• Статус прокси: <b>2/3 онлайн</b>") {
		t.Errorf("expected '• Статус прокси: <b>2/3 онлайн</b>', got:\n%s", text)
	}
	if !strings.Contains(text, "└ 🖥 m31a: ⚠️ <b>1/2 онлайн</b>") {
		t.Errorf("expected warning for node '└ 🖥 m31a: ⚠️ <b>1/2 онлайн</b>', got:\n%s", text)
	}
	// Down proxy should indicate node
	if !strings.Contains(text, "🔴 Требуют внимания:") || !strings.Contains(text, "[m31a] NodeProxy2") {
		t.Errorf("expected down node proxy in attention list, got:\n%s", text)
	}
}

func TestGetMenuText_WithNodes_NodeOffline(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "MasterProxy1", Address: "1.1.1.1:443", Online: true},
		},
	}
	mgr := &viewsTestNodeManager{
		nodes: []NodeInfo{
			{Name: "m31a", Up: false, EverReported: true, LastReport: time.Now().Add(-10 * time.Minute)},
		},
	}
	b := &Bot{source: src, nodeMgr: mgr}

	text := b.getMenuText()

	if !strings.Contains(text, "└ 🖥 m31a: 🔴 <b>оффлайн</b>") {
		t.Errorf("expected offline node branch, got:\n%s", text)
	}
	if !strings.Contains(text, "🔴 Требуют внимания:") || !strings.Contains(text, "🖥 Нода m31a: оффлайн") {
		t.Errorf("expected offline node in attention list, got:\n%s", text)
	}
}

func TestGetStatusText_WithoutNodes(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "Proxy1", Online: true, LatencyMs: 45},
			{StableID: "p2", Name: "Proxy2", Online: false},
		},
	}
	b := &Bot{source: src}

	text := b.getStatusText()
	if !strings.Contains(text, "<b>Статус прокси-хостов: 1/2 онлайн</b>") {
		t.Errorf("expected standard header, got:\n%s", text)
	}
	if strings.Contains(text, "Локальный чекер") {
		t.Errorf("expected no local checker section header without nodes, got:\n%s", text)
	}
}

func TestGetStatusText_WithNodes_Grouped(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "MasterProxy", Online: true, LatencyMs: 25},
			{StableID: "p2", Name: "NodeProxy1", Online: true, LatencyMs: 50, NodeName: "m31a", NodeASN: "AS60879"},
			{StableID: "p3", Name: "NodeProxy2", Online: false, NodeName: "m31a", NodeASN: "AS60879"},
		},
	}
	mgr := &viewsTestNodeManager{
		nodes: []NodeInfo{
			{Name: "m31a", Up: true, ASN: "AS60879", Online: 1, Total: 2, EverReported: true, LastReport: time.Now()},
		},
	}
	b := &Bot{source: src, nodeMgr: mgr}

	text := b.getStatusText()

	// Should have header
	if !strings.Contains(text, "<b>Статус прокси-хостов: 2/3 онлайн</b>") {
		t.Errorf("expected header 'Статус прокси-хостов: 2/3 онлайн', got:\n%s", text)
	}
	// Should have local checker section
	if !strings.Contains(text, "🏠 <b>Локальный чекер · 1/1 онлайн:</b>") {
		t.Errorf("expected local checker section, got:\n%s", text)
	}
	if !strings.Contains(text, "✅ <b>MasterProxy</b> — 25 ms") {
		t.Errorf("expected MasterProxy in local section, got:\n%s", text)
	}
	// Should have node section with health and counts
	if !strings.Contains(text, "🖥 <b>Нода m31a</b>") || !strings.Contains(text, "1/2 онлайн") {
		t.Errorf("expected node section with online count, got:\n%s", text)
	}
	if !strings.Contains(text, "✅ <b>NodeProxy1</b> — 50 ms") {
		t.Errorf("expected NodeProxy1 under node section, got:\n%s", text)
	}
	if !strings.Contains(text, "🔴 <b>NodeProxy2</b> — недоступен") {
		t.Errorf("expected NodeProxy2 under node section, got:\n%s", text)
	}
}
