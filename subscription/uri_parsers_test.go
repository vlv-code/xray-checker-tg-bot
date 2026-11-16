package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// The tests in this file are adversarial regression PINs: they lock in the
// CURRENT contracts of the share-link parsers (uri_parsers.go) and of the
// convertOutbound port guards (json_convert.go). Malformed input must never
// panic; each case asserts either an error/nil result or the exact currently
// produced config, not a desired future behavior.

func mustB64(t *testing.T, s string) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// TestParseShareLink_WellFormed pins the happy paths of parseShareLink for the
// common share-link schemes. parseShareLink is deliberately scheme-agnostic:
// it extracts server/port/name plus a small set of query params and never
// decodes the userinfo payload (ss/trojan/hysteria2 credentials are consumed
// later by libXray).
func TestParseShareLink_WellFormed(t *testing.T) {
	p := NewParser()
	cases := []struct {
		name     string
		link     string
		server   string
		port     int
		dispName string
		ltype    string
		encrypt  string
		path     string
		host     string
		insecure bool
	}{
		{name: "vless ws tls full", link: "vless://11111111-2222-3333-4444-555555555555@example.com:443?type=ws&security=tls#vless-node",
			server: "example.com", port: 443, dispName: "vless-node", ltype: "ws"},
		{name: "trojan sni", link: "trojan://hunter2@trojan.example.com:443?sni=cdn.example.com#trojan-node",
			server: "trojan.example.com", port: 443, dispName: "trojan-node"},
		{name: "ss base64 userinfo form", link: "ss://" + mustB64(t, "aes-256-gcm:secret") + "@1.2.3.4:8388#ss-node",
			server: "1.2.3.4", port: 8388, dispName: "ss-node"},
		{name: "ss userinfo is not decoded", link: "ss://" + mustB64(t, "onlypassword") + "@1.2.3.4:8388#ss-node2",
			// garbage-without-method-separator in the userinfo payload must not
			// affect parsing: userinfo is opaque to parseShareLink.
			server: "1.2.3.4", port: 8388, dispName: "ss-node2"},
		{name: "hysteria2 with sni", link: "hysteria2://authpass@hy.example.com:443?sni=hy-cdn.example.com#hy-node",
			server: "hy.example.com", port: 443, dispName: "hy-node"},
		{name: "hysteria2 empty auth still parses", link: "hysteria2://@hy.example.com:443?sni=hy-cdn.example.com#hy-node2",
			// empty userinfo is not an error; auth validation happens downstream.
			server: "hy.example.com", port: 443, dispName: "hy-node2"},
		{name: "query params mapped", link: "vless://11111111-2222-3333-4444-555555555555@5.6.7.8:8443?type=grpc&encryption=auto&path=%2Fws&host=cdn.example.com&allowInsecure=1#q",
			server: "5.6.7.8", port: 8443, dispName: "q", ltype: "grpc", encrypt: "auto",
			path: "/ws", host: "cdn.example.com", insecure: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pl := p.parseShareLink(c.link)
			if pl == nil {
				t.Fatalf("expected a parsedLink, got nil for %q", c.link)
			}
			if pl.Server != c.server || pl.Port != c.port {
				t.Errorf("endpoint got %s:%d want %s:%d", pl.Server, pl.Port, c.server, c.port)
			}
			if pl.Name != c.dispName {
				t.Errorf("name got %q want %q", pl.Name, c.dispName)
			}
			if pl.Type != c.ltype || pl.Encryption != c.encrypt || pl.Path != c.path || pl.Host != c.host {
				t.Errorf("query got type=%q enc=%q path=%q host=%q want type=%q enc=%q path=%q host=%q",
					pl.Type, pl.Encryption, pl.Path, pl.Host, c.ltype, c.encrypt, c.path, c.host)
			}
			if pl.AllowInsecure != c.insecure {
				t.Errorf("allowInsecure got %v want %v", pl.AllowInsecure, c.insecure)
			}
		})
	}
}

