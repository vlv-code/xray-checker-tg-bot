# Настройки нод: наследование от мастера, per-node override и разделение владения Check-Host аудитом

Дата: 2026-09-24
Статус: реализовано (ветка feat/nodes-settings-audit). Надстройка над спекой 2026-09-17-nodes-push-design.md
Ветка: `feat/nodes-settings-audit`

## 1. Цель

1. **Двухуровневые настройки**: нода по умолчанию наследует настройки проверок мастера;
   через TG-бота мастер может переопределить любой ключ для конкретной ноды
   индивидуально и вернуть обратно к наследованию.
2. **Паритет слоёв проверок**: инвентаризация показала, что слои 1–5 (базовая
   проверка, health-проба, таргеты, глубокая диагностика, Check-Host обогащение)
   уже выполняются на каждой ноде. Единственный разрыв — фоновый Check-Host аудит
   (слой 6): выполняется только мастером, хотя и по хостам нод тоже.
3. **Разделение владения аудитом**: мастер аудирует только свои хосты; каждая нода —
   свои; мастер поднимает алерты по хостам нод из данных отчётов. Без двойной
   нагрузки на check-host.net.

## 2. Настройки: модель

### Ключи (переопределяемые)

`check_interval`, `check_method`, `ip_check_url`, `status_check_url`,
`download_url`, `proxy_timeout`, `download_timeout`, `download_min_size`,
`check_concurrency`, `subs_update_interval`, `target_urls` (список через запятую).

### Хранение override'ов

`nodes/nodes_store.go` (NodesStore): формат файла меняется с
`map[имя]tokenHash` на `map[имя]nodeRecord{TokenHash, *NodeSettings}` с
прозрачной миграцией legacy-записей (как в URLStore). `NodeSettings` —
типизированные pointer-поля: `nil` = наследовать от мастера.

### Merge на мастере

В `SetConfigSource(nodeName)` (main.go): база — как сегодня (`BotConfig` для
check_interval/target_urls + `CLIConfig.Proxy.*` и
`CLIConfig.Subscription.UpdateInterval` для новых полей), поверх — override'ы
ноды. Чистая функция `ResolveNodeSync(base, overrides)` — тестируется отдельно.
Валидация override'а — при вводе через бота (метод ∈ ip/status/download,
URL http(s), числа > 0; target_urls — непустые http(s)-URL'ы).

### Payload

`NodeConfigSync` дополняется разрешёнными (merged) значениями без omitempty у
новых числовых полей (0 — легитимное значение, напр. concurrency=unlimited):
`CheckMethod, IpCheckURL, StatusCheckURL, DownloadURL string; ProxyTimeoutSec,
DownloadTimeoutSec, DownloadMinSize int64/int; CheckConcurrency,
SubsUpdateIntervalSec int; TargetURLs []string`. При `SyncEnabled` нода
применяет весь набор целиком — семантика «тянутся с мастера».

### Применение на ноде (post-report, под SyncEnabled)

- `ProxyChecker.SetRuntimeCheckSettings(...)` — под мьютексом: валидный метод,
  URL'ы, таймауты, min size, concurrency; пересборка `httpClient` при смене
  proxy_timeout. Чтения `checkMethod`/`checkConcurrency` вне лока переводятся
  под `RLock`.
- `check_interval` → существующий `rescheduleChecks`.
- `subs_update_interval` → новый `rescheduleSubscriptionUpdates` (по образцу
  rescheduleChecks; только если на ноде включён Subscription.Update).
- `target_urls` → `tm.SetTargets(...)` безусловно (теперь список всегда
  разрешён; пустой = встроенные дефолты).

In-memory, самовосстановление первым отчётом после рестарта ноды (без стора).

## 3. Аудит: разделение владения

- **Мастер**: `RunCheckHostAudit` пропускает прокси с `NodeName != ""` —
  аудирует только локальные хосты.
- **Нода**: новый аудитор — по sync-настройкам `CheckHostBgEnabled` /
  `CheckHostIntervalHours` (до первого отчёта — bootstrap из env
  `CHECKHOST_BG_ENABLED`/`CHECKHOST_INTERVAL_HOURS`). Дедуп уникальных хостов
  (TCP/UDP по протоколу прокси), rate-limit 3 сек между вызовами, первый запуск
  +2 мин после старта, пере-расписание при смене интервала по sync.
- **Транспорт**: `ReportPayload.CheckHostAudits map[addr]CheckHostSummary` —
  свежие результаты аудита ноды едут в каждом отчёте. Реестр мастера хранит
  `node → addr → summary` (доступ `NodeAudits(node)`).
- **Алерты мастера по хостам нод**: `Bot.ProcessNodeCheckHostAudits(nodeName,
  audits)` — та же логика, что в `RunCheckHostAudit` (тот же alertKey
  `checkhost:<addr>`, alertTracker, quiet hours → eventBuffer, resolve при
  восстановлении), с атрибуцией ноды в тексте. Вызывается из onUpdate после
  каждого принятого отчёта; идемпотентно за счёт трекера.

## 4. Бот

- `NodeManager` + 3 метода: `NodeSettingsView(node)` (эффективные значения +
  отмеченные override'ы), `SetNodeSetting(node, key, value)`,
  `ResetNodeSettings(node, key)` (key="" — сброс всех).
- Команды: `/nodeset <имя>` — просмотр; `/nodeset <имя> <ключ> <значение>` —
  задать; `/nodereset <имя> [ключ]` — вернуть к наследованию. Секция в меню
  ноды, обновление `/help`.

## 5. Ошибки и ограничения

- Неизвестный ключ/невалидное значение `/nodeset` — отказ со списком ключей.
- Нода с `SyncEnabled=false` живёт своими env (bootstrap-режим).
- Нода лежит → её хосты не аудируются (прокси уже алертятся как упавшие);
  активные Check-Host алерты их хостов остаются висеть до возврата ноды —
  осознанно (не чистим: аудиты вернутся с её отчётом).
- Мастер шлёт полный merged-набор: после первого отчёта env-настройки ноды по
  этим ключам игнорируются до отключения sync.

## 6. Тесты

Store (миграция, set/reset, удаление с нодой), ResolveNodeSync (база/override/
сброс), payload round-trip, SetRuntimeCheckSettings (поля, пересборка
httpClient, невалидный метод, гонки — CI), аудитор ноды (дедуп, UDP, интервал,
state), реестр (NodeAudits), алерты из отчётов (переходы, тишина, resolve,
идемпотентность), скоуп мастера (локальные хосты), команды с мок-менеджером.
Полный регресс.
