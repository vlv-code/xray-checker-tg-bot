# Weak Spots, Risks, and Technical Debt

Registry of fragile areas, known bugs, and constraints of the repository. Agents MUST consult this registry before modifying listed modules.
Based on core inspection of 2026-11-16.

---

## 1. Fragile Modules and High Coupling
- `main.go` (1146 LOC): monolithic composition root — config, network env, geo, URL store, node registry (master/node modes), gocron schedulers, metrics, web server, Telegram bot wiring, graceful shutdown. Any subsystem signature change ripples here.
- `telegram/views.go` (1327 LOC): formatting monolith for Telegram HTML output. A parse-mode error in any formatter breaks message delivery (bot sends fail silently to `_ =` handlers).
- `telegram/diagnostics.go` (1162 LOC) + `telegram/menu.go` (970 LOC): large interactive surfaces with prefix-routed `callback_data` (64-byte Telegram limit constrains the routing scheme; adding new callback types requires auditing all prefixes).
- `checker/checker.go` (948 LOC): central check pipeline consumed by scheduler, diagnostics, and node mode simultaneously.
- Race-sensitive design: msg-seq invalidation (`invalidateMsgSeq`/`nextMsgSeq`/`isMsgSeqValid`) protects concurrent message edits; CI runs `go test -race` — local changes MUST be verified with `-race` too.
- `nodes/nodes_store.go`: transparent legacy migration of persisted formats (plaintext tokens → hashed, flat map → nodeRecord). Migration code is fragile: never reorder migration steps.

## 2. Environment and Test Constraints
- External runtime dependencies: Telegram Bot API (long polling; token required), subscription panels (http(s)/file/folder/base64 sources), Check-Host.net API (periodic audits), GitHub releases (geo dat downloads at image build; flaky network breaks builds).
- `geo/geoip.dat`, `geo/geosite.dat`: large binaries committed to repo AND downloaded in Dockerfile; local runs use the committed ones, container uses downloaded ones — version drift possible.
- `docs/` contains BOTH `package-lock.json` AND `pnpm-lock.yaml` — dual package-manager state; decide one before touching docs deps.
- CGO_ENABLED=0 build with `xtls/libxray` + `xray-core` pseudo-version pin (`v1.260327.1-0.20260627131803-45cf2898ab12`): upgrading xray-core requires validating the pin resolves and the runner still links.
- Windows dev host (this machine): `.env.example` documents paths pinned to `/app/data` (container); local runs need explicit path overrides or default files land in CWD.
- CI (`.github/workflows/test.yml`) enforces `go vet` + `go test -race -timeout 5m ./...` — parity required locally before push.
- Fixed `subscriptionHWID` constant and User-Agent impersonation (`Happ/1.0`, `Xray-Checker Pro Max`): panels gate response format by client identity; changing these silently changes what panels return.

## 3. Deferred Technical Debt
- Anti-flapping and alert lifecycle state (clean/live modes, quiet hours, digests) persisted via JSON stores — schema evolution needs the same migration discipline as nodes_store.
- Master/node topology: node-side Check-Host audit ownership split (v2.5.0) — report-driven RU alerts; contract between node reporter and master aggregator is implicit (no formal schema).
- Prometheus Pushgateway + Basic Auth for web/metrics: credentials in `.env*`; rotation procedure undocumented.
- `.env.example` is ~19KB of commented knobs — new config flags must stay in sync with `config/config.go`, `.env.example`, `README.md`, `README_RU.md` (4-place sync).

## 4. Deep Audit Findings (2026-11-16, read-only review; top confirmed defects)
Security/leaks:
- `subscription/fetcher.go:86-88` — transport errors return raw `*url.Error` embedding full subscription URL incl. `?token=`; `RedactURL` not applied to error → token leaks to logs and Telegram replies (propagates via subscription.go:136, main.go:611/811/845/1057). VERIFIED.
- `config/proxy_env.go:95-98` — HTTP_PROXY/HTTPS_PROXY/BOOTSTRAP_PROXY logged verbatim at startup; proxy URLs with embedded credentials leak passwords into logs. VERIFIED.
- `subscription/fetcher.go` — no `CheckRedirect` on `subscriptionClient`; redirect targets and DNS-rebind under configured outbound proxy are NOT re-validated (dial-time guard sees only the proxy address). README.md:341-346 claim is misleading for redirects. User headers (`SUBSCRIPTION_HEADERS`) also forwarded cross-host on redirect.
- `telegram/callbacks.go` — mutating callbacks (toggle_proxy, interval, tz, quiet, nodes:delsub, etc.) have NO `isAuthorizedMutating` gate, while equivalent commands do (commands.go:63-77) — admin gate bypass via buttons in multi-user allowed chats. VERIFIED.
- `nodes/registry.go:160` — `RemoveNode` deletes `byToken[st.cfg.Token]` where static (NODES env) nodes store PLAINTEXT token but byToken is keyed by HASH → revocation silently fails (ghost token keeps authenticating; re-register leaves two valid tokens). VERIFIED.
- `nodes/registry.go:229-231` — `HandleReport` derefs `r.nodes[name]` without existence check → nil-pointer panic loop for ghost-token nodes (reachable via the bug above). VERIFIED.
- `nodes/nodes_store.go:103-108` — migration hashes corrupted empty `TokenHash` instead of rejecting → fabricated credential from well-known empty-string hash; migration persist failure silently discarded (plaintext stays at rest, no warning).
- `nodes/nodes.go:28,33` — malformed NODES entry error prints full `name|token` bearer token to logs (startup-only).

Races/correctness:
- `checker/checker.go:836` — bare read of `pc.checkMethod` (writer under pc.mu at :109-114) → data race. VERIFIED. Same class: `pc.httpClient` pointer swap at :133-136 vs reader :232.
- `main.go:276/347, 605/367` — `config.CLIConfig` runtime-mutated ints (CheckInterval, UpdateInterval) written under scheduler mutexes but read unlocked from registry/bot/web goroutines.
- `xray/runner.go:74-83` + `main.go:178-182` — `r.instance` unsynchronized; deferred Stop races live scheduler-driven restart; none of the 4 gocron schedulers is ever stopped at shutdown.
- Zero `recover()` repo-wide — any handler/goroutine panic kills the whole daemon (bot.go:421 update loop, checker fan-out, report goroutines).
- `telegram/menu.go:273` + `callbacks.go:50,183` — button emits `menu:checkhost:run_now` but switch handles `menu:checkhost:run` → dead button, endless spinner, audit never starts. VERIFIED.
- `subscription/subscription.go:133-151` — multi-subscription partial failure drops the failed source's proxies (violates "failed fetch keeps previous proxy set" invariant; total-failure path is fine).
- `main.go:782-786` — `--run-once` node mode exits before fire-and-forget report goroutine delivers (~52s worst case) → report lost.
- `telegram/alerts.go:200-206` — proxies removed while down never resolve their AlertTracker entry → stuck outage messages; :294-308 live mode sends orphan "recovered" after flap-suppressed downs.
- `telegram/callbacks.go:445-535` — synchronous diagnostics run inline in the single update-loop goroutine → all chats freeze for the probe duration.
- `telegram/bot.go:697-709` — delete-then-send menu replacement non-atomic; on send failure menu lost permanently, stale lastMenuMsg.
- No 429/RetryAfter handling anywhere in telegram/; `EventBuffer` unbounded unless morning digest fires.
- `main.go:795,807` — non-positive env intervals (0/negative) silently disable checks/updates (gocron Do() error discarded; runtime paths clamp, env path doesn't). BOM-prefixed subscriptions fail to parse (parser.go:64, TrimSpace doesn't strip U+FEFF).
- Test-suite holes: no negative allowlist test, `escapeHTML` untested at ~30 call sites, share-link/base64-adversarial parsing untested, SSRF redirect/rebind untested, `nodes_store` corrupted-JSON untested, ~2000 LOC of telegram handlers (callbacks/commands/checkhost/alerts/views) untested.
- Docs: 24 of 65 config knobs absent from README.md/README_RU.md tables (env.example + docs/envs.md cover all 65).

### Resolution status (2026-11-16, fix wave — flash-worker delegation, dev-lead verified)
FIXED (RED→GREEN tests + gates green, CI -race pending):
- fetcher token leak (redactURLError + RedactURL userinfo/fallback hardening) — FIXED
- SSRF redirect re-validation + cross-host header stripping + 10-hop limit (residual proxy-side DNS rebinding documented in code) — FIXED
- proxy_env creds logging (redactProxyURL) — FIXED
- NODES token in error text — FIXED
- nodes_store empty-token migration fail-closed + loud persist failure — FIXED
- callbacks admin gate (20 mutating routes gated; empty-admins = all, mirrors commands) — FIXED
- dead `menu:checkhost:run_now` button (case renamed + runAuditNow bypasses bg gate) — FIXED
- registry canonical hex token (ghost-token revocation + HandleReport fail-closed 401 + SetNodesStore collision guard) — FIXED
- checker races (checkMethod/httpClient under RLock) + panic isolation in check fan-out (markPanicResult) — FIXED
- main lifecycle: config.RuntimeMu for runtime-mutated CLIConfig fields; RunOnce synchronous report flush (flushReporterOnce); xray Runner.mu; scheduler defers (shutdown order: sweeps→geo→update→check→bot→auditor→xray→asn→cancel); safeGo panic isolation for 5 main goroutines — FIXED
- bot.go: update-loop recover (handleUpdateSafely), stats-save Warn, send-first menu replacement, ThreadID in MESSAGE_TOO_LONG fallback — FIXED
- partial subscription failure: per-URL last-known-good cache with eviction — FIXED
- BOM-prefixed subscriptions — FIXED; config Validate rejects non-positive intervals — FIXED (5 existing config tests updated to valid intervals — legitimate behavior change, not emasculation)
STILL OPEN (P2/P3, deferred): ~~429/RetryAfter handling~~ ~~unbounded EventBuffer~~ ~~openapi.yaml drift~~ ~~README knob gaps~~ ~~test-suite holes~~ ~~registry shallow copies~~ ~~subs_store fsync~~ ~~reporter substring matching~~ ~~dead port check~~ — ALL FIXED in wave 3 (2026-11-16, 6 flash-workers): 429 retry via callWithRateLimitRetry (telegoapi.Error, cap 10s, ≤2 retries, ctx-aware, no locks held); EventBuffer capped 500 + dropped-counter + daytime-digest drain; escapeHTML → html.EscapeString (attribute-safe, 14 unescaped spots fixed in views/diagnostics/commands/releases/callbacks); allowlist negative tests + escapeHTML table + 52 share-link/base64 adversarial pins + nodes_store corrupted-JSON pins; openapi.yaml synced (+5 paths, +8 schemas, bearerAuth, PyYAML-validated); READMEs 24 knobs each, SSRF/HTTP_PROXY passages rewritten (both languages).
REMAINING (wave 3 leftovers, low priority): ~~synchronous diagnostics block update loop~~ ~~nodes_commands <pre> escape~~ ~~.env.example stale comment~~ ~~alerts quiet-hours 429 routing~~ — FIXED in wave 4 (2026-11-16, 2 flash-workers): menu:diag:details/pick_deep now async with ⏳ placeholder + msg-seq guard (fresh-cache fast path stays synchronous; goroutines under runUpdateSafely); quiet-hours recovery edit/delete collected as params under b.mu and executed after Unlock() via callWithRateLimitRetry (TryLock-probe test proves no lock held during 429 sleep); buildNodeRegisteredText escapes token+reportURL; .env.example MASTER_STALE_TIMEOUT_SEC corrected to +60s (user-approved Red Zone edit; code: StaleGrace=60s, registry.go:18-21).
ONLY REMAINING KNOWN ITEM: rich-mode daytime digest does not drain the event buffer (bounded at 500 anyway — cosmetic); `-race` unrunable on this host (see observations) — CI is the gate.

### Guardian peer review of the fix wave (2026-11-16, duo_peer_review; baseline a191063)

**RESOLVED — P0 BLOCKERS (Guardian re-gate 2026-11-16: both verified fixed; evidence trail preserved):**
- `subscription/fetcher.go:134-135` — the redaction refactor (`client.Do` → `doSubscriptionRequest`) DROPPED the `if err != nil { return nil, err }` guard. On any transport failure (unreachable upstream, DNS failure, dial timeout — routine paths per graceful-degradation North Star) `resp` is nil and `defer resp.Body.Close()` panics. RED evidence reproduced by Guardian (public-IP literal on a closed port → nil-pointer deref, 15s dial timeout then panic). Blast radius: the periodic subscription refetch job (`main.go:898-906`, gocron `updateScheduler`) has NO recover → **whole-process crash**; the bot path is contained by `handleUpdateSafely` but loses the user-facing error reply (subscription silently not added). Masking reason: `fetcher_redact_test.go` exercises `doSubscriptionRequest` directly, never `fetchURLContent` on the failure path — local gates stayed green. Remediation (1 line): restore the error check between `doSubscriptionRequest` and the `defer`; add a `fetchURLContent`-level transport-failure test (RED first).
- `main.go:884-890` vs `main.go:431` — **shutdown deadlock**: the new check-scheduler stop defer holds `checkSchedulerMu` while calling `checkScheduler.Stop()`, and gocron v1.37.0 `Stop()` waits for the running singleton check job (`SingletonMode()`, main.go:873-875), whose closure (`runCheckIteration`) re-acquires `checkSchedulerMu` at main.go:431 → circular wait; Stop never returns, LIFO blocks all later-registered defers (bot Stop, xray runner Stop). Remediation directions: call `Stop()` without holding `checkSchedulerMu`, or drop the `checkSchedulerMu` acquisition at main.go:431 (that read only needs `RuntimeMu.RLock`).

**RESOLVED — P1 (Guardian re-gate 2026-11-16: cloneProxyConfig covers all 6 reference fields — exact match with models.ProxyConfig reference-field set; both boundaries cloned; verified fixed):**
- `subscription/subscription.go:40-46, 182-190` + `xray/utils.go:12,16` + `models/proxy_config.go:215-233` — last-known-good cache **aliases** `*ProxyConfig` pointers with the live checker set; on a partial multi-source failure (2+ sources, one failing, surviving source changed) `PrepareProxyConfigs`/`AssignStableIDs` WRITE `Index`/`StableID` on those shared structs, racing unserialized web/bot readers (web/api.go:100,108). Baseline had no aliasing (stale pointers were dropped). Fix: deep-copy cached configs on merge (`&(*cfg)`), or re-clone before `PrepareProxyConfigs`.

**P3 follow-ups (non-blocking, registered for backlog):**
- `telegram/alerts.go:254,264,301,317,333,354` — several non-quiet recovery edit/delete paths still call the Telegram API without `callWithRateLimitRetry`; a 429 silently drops the edit/delete while the tracker already resolved (pre-existing asymmetry, only partially covered by the wave).
- `checker/checkhost.go:445` (pre-existing, unchanged file) — `res.City`/`res.Country`/`statusText` (= check-host API `res.Error`) interpolated RAW into an HTML-parse-mode message; a `<`/`&` in an API error string breaks delivery (HTTP 400).
- `telegram/bot_robustness_test.go:29-31` — comment overstates the trigger: Telegram sends `*InaccessibleMessage` for inaccessible-message callbacks (handled at callbacks.go:62-64); the typed-nil pin is a defensive hazard, not a wire payload. Pin itself is valuable; also fails if `handleCallbackQuery` gets its own nil-guard (documented, by design).
- `telegram/nodes_commands.go:318` — `e.Key` interpolated raw while `e.Value` is escaped (safe only because keys come from the fixed `nodeSettingKeys` table); cosmetic inconsistency with `/nodereset` reply which escapes the key.
- `subscription/subscription.go:186-189` + `main.go:614-616` — failed-but-cached source records `RecordUpdate` with `LastUpdate=now` (stale-data telemetry slightly misleading).
- `subscription/subscription.go:117-119` — empty URL set early-returns without cache eviction (bounded, harmless).
- Escaping tests: no idempotency test for already-escaped input; no RAW-contract test pinning the `RichTextPlain` detailLines path (opposite escaping contract, currently correct, untested).
- Startup reads of RuntimeMu-guarded fields bypass RuntimeMu (`main.go:724, 873, 898`) — safe today via happens-before, violates the runtime_lock.go convention.
- `nodes/registry.go:149-156` — RegisterNode inserts in-memory before persisting; a `Save` failure leaves an unpersisted node until restart.
- `telegram/bot.go:203-205` — `intervalHandler` written unlocked (safe only by call ordering before `StartCommands`); `AlertTracker.Resolve` disk persist under `b.mu` widened to the removed-proxy path (`telegram/alerts.go:213-220`).
- Legacy migration double-hashes a store value already in plain hex without prefix (`nodes_store.go:112-113`) — fails closed (re-register), acceptable but worth knowing.

