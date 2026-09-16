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
| `/togglenode <name\|ID>` | Enable or disable checking for a specific proxy node |
| `/checkhost <host[:port]>` | Global reachability audit of any host/IP across worldwide Check-Host nodes |
| `/checkhost_bg [on\|off\|1h\|run]` | Manage background Check-Host auditing and RU reachability alerts |
| `/interval [seconds]` | View or change proxy check interval dynamically (e.g. `/interval 60`) |
| `/stats` | Uptime statistics (%), top problematic proxies, and recent outage history |
| `/quiet` | Configure quiet hours schedule and snooze intervals (1h, 4h, morning) |
| `/targets` | View and manage fallback target check URLs |
| `/subs` | List active subscriptions (configured + added via bot) |
| `/addsub <URL>` | Dynamically add a new subscription URL without restarting |
| `/delsub <URL>` | Remove a previously added dynamic subscription |
| `/digest` | Trigger an immediate status digest in chat |
| `/help` | Display quick help summary |

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
* `TELEGRAM_CHAT_IDS` — your Telegram user ID or group ID.

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
| `TELEGRAM_CHAT_IDS` | `""` | Comma-separated list of authorized Telegram chat IDs |
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
| `PROXY_TARGET_URLS` | Cloudflare/Google 204 | Custom fallback endpoints for checking proxy availability |
| `WEB_ENABLED` | `true` | Enable web dashboard panel (`false` for headless mode) |
| `METRICS_PORT` | `2112` | Web dashboard and Prometheus metrics port |
| `METRICS_PROTECTED` | `false` | Enable Basic Auth protection for web dashboard |
| `METRICS_USERNAME` | `metricsUser` | Web UI login username |
| `METRICS_PASSWORD` | — | Web UI login password |
| `HTTP_PROXY` / `ALL_PROXY` | `""` | Outbound proxy (`socks5://...` or `http://...`) for Telegram and external APIs |

> [!NOTE]
> The **Default** column shows the code default applied when a variable is unset. The bundled
> `.env.example` pins the store paths to `/app/data/*.json` so that state survives Docker
> container rebuilds (only `/app/data` and `/app/geo` are bind-mounted in
> `docker-compose.example.yml`). Keep those overrides if you deploy via Docker Compose.

### Outbound Proxy (for restricted networks)
If your server is in an environment where Telegram API is blocked (e.g. Russian VPS), configure SOCKS5 or HTTP outbound proxy variables in `.env`:
```env
HTTP_PROXY=socks5://xray-client:1080
HTTPS_PROXY=socks5://xray-client:1080
ALL_PROXY=socks5://xray-client:1080
NO_PROXY=localhost,127.0.0.1
```
All outgoing bot requests to Telegram API and Check-Host will be routed through the proxy.

### Headless Mode (No Web Dashboard)
If you only need the Telegram Bot and Prometheus metrics without the web panel:
Set in `.env`:
```env
WEB_ENABLED=false
```
`/metrics` remains active while saving RAM and CPU.
