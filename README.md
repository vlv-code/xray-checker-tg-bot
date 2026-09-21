# Xray Checker

[![GitHub Release](https://img.shields.io/github/v/release/vlv-code/xray-checker-tg-bot?style=flat&color=blue)](https://github.com/vlv-code/xray-checker-tg-bot/releases/latest)
[![DockerHub](https://img.shields.io/badge/Docker-ready-blue)](https://github.com/vlv-code/xray-checker-tg-bot)
[![License](https://img.shields.io/badge/License-AGPL%20v3-blue)](https://github.com/vlv-code/xray-checker-tg-bot/blob/main/LICENSE)
[![ru](https://img.shields.io/badge/lang-ru-blue)](README_RU.md)
[![en](https://img.shields.io/badge/lang-en-red)](README.md)

> [!NOTE]
> **This repository is a fork of the original [kutovoys/xray-checker](https://github.com/kutovoys/xray-checker)** featuring a fully integrated Telegram Bot built on the official `mymmrac/telego` library:
> - ⚡ **Multi-Stage Connection Diagnostics**: Isolated analysis of DNS resolution, TCP ping (RTT), TLS handshake, Xray SOCKS tunnel, and target endpoints (paginated view + Telegram 10.1 rich format).
> - 🌐 **Check-Host.net Background Audit**: Periodic reachability checks across all subscription hosts with automated Russian blocking alerts (`RUAvailable == false`) and deduplication.
> - 🚫 **Individual Node Disabling**: Toggle checks for specific proxy connections directly in the bot menu without modifying subscription files.
> - ⏱️ **Dynamic Runtime Check Interval**: Change check frequency on the fly (`/interval` or menu) without container restarts.
> - 🔔 **Smart Alert Lifecycle**: Clean Mode (auto-deletes alerts upon recovery) and Live Mode (updates original outage message with total downtime) with persistent state and anti-flapping.
> - 🌙 **Quiet Night Hours & Digests**: Suppress alert sounds overnight, automatic morning digest, and periodic daytime summaries.
> - 📈 **Persistent Uptime Statistics**: Sliding incident log and reliability rankings (`/stats`).
> - 📋 **Dynamic Subscription Management**: Add and remove subscriptions in chat (`/subs`, `/addsub`, `/delsub`).
> - 📦 **Headless Mode**: Run without web dashboard (`WEB_ENABLED=false`).

---

## 🚀 Key Features

* 🔍 **Xray Proxy Monitoring**: Supports VLESS (including Reality & Vision), VMess, Trojan, Shadowsocks, WireGuard, and Hysteria2.
* 🤖 **Telegram Bot on `mymmrac/telego`**: Interactive inline keyboard menu, instant outage and recovery alerts.
* ⚡ **Multi-Stage Diagnostics (`/diag`)**: Pinpoints exact point of failure (DNS, TCP, TLS, Xray session reset, CDN blocking).
* 🌐 **Check-Host.net Integration**: Automatic multi-node reachability checks from Russia and worldwide locations during outages.
* ⏱️ **Runtime Interval Tuning**: `/interval <seconds>` command and interactive menu presets.
* 🔄 **Auto-Reloading Subscriptions**: Multiple subscription URLs (Base64, JSON, share links) with automated periodic sync.
* 📊 **Prometheus Metrics**: Exports `/metrics` for Grafana scraping and supports Prometheus Pushgateway.
* 🌐 **Web Dashboard**: Monitoring UI with light/dark themes and public status page.
* 🔒 **Security**: Protect metrics and dashboard with HTTP Basic Auth.
* 🐳 **Docker & Docker Compose**: Instant single-command deployment with minimal resource footprint.

---

## 🤖 Telegram Bot Commands

All features are accessible via the interactive **`/menu`** or direct chat commands:

| Command | Description |
|---|---|
| `/menu` or `/start` | Open main interactive menu with real-time status summary and navigation |
| `/status` | Real-time status summary of all configured proxies (online/offline, latency) |
| `/diag` | Detailed multi-stage diagnostics (DNS, TCP RTT, TLS, Targets + Check-Host) |
| `/settings` | Bot settings, interval, alert mode, and node disabling |
| `/togglenode <name|ID>` | Enable or disable checking for a specific proxy node |
| `/togglehost <host>` | Enable or disable checking for all proxies sharing a specific host |
| `/checkhost <host[:port]>` | Global reachability audit of any host/IP across worldwide Check-Host nodes |
| `/checkhost_bg [on|off|1h|run]` | Manage background Check-Host auditing and RU reachability alerts |
| `/interval [seconds]` | View or change proxy check interval dynamically (e.g. `/interval 60`) |
| `/tz [timezone]` | View or set bot timezone for quiet hours and digests (e.g. `/tz Europe/Moscow`) |
| `/checkupdate` | Check for new GitHub releases of Xray Checker and view changelog |
| `/stats` | Uptime statistics (%), top problematic proxies, and recent outage history |
| `/quiet` | Configure quiet hours schedule and snooze intervals (1h, 4h, morning) |
| `/targets` | View and manage fallback target check URLs |
| `/subs` | List active subscriptions (configured + added via bot) |
| `/addsub <URL>` | Dynamically add a new subscription URL without restarting |
| `/delsub <URL>` | Remove a previously added dynamic subscription |
| `/nodes` | Health status, ASN, and summary of remote checker nodes |
| `/nodeadd [name]` | Connect and register a new remote node (generates token & run command) |
| `/nodedel <name>` | Delete a remote node from master |
| `/nodesubs <name>` | List desired subscriptions assigned to a remote node |
| `/nodeaddsub <name> <URL>` | Assign a subscription URL to a remote node |
| `/nodedelsub <name> <URL>` | Unassign a subscription URL from a remote node |
| `/digest` | Trigger an immediate status digest in chat |
| `/id` | Print this chat's `chat_id` and `topic_id` for `TELEGRAM_CHAT_IDS` |
| `/help` | Display quick help summary |

### Groups and Forum Topics

The bot serves private chats, plain groups and forum supergroups. Each `TELEGRAM_CHAT_IDS` entry is an independent delivery target:

* `123456789` — a private chat or a whole group (the General topic in forum groups)
* `-100987654321:42` — topic `42` of a forum supergroup

Every target receives its own copy of alerts, recovery notes and digests, and commands always reply in the chat or topic where they were invoked.

**Setup:**

1. Add the bot to the group and allow it to send messages.
2. Run `/id` in the group or in a specific topic — it replies with a ready-to-paste `TELEGRAM_CHAT_IDS` entry.
3. Put that value into `.env` and restart.

**Privacy mode.** By default Telegram bots in groups only see commands that mention the bot (for example `/menu@YourBot`) and replies to its messages; outgoing alerts are not affected. To use short commands like `/menu`, disable privacy via [@BotFather](https://t.me/BotFather) (`/mybots` → Bot Settings → Group Privacy → Off, then remove and re-add the bot to existing groups), or simply make the bot a group admin.

---

## 🖥 Remote Nodes Architecture (Push Model)

Run additional lightweight checker node instances anywhere in the world (VPS, home server, Raspberry Pi) and have them push check results to your master instance that owns the Telegram bot:

- **1-Click Connection from Telegram**:
  - Open **`[ 🖥 Ноды ]`** ➔ **`[ ➕ Подключить ноду ]`** (or send `/nodeadd <name>`).
  - The bot automatically generates a secure 64-hex bearer token and replies with a copy-pasteable **`docker run`** command and **Docker Compose** block.
  - Nodes are hot-registered on the master and saved to `/app/data/nodes.json` (persists across restarts and container rebuilds).
- **Interactive Subscription Management**:
  - Open **`[ 🖥 Ноды ]`** ➔ **`[ ⚙️ Настройки нод ]`** ➔ **`[ 📋 Подписки нод ]`**.
  - Select any node to inspect its assigned subscriptions, proxy counts from its latest report, and delete subscriptions with 1-click (`[ 🗑 Удалить: #N ]`).
  - Assign new subscriptions with **`[ ➕ Назначить подписку ]`** by simply sending the subscription URL in chat.
  - Subscriptions are dynamically synced to the node upon its next report cycle.
- **Pre-Built Docker Image on GHCR**:
  - Multi-arch (`linux/amd64`, `linux/arm64`) image published to GitHub Container Registry:
    ```bash
    docker run -d --name xray-node-1 \
      --restart unless-stopped \
      -e REPORT_URL=http://<MASTER_IP>:2112/api/v1/nodes/report \
      -e REPORT_TOKEN=<64_HEX_TOKEN> \
      ghcr.io/vlv-code/xray-checker-tg-bot:latest
    ```
- **Outage Alerts & ASN Identification**:
  - Master alerts on every node's proxy failures (`🖥 [Нода] <name>` identity).
  - Automatically notifies when a node goes down or recovers.
  - Displays the node's ISP / Hosting Provider ASN (via local `db-ip asn-lite` database).
- **Restricted & Enterprise Networks Support**:
  - Remote nodes run smoothly behind enterprise firewalls, NAT, and corporate proxies.
  - Smart traffic separation: master reports bypass proxies automatically (`NO_PROXY` builder), port checks stay direct, and external asset downloads can use a dedicated `BOOTSTRAP_PROXY`.
  - Ready-made [docker-compose.node.yml](docker-compose.node.yml) and systemd service [scripts/xray-checker-node.service](scripts/xray-checker-node.service).
  - Complete guide: [docs/restricted-networks.md](docs/restricted-networks.md).

---

## ⚡ Connection Diagnostics

### 1. Multi-Stage Diagnostics (`/diag`)
Tests all nodes across 5 independent stages and formulates an objective fact-based verdict:
* **DNS Lookup**: Domain resolution and DNS query latency.
* **TCP Ping (RTT)**: Direct connection to node `IP:Port` (differentiates timeout from closed port).
* **TLS Handshake Probe**: Tests TLS/SNI handshake (detects domain/SNI filtering).
* **Xray Tunnel & Targets**: Verifies traffic forwarding through proxy to targets (Cloudflare, Google, etc.).
* **Check-Host Fallback**: If a node fails locally, the bot automatically verifies port reachability from Russian and international probe nodes.

```text
⚡ Detailed diagnostics report (2 proxies):

🟢 NL-Amsterdam (VLESS)
  • DNS: ✅ 185.120.45.10 (18 ms)
  • TCP (443): ✅ 42 ms
  • TLS: ✅ 58 ms
  • Cloudflare 204: ✅ 65 ms
  • Google 204: ✅ 71 ms
  💡 Verdict: Fully operational

🔴 DE-Frankfurt (VLESS)
  • DNS: ✅ 45.132.18.2 (20 ms)
  • TCP (443): ❌ TCP connection timeout (unreachable from checker host)
  • Check-Host (TCP): RU ❌ unreachable, World ✅ reachable
    🔗 report
  💡 Verdict: Node TCP timeout | Check-Host: unreachable from RU nodes, but responds from foreign networks
```

### 2. Targeted Global Audit (`/checkhost`)
Command `/checkhost <host[:port]>` or the `[ 🌐 Check-Host ]` button in `/menu` runs a full worldwide check:
```text
🌐 Check-Host results for 185.120.45.10:443 (TCP):

🇷🇺 Russia:
  • Moscow (RU): ❌ Connection timed out
  • Saint Petersburg (RU): ❌ Connection timed out

🇪🇺 Europe:
  • Nuremberg (DE): ✅ 18 ms
  • Amsterdam (NL): ✅ 22 ms
  • Helsinki (FI): ✅ 26 ms

🇺🇸 Americas:
  • Los Angeles (US): ✅ 115 ms
  • New York (US): ✅ 95 ms

💡 Conclusion: Host is unreachable from RU nodes, but responds from foreign networks
🔗 Permanent report link
```

---

## 🔔 Release Update Notifications

Stay up to date with new features, bug fixes, and security improvements:
- **Automated Alerts**: The bot periodically queries GitHub releases (`TELEGRAM_RELEASE_ALERTS=true`, checking every 6 hours by default via `TELEGRAM_RELEASE_CHECK_INTERVAL_HOURS`).
- **Interactive Upgrade Info**: When a new release is available, the bot sends an alert with formatted changelog notes and container update commands.
- **On-Demand Check**: Run `/checkupdate` at any time or click **`[ 🔄 Проверить обновления ]`** inside **`[ ⚙️ Настройки ]`**.
- **Admin Control**: Restrict update and configuration commands using `TELEGRAM_ADMIN_USER_IDS`.

---

## 🛠️ Quick Start

### 1. Clone the repository
```bash
git clone https://github.com/vlv-code/xray-checker-tg-bot.git
cd xray-checker-tg-bot
```

### 2. Configure environment
```bash
cp .env.example .env
nano .env
```
Fill in the essential variables:
* `SUBSCRIPTION_URL` — your proxy subscription link.
* `TELEGRAM_BOT_TOKEN` — bot token from [@BotFather](https://t.me/BotFather).
* `TELEGRAM_CHAT_IDS` — your Telegram user ID, group ID, or `group:topic` (see [Groups and Forum Topics](#groups-and-forum-topics)).

### 3. Start with Docker Compose
```bash
cp docker-compose.example.yml docker-compose.yml
docker compose up -d --build
```
The web dashboard and Prometheus metrics will be available at: `http://<server_ip>:2112`.

> **Note on volume permissions:** Docker creates the `./data` and `./geo` host
> directories as `root:root` on first start. The image entrypoint fixes their
> ownership automatically (it starts as root, chowns the mounts, then drops
> privileges to the unprivileged `appuser`). If you run the container with an
> explicit `user:` override and see `permission denied` errors, fix ownership
> on the host once:
> ```bash
> mkdir -p data geo && sudo chown -R 1000:1000 data geo
> ```

### 4. Deploying a Remote Node
To run a monitoring node on a separate server, different region, or restricted network:
1. Register the node on the master via Telegram bot: `/nodeadd <node_name>` (receives auth token).
2. Use the template [docker-compose.node.yml](docker-compose.node.yml) or run directly:
   ```bash
   docker run -d --name xray-node-1 \
     --restart unless-stopped \
     -e REPORT_URL=http://<MASTER_IP>:2112/api/v1/nodes/report \
     -e REPORT_TOKEN=<TOKEN_FROM_BOT> \
     ghcr.io/vlv-code/xray-checker-tg-bot:v2.4.1
   ```
3. Full deployment guide, systemd setup, and network troubleshooting: **[docs/restricted-networks.md](docs/restricted-networks.md)**.

---

## 🔄 Updating on Server

To pull latest improvements and restart the container:
```bash
cd /opt/xray-checker   # or your deployment directory
git pull origin main
docker compose up -d --build
```

---

## ⚙️ Configuration Reference (`.env`)

| Variable | Default | Description |
|---|---|---|
| `SUBSCRIPTION_URL` | `""` | Proxy subscription link(s) (Base64/JSON URLs, share links) |
| `SUBSCRIPTION_STORE_PATH` | `subscriptions.json` | Persistent storage path for `/addsub` subscriptions |
| `TELEGRAM_BOT_TOKEN` | `""` | Telegram bot token from @BotFather (enables the bot) |
| `TELEGRAM_CHAT_IDS` | `""` | Comma-separated delivery targets: chat IDs and `chat:topic` forum topics (e.g. `-100987654321:42`) |
| `TELEGRAM_ALERT_MODE` | `clean` | Alert mode: `clean` (auto-deletes) or `live` (edits outage alert) |
| `TELEGRAM_RICH_MODE`  | `false` | Enable Telegram Bot API 10.1 rich messages for diagnostics (table + collapsible details) |
| `CHECKHOST_BG_ENABLED` | `true` | Periodic background Reachability audit via Check-Host.net |
| `CHECKHOST_INTERVAL_HOURS` | `1` | Background Check-Host audit interval in hours (1, 2, 4, 6, 12) |
| `CHECKHOST_ALERT_ENABLED` | `true` | Send Telegram alert if a node is unreachable from Russia |
| `ALERT_STORE_PATH` | `alerts.json` | Persistent storage path for active alerts (anti-flapping across restarts) |
| `TELEGRAM_QUIET_HOURS_ENABLED`| `true` | Suppress alert sound pings overnight |
| `TELEGRAM_QUIET_HOURS_START`  | `23:00` | Start of quiet hours (HH:MM) |
| `TELEGRAM_QUIET_HOURS_END`    | `08:00` | End of quiet hours (HH:MM) and morning digest trigger |
| `TELEGRAM_DAY_DIGEST_ENABLED` | `true` | Send periodic daytime health summaries |
| `STATS_STORE_PATH` | `stats.json` | Persistent storage path for outage history and stats |
| `BOT_CONFIG_STORE_PATH` | `bot_config.json` | Persistent storage path for runtime bot settings (including disabled nodes) |
| `PROXY_CHECK_INTERVAL` | `300` | Check interval in seconds (also tunable via `/interval`) |
| `PROXY_CHECK_METHOD` | `ip` | Check method: `ip` (IP echo), `status` (HTTP 204), or `download` (file download) |
| `PROXY_TIMEOUT` | `30` | Per-proxy check timeout in seconds |
| `PROXY_CHECK_CONCURRENCY` | `0` | Max proxies checked in parallel per cycle (0 = unlimited) |
| `PROXY_TARGET_URLS` | Cloudflare/Google 204 | Custom fallback endpoints for checking proxy availability |
| `SUBSCRIPTION_UPDATE` | `true` | Periodically re-fetch subscriptions and apply changes |
| `SUBSCRIPTION_UPDATE_INTERVAL` | `300` | Seconds between subscription re-fetches |
| `WEB_ENABLED` | `true` | Enable web dashboard panel (`false` for headless mode) |
| `WEB_PUBLIC` | `false` | Public status page mode (requires `METRICS_PROTECTED=true`) |
| `METRICS_PORT` | `2112` | Web dashboard and Prometheus metrics port |
| `METRICS_PROTECTED` | `false` | Enable Basic Auth protection for web dashboard |
| `METRICS_USERNAME` | `metricsUser` | Web UI login username |
| `METRICS_PASSWORD` | — | Web UI login password |
| `METRICS_PUSH_URL` | `""` | Push metrics to a Prometheus Pushgateway instead of exposing `/metrics` |
| `SIMULATE_LATENCY` | `true` | Add measured latency (capped at 2s) to `/config/{id}` badge responses |
| `LOG_LEVEL` | `info` | Application log level (`debug\|info\|warn\|error\|none`) |
| `RUN_ONCE` | `false` | Run a single check cycle and exit (for cron/scheduled jobs) |
| `NODES` | `""` | Comma-separated remote checker nodes as `name\|token` for master instance |
| `NODES_STORE_PATH` | `node_subs.json` | Persistent storage for node-assigned desired subscriptions (and sibling `nodes.json`) |
| `MASTER_PUBLIC_URL` | `""` | Public master ingest URL shown in `/nodeadd` commands (auto-detected if empty) |
| `REPORT_URL` | `""` | Master ingest endpoint URL for node push reports |
| `REPORT_TOKEN` | `""` | Bearer token matching node entry in master's `NODES` list |
| `ASN_DB_URL` | db-ip asn-lite | URL of gzipped ASN mmdb database for node network operator lookup |
| `HTTP_PROXY` / `ALL_PROXY` | `""` | Outbound proxy (`socks5://...` or `http://...`) for Telegram and external APIs |

> [!NOTE]
> The **Default** column shows the code default applied when a variable is unset. The bundled
> `.env.example` documents every variable with comments (and pins the store paths to
> `/app/data/*.json` so that state survives Docker container rebuilds — only `/app/data` and
> `/app/geo` are bind-mounted in `docker-compose.example.yml`). A per-variable reference
> also lives in the [documentation site](docs/src/content/docs/configuration/envs.md).

### Authentication & Public Routes
With `METRICS_PROTECTED=true`, Basic Auth covers everything the HTTP server exposes — the
dashboard, `/metrics`, `/config/{id}` pages, `/static/` assets and all `/api/` routes; only
`/health` stays open for load-balancer probes. The public status page mode (`WEB_PUBLIC=true`,
which requires `METRICS_PROTECTED=true`) opens the dashboard, config pages and
`GET /api/v1/public/proxies` to unauthenticated visitors, while `/metrics` and the management
API remain password-protected.

### Outbound Proxy (for restricted networks)
If your server is in an environment where Telegram API is blocked (e.g. Russian VPS), configure SOCKS5 or HTTP outbound proxy variables in `.env`:
```env
HTTP_PROXY=socks5://xray-client:1080
HTTPS_PROXY=socks5://xray-client:1080
ALL_PROXY=socks5://xray-client:1080
NO_PROXY=localhost,127.0.0.1
```
All outgoing bot requests to Telegram API and Check-Host will be routed through the proxy.

### Subscription Sources & SSRF Protection
`SUBSCRIPTION_URL` accepts remote `http(s)://` URLs, local sources (`file:///path/sub.txt`,
`folder:///path/configs/`, `base64://...`), and raw share links (`vless://...`, `vmess://...`)
pasted directly.

Remote subscription URLs are guarded by built-in SSRF protection: targets on `localhost`,
private (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), link-local, CGNAT, IPv6 transition
(NAT64 / 6to4 / Teredo — addresses that wrap an IPv4 host) and other reserved ranges are
rejected — both at URL validation time and at connection time, including hostnames
that resolve to such addresses. There is no opt-out switch, and routing the fetch through
`HTTP_PROXY` does not bypass it (validation happens before the proxy is consulted).

Consequence: a panel hosted on your LAN (e.g. Marzban/Remnawave behind `192.168.x.x`)
cannot be used as a remote subscription source. Either expose the panel on a public
address, or sync its subscription output to a local file and reference it via `file://`.
Note that the proxies themselves may point at private addresses — the restriction applies
only to fetching the subscription.

### Headless Mode (No Web Dashboard)
If you only need the Telegram Bot and Prometheus metrics without the web panel:
Set in `.env`:
```env
WEB_ENABLED=false
```
`/metrics` remains active while saving RAM and CPU.
