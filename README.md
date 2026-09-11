# Xray Checker

<div align="center">

[![GitHub Release](https://img.shields.io/github/v/release/kutovoys/xray-checker?color=blue)](https://github.com/kutovoys/xray-checker/releases/latest)
[![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/kutovoys/xray-checker/build-publish.yml)](https://github.com/kutovoys/xray-checker/actions/workflows/build-publish.yml)
[![GitHub Downloads (all assets, all releases)](https://img.shields.io/github/downloads/kutovoys/xray-checker/total?logo=github&color=blue)](https://github.com/kutovoys/xray-checker/releases/latest)
[![Docker Pulls](https://img.shields.io/docker/pulls/kutovoys/xray-checker?logo=docker&label=pulls)](https://hub.docker.com/r/kutovoys/xray-checker/)
[![GitHub License](https://img.shields.io/github/license/kutovoys/xray-checker?color=greeen)](https://github.com/kutovoys/xray-checker/blob/main/LICENSE)
[![ru](https://img.shields.io/badge/lang-ru-blue)](https://github.com/kutovoys/xray-checker/blob/main/README_RU.md)
[![en](https://img.shields.io/badge/lang-en-red)](https://github.com/kutovoys/xray-checker/blob/main/README.md)

</div>
<div align="center">

[![Documentation](https://img.shields.io/badge/Docs-xray--checker.kutovoy.dev-blue)](https://xray-checker.kutovoy.dev/)
[![DockerHub](https://img.shields.io/badge/DockerHub-kutovoys%2Fxray--checker-blue)](https://hub.docker.com/r/kutovoys/xray-checker/)
[![Live Demo](https://img.shields.io/badge/Demo-live-green)](https://demo-xray-checker.kutovoy.dev/)
[![Telegram Chat](https://img.shields.io/badge/Telegram-Chat-blue?logo=telegram&)](https://t.me/+uZCGx_FRY0tiOGIy)

</div>

Xray Checker is a tool for monitoring proxy server availability with support for VLESS, VMess, Trojan, and Shadowsocks protocols. It automatically tests connections through Xray Core and provides metrics for Prometheus, as well as API endpoints for integration with monitoring systems.

<div align="center">
  <img src=".github/screen/xray-checker.webp" alt="Dashboard Screenshot">
</div>

> [!TIP]
> **Try the Live Demo:** See Xray Checker in action at [demo-xray-checker.kutovoy.dev](https://demo-xray-checker.kutovoy.dev/)

## 🚀 Key Features

- 🔍 Monitoring of Xray proxy servers (VLESS, VMess, Trojan, Shadowsocks)
- 🤖 Integrated Telegram Bot: real-time outage & recovery alerts, /status and dynamic subscription management (/subs, /addsub, /delsub)
- 🔄 Automatic configuration updates from subscription (multiple subscriptions supported)
- 📊 Prometheus metrics export with Pushgateway support
- 🌐 REST API with OpenAPI/Swagger documentation
- 🌓 Web interface with dark/light theme
- 🎨 Full web customization (custom logo, styles, or entire template)
- 📄 Public status page for VPN services (no authentication required)
- 📥 Endpoints for monitoring system integration (Uptime Kuma, etc.)
- 🔒 Basic Auth protection for metrics and web interface
- 🐳 Docker and Docker Compose support
- 🌍 Automatic geo files management (geoip.dat, geosite.dat)
- 📝 Flexible configuration loading:
  - URL subscriptions (base64, JSON)
  - Share links (vless://, vmess://, trojan://, ss://)
  - JSON configuration files
  - Folders with configurations

Full list of features available in the [documentation](https://xray-checker.kutovoy.dev/intro/features).

## 🤖 Telegram Bot

Xray Checker includes a built-in Telegram bot for alerts and live subscription management:

### Features
* **Alerting**: Instant notification when a proxy goes offline and when it recovers (with latency measurements). Initial state is seeded silently on startup to prevent alert spam.
* **Interactive Commands** (restricted to allowed chat IDs):
  * `/status` — View online/offline status and latency of all proxies
  * `/subs` — List all active subscriptions (🔒 static from env/flags, ➕ dynamic from bot)
  * `/addsub <URL>` — Validate a new subscription, save it, immediately reload Xray configs and re-check proxies
  * `/delsub <URL>` — Remove a dynamically added subscription and reload
  * `/help` — Display bot commands and help

### Configuration

| Environment Variable | CLI Flag | Default | Description |
|---|---|---|---|
| `TELEGRAM_BOT_TOKEN` | `--telegram-bot-token` | `""` | Telegram bot token from [@BotFather](https://t.me/BotFather) (enables the bot when set) |
| `TELEGRAM_CHAT_IDS` | `--telegram-chat-id` | `""` | Chat ID(s) allowed to use the bot and receive alerts (repeatable flag / comma-separated env) |
| `TELEGRAM_NOTIFY_ON_RECOVERY` | `--telegram-notify-on-recovery` | `true` | Send notification when a proxy comes back online |
| `TELEGRAM_COMMANDS_ENABLED` | `--telegram-commands` | `true` | Enable interactive commands (`/status`, `/help`, etc.) |
| `TELEGRAM_MANAGE_SUBSCRIPTIONS` | `--telegram-manage-subscriptions` | `true` | Allow managing subscriptions via `/addsub`, `/delsub`, `/subs` |
| `SUBSCRIPTION_STORE_PATH` | `--subscription-store-path` | `subscriptions.json` | Path to JSON file where bot-added subscriptions are persisted across restarts |


## 🚀 Quick Start

### 1. Clone repository
```bash
git clone https://github.com/vlv-code/xray-checker-tg-bot.git
cd xray-checker-tg-bot
```

### 2. Configure via `.env`
Copy the template configuration file:
```bash
cp .env.example .env
```
Open `.env` in your editor and fill in your settings:
```bash
nano .env
```
Key settings to configure:
* `SUBSCRIPTION_URL`: Your proxy subscription URL
* `TELEGRAM_BOT_TOKEN`: Token from [@BotFather](https://t.me/BotFather)
* `TELEGRAM_CHAT_IDS`: Your Telegram user ID or group ID
* `SUBSCRIPTION_STORE_PATH`: Path to persist bot-added subscriptions (default `/app/data/subscriptions.json` inside container)

### 3. Launch with Docker Compose
```bash
cp docker-compose.example.yml docker-compose.yml
docker compose up -d --build
```
The dashboard and metrics will be accessible at `http://localhost:2112`.


## 📈 Project Statistics

<a href="https://star-history.com/#kutovoys/xray-checker&Date">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=kutovoys/xray-checker&type=Date&theme=dark" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=kutovoys/xray-checker&type=Date" />
   <img alt="Star History Chart" src="https://api.star-history.com/svg?repos=kutovoys/xray-checker&type=Date" />
 </picture>
</a>

## 🤝 Contributing

We welcome any contributions to Xray Checker! If you want to help:

1. Fork the repository
2. Create a branch for your changes
3. Make and test your changes
4. Create a Pull Request

For more details on how to contribute, read the [contributor's guide](https://xray-checker.kutovoy.dev/contributing/development-guide).

<p align="center">
Thanks to the all contributors who have helped improve Xray Checker:
</p>
<p align="center">
<a href="https://github.com/kutovoys/xray-checker/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=kutovoys/xray-checker" />
</a>
</p>
<p align="center">
  Made with <a rel="noopener noreferrer" target="_blank" href="https://contrib.rocks">contrib.rocks</a>
</p>

## VPN Recommendation

For secure and reliable internet access, we recommend [BlancVPN](https://getblancvpn.com/pricing?promo=klugscl&ref=xc-readme). Use promo code `KLUGSCL` for 15% off your subscription.
