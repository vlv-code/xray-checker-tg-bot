package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"xray-checker/models"
)

type originalLinkData struct {
	Name          string
	Encryption    string
	Type          string
	Path          string
	Host          string
	AllowInsecure bool
}

type parsedLink struct {
	Server        string
	Port          int
	Name          string
	Encryption    string
	Type          string
	Path          string
	Host          string
	AllowInsecure bool
}

// parseWireGuardURI parses a wg://<base64(conf)> line, where the base64 payload
// is a standard WireGuard .conf (INI text). An optional #name fragment sets the
// display name. Returns nil if the line is not a usable WireGuard config.
func parseWireGuardURI(line string) *models.ProxyConfig {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "wg://") {
		return nil
	}
	line = line[len("wg://"):]

	name := ""
	if i := strings.IndexByte(line, '#'); i >= 0 {
		name = line[i+1:]
		line = line[:i]
	}
	line = strings.TrimSpace(line)

	conf, ok := decodeFlexibleBase64(line)
	if !ok {
		return nil
	}

	pc := parseWireGuardConf(string(conf))
	if pc == nil {
		return nil
	}
	pc.Protocol = "wireguard"
	if name != "" {
		if un, err := url.PathUnescape(name); err == nil {
			name = un
		}
		pc.Name = name
	}
	if pc.Name == "" {
		pc.Name = fmt.Sprintf("wireguard-%s", pc.Server)
	}
	return pc
}

// decodeFlexibleBase64 decodes a base64 string trying standard and URL-safe
// alphabets, with and without padding.
func decodeFlexibleBase64(s string) ([]byte, bool) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}

// parseWireGuardConf parses a WireGuard .conf (INI) into a ProxyConfig.
// Server/Port hold the first peer's endpoint. Returns nil if the minimum
// required fields (private key, peer public key, endpoint) are missing.
func parseWireGuardConf(text string) *models.ProxyConfig {
	pc := &models.ProxyConfig{}
	section := ""

	for _, raw := range strings.Split(text, "\n") {
		ln := strings.TrimSpace(raw)
		if ln == "" || strings.HasPrefix(ln, "#") || strings.HasPrefix(ln, ";") {
			continue
		}
		if strings.HasPrefix(ln, "[") && strings.HasSuffix(ln, "]") {
			section = strings.ToLower(strings.TrimSpace(ln[1 : len(ln)-1]))
			continue
		}
		eq := strings.IndexByte(ln, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(ln[:eq]))
		val := strings.TrimSpace(ln[eq+1:])

		switch section {
		case "interface":
			switch key {
			case "privatekey":
				pc.WGPrivateKey = val
			case "address":
				pc.WGAddresses = splitCSVList(val)
			case "dns":
				pc.WGDNS = splitCSVList(val)
			case "mtu":
				pc.WGMTU = atoiOrZero(val)
			}
		case "peer":
			switch key {
			case "publickey":
				pc.WGPeerPublicKey = val
			case "presharedkey":
				pc.WGPreSharedKey = val
			case "endpoint":
				if host, portStr, err := net.SplitHostPort(val); err == nil {
					pc.Server = host
					pc.Port = atoiOrZero(portStr)
				}
			case "allowedips":
				pc.WGAllowedIPs = splitCSVList(val)
			case "persistentkeepalive":
				pc.WGKeepalive = atoiOrZero(val)
			}
		}
	}

	if pc.WGPrivateKey == "" || pc.WGPeerPublicKey == "" || pc.Server == "" || pc.Port == 0 {
		return nil
	}
	return pc
}