// TestParseShareLink_Adversarial feeds malformed URIs and pins the current
// graceful outcome (nil, or a config with the exact currently-produced
// fields). A test completing without a panic is itself part of the contract.
func TestParseShareLink_Adversarial(t *testing.T) {
	p := NewParser()
	cases := []struct {
		name    string
		link    string
		wantNil bool
		server  string
		port    int
	}{
		{name: "empty string", link: "", wantNil: true},
		{name: "scheme only", link: "vless://", wantNil: true},
		{name: "no authority at all", link: "://x:notaport", wantNil: true},
		{name: "missing @ (no userinfo)", link: "vless://example.com:443",
			// scheme- and userinfo-agnostic: a bare host:port still parses.
			server: "example.com", port: 443},
		{name: "empty uuid (empty userinfo)", link: "vless://@example.com:443#x",
			server: "example.com", port: 443},
		{name: "unbracketed ipv6", link: "vless://[::1", wantNil: true},
		{name: "port above range is not validated here", link: "vless://example.com:99999#x",
			// regression pin: parseShareLink has no 65535 upper-bound check
			// (config validation happens later); pin the current pass-through.
			server: "example.com", port: 99999},
		{name: "port 0", link: "vless://example.com:0#x", wantNil: true},
		{name: "port not numeric", link: "vless://example.com:notaport#x", wantNil: true},
		{name: "multiple @ in userinfo", link: "vless://a:b@c@example.com:443#x",
			// net/url splits on the LAST '@'.
			server: "example.com", port: 443},
		{name: "userinfo with colon", link: "vless://user:pass@example.com:443#x",
			server: "example.com", port: 443},
		{name: "malformed %zz escape in fragment", link: "vless://example.com:443#na%zzme", wantNil: true},
		{name: "malformed %zz escape in query is silently dropped", link: "vless://example.com:443?type=%zz#x",
			// url.Values parsing ignores escape errors and drops the bad pair.
			server: "example.com", port: 443},
		{name: "fragment containing #", link: "vless://example.com:443#my#name",
			server: "example.com", port: 443},
		{name: "unicode fragment", link: "vless://example.com:443#Имя-сервера-🚀",
			server: "example.com", port: 443},
		{name: "10k-char fragment", link: "vless://example.com:443#" + strings.Repeat("a", 10000),
			server: "example.com", port: 443},
		{name: "10k-char host is parsed as one long host", link: "vless://" + strings.Repeat("h", 10000) + ":443#x",
			server: strings.Repeat("h", 10000), port: 443},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pl := p.parseShareLink(c.link)
			if c.wantNil {
				if pl != nil {
					t.Fatalf("expected nil, got %+v", pl)
				}
				return
			}
			if pl == nil {
				t.Fatalf("expected a parsedLink, got nil")
			}
			if pl.Server != c.server || pl.Port != c.port {
				t.Errorf("endpoint got %s:%d want %s:%d", pl.Server, pl.Port, c.server, c.port)
			}
		})
	}
}

// TestParseVMessLink pins the vmess:// base64-JSON contract: port accepted as
// JSON number or numeric string, and every malformed payload returns nil.
func TestParseVMessLink(t *testing.T) {
	p := NewParser()
	type vmess map[string]interface{}
	mk := func(t *testing.T, v vmess) string {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal vmess json: %v", err)
		}
		return "vmess://" + mustB64(t, string(b))
	}

	cases := []struct {
		name     string
		link     string
		wantNil  bool
		server   string
		port     int
		dispName string
	}{
		{name: "port as string", link: mk(t, vmess{"v": "2", "ps": "vm-str", "add": "1.2.3.4", "port": "443", "id": "u", "scy": "auto"}),
			server: "1.2.3.4", port: 443, dispName: "vm-str"},
		{name: "port as number", link: mk(t, vmess{"v": "2", "ps": "vm-num", "add": "1.2.3.4", "port": 443, "id": "u"}),
			server: "1.2.3.4", port: 443, dispName: "vm-num"},
		{name: "full happy path", link: mk(t, vmess{"v": "2", "ps": "vm-full", "add": "5.6.7.8", "port": 8443, "id": "u",
			"net": "ws", "host": "cdn.example.com", "path": "/ws"}),
			server: "5.6.7.8", port: 8443, dispName: "vm-full"},
		{name: "invalid base64", link: "vmess://!!!not-base64!!!", wantNil: true},
		{name: "empty payload", link: "vmess://", wantNil: true},
		{name: "base64 of invalid json", link: "vmess://" + mustB64(t, "this is not json {"), wantNil: true},
		{name: "json missing port", link: mk(t, vmess{"v": "2", "ps": "no-port", "add": "1.2.3.4", "id": "u"}), wantNil: true},
		{name: "empty json object", link: mk(t, vmess{}), wantNil: true},
		{name: "port non-numeric string", link: mk(t, vmess{"add": "1.2.3.4", "port": "abc"}), wantNil: true},
		{name: "port number 0", link: mk(t, vmess{"add": "1.2.3.4", "port": 0}), wantNil: true},
		{name: "port present but add missing keeps empty server", link: mk(t, vmess{"ps": "no-add", "port": 443}),
			// regression pin: add-less JSON still yields a config with empty Server.
			server: "", port: 443, dispName: "no-add"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pl := p.parseVMessLink(c.link)
			if c.wantNil {
				if pl != nil {
					t.Fatalf("expected nil, got %+v", pl)
				}
				return
			}
			if pl == nil {
				t.Fatalf("expected a parsedLink, got nil")
			}
			if pl.Server != c.server || pl.Port != c.port {
				t.Errorf("endpoint got %s:%d want %s:%d", pl.Server, pl.Port, c.server, c.port)
			}
			if pl.Name != c.dispName {
				t.Errorf("name got %q want %q", pl.Name, c.dispName)
			}
		})
	}
}

