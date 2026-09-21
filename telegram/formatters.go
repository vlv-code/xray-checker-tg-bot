package telegram

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

func formatTimeAgo(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return "только что"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dм назад", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dч назад", int(d.Hours()))
	}
	return fmt.Sprintf("%dд назад", int(d.Hours()/24))
}

func formatProtocolTransport(protocol, transport, security string) string {
	protoName := strings.ToUpper(protocol)
	switch strings.ToLower(protocol) {
	case "vless":
		protoName = "VLESS"
	case "vmess":
		protoName = "VMess"
	case "trojan":
		protoName = "Trojan"
	case "shadowsocks":
		protoName = "Shadowsocks"
	case "hysteria2":
		protoName = "Hysteria2"
	case "wireguard":
		protoName = "WireGuard"
	}

	var transportName string
	secLower := strings.ToLower(security)
	transLower := strings.ToLower(transport)

	if secLower == "reality" {
		transportName = "Reality"
	} else if transLower != "" {
		switch transLower {
		case "ws":
			transportName = "WebSocket"
		case "tcp":
			if secLower == "tls" {
				transportName = "TLS"
			} else {
				transportName = "TCP"
			}
		case "grpc":
			transportName = "gRPC"
		case "httpupgrade":
			transportName = "HTTPUpgrade"
		case "splithttp", "xhttp":
			transportName = "xHTTP"
		default:
			transportName = strings.ToUpper(transport)
		}
	} else if secLower != "" && secLower != "none" {
		transportName = strings.ToUpper(security)
	}

	if transportName != "" {
		return protoName + " / " + transportName
	}
	return protoName
}

func simplifyTargetName(targetURL string) string {
	switch {
	case strings.Contains(targetURL, "cloudflare"):
		return "Cloudflare 204"
	case strings.Contains(targetURL, "gstatic") || strings.Contains(targetURL, "google"):
		return "Google 204"
	case strings.Contains(targetURL, "ipify"):
		return "ipify.org"
	default:
		u, err := url.Parse(targetURL)
		if err == nil && u.Host != "" {
			return u.Host
		}
		return targetURL
	}
}

func formatTargetErrorInsideTunnel(errStr string, statusCode int) string {
	if statusCode == 403 || statusCode == 429 {
		return fmt.Sprintf("HTTP %d (доступ ограничен целевым сервисом с IP прокси / капча)", statusCode)
	}
	if strings.Contains(errStr, "Timeout") {
		return "Timeout (таймаут ответа через прокси: проблема маршрута/IPv6 на сервере прокси или сбой сервиса)"
	}
	if strings.Contains(errStr, "EOF") || strings.Contains(errStr, "Connection reset") {
		return "EOF (соединение сброшено целевым сервисом через прокси)"
	}
	return errStr
}

func splitMessage(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var cur strings.Builder
	var openTags []string

	for _, line := range lines {
		closing := closeTags(openTags)
		if cur.Len() > 0 && cur.Len()+len(line)+1+len(closing) > limit {
			cur.WriteString(closing)
			chunks = append(chunks, cur.String())
			cur.Reset()

			cur.WriteString(openTagsPrefix(openTags))
		}
		if cur.Len() > 0 && cur.Len() != len(openTagsPrefix(openTags)) {
			cur.WriteString("\n")
		}
		cur.WriteString(line)
		openTags = updateOpenTags(line, openTags)
	}
	if cur.Len() > 0 {
		cur.WriteString(closeTags(openTags))
		chunks = append(chunks, cur.String())
	}
	return chunks
}

func updateOpenTags(text string, openTags []string) []string {
	i := 0
	for i < len(text) {
		if text[i] == '<' {
			end := strings.IndexByte(text[i:], '>')
			if end == -1 {
				break
			}
			tagContent := strings.TrimSpace(text[i+1 : i+end])
			if strings.HasPrefix(tagContent, "/") {
				tagName := strings.ToLower(strings.TrimPrefix(tagContent, "/"))
				for j := len(openTags) - 1; j >= 0; j-- {
					openName := strings.ToLower(strings.Fields(openTags[j])[0])
					if openName == tagName {
						openTags = append(openTags[:j], openTags[j+1:]...)
						break
					}
				}
			} else if !strings.HasSuffix(tagContent, "/") {
				parts := strings.Fields(tagContent)
				if len(parts) > 0 {
					tagName := strings.ToLower(parts[0])
					switch tagName {
					case "b", "strong", "i", "em", "u", "ins", "s", "strike", "del", "code", "pre", "tg-spoiler", "blockquote":
						openTags = append(openTags, tagName)
					case "a":
						openTags = append(openTags, tagContent)
					}
				}
			}
			i += end + 1
		} else {
			i++
		}
	}
	return openTags
}

func closeTags(tags []string) string {
	var sb strings.Builder
	for i := len(tags) - 1; i >= 0; i-- {
		tagName := strings.Fields(tags[i])[0]
		sb.WriteString("</" + tagName + ">")
	}
	return sb.String()
}

func openTagsPrefix(tags []string) string {
	var sb strings.Builder
	for _, tag := range tags {
		sb.WriteString("<" + tag + ">")
	}
	return sb.String()
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
