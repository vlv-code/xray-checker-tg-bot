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

## 3. Option B: Deployment via Systemd (Without Docker)

For servers where Docker is unavailable or cannot pull from external registries:

1. Copy the compiled `xray-checker` binary to `/usr/local/bin/xray-checker` and make it executable:
   ```bash
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
   ```

6. Check service logs:
   ```bash
   journalctl -u xray-checker-node -f
   ```

---

## 4. Diagnostics Interpretation in Telegram Bot

### Direct Port Timed Out (`⚠️`), but Tunnel Operational
- **Display:** `• TCP (443): ⚠️ Timeout (provider filtering or server anti-probe protection, but tunnel is active)`
- **Explanation:** Modern censorship-resistant protocols (Reality, Trojan, ShadowTLS) intentionally drop or ignore unauthenticated TCP SYN probes on port 443 ("stealth mode"). Alternatively, the local firewall may filter bare TCP 443 while allowing established flows.
- **Key Takeaway:** If HTTP 204 succeeds through the tunnel, the proxy is fully functional and healthy.

### Single Target Failure Inside Tunnel (e.g. Google Timeout while Cloudflare Passes)
- **Display:** `• gstatic: ❌ Timeout (proxy response timeout: routing/IPv6 issue on proxy server or service disruption)`
- **Explanation:** Traffic to Google travels inside the encrypted proxy tunnel. The node's local network does not cause this. It typically indicates lack of IPv6 connectivity on the proxy VPS or temporary rate limiting by Google on the proxy IP.

---

## 5. Troubleshooting & Network Caveats

### Docker Bridge Stale Network State (`i/o timeout`, `context deadline exceeded`)
- **Symptom:** The container logs timeout errors when connecting to subscriptions or remote servers, even though the host network can reach them.
- **Cause:** Docker daemon may maintain stale conntrack entries or bridge socket state across fast restarts.
- **Solution:** Fully tear down and recreate the container network stack:
  ```bash
  docker compose down
  docker compose up -d
  ```

### YAML Parse Error (`invalid trailing UTF-8 octet`)
- **Symptom:** `yaml: offset ...: invalid trailing UTF-8 octet` during `docker compose pull` or `up`.
- **Cause:** Copy-pasting non-ASCII characters (e.g. Cyrillic comments) through terminal sessions that alter multi-byte UTF-8 sequences.
- **Solution:** Use clean ASCII configurations without non-ASCII comments or create files via `cat << 'EOF' > docker-compose.yml`.

### Use Pre-built GHCR Images Instead of `build:`
- Do not use `build: https://github.com/...` on remote nodes. Building from source in restricted networks frequently fails on Go module proxies and GitHub release downloads. Always use the pre-built multi-arch image:
  ```yaml
  image: ghcr.io/vlv-code/xray-checker-tg-bot:v2.4.0
  ```