package subscription

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"xray-checker/logger"
	"xray-checker/models"
)

type libXrayResponse struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

type libXrayOutbound struct {
	Protocol       string                 `json:"protocol"`
	SendThrough    string                 `json:"sendThrough"`
	Tag            string                 `json:"tag"`
	Settings       *libXraySettings       `json:"settings"`
	StreamSettings *libXrayStreamSettings `json:"streamSettings"`
}

type libXraySettings struct {
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Level      int    `json:"level"`
	ID         string `json:"id"`
	Flow       string `json:"flow"`
	Encryption string `json:"encryption"`
	AlterId    int    `json:"alterId"`
	Security   string `json:"security"`
	Password   string `json:"password"`
	Method     string `json:"method"`
	Version    int32  `json:"version"`
	Auth       string `json:"auth"`
	User       string `json:"user"`
	Pass       string `json:"pass"`
}

type libXrayStreamSettings struct {
	Network             string                      `json:"network"`
	Security            string                      `json:"security"`
	TlsSettings         *libXrayTlsSettings         `json:"tlsSettings"`
	RealitySettings     *libXrayRealitySettings     `json:"realitySettings"`
	RawSettings         *libXrayRawSettings         `json:"rawSettings"`
	WsSettings          *libXrayWsSettings          `json:"wsSettings"`
	GrpcSettings        *libXrayGrpcSettings        `json:"grpcSettings"`
	HttpSettings        *libXrayHttpSettings        `json:"httpSettings"`
	HttpupgradeSettings *libXrayHttpupgradeSettings `json:"httpupgradeSettings"`
	XhttpSettings       json.RawMessage             `json:"xhttpSettings"`
	SplithttpSettings   json.RawMessage             `json:"splithttpSettings"`
	KcpSettings         json.RawMessage             `json:"kcpSettings"`
	HysteriaSettings    *libXrayHysteriaSettings    `json:"hysteriaSettings"`
	Sockopt             *libXraySockopt             `json:"sockopt"`
	FinalMask           *libXrayFinalMask           `json:"finalMask"`
}

type libXrayTlsSettings struct {
	ServerName           string   `json:"serverName"`
	AllowInsecure        bool     `json:"allowInsecure"`
	Fingerprint          string   `json:"fingerprint"`
	Alpn                 []string `json:"alpn"`
	PinnedPeerCertSha256 string   `json:"pinnedPeerCertSha256"`
	VerifyPeerCertByName string   `json:"verifyPeerCertByName"`
}