func splitCSVList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func atoiOrZero(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// parseProxyURI parses a socks/http/https forward-proxy URI into a ProxyConfig.
// Supported forms (optional #name fragment on any):
//
//	socks://base64(user:pass)@host:port      (standard subscription form)
//	socks5://user:pass@host:port             (socks5h:// also accepted)
//	http://user:pass@host:port
//	https://user:pass@host:port              (TLS to the proxy itself)
//
// Optional query params: sni=<name>, allowInsecure=true|1 (alias insecure=).
// Returns nil if the line is not a usable forward proxy (so other URI schemes
// and plain web URLs fall through to the normal parsing path).
func parseProxyURI(line string) *models.ProxyConfig {
	line = strings.TrimSpace(line)

	var scheme string
	for _, s := range []string{"socks5h", "socks5", "socks", "https", "http"} {
		if strings.HasPrefix(line, s+"://") {
			scheme = s
			line = line[len(s)+3:]
			break
		}
	}
	if scheme == "" {
		return nil
	}

	// Fragment -> name.
	name := ""
	if i := strings.IndexByte(line, '#'); i >= 0 {
		name = line[i+1:]
		line = line[:i]
	}
	// Query params.
	var query url.Values
	if i := strings.IndexByte(line, '?'); i >= 0 {
		query, _ = url.ParseQuery(line[i+1:])
		line = line[:i]
	}
	// Path: forward proxies have none; a real path means this is a web URL.
	if i := strings.IndexByte(line, '/'); i >= 0 {
		path := line[i:]
		line = line[:i]
		if (scheme == "http" || scheme == "https") && path != "/" {
			return nil
		}
	}

	// userinfo@host:port (last '@'; base64 userinfo never contains '@').
	userinfo := ""
	hostport := line
	if i := strings.LastIndexByte(line, '@'); i >= 0 {
		userinfo = line[:i]
		hostport = line[i+1:]
	}

	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil || host == "" {
		return nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil
	}

	pc := &models.ProxyConfig{Server: host, Port: port, Type: "tcp"}
	switch scheme {
	case "socks", "socks5", "socks5h":
		pc.Protocol = "socks"
	case "http":
		pc.Protocol = "http"
	case "https":
		pc.Protocol = "http"
		pc.Security = "tls"
	}

	// Credentials: explicit "user:pass", else base64(user:pass), else user only.
	if userinfo != "" {
		if i := strings.IndexByte(userinfo, ':'); i >= 0 {
			pc.Username, _ = url.QueryUnescape(userinfo[:i])
			pc.Password, _ = url.QueryUnescape(userinfo[i+1:])
		} else if decoded, derr := base64.StdEncoding.DecodeString(userinfo); derr == nil && strings.Contains(string(decoded), ":") {
			parts := strings.SplitN(string(decoded), ":", 2)
			pc.Username, pc.Password = parts[0], parts[1]
		} else {
			pc.Username, _ = url.QueryUnescape(userinfo)
		}
	}

	if sni := query.Get("sni"); sni != "" {
		pc.SNI = sni
	}
	if pc.Security == "tls" {
		if pc.SNI == "" {
			pc.SNI = host
		}
		// xray-core removed allowInsecure; pin a (self-signed) cert by its
		// sha256 instead, or verify against a specific name.
		if v := query.Get("pinnedPeerCertSha256"); v != "" {
			pc.PinnedPeerCertSha256 = v
		} else if v := query.Get("pcs"); v != "" {
			pc.PinnedPeerCertSha256 = v
		}
		if v := query.Get("verifyPeerCertByName"); v != "" {
			pc.VerifyPeerCertByName = v
		} else if v := query.Get("vcn"); v != "" {
			pc.VerifyPeerCertByName = v
		}
	}

	if name != "" {
		if un, derr := url.PathUnescape(name); derr == nil {
			pc.Name = un
		} else {
			pc.Name = name
		}
	} else {
		pc.Name = fmt.Sprintf("%s:%d", host, port)
	}

	return pc
}

func (p *Parser) parseOriginalLinks(rawData []byte) map[string]*originalLinkData {
	result := make(map[string]*originalLinkData)

	decoded := p.tryDecodeBase64(rawData)

	lines := strings.Split(string(decoded), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		data := p.parseShareLink(line)
		if data != nil {
			key := fmt.Sprintf("%s:%d", data.Server, data.Port)
			result[key] = &originalLinkData{
				Name:          data.Name,
				Encryption:    data.Encryption,
				Type:          data.Type,
				Path:          data.Path,
				Host:          data.Host,
				AllowInsecure: data.AllowInsecure,
			}
		}
	}

	return result
}

func (p *Parser) parseShareLink(link string) *parsedLink {
	if strings.HasPrefix(link, "vmess://") {
		return p.parseVMessLink(link)
	}

	u, err := url.Parse(link)
	if err != nil {
		return nil
	}

	result := &parsedLink{
		Name: u.Fragment,
	}

	host := u.Hostname()
	portStr := u.Port()
	if portStr == "" {
		return nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port == 0 {
		return nil
	}
	result.Server = host
	result.Port = port

	query := u.Query()
	result.Type = query.Get("type")
	result.Encryption = query.Get("encryption")
	result.Path = query.Get("path")
	result.Host = query.Get("host")
	result.AllowInsecure = query.Get("allowInsecure") == "1" || query.Get("allowInsecure") == "true"

	return result
}

func (p *Parser) parseVMessLink(link string) *parsedLink {
	encoded := strings.TrimPrefix(link, "vmess://")
	decoded, err := p.decodeBase64(encoded)
	if err != nil {
		return nil
	}

	var vmess map[string]interface{}
	if err := json.Unmarshal(decoded, &vmess); err != nil {
		return nil
	}

	result := &parsedLink{}

	if ps, ok := vmess["ps"].(string); ok {
		result.Name = ps
	}
	if add, ok := vmess["add"].(string); ok {
		result.Server = add
	}

	switch port := vmess["port"].(type) {
	case float64:
		result.Port = int(port)
	case string:
		if p, err := strconv.Atoi(port); err == nil {
			result.Port = p
		}
	}

	if result.Port == 0 {
		return nil
	}

	if net, ok := vmess["net"].(string); ok {
		result.Type = net
	}
	if host, ok := vmess["host"].(string); ok {
		result.Host = host
	}
	if path, ok := vmess["path"].(string); ok {
		result.Path = path
	}

	return result
}
