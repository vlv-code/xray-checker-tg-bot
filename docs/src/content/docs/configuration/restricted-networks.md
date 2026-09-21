---
title: Restricted & Enterprise Networks
description: Deploying remote checker nodes behind corporate firewalls, NAT, and restricted environments
---

This guide describes how to install and operate remote `xray-checker` nodes in restricted network conditions: enterprise firewalls, corporate HTTP proxies, NAT, isolated intranets, and environments where direct access to GitHub or Docker Hub is filtered or blocked.

---

## 1. Network Flow Architecture

To ensure remote nodes accurately test proxies and reliably push reports to the master, network traffic is strictly isolated across four distinct channels:

```
                  ┌──────────────────────────────────────────────────────────┐
                  │                 Remote Checker Node                      │
                  └─────┬──────────────────┬───────────────────┬─────────────┘
                        │                  │                   │
             (1) Master Report      (2) Direct Ping    (3) Tunnel Probe    (4) Bootstrap (opt.)
             REPORT_URL             TCP/UDP            SOCKS5 127.0.0.1    BOOTSTRAP_PROXY
                        │                  │                   │                   │
                        ▼                  ▼                   ▼                   ▼
                  ┌───────────┐      ┌───────────┐       ┌───────────┐       ┌───────────┐
                  │  Master   │      │   Proxy   │       │ Xray Core │       │ Corporate │
                  │  Instance │      │  Server   │       │  Inbound  │       │   Proxy   │
                  │(direct/VPN│      │(port 443) │       │ (tunnel)  │       │(10809/3128│
                  └───────────┘      └───────────┘       └─────┬─────┘       └─────┬─────┘
                                                               │                   │
                                                               ▼                   ▼
                                                         Target sites        Geo bases &
                                                         (Cloudflare,        subscriptions
                                                          Google 204)        (GitHub, etc)
```

1. **Master Ingest Reports (`REPORT_URL`):**
   - Pushed directly to the master server.
   - The built-in `NO_PROXY` builder automatically excludes the master host/IP, `localhost`, `127.0.0.1`, `::1`, and all RFC1918 private subnets (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.0.0/16`) from any system-level HTTP proxy.
2. **Direct Proxy Ping (`TCP/UDP`):**
   - Tests direct reachability of the proxy port (e.g. `443`) from the node host.
   - Uses native OS sockets (`net.Dialer`), bypassing HTTP proxies.
3. **Target Probes (Cloudflare, Google 204):**
   - Routed **inside the encrypted proxy tunnel** via Xray's local SOCKS5 inbound (`127.0.0.1:100xx`).
   - The local firewall/ISP cannot inspect or selectively filter Google or Cloudflare traffic inside this tunnel. Target timeouts reflect routing, IPv6, or egress filtering on the proxy server itself.
4. **Subscriptions & Geo Databases (`BOOTSTRAP_PROXY`):**
   - If direct internet access to GitHub or remote subscription panels is blocked, configure `BOOTSTRAP_PROXY=http://proxy:port`.
   - `BOOTSTRAP_PROXY` is used **exclusively** for fetching external subscription and geo resources without interfering with master communications or proxy checks.

---

## 2. Option A: Deployment via Docker Compose

In `ghcr.io/vlv-code/xray-checker-tg-bot:latest`, `geosite.dat` and `geoip.dat` databases are pre-bundled in the image so the container starts immediately without requiring outbound GitHub access.

Use the provided [docker-compose.node.yml](https://github.com/vlv-code/xray-checker-tg-bot/blob/main/docker-compose.node.yml) template:

```yaml
services:
  xray-node:
    image: ghcr.io/vlv-code/xray-checker-tg-bot:latest
    container_name: xray-node
    restart: unless-stopped
    environment:
      # Master ingest URL and node bearer token (generated via /nodeadd <name> on master)
      - REPORT_URL=http://<YOUR_MASTER_IP>:2112/api/v1/nodes/report
      - REPORT_TOKEN=CHANGE_ME_TO_YOUR_NODE_BEARER_TOKEN
      # Uncomment if external downloads require a corporate proxy:
      # - BOOTSTRAP_PROXY=http://192.168.27.121:10809
    volumes:
      - node-data:/app/data
      - node-geo:/app/geo

volumes:
  node-data:
  node-geo:
```

Start the node:
```bash
docker compose -f docker-compose.node.yml up -d
```

:::note[Docker Daemon Proxies in ~/.docker/config.json]
If your host configures global Docker daemon proxies (`"proxies": { "default": { "httpProxy": ... } }`), Docker automatically injects `HTTP_PROXY` into all containers. `xray-checker` automatically detects and sanitizes this by adding master and intranet addresses to `NO_PROXY`.
:::

---

## 3. Fallback: Deployment via Systemd (Without Docker)

For servers where Docker is unavailable, blocked, or isolated from external registries:

1. Download the compiled `xray-checker-tg-bot-*-linux-amd64.tar.gz` from [GitHub Releases](https://github.com/vlv-code/xray-checker-tg-bot/releases) on any machine with internet access and copy it to the node:
   ```bash
   scp xray-checker root@node-server:/usr/local/bin/xray-checker
   chmod +x /usr/local/bin/xray-checker
   ```

2. Create a dedicated system user and directories:
   ```bash
   useradd -r -s /bin/false xray-checker
   mkdir -p /opt/xray-checker/geo /opt/xray-checker/data
   chown -R xray-checker:xray-checker /opt/xray-checker
   ```

3. Install the systemd service unit:
   ```bash
   cp scripts/xray-checker-node.service /etc/systemd/system/xray-checker-node.service
   ```

4. Configure your `REPORT_URL` and `REPORT_TOKEN` in `/etc/systemd/system/xray-checker-node.service`.

5. Enable and start the service:
   ```bash
   systemctl daemon-reload
   systemctl enable --now xray-checker-node
   journalctl -u xray-checker-node -f
   ```

---

## 4. Understanding Status Differences in Diagnostics

### Protocol-Specific Differences
- **TCP Protocols (VLESS, Trojan, Shadowsocks)**: probe raw TCP port `443`.
- **UDP Protocols (Hysteria, TUIC)**: operate solely over UDP/QUIC and perform UDP reachability checks instead of TCP pings.

### Direct TCP 443 Timed Out (`⚠️` or `❌`), but Tunnel Works
- **Reason:** Reality or stealth anti-probing on the server drops unauthenticated TCP probes, or the local ISP throttles bare TCP 443. Once connected via Xray with authentic keys/SNI, the tunnel passes traffic normally.

### Check-Host Deduplication
- To prevent API rate limits when multiple proxies fail simultaneously on the same host, Check-Host audits are automatically deduplicated per host.