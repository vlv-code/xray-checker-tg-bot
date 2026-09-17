package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xray-checker/config"
	"xray-checker/nodes"
)

func TestMaskMiddle(t *testing.T) {
	cases := map[string]string{
		"":                                     "",
		"short":                                "****", // <= 8 chars fully masked
		"12345678":                             "****", // exactly 8 still fully masked
		"123456789":                            "1234...6789",
		"d342d11e-d424-4583-b36e-524ab1f0afa4": "d342...afa4",
	}
	for in, want := range cases {
		if got := maskMiddle(in); got != want {
			t.Errorf("maskMiddle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeGeneratedConfigMasksSecretsKeepsPublic(t *testing.T) {
	// Mirrors the shape of a generated vless+reality+hysteria outbound.
	outbound := map[string]interface{}{
		"tag":      "node_0",
		"protocol": "vless",
		"settings": map[string]interface{}{
			"vnext": []map[string]interface{}{
				{
					"address": "example.com",
					"port":    443,
					"users": []map[string]interface{}{
						{"id": "d342d11e-d424-4583-b36e-524ab1f0afa4", "encryption": "none"},
					},
				},
			},
		},
		"streamSettings": map[string]interface{}{
			"realitySettings": map[string]interface{}{
				"publicKey": "Vft7...PuBLiCkeYmaterialShouldStay",
				"shortId":   "0123abcd",
			},
			"hysteriaSettings": map[string]interface{}{
				"auth": "super-secret-hysteria-auth-token",
			},
			"kcpSettings": map[string]interface{}{
				"seed": "my-kcp-seed-value",
			},
		},
	}

	got := sanitizeGeneratedConfig(outbound)
	if got == nil {
		t.Fatal("sanitizeGeneratedConfig returned nil")
	}

	user := got["settings"].(map[string]interface{})["vnext"].([]interface{})[0].(map[string]interface{})["users"].([]interface{})[0].(map[string]interface{})
	if user["id"] != "d342...afa4" {
		t.Errorf("uuid not masked: %v", user["id"])
	}

	stream := got["streamSettings"].(map[string]interface{})
	if stream["realitySettings"].(map[string]interface{})["publicKey"] == "****" {
		t.Error("publicKey must NOT be masked (it is public material)")
	}
	if stream["hysteriaSettings"].(map[string]interface{})["auth"] != "supe...oken" {
		t.Errorf("hysteria auth not masked: %v", stream["hysteriaSettings"].(map[string]interface{})["auth"])
	}
	if stream["kcpSettings"].(map[string]interface{})["seed"] != "my-k...alue" {
		t.Errorf("kcp seed not masked: %v", stream["kcpSettings"].(map[string]interface{})["seed"])
	}
	// Non-secret fields are preserved untouched.
	if got["tag"] != "node_0" || got["protocol"] != "vless" {
		t.Errorf("non-secret fields altered: tag=%v protocol=%v", got["tag"], got["protocol"])
	}
}

func TestShouldShowServerDetails(t *testing.T) {
	cases := []struct {
		name    string
		show    bool
		public  bool
		trusted bool
		want    bool
	}{
		{"off by default", false, false, false, false},
		{"on, private", true, false, false, true},
		{"on, public, untrusted -> hidden", true, true, false, false},
		{"on, public, trusted -> shown", true, true, true, true},
		{"off, public, trusted -> still off", false, true, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config.CLIConfig.Web.ShowServerDetails = c.show
			config.CLIConfig.Web.Public = c.public
			config.CLIConfig.Web.TrustedExternalAuth = c.trusted
			if got := shouldShowServerDetails(); got != c.want {
				t.Errorf("shouldShowServerDetails() = %v, want %v", got, c.want)
			}
		})
	}
	// reset
	config.CLIConfig.Web.ShowServerDetails = false
	config.CLIConfig.Web.Public = false
	config.CLIConfig.Web.TrustedExternalAuth = false
}

func TestSanitizeGeneratedConfigMasksWireGuardAndSocksSecrets(t *testing.T) {
	wgOutbound := map[string]interface{}{
		"tag":      "wg_node",
		"protocol": "wireguard",
		"settings": map[string]interface{}{
			"secretKey": "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d=",
			"peers": []map[string]interface{}{
				{
					"publicKey":    "pubKeyMaterialShouldNotBeMasked=",
					"preSharedKey": "presharedKeySecretMaterialShouldBeMasked=",
				},
			},
		},
	}

	socksOutbound := map[string]interface{}{
		"tag":      "socks_node",
		"protocol": "socks",
		"settings": map[string]interface{}{
			"servers": []map[string]interface{}{
				{
					"address": "1.2.3.4",
					"port":    1080,
					"users": []map[string]interface{}{
						{"user": "socksuser", "pass": "verysecretpassword"},
					},
				},
			},
		},
	}

	sanitizedWG := sanitizeGeneratedConfig(wgOutbound)
	wgSettings := sanitizedWG["settings"].(map[string]interface{})
	if wgSettings["secretKey"] != "a1b2...c3d=" {
		t.Errorf("WireGuard secretKey not masked: %v", wgSettings["secretKey"])
	}
	wgPeer := wgSettings["peers"].([]interface{})[0].(map[string]interface{})
	if wgPeer["publicKey"] != "pubKeyMaterialShouldNotBeMasked=" {
		t.Errorf("WireGuard publicKey should not be masked: %v", wgPeer["publicKey"])
	}
	if wgPeer["preSharedKey"] != "pres...ked=" {
		t.Errorf("WireGuard preSharedKey not masked: %v", wgPeer["preSharedKey"])
	}

	sanitizedSocks := sanitizeGeneratedConfig(socksOutbound)
	socksUser := sanitizedSocks["settings"].(map[string]interface{})["servers"].([]interface{})[0].(map[string]interface{})["users"].([]interface{})[0].(map[string]interface{})
	if socksUser["pass"] != "very...word" {
		t.Errorf("Socks pass not masked: %v", socksUser["pass"])
	}
}

func TestAPINodesHandler(t *testing.T) {
	reg := nodes.NewRegistry([]nodes.NodeConfig{{Name: "n1", Token: "t1"}}, nil, nil)
	rec := httptest.NewRecorder()
	APINodesHandler(reg)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var resp struct {
		Success bool               `json:"success"`
		Data    []nodes.NodeHealth `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Success || len(resp.Data) != 1 || resp.Data[0].Name != "n1" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.Data[0].EverReported || resp.Data[0].Up {
		t.Errorf("pending node must be down/unreported: %+v", resp.Data[0])
	}
}
