---
title: Development Guide
description: Development guide
---

### Setting Up Development Environment

1. Requirements:

   - Go 1.26 or later
   - Git
   - Make (optional, for using Makefile)

2. Clone the repository:

```bash
git clone https://github.com/vlv-code/xray-checker-tg-bot.git
cd xray-checker-tg-bot
```

3. Install dependencies:

```bash
go mod download
```

4. Build the project:

```bash
make build
# or
go build -o xray-checker
```

### Project Structure

```
.
├── asn/           # MaxMind ASN database and lookup logic
├── checker/       # Proxy checking logic and multi-stage diagnostics
├── config/        # Configuration handling and environment variables
├── geo/           # Geo files (geoip.dat, geosite.dat)
├── logger/        # Structured logging
├── metrics/       # Prometheus metrics
├── models/        # Data models
├── nodes/         # Distributed nodes registry, auth, and reporting
├── subscription/  # Subscription parsing and management
├── telegram/      # Telegram bot, commands, callbacks, and alerts
├── web/           # Web interface, API, and assets
├── xray/          # Xray integration and runner
├── go.mod         # Go modules file
└── main.go        # Application entry point
```

### Making Changes

1. Create a new branch:

```bash
git checkout -b feature/your-feature-name
```

2. Make your changes
3. Run tests
4. Update documentation if needed
5. Submit a pull request

### Local Testing

1. Set up test configuration:

```bash
export SUBSCRIPTION_URL=your_test_subscription
```

2. Run in development mode:

```bash
go run main.go
```

3. Run with specific features:

```bash
go run main.go --proxy-check-method=status --metrics-protected=true
```