// TestParseOriginalLinks pins the line-loop contract: every line goes through
// parseShareLink, results are keyed "server:port", later lines win on
// duplicate keys, and garbage lines are skipped without affecting others.
// Note: parseOriginalLinks handles socks5/http lines too, because
// parseShareLink parses any host:port URL generically.
func TestParseOriginalLinks(t *testing.T) {
	p := NewParser()

	vlessLine := "vless://11111111-2222-3333-4444-555555555555@example.com:443?type=ws&security=tls#vless-name"
	vmessLine := "vmess://" + mustB64(t, `{"v":"2","ps":"vmess-name","add":"1.2.3.4","port":"8443","id":"u","net":"ws"}`)
	trojanLine := "trojan://pw@trojan.example.com:443?sni=t#trojan-name"
	socksLine := "socks5://user:pass@10.0.0.1:1080#socks-name"

	t.Run("mixed well-formed blob", func(t *testing.T) {
		blob := strings.Join([]string{vlessLine, vmessLine, trojanLine, socksLine}, "\n")
		got := p.parseOriginalLinks([]byte(blob))
		if len(got) != 4 {
			t.Fatalf("expected 4 entries, got %d: %+v", len(got), got)
		}
		if e := got["example.com:443"]; e == nil || e.Name != "vless-name" || e.Type != "ws" {
			t.Errorf("vless entry wrong: %+v", got["example.com:443"])
		}
		if e := got["1.2.3.4:8443"]; e == nil || e.Name != "vmess-name" || e.Host != "" {
			t.Errorf("vmess entry wrong: %+v", got["1.2.3.4:8443"])
		}
		if e := got["trojan.example.com:443"]; e == nil || e.Name != "trojan-name" {
			t.Errorf("trojan entry wrong: %+v", got["trojan.example.com:443"])
		}
		if e := got["10.0.0.1:1080"]; e == nil || e.Name != "socks-name" {
			t.Errorf("socks5 entry wrong: %+v", got["10.0.0.1:1080"])
		}
	})

	t.Run("empty input yields empty non-nil map", func(t *testing.T) {
		for _, in := range []string{"", "\n\n   \n"} {
			got := p.parseOriginalLinks([]byte(in))
			if got == nil {
				t.Fatalf("expected empty map for %q, got nil", in)
			}
			if len(got) != 0 {
				t.Errorf("expected 0 entries for %q, got %d", in, len(got))
			}
		}
	})

	t.Run("garbage lines skipped", func(t *testing.T) {
		blob := strings.Join([]string{"this is not base64!!", "://x:notaport", vlessLine, "vmess://!!!bad!!!"}, "\n")
		got := p.parseOriginalLinks([]byte(blob))
		if len(got) != 1 {
			t.Fatalf("expected exactly 1 entry, got %d: %+v", len(got), got)
		}
		if _, ok := got["example.com:443"]; !ok {
			t.Errorf("valid line lost among garbage: %+v", got)
		}
	})

	t.Run("duplicate key last line wins", func(t *testing.T) {
		blob := "vless://u@example.com:443#first\nvless://u@example.com:443#second"
		got := p.parseOriginalLinks([]byte(blob))
		if len(got) != 1 || got["example.com:443"].Name != "second" {
			t.Errorf("duplicate handling wrong: %+v", got)
		}
	})

	t.Run("very long and unicode names", func(t *testing.T) {
		long := "vless://u@long.example.com:443#" + strings.Repeat("b", 10000)
		uni := "vless://u@uni.example.com:443#Имя-сервера-🚀"
		got := p.parseOriginalLinks([]byte(strings.Join([]string{long, uni}, "\n")))
		if len(got) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(got))
		}
		if n := got["long.example.com:443"].Name; len(n) != 10000 {
			t.Errorf("long name truncated: %d chars", len(n))
		}
		if n := got["uni.example.com:443"].Name; n != "Имя-сервера-🚀" {
			t.Errorf("unicode name mangled: %q", n)
		}
	})
}

