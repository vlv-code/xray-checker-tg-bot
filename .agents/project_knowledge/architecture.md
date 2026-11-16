# Architecture Slice

High-level architecture of xray-checker-tg-bot based on core inspection of 2026-11-16.
Update via the `sync-architecture` skill when subsystems change.

---

## Stack
`Go 1.26` · `xtls/libxray` + `xray-core` (proxy engine, CGO_ENABLED=0) · `mymmrac/telego` (Telegram Bot API) · `go-co-op/gocron` (scheduling) · `alecthomas/kong` (CLI/env config) · `oschwald/maxminddb-golang` (GeoIP) · `prometheus/client_golang` (metrics) · Docker multi-stage Alpine + UPX + su-exec · GitHub Actions CI

## Subsystems

| Subsystem | Location | Role |
|---|---|---|
| Composition root | `main.go`, `node_manager.go` | Wiring: config, geo files, URL store, node registry (master/node modes), gocron schedulers, metrics, web server, Telegram bot, graceful shutdown. Node mode (`--report.url`) runs per-node audit and reports to master. |
| Subscription ingestion | `subscription/` | Fetch (SSRF-protected, shared http.Client with per-hop redirect re-validation + cross-host header stripping), parse (Base64/JSON/share-links/URI parsers: vless/vmess/trojan/ss/hysteria2/wg/socks), reconcile (add/remove proxies, restart Xray on change), cache (atomic tmp+rename), runtime URL store (persists `/addsub` additions). Per-URL in-memory last-known-good cache substitutes proxies of temporarily failed sources (partial-failure resilience; total failure still handled by main). |
| Check pipeline | `checker/` | Per-proxy checks: IP check, status check, download check via local Xray SOCKS; `target.go` multi-stage endpoint diagnostics (DNS → TCP RTT → TLS → Xray session → targets); `checkhost.go` Check-Host.net API (RU/World split); `error_taxonomy.go` failure classification. |
| Proxy engine | `xray/` | Xray config generation (tag-based routing, no geoip:/geosite: references), runner (libxray lifecycle), geo files manager (optional, graceful degradation). |
| Telegram bot | `telegram/` | Long polling, chat allowlist, interactive menus (`menu.go`, prefix-routed callbacks in `callbacks.go` with msg-seq race protection), multi-stage diagnostics views, alerts lifecycle (`alerts.go`, `alert_tracker.go`: clean/live modes, anti-flapping, quiet hours, digests), stats, runtime subscription management, per-node settings commands (`/nodeset`, `/nodereset`). |
| Node topology | `nodes/` | Registry of reporting nodes (tokens hashed sha256 at rest), per-node settings overrides (inheritance from master), node-side Check-Host auditor, report-driven RU-blocked alerts (reporter). |
| Web dashboard | `web/` | HTTP API + status page (Basic Auth), OpenAPI spec (`openapi.yaml`), static assets/templates, headless mode (`WEB_ENABLED=false`). |
| Observability | `metrics/`, `logger/` | Prometheus collector + `/metrics` endpoint, Pushgateway support; leveled logger with silent mode. |
| Support | `config/`, `models/`, `asn/`, `geo/` | kong CLI+env parsing (4-place sync: config.go ↔ .env.example ↔ README ↔ README_RU), proxy config models, ASN lookup (nop fallback), GeoIP databases. |

## Schedulers & Background Tasks (gocron)
- Proxy check loop — interval runtime-tunable (`/interval`), per-node overrides via `nodes.NodeSettings`.
- Subscription re-fetch (`SUBSCRIPTION_UPDATE_INTERVAL`, default 300s) → reconcile → Xray restart with updated config; failed fetch keeps previous proxy set.
- Check-Host background audit (master + node-side) → RU-blocked alerts with dedup.
- Alert lifecycle: anti-flapping, quiet night hours, morning digest, daytime summaries.
- All intervals validated positive at startup (`config.Validate()`); runtime mutation paths clamp ≥10s.