type libXrayRealitySettings struct {
	ServerName  string `json:"serverName"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
	ShortId     string `json:"shortId"`
}

type libXrayRawSettings struct {
	Header *struct {
		Type    string `json:"type"`
		Request *struct {
			Path    []string `json:"path"`
			Headers *struct {
				Host []string `json:"Host"`
			} `json:"headers"`
		} `json:"request"`
	} `json:"header"`
}

type libXrayWsSettings struct {
	Path    string `json:"path"`
	Headers *struct {
		Host string `json:"Host"`
	} `json:"headers"`
	Host string `json:"host"`
}

type libXrayGrpcSettings struct {
	ServiceName string `json:"serviceName"`
	MultiMode   bool   `json:"multiMode"`
}

type libXrayHttpSettings struct {
	Path string   `json:"path"`
	Host []string `json:"host"`
}

type libXrayHttpupgradeSettings struct {
	Path string `json:"path"`
	Host string `json:"host"`
}

type libXrayXhttpSettings struct {
	Path string `json:"path"`
	Host string `json:"host"`
	Mode string `json:"mode"`
}

type libXrayHysteriaSettings struct {
	Version int32  `json:"version"`
	Auth    string `json:"auth"`
}

type libXraySockopt struct {
	FinalMask *libXrayFinalMask `json:"finalMask"`
}

type libXrayFinalMask struct {
	QuicParams *libXrayQuicParams `json:"quicParams"`
	Udp        []libXrayMask      `json:"udp"`
}

type libXrayQuicParams struct {
	Congestion string         `json:"congestion"`
	BrutalUp   string         `json:"brutalUp"`
	BrutalDown string         `json:"brutalDown"`
	UdpHop     *libXrayUdpHop `json:"udpHop"`
}

type libXrayUdpHop struct {
	// libXray serializes xray-core's conf.UdpHop, whose port list key is "ports".
	PortList json.RawMessage    `json:"ports"`
	Interval *libXrayInt32Range `json:"interval"`
}

type libXrayInt32Range struct {
	From int32 `json:"from"`
	To   int32 `json:"to"`
}

type libXrayMask struct {
	Type     string           `json:"type"`
	Settings *json.RawMessage `json:"settings"`
}

type libXraySalamander struct {
	Password string `json:"password"`
}

type xrayStandardSettings struct {
	Vnext []struct {
		Address string `json:"address"`
		Port    int    `json:"port"`
		Users   []struct {
			ID         string `json:"id"`
			Flow       string `json:"flow"`
			Encryption string `json:"encryption"`
			AlterId    int    `json:"alterId"`
			Security   string `json:"security"`
			Level      int    `json:"level"`
		} `json:"users"`
	} `json:"vnext"`
	Servers []struct {
		Address  string `json:"address"`
		Port     int    `json:"port"`
		Password string `json:"password"`
		Method   string `json:"method"`
		Flow     string `json:"flow"`
		Users    []struct {
			User string `json:"user"`
			Pass string `json:"pass"`
		} `json:"users"`
	} `json:"servers"`
}

// convertWireGuardOutbound fills a ProxyConfig from an xray "wireguard" outbound's
// settings: secretKey/address/mtu plus the first peer's publicKey/endpoint/
// allowedIPs/keepAlive/preSharedKey. Server/Port hold the peer endpoint.
func (p *Parser) convertWireGuardOutbound(settings json.RawMessage, pc *models.ProxyConfig) (*models.ProxyConfig, error) {
	var wg struct {
		SecretKey string   `json:"secretKey"`
		Address   []string `json:"address"`
		MTU       int      `json:"mtu"`
		Peers     []struct {
			PublicKey    string   `json:"publicKey"`
			PreSharedKey string   `json:"preSharedKey"`
			Endpoint     string   `json:"endpoint"`
			KeepAlive    int      `json:"keepAlive"`
			AllowedIPs   []string `json:"allowedIPs"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(settings, &wg); err != nil {
		return nil, fmt.Errorf("failed to parse wireguard settings: %v", err)
	}
	if len(wg.Peers) == 0 {
		return nil, fmt.Errorf("no wireguard peers found")
	}
	peer := wg.Peers[0]
	host, portStr, err := net.SplitHostPort(peer.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid wireguard endpoint %q: %v", peer.Endpoint, err)
	}
	pc.Server = host
	pc.Port = atoiOrZero(portStr)
	pc.WGPrivateKey = wg.SecretKey
	pc.WGAddresses = wg.Address
	pc.WGMTU = wg.MTU
	pc.WGPeerPublicKey = peer.PublicKey
	pc.WGPreSharedKey = peer.PreSharedKey
	pc.WGAllowedIPs = peer.AllowedIPs
	pc.WGKeepalive = peer.KeepAlive

	if pc.WGPrivateKey == "" || pc.WGPeerPublicKey == "" || pc.Server == "" || pc.Port == 0 {
		return nil, fmt.Errorf("incomplete wireguard config (missing secretKey/publicKey/endpoint)")
	}
	if pc.Name == "" {
		pc.Name = fmt.Sprintf("wireguard-%s", pc.Server)
	}
	return pc, nil
}

