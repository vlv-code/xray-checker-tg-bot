package models

import "testing"

func baseProxy() *ProxyConfig {
	return &ProxyConfig{
		Protocol:    "vless",
		Server:      "example.com",
		Port:        443,
		UUID:        "00000000-0000-0000-0000-000000000000",
		Security:    "reality",
		Type:        "tcp",
		SNI:         "example.com",
		PublicKey:   "PBK",
		ShortID:     "sid1",
		Flow:        "xtls-rprx-vision",
		Fingerprint: "chrome",
		Name:        "Server A",
	}
}

// Every connection-distinguishing field must change the stableID.
func TestGenerateStableID_DistinguishesConnectionFields(t *testing.T) {
	base := baseProxy().GenerateStableID()
	cases := map[string]func(*ProxyConfig){
		"fp":         func(p *ProxyConfig) { p.Fingerprint = "firefox" },
		"path":       func(p *ProxyConfig) { p.Path = "/other" },
		"host":       func(p *ProxyConfig) { p.Host = "h2.example.com" },
		"shortId":    func(p *ProxyConfig) { p.ShortID = "sid2" },
		"flow":       func(p *ProxyConfig) { p.Flow = "" },
		"sni":        func(p *ProxyConfig) { p.SNI = "other.com" },
		"security":   func(p *ProxyConfig) { p.Security = "tls" },
		"type":       func(p *ProxyConfig) { p.Type = "ws" },
		"headerType": func(p *ProxyConfig) { p.HeaderType = "http" },
		"encryption": func(p *ProxyConfig) { p.Encryption = "none" },
		"server":     func(p *ProxyConfig) { p.Server = "other.com" },
		"port":       func(p *ProxyConfig) { p.Port = 8443 },
		"uuid":       func(p *ProxyConfig) { p.UUID = "11111111-1111-1111-1111-111111111111" }, // vless route-id: same node, different exit
		"publicKey":  func(p *ProxyConfig) { p.PublicKey = "PBK2" },
		"alpn":       func(p *ProxyConfig) { p.ALPN = []string{"h2"} },
	}
	for name, mut := range cases {
		p := baseProxy()
		mut(p)
		if got := p.GenerateStableID(); got == base {
			t.Errorf("changing %s must change stableID (still %s)", name, got)
		}
	}
}

// Name/SubName/Index must NOT affect the stableID (renames keep monitors stable).
func TestGenerateStableID_IgnoresNameAndIndex(t *testing.T) {
	base := baseProxy().GenerateStableID()
	cases := map[string]func(*ProxyConfig){
		"Name":    func(p *ProxyConfig) { p.Name = "Totally Different Name" },
		"SubName": func(p *ProxyConfig) { p.SubName = "another-sub" },
		"Index":   func(p *ProxyConfig) { p.Index = 99 },
	}
	for name, mut := range cases {
		p := baseProxy()
		mut(p)
		if got := p.GenerateStableID(); got != base {
			t.Errorf("%s must not affect stableID: %s != %s", name, got, base)
		}
	}
}

// stableID is public, so it must NOT change with low-entropy, human-chosen secrets
// (trojan/shadowsocks password, hysteria auth) — a truncated hash of those could be
// brute-forced. (The high-entropy UUID is kept; see DistinguishesConnectionFields.)
func TestGenerateStableID_ExcludesSecrets(t *testing.T) {
	tr := &ProxyConfig{Protocol: "trojan", Server: "s.example.com", Port: 443, Password: "secret-one", Name: "T"}
	tr2 := &ProxyConfig{Protocol: "trojan", Server: "s.example.com", Port: 443, Password: "secret-two", Name: "T"}
	if tr.GenerateStableID() != tr2.GenerateStableID() {
		t.Errorf("trojan password (secret) must not affect stableID")
	}

	hy := &ProxyConfig{Protocol: "hysteria", Server: "h.example.com", Port: 443, HysteriaAuth: "auth-one", Name: "H"}
	hy2 := &ProxyConfig{Protocol: "hysteria", Server: "h.example.com", Port: 443, HysteriaAuth: "auth-two", Name: "H"}
	if hy.GenerateStableID() != hy2.GenerateStableID() {
		t.Errorf("hysteria auth (secret) must not affect stableID")
	}
}

func TestGenerateStableID_AlpnOrderIndependent(t *testing.T) {
	a := baseProxy()
	a.ALPN = []string{"h2", "http/1.1"}
	b := baseProxy()
	b.ALPN = []string{"http/1.1", "h2"}
	if a.GenerateStableID() != b.GenerateStableID() {
		t.Errorf("ALPN order should not affect stableID")
	}
}

func mk(name, path, fp string) *ProxyConfig {
	p := baseProxy()
	p.Name = name
	p.Path = path
	p.Fingerprint = fp
	return p
}

// All IDs unique across the cluster cases: path-only (#149), fp-only (#157),
// same-connection-diff-name, and true duplicates.
func TestAssignStableIDs_AllUnique(t *testing.T) {
	proxies := []*ProxyConfig{
		mk("A", "/p1", "chrome"),
		mk("B", "/p2", "chrome"),  // differs by path (#149)
		mk("C", "/p1", "firefox"), // differs by fp (#157)
		mk("Dup1", "/p1", "chrome"),
		mk("Dup2", "/p1", "chrome"), // same connection as A, distinct name
		mk("A", "/p1", "chrome"),    // true duplicate of A (incl name)
	}
	for i, p := range proxies {
		p.Index = i
	}
	AssignStableIDs(proxies)

	seen := map[string]int{}
	for _, p := range proxies {
		if p.StableID == "" {
			t.Fatal("empty stableID after AssignStableIDs")
		}
		seen[p.StableID]++
	}
	if len(seen) != len(proxies) {
		t.Errorf("expected %d unique IDs, got %d: %v", len(proxies), len(seen), seen)
	}
}

// Same-connection-different-name configs get the same ID regardless of input order.
func TestAssignStableIDs_DeterministicAcrossReorder(t *testing.T) {
	build := func() []*ProxyConfig {
		return []*ProxyConfig{
			mk("A", "/p1", "chrome"),    // group X (members: A, Dup1, Dup2)
			mk("Dup1", "/p1", "chrome"), // group X
			mk("Dup2", "/p1", "chrome"), // group X
			mk("B", "/p2", "chrome"),    // group Y (singleton)
		}
	}

	forward := build()
	for i, p := range forward {
		p.Index = i
	}
	AssignStableIDs(forward)
	idByName := map[string]string{}
	for _, p := range forward {
		idByName[p.Name] = p.StableID
	}

	rev := build()
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	for i, p := range rev {
		p.Index = i
	}
	AssignStableIDs(rev)
	for _, p := range rev {
		if idByName[p.Name] != p.StableID {
			t.Errorf("%s got different ID across reorder: %s vs %s", p.Name, idByName[p.Name], p.StableID)
		}
	}
}

func TestAssignStableIDs_PrepopulatedWithBaseHash_ResolvesCollisions(t *testing.T) {
	p1 := mk("A", "/p1", "chrome")
	p2 := mk("Dup", "/p1", "chrome")
	p1.Index = 0
	p2.Index = 1
	// Simulate subscription parsing where convertOutbound pre-populates StableID with GenerateStableID()
	p1.StableID = p1.GenerateStableID()
	p2.StableID = p2.GenerateStableID()

	proxies := []*ProxyConfig{p1, p2}
	AssignStableIDs(proxies)

	if p1.StableID == p2.StableID {
		t.Errorf("expected duplicate proxies to have distinct StableIDs after AssignStableIDs, but both got %q", p1.StableID)
	}
}