## Concurrency & Panic-Isolation Contracts (fix wave 2026-11-16, Guardian-verified)
- `config.RuntimeMu` (`config/runtime_lock.go`) guards runtime-mutated `CLIConfig` fields (CheckInterval, UpdateInterval). **Lock order invariant: RuntimeMu is always acquired INSIDE scheduler mutexes** (`checkSchedulerMu`, `updateSchedulerMu`), never the reverse.
- Panic isolation: `safeGo` (main.go) for long-lived goroutines (node report push, initial check iteration, background subscription retry, http server, telegram bot retry); `runUpdateSafely`/`handleUpdateSafely` (telegram/bot.go) wrap update dispatch; `markPanicResult` isolates checker fan-out. Former P0-2 shutdown deadlock (stop-defer held `checkSchedulerMu` while gocron `Stop()` drained a check job that re-acquired it) is FIXED: the job's interval read extracted to package-level `currentCheckInterval()` acquiring only RuntimeMu, regression-pinned by `TestCurrentCheckIntervalNeedsOnlyRuntimeMu` (main_lifecycle_test.go). Remaining known gap (P3 backlog): periodic `updateScheduler`/`geoScheduler` job closures (main.go:690, 908, 933) are not panic-isolated — fetch/parse paths now return errors instead of panicking (P0-1 guard), but an unexpected panic in those closures would still crash the process.
- Telegram 429 handling: `callWithRateLimitRetry` (telegoapi.Error, cap 10s, ≤2 retries, ctx-aware) — bot mutexes are never held during retry sleeps (TryLock-probe pinned by test).

## Persistence (all JSON, atomic tmp+rename, mutex-guarded, under `/app/data`)
- `subscriptions.json` (URLStore: static + runtime-added subscription URLs)
- `proxy_cache.json` (cold-start cache when upstream unreachable)
- node registrations (hashed tokens) + per-node settings overrides, node subs, stats/incident log, alert tracker state

## End-to-End Data Flows (Pre-Flight Flow Tracing)

### 1. Proxy check flow
`[Origin: gocron tick] ➔ [checker/ pipeline: IP/status/download via xray SOCKS] ➔ [metrics/ Prometheus + nodes/ report] ➔ [Consumer: /metrics scrape, Grafana, Telegram alerts]`

### 2. Subscription sync flow
`[Origin: scheduler / manual /addsub] ➔ [subscription/fetcher (SSRF guard) ➔ parser ➔ reconcile] ➔ [xray/ config regen + runner restart; cache + URLStore persisted atomically] ➔ [Consumer: proxy check pipeline]`

### 3. Telegram interaction flow
`[Origin: user tap/command] ➔ [telego long polling ➔ callbacks.go prefix router (64-byte callback_data, msg-seq guard, chat allowlist)] ➔ [Domain: checker/nodes/subscription] ➔ [views.go HTML formatting] ➔ [Consumer: Telegram chat message edits/toasts]`

### 4. Check-Host audit flow (master/node split)
`[Origin: scheduler outage trigger / node auditor] ➔ [checker/checkhost.go API (RU vs World nodes)] ➔ [nodes/reporter → master registry] ➔ [Consumer: RU-blocked Telegram alerts with dedup]`

### 5. Deployment flow
`[Origin: git push] ➔ [GitHub Actions: test.yml (vet+race) → build-publish/docker-ghcr/release] ➔ [Docker multi-stage build: UPX binary, geo dat download, su-exec privilege drop] ➔ [Consumer: docker-compose master + node instances]`

### 6. Cheburcheck active probe flow (2026-11-16, active-only — no scheduler, no passive registry data)
`[Origin: manual /cheburcheck command | menu:chebur:<stableID> button | deep-diagnostics offline enrichment] ➔ [checker/cheburcheck.go: GET /api/v1/check (id-only decode, passive fields dropped) → per-scanner SSE /probe/{id}?token=<scanner-token> (no API key)] ➔ [aggregate verdicts: ok/uncertain/cdn_block/sni_block/dns_spoofing/tspu_block/whitelist] ➔ [Consumer: Telegram report (FormatCheburcheckReport, escaped) + ProxyDiagReport.Chebur + EnrichVerdictWithChebur]`
- Scanners hardcoded in code (`DefaultCheburScanners`, EMPTY by default — public repo; tokens pasted at deploy); empty list ⇒ feature fully inactive (ErrCheburInactive, graceful no-op everywhere).
- Shares the checker package token-bucket limiter (1 rps, burst 2); singleflight per bare host in RunDiagnostics (`chebCache`/`chebMu`); failures degrade to warn+skip, never break diagnostics.