// sanitizeMetricsLabels trims label keys and drops entries with an empty key or
// value. It returns nil for an empty result so proxies without custom labels keep
// a nil map. Prometheus-name validity is enforced later in the metrics collector.
func sanitizeMetricsLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		key := strings.TrimSpace(k)
		if key == "" || v == "" {
			continue
		}
		out[key] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (p *Parser) convertOutbound(raw json.RawMessage, index int, originalData map[string]*originalLinkData) (*models.ProxyConfig, error) {
	var baseOutbound struct {
		Protocol       string                 `json:"protocol"`
		Tag            string                 `json:"tag"`
		SendThrough    string                 `json:"sendThrough"`
		Settings       json.RawMessage        `json:"settings"`
		StreamSettings *libXrayStreamSettings `json:"streamSettings"`
		MetricsLabels  map[string]string      `json:"metricsLabels"`
	}
	if err := json.Unmarshal(raw, &baseOutbound); err != nil {
		return nil, err
	}

	if baseOutbound.Protocol == "freedom" || baseOutbound.Protocol == "blackhole" || baseOutbound.Protocol == "dns" {
		return nil, nil
	}

	pc := &models.ProxyConfig{
		Index:         index,
		Name:          baseOutbound.SendThrough,
		Protocol:      baseOutbound.Protocol,
		MetricsLabels: sanitizeMetricsLabels(baseOutbound.MetricsLabels),
	}

	if pc.Name == "" {
		pc.Name = baseOutbound.Tag
	}

	// WireGuard outbounds have a distinct settings shape (secretKey/address/peers)
	// and no streamSettings, so handle them up front.
	if baseOutbound.Protocol == "wireguard" {
		return p.convertWireGuardOutbound(baseOutbound.Settings, pc)
	}

	var flatSettings libXraySettings
	if err := json.Unmarshal(baseOutbound.Settings, &flatSettings); err == nil && flatSettings.Address != "" {
		pc.Server = flatSettings.Address
		pc.Port = flatSettings.Port

		switch baseOutbound.Protocol {
		case "vless":
			pc.UUID = flatSettings.ID
			pc.Flow = flatSettings.Flow
			pc.Encryption = flatSettings.Encryption
			pc.Level = flatSettings.Level
		case "vmess":
			pc.UUID = flatSettings.ID
			pc.AlterId = flatSettings.AlterId
			pc.Security = flatSettings.Security
			pc.Level = flatSettings.Level
		case "trojan":
			pc.Password = flatSettings.Password
		case "shadowsocks":
			pc.Password = flatSettings.Password
			pc.Method = flatSettings.Method
		case "hysteria":
			pc.HysteriaAuth = flatSettings.Auth
		case "socks", "http":
			pc.Username = flatSettings.User
			pc.Password = flatSettings.Pass
		}
	} else {
		var stdSettings xrayStandardSettings
		if err := json.Unmarshal(baseOutbound.Settings, &stdSettings); err != nil {
			return nil, fmt.Errorf("failed to parse settings: %v", err)
		}

		switch baseOutbound.Protocol {
		case "vless", "vmess":
			if len(stdSettings.Vnext) == 0 || len(stdSettings.Vnext[0].Users) == 0 {
				return nil, fmt.Errorf("no vnext/users found")
			}
			pc.Server = stdSettings.Vnext[0].Address
			pc.Port = stdSettings.Vnext[0].Port
			user := stdSettings.Vnext[0].Users[0]
			pc.UUID = user.ID
			pc.Flow = user.Flow
			pc.Encryption = user.Encryption
			pc.AlterId = user.AlterId
			pc.Level = user.Level
			if baseOutbound.Protocol == "vmess" {
				pc.Security = user.Security
			}
		case "trojan", "shadowsocks":
			if len(stdSettings.Servers) == 0 {
				return nil, fmt.Errorf("no servers found")
			}
			srv := stdSettings.Servers[0]
			pc.Server = srv.Address
			pc.Port = srv.Port
			pc.Password = srv.Password
			pc.Method = srv.Method
			pc.Flow = srv.Flow
		case "socks", "http":
			if len(stdSettings.Servers) == 0 {
				return nil, fmt.Errorf("no servers found")
			}
			srv := stdSettings.Servers[0]
			pc.Server = srv.Address
			pc.Port = srv.Port
			if len(srv.Users) > 0 {
				pc.Username = srv.Users[0].User
				pc.Password = srv.Users[0].Pass
			}
		case "hysteria":
			var hySettings struct {
				Address string `json:"address"`
				Port    int    `json:"port"`
				Version int32  `json:"version"`
				Auth    string `json:"auth"`
			}
			if err := json.Unmarshal(baseOutbound.Settings, &hySettings); err != nil {
				return nil, fmt.Errorf("failed to parse hysteria settings: %v", err)
			}
			pc.Server = hySettings.Address
			pc.Port = hySettings.Port
			pc.HysteriaAuth = hySettings.Auth
		default:
			return nil, fmt.Errorf("unsupported protocol: %s", baseOutbound.Protocol)
		}
	}

	if pc.Server == "" || pc.Port == 0 {
		return nil, fmt.Errorf("failed to parse server/port")
	}

	if pc.Port == 0 || pc.Port == 1 {
		return nil, nil
	}

	if baseOutbound.StreamSettings != nil {
		ss := baseOutbound.StreamSettings
		pc.Type = ss.Network
		pc.Security = ss.Security

		if ss.TlsSettings != nil {
			pc.SNI = ss.TlsSettings.ServerName
			pc.AllowInsecure = ss.TlsSettings.AllowInsecure
			pc.Fingerprint = ss.TlsSettings.Fingerprint
			pc.ALPN = ss.TlsSettings.Alpn
			pc.PinnedPeerCertSha256 = ss.TlsSettings.PinnedPeerCertSha256
			pc.VerifyPeerCertByName = ss.TlsSettings.VerifyPeerCertByName
		}

		if ss.RealitySettings != nil {
			pc.SNI = ss.RealitySettings.ServerName
			pc.Fingerprint = ss.RealitySettings.Fingerprint
			pc.PublicKey = ss.RealitySettings.PublicKey
			pc.ShortID = ss.RealitySettings.ShortId
		}

		if ss.Network == "raw" {
			pc.Type = "tcp"
		}

		if ss.RawSettings != nil && ss.RawSettings.Header != nil {
			pc.HeaderType = ss.RawSettings.Header.Type
			if ss.RawSettings.Header.Request != nil {
				if len(ss.RawSettings.Header.Request.Path) > 0 {
					pc.Path = ss.RawSettings.Header.Request.Path[0]
				}
				if ss.RawSettings.Header.Request.Headers != nil && len(ss.RawSettings.Header.Request.Headers.Host) > 0 {
					pc.Host = ss.RawSettings.Header.Request.Headers.Host[0]
				}
			}
		}

		if ss.WsSettings != nil {
			pc.Path = ss.WsSettings.Path
			if ss.WsSettings.Headers != nil {
				pc.Host = ss.WsSettings.Headers.Host
			}
			if pc.Host == "" {
				pc.Host = ss.WsSettings.Host
			}
		}

		if ss.GrpcSettings != nil {
			pc.ServiceName = ss.GrpcSettings.ServiceName
			pc.MultiMode = ss.GrpcSettings.MultiMode
		}

		if ss.HttpSettings != nil {
			pc.Path = ss.HttpSettings.Path
			if len(ss.HttpSettings.Host) > 0 {
				pc.Host = strings.Join(ss.HttpSettings.Host, ",")
			}
		}

		if ss.HttpupgradeSettings != nil {
			pc.Type = "httpupgrade"
			pc.Path = ss.HttpupgradeSettings.Path
			pc.Host = ss.HttpupgradeSettings.Host
		}

		if (ss.Network == "kcp" || ss.Network == "mkcp") && len(ss.KcpSettings) > 0 {
			pc.RawKcpSettings = string(ss.KcpSettings)
		}

		if ss.Network == "xhttp" || ss.Network == "splithttp" {
			pc.Type = ss.Network

			var rawSettings json.RawMessage
			if len(ss.XhttpSettings) > 0 {
				rawSettings = ss.XhttpSettings
			} else if len(ss.SplithttpSettings) > 0 {
				rawSettings = ss.SplithttpSettings
			}

			if len(rawSettings) > 0 {
				pc.RawXhttpSettings = string(rawSettings)
				var parsed libXrayXhttpSettings
				if err := json.Unmarshal(rawSettings, &parsed); err == nil {
					pc.Path = parsed.Path
					pc.Host = parsed.Host
					pc.Mode = parsed.Mode
				}
			}
		}

		// Hysteria stream settings
		if ss.HysteriaSettings != nil {
			if pc.HysteriaAuth == "" {
				pc.HysteriaAuth = ss.HysteriaSettings.Auth
			}
		}

		// Extract QuicParams and Salamander from FinalMask
		// FinalMask can be at streamSettings level or inside sockopt
		finalMask := ss.FinalMask
		if finalMask == nil && ss.Sockopt != nil {
			finalMask = ss.Sockopt.FinalMask
		}
		if finalMask != nil {
			if finalMask.QuicParams != nil {
				qp := finalMask.QuicParams
				pc.HysteriaUp = qp.BrutalUp
				pc.HysteriaDown = qp.BrutalDown
				if qp.UdpHop != nil {
					if qp.UdpHop.PortList != nil {
						var ports string
						if err := json.Unmarshal(qp.UdpHop.PortList, &ports); err == nil {
							pc.HysteriaPorts = ports
						} else {
							logger.Debug("Failed to parse UdpHop portList as string: %v", err)
						}
					}
					if qp.UdpHop.Interval != nil {
						pc.HysteriaHopInterval = qp.UdpHop.Interval.From
					}
				}
			}
			if len(finalMask.Udp) > 0 {
				mask := finalMask.Udp[0]
				if mask.Type == "salamander" && mask.Settings != nil {
					pc.HysteriaObfs = "salamander"
					var sal libXraySalamander
					if err := json.Unmarshal(*mask.Settings, &sal); err == nil {
						pc.HysteriaObfsPassword = sal.Password
					} else {
						logger.Debug("Failed to parse salamander settings: %v", err)
					}
				}
			}
		}
	}

	key := fmt.Sprintf("%s:%d", pc.Server, pc.Port)
	if orig, ok := originalData[key]; ok {
		if pc.Encryption == "" || pc.Encryption == "none" {
			if orig.Encryption != "" {
				pc.Encryption = orig.Encryption
			}
		}
		if orig.AllowInsecure {
			pc.AllowInsecure = true
		}
	}

	if err := pc.Validate(); err != nil {
		return nil, err
	}

	pc.StableID = pc.GenerateStableID()

	return pc, nil
}