**Guardian verdict on the wave items (details in session report):** the four claimed race fixes (checker.go:229-235/843-846, RuntimeMu lock order, xray Runner.mu, registry canonical token) all PASS with exhaustive guard inventories; spots (a)(c)(d)(e), escape migration, emasculation sweep — all PASS; security invariants (SSRF redirect policy, fail-closed token migration, 20-route admin gate, allowlist pins) verified as strengthening; gates reproduced green (build/vet/test 12 pkgs/gofmt). CI `go test -race` remains the race gate. **Overall verdict: FAIL (conditional) — 2×P0 + 1×P1 above must be fixed before release.** Boundary: 8 Red Zone files touched by the wave (`.env.example`, `nodes/nodes.go`, `nodes/nodes_store.go`, `nodes/subs_store.go`, `subscription/fetcher.go`, `subscription/redact_test.go`, `telegram/bot.go`, `web/openapi.yaml`) — all correspond to registry-tracked fixes; require user ratification at commit time.

### Dev-lead blocker resolution (2026-11-16, addendum — Guardian entries above left intact)
- **P0-1 FIXED** (`subscription/fetcher.go`): err-guard restored between `doSubscriptionRequest` and `defer resp.Body.Close()` with an invariant comment at the call site. RED reproduced at the REAL call path (`TestFetchURLContentTransportFailureReturnsErrorNotPanic`, fetcher_callpath_test.go: swapped always-failing transport + public IP literal, no network): nil-pointer deref panic pre-fix → clean redacted error post-fix. Process lesson: a seam extracted from a call site must keep a call-site-level test — seam tests alone masked the dropped guard (see observations.md entry by Guardian).
- **P0-2 FIXED** (`main.go`, Guardian direction (2)): the check job's interval read extracted to package-level `currentCheckInterval()` acquiring ONLY `config.RuntimeMu` (reschedule writers pair on RuntimeMu inside checkSchedulerMu — read stays fully synchronized); `checkSchedulerMu` hoisted to package level for the test pin. Circular wait broken (stop-defer Lock → Stop drains job → job no longer needs the lock). Regression pin `TestCurrentCheckIntervalNeedsOnlyRuntimeMu` (main_lifecycle_test.go): holds checkSchedulerMu, helper must complete within 2s. Also resolves the P3 note about the main.go:431 read bypassing the RuntimeMu convention.
- **P1 FIXED** (`subscription/subscription.go`): `cloneProxyConfig`/`cloneProxyConfigs` deep-copy all 6 reference fields (ALPN, Settings, MetricsLabels, WGAddresses, WGAllowedIPs, WGDNS); applied on BOTH cache store (`rememberLastGoodLocked`) and the failure-merge branch. Cache fully isolated from live-set mutations (Index/StableID/positional). Evidence: compile-RED + aliasing tests (lastgood_clone_test.go) + Guardian's independent reproduction. The incorrect "pointers shared on purpose" comment (root of the bad assumption) is removed.
- Gates after fixes: build / vet / test (12 pkgs) / gofmt — green. State: awaiting Guardian re-gate on the three points, then Steward.