// TestDecodeFlexibleBase64 pins the alphabet/padding tolerance matrix.
func TestDecodeFlexibleBase64(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		wantOK bool
		want   string
	}{
		{name: "standard padded", in: base64.StdEncoding.EncodeToString([]byte("hello wg")), wantOK: true, want: "hello wg"},
		{name: "raw unpadded", in: base64.RawStdEncoding.EncodeToString([]byte("hello wg")), wantOK: true, want: "hello wg"},
		{name: "url-safe padded", in: base64.URLEncoding.EncodeToString([]byte("subj?_>wg")), wantOK: true, want: "subj?_>wg"},
		{name: "url-safe raw", in: base64.RawURLEncoding.EncodeToString([]byte("subj?_>wg")), wantOK: true, want: "subj?_>wg"},
		{name: "empty string decodes to empty", in: "", wantOK: true, want: ""},
		{name: "whitespace trimmed", in: "  " + base64.StdEncoding.EncodeToString([]byte("x")) + "\n", wantOK: true, want: "x"},
		{name: "wrong padding mid-string", in: "ab=c", wantOK: false},
		{name: "truncated leftover is still raw-decodable", in: "abc", wantOK: true, want: "i\xb7"},
		{name: "invalid chars", in: "abc!def", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := decodeFlexibleBase64(c.in)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (got %q)", ok, c.wantOK, got)
			}
			if ok && string(got) != c.want {
				t.Errorf("decoded %q, want %q", got, c.want)
			}
		})
	}
}

// TestConvertOutbound_PortGuards pins the port guards in json_convert.go
// (convertOutbound): port 0 -> hard error; port 1 -> silently dropped
// (nil, nil, legacy upstream behavior preserved deliberately); port > 1 ->
// config returned. The Port==0 half of the second guard is unreachable and
// must remain so after cleanup: port 0 must KEEP erroring, never become a
// silent drop.
func TestConvertOutbound_PortGuards(t *testing.T) {
	p := NewParser()
	mkOutbound := func(port int) []byte {
		return []byte(`{"protocol":"vless","tag":"guard-test","settings":{"address":"203.0.113.10","port":` + strconv.Itoa(port) + `,"id":"11111111-2222-3333-4444-555555555555"}}`)
	}

	t.Run("port 0 errors", func(t *testing.T) {
		pc, err := p.convertOutbound(mkOutbound(0), 0, nil)
		if err == nil {
			t.Fatalf("expected an error for port 0, got pc=%+v", pc)
		}
		if pc != nil {
			t.Errorf("expected nil config on error, got %+v", pc)
		}
	})

	t.Run("port 1 silently dropped", func(t *testing.T) {
		pc, err := p.convertOutbound(mkOutbound(1), 0, nil)
		if err != nil {
			t.Fatalf("port 1 drop must be silent (nil, nil), got err=%v", err)
		}
		if pc != nil {
			t.Errorf("port 1 config must be dropped, got %+v", pc)
		}
	})

	t.Run("port 2 passes", func(t *testing.T) {
		pc, err := p.convertOutbound(mkOutbound(2), 0, nil)
		if err != nil || pc == nil {
			t.Fatalf("port 2 must yield a config, got pc=%+v err=%v", pc, err)
		}
		if pc.Port != 2 || pc.Server != "203.0.113.10" || pc.Protocol != "vless" {
			t.Errorf("unexpected config %+v", pc)
		}
	})
}
