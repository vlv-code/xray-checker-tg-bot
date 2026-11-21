package subscription

import (
	"encoding/base64"
	"strings"
	"testing"
)

// The Fuzz* targets in this file are the systemic guard for the subscription
// parsers: in normal `go test` runs they execute only their seed corpus (fast,
// deterministic, no network/filesystem access), and nightly they are fuzzed
// with a per-target time cap by .github/workflows/fuzz.yml.
//
// Hermeticity contract (verified against the current code):
//   - parseShareLink, parseVMessLink and decodeFlexibleBase64 are pure
//     functions.
//   - Parser.Parse is pure EXCEPT for three source-type prefixes: http:// and
//     https:// route to fetchURLContent (real HTTP requests, see
//     fetcher.go), file:// and folder:// read the local filesystem. FuzzParse
//     skips those prefixes with t.Skip so the corpus stays hermetic; the
//     fetch path itself has dedicated SSRF/policy tests (ssrf_test.go,
//     fetcher_redact_test.go, redirect_policy_test.go).
//   - Share-link and JSON payloads route through libXray
//     (ConvertShareLinksToXrayJson), which is local CPU-only conversion —
//     no sockets are opened.
//
// Property assertions below are pinned to contracts that hold TODAY:
//   - parseShareLink / parseVMessLink: a non-nil result always has Port != 0
//     (port 0 and unparseable ports return nil). There is deliberately NO
//     65535 upper bound at this layer — that pass-through is regression-pinned
//     in uri_parsers_test.go ("port above range is not validated here"); the
//     bound lives in models.ProxyConfig.Validate, which runs outside
//     Parser.Parse, and convertOutbound only errors on port 0 / drops port 1.
//   - decodeFlexibleBase64: whatever it accepts must round-trip through the
//     standard alphabet (StdEncoding is tried first in its alphabet list).
//   - Parser.Parse: returns (nil result, error) or (result, nil) — never
//     both and never neither.

func fuzzB64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// FuzzParseShareLink fuzzes Parser.parseShareLink with well-formed share
// links (seeds from TestParseShareLink_WellFormed) plus the malformed corpus
// from the adversarial pins (TestParseShareLink_Adversarial), including the
// oversized 10k-fragment/10k-host inputs and the out-of-range port
// pass-through.
func FuzzParseShareLink(f *testing.F) {
	p := NewParser()

	seeds := []string{
		// Well-formed links (uri_parsers_test.go).
		"vless://11111111-2222-3333-4444-555555555555@example.com:443?type=ws&security=tls#vless-node",
		"trojan://hunter2@trojan.example.com:443?sni=cdn.example.com#trojan-node",
		"ss://" + fuzzB64("aes-256-gcm:secret") + "@1.2.3.4:8388#ss-node",
		"ss://" + fuzzB64("onlypassword") + "@1.2.3.4:8388#ss-node2",
		"hysteria2://authpass@hy.example.com:443?sni=hy-cdn.example.com#hy-node",
		"hysteria2://@hy.example.com:443?sni=hy-cdn.example.com#hy-node2",
		"vless://11111111-2222-3333-4444-555555555555@5.6.7.8:8443?type=grpc&encryption=auto&path=%2Fws&host=cdn.example.com&allowInsecure=1#q",
		// vmess dispatch coverage (parseShareLink delegates vmess://).
		"vmess://" + fuzzB64(`{"v":"2","ps":"vm","add":"1.2.3.4","port":"443","id":"u"}`),
		// Malformed corpus from the adversarial pins.
		"",
		"vless://",
		"://x:notaport",
		"vless://example.com:443",
		"vless://@example.com:443#x",
		"vless://[::1",
		"vless://example.com:99999#x",
		"vless://example.com:0#x",
		"vless://example.com:notaport#x",
		"vless://a:b@c@example.com:443#x",
		"vless://user:pass@example.com:443#x",
		"vless://example.com:443#na%zzme",
		"vless://example.com:443?type=%zz#x",
		"vless://example.com:443#my#name",
		"vless://example.com:443#Имя-сервера-🚀",
		"vless://example.com:443#" + strings.Repeat("a", 10000),
		"vless://" + strings.Repeat("h", 10000) + ":443#x",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, link string) {
		pl := p.parseShareLink(link)
		if pl != nil && pl.Port == 0 {
			t.Errorf("parseShareLink returned a config with Port == 0 for %q", link)
		}
	})
}

// FuzzParseVMessLink fuzzes Parser.parseVMessLink with base64-JSON payloads:
// valid vmess links from TestParseVMessLink, the malformed payloads from the
// pins, and raw binary garbage (invalid UTF-8 / NUL bytes) that the mutator
// can extend into vmess:// bodies.
func FuzzParseVMessLink(f *testing.F) {
	p := NewParser()

	vmessJSON := func(payload string) string { return "vmess://" + fuzzB64(payload) }

	seeds := []string{
		// Valid vmess links from TestParseVMessLink.
		vmessJSON(`{"v":"2","ps":"vm-str","add":"1.2.3.4","port":"443","id":"u","scy":"auto"}`),
		vmessJSON(`{"v":"2","ps":"vm-num","add":"1.2.3.4","port":443,"id":"u"}`),
		vmessJSON(`{"v":"2","ps":"vm-full","add":"5.6.7.8","port":8443,"id":"u","net":"ws","host":"cdn.example.com","path":"/ws"}`),
		vmessJSON(`{"v":"2","ps":"no-port","add":"1.2.3.4","id":"u"}`),
		vmessJSON(`{}`),
		vmessJSON(`{"add":"1.2.3.4","port":"abc"}`),
		vmessJSON(`{"add":"1.2.3.4","port":0}`),
		vmessJSON(`{"ps":"no-add","port":443}`),
		// Malformed payloads from the pins.
		"vmess://!!!not-base64!!!",
		"vmess://",
		vmessJSON(`this is not json {`),
		// Raw binary garbage bytes.
		"vmess://" + string([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0xFF}),
		string([]byte{0x00, 0x01, 0x02, 0x03, 0xFF, 0xFE, 0xFD, 0xFC}),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, link string) {
		pl := p.parseVMessLink(link)
		if pl != nil && pl.Port == 0 {
			t.Errorf("parseVMessLink returned a config with Port == 0 for %q", link)
		}
	})
}

