package nodes

import "testing"

// Hotfix RED-first: a proxy the user disabled manually (wave-H per-proxy
// exclusion) must not be counted as a failure in NodeHealth. Disabled is
// counted separately so badges can render «O/A · N⏸» instead of «⚠️»,
// matching the master's own summary («В сети | Сбоев | Отключено»).
func TestHealthSnapshot_CountsDisabledProxiesSeparately(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	h := reg.HandleReport()

	rec := postReport(t, h, "t1", ReportPayload{Version: "1", CheckIntervalSec: 60,
		Proxies: []ReportProxy{
			{StableID: "a", Online: true},
			{StableID: "b", Online: true},
			{StableID: "c", Disabled: true},
			{StableID: "d", Online: false},
		}})
	if rec.Code != 200 {
		t.Fatalf("report rejected: %s", rec.Body.String())
	}

	var n1 *NodeHealth
	for i := range reg.HealthSnapshot() {
		if reg.HealthSnapshot()[i].Name == "n1" {
			n1 = &reg.HealthSnapshot()[i]
		}
	}
	if n1 == nil {
		t.Fatal("n1 missing from HealthSnapshot")
	}
	if n1.Online != 2 || n1.Disabled != 1 || n1.Total != 4 {
		t.Fatalf("want Online=2 Disabled=1 Total=4, got Online=%d Disabled=%d Total=%d",
			n1.Online, n1.Disabled, n1.Total)
	}
}