// extractOutbounds extracts proxy configs from libXray response data.
func (p *Parser) extractOutbounds(data json.RawMessage, originalData map[string]*originalLinkData) []*models.ProxyConfig {
	var xrayConfig struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(data, &xrayConfig); err != nil {
		logger.Debug("Failed to parse libXray config data: %v", err)
		return nil
	}

	logger.Debug("Parsed %d outbounds", len(xrayConfig.Outbounds))

	var proxyConfigs []*models.ProxyConfig
	configIndex := 0
	for _, outboundRaw := range xrayConfig.Outbounds {
		proxyConfig, err := p.convertOutbound(outboundRaw, configIndex, originalData)
		if err != nil {
			logger.Debug("Skipping outbound: %v", err)
			continue
		}
		if proxyConfig != nil {
			proxyConfigs = append(proxyConfigs, proxyConfig)
			configIndex++
		}
	}

	return proxyConfigs
}

// nameGroupedProxies assigns display names to the proxies parsed from a single JSON
// config (one `remarks` group). A single-node group takes the group name; a
// multi-node group (a balancer) names each node "<group> | <node>" so the nodes are
// tracked individually. Each proxy's Name is expected to already hold its outbound
// tag (set by convertOutbound); server:port is used as a fallback and to
// disambiguate nodes that share a tag.
func nameGroupedProxies(remarks string, group []*models.ProxyConfig) {
	if len(group) == 0 {
		return
	}
	if len(group) == 1 {
		if remarks != "" {
			group[0].Name = remarks
		}
		return
	}

	nodeLabel := func(pc *models.ProxyConfig) string {
		if pc.Name != "" {
			return pc.Name
		}
		return fmt.Sprintf("%s:%d", pc.Server, pc.Port)
	}
	labelCounts := make(map[string]int, len(group))
	for _, pc := range group {
		labelCounts[nodeLabel(pc)]++
	}
	for _, pc := range group {
		node := nodeLabel(pc)
		if labelCounts[node] > 1 {
			node = fmt.Sprintf("%s (%s:%d)", node, pc.Server, pc.Port)
		}
		if remarks != "" {
			pc.Name = fmt.Sprintf("%s | %s", remarks, node)
			pc.GroupName = remarks
		} else {
			pc.Name = node
		}
	}
}