// FuzzDecodeFlexibleBase64 fuzzes the alphabet/padding-tolerant base64
// decoder (seeds from TestDecodeFlexibleBase64). Property asserted: whatever
// decodeFlexibleBase64 accepts must round-trip through the standard alphabet
// (StdEncoding is tried first inside decodeFlexibleBase64, so re-encoding the
// decoded bytes with it and decoding again must always succeed with the same
// bytes).
func FuzzDecodeFlexibleBase64(f *testing.F) {
	seeds := []string{
		base64.StdEncoding.EncodeToString([]byte("hello wg")),     // standard padded
		base64.RawStdEncoding.EncodeToString([]byte("hello wg")),  // raw unpadded
		base64.URLEncoding.EncodeToString([]byte("subj?_>wg")),    // url-safe padded
		base64.RawURLEncoding.EncodeToString([]byte("subj?_>wg")), // url-safe raw
		"", // empty string decodes to empty
		"  " + base64.StdEncoding.EncodeToString([]byte("x")) + "\n", // whitespace trimmed
		"ab=c",    // wrong padding mid-string
		"abc",     // truncated leftover is still raw-decodable
		"abc!def", // invalid chars
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		decoded, ok := decodeFlexibleBase64(s)
		if !ok {
			return
		}
		round, ok2 := decodeFlexibleBase64(base64.StdEncoding.EncodeToString(decoded))
		if !ok2 || string(round) != string(decoded) {
			t.Errorf("standard-alphabet roundtrip failed for input %q: decoded %q, got back %q (ok=%v)",
				s, decoded, round, ok2)
		}
	})
}

// FuzzParse is the end-to-end target over Parser.Parse: base64
// subscriptions, JSON array/object subscriptions, plain share-link payloads,
// share-link line mixes and WireGuard/direct-proxy lines (seeds from
// parser_test.go). Degenerate empty/whitespace inputs are seeded too.
func FuzzParse(f *testing.F) {
	p := NewParser()

	jsonArray := `[{"remarks":"Panel","outbounds":[
		{"protocol":"socks","tag":"s1","settings":{"address":"1.2.3.4","port":1080}}
	]}]`
	jsonObject := `{"remarks":"Single","outbounds":[
		{"protocol":"socks","tag":"s2","settings":{"address":"5.6.7.8","port":1080}}
	]}`
	wgConf := "[Interface]\n" +
		"PrivateKey = WBkVvO3vdhF9VOaSokEQPSLpGQajKi2fpwKLODlySmk=\n" +
		"Address = 10.9.59.216/32\n" +
		"[Peer]\n" +
		"PublicKey = xBsu74OtcatjpRMfW58muk/95FkaiSSYbZeM+6bRZ1Y=\n" +
		"Endpoint = wg.example.com:51820\n"

	shareMix := strings.Join([]string{
		"vless://11111111-2222-3333-4444-555555555555@example.com:443?type=ws&security=tls#vless-node",
		"vmess://" + fuzzB64(`{"v":"2","ps":"vmess-name","add":"1.2.3.4","port":"8443","id":"u","net":"ws"}`),
		"trojan://pw@trojan.example.com:443?sni=t#trojan-name",
		"socks5://user:pass@10.0.0.1:1080#socks-name",
	}, "\n")

	seeds := []string{
		// base64 subscription (TestParse_Base64WrappedJSON).
		fuzzB64(jsonArray),
		// BOM-prefixed variants (TestParse_BOMPrefixedPayloads).
		"\xEF\xBB\xBF" + fuzzB64(jsonArray),
		jsonObject,
		"\uFEFF" + jsonObject,
		// Plain share-link payload (TestParse_PlainAndBase64ShareLinks).
		"vmess://eyJ2IjoiMiIsInBzIjoidGVzdCIsImFkZCI6IjEuMi4zLjQiLCJwb3J0IjoiNDQzIiwiaWQiOiJhYmNkZWYwMC0xMjM0LTU2NzgtOWFiYy1kZWYwMTIzNDU2NzgiLCJzY3kiOiJhdXRvIn0=",
		// Share-link line mix (TestParseOriginalLinks blob).
		shareMix,
		// WireGuard line (TestParseWireGuardURI conf).
		"wg://" + fuzzB64(wgConf) + "#WG Node",
		// Degenerate inputs.
		"",
		"\n\n   \n",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Hermeticity guard: these prefixes route Parse to the network
		// fetcher (fetchURLContent performs real HTTP requests) or to the
		// local filesystem (file:///folder://). Everything else
		// (base64://, data:text/plain;base64,, raw share links, JSON) is
		// local CPU-only parsing.
		for _, prefix := range []string{"http://", "https://", "file://", "folder://"} {
			if strings.HasPrefix(input, prefix) {
				t.Skip("impure source type (network/filesystem) — out of fuzz scope")
			}
		}

		result, err := p.Parse(input)
		if err != nil {
			if result != nil {
				t.Errorf("Parse returned both a result and an error: %+v / %v", result, err)
			}
			return
		}
		if result == nil {
			t.Errorf("Parse returned nil result with nil error for input %q", input)
		}
	})
}
