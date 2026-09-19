# Глубокая диагностика на нодах: многоэтапные замеры, мульти-таргеты и полные отчёты

Дата: 2026-09-19  
Статус: утверждённый дизайн (brainstorming)  
Ветка: `main`  

## 1. Проблема и цель

Сейчас удалённые чекер-ноды передают на мастер только плоские метрики (`Online`, `LatencyMs`, `LastErrorMsg`).  
В результате:
- На мастере детальный отчёт (`/diag` -> «Детальный отчёт») отображает глубокую диагностику: DNS-резолв с IP и задержкой, прямой TCP/UDP-пинг порта, TLS-рукопожатие, параллельные HTTP-тесты туннеля через Cloudflare и Google, результаты Check-Host и аналитический вердикт.
- На ноде выводятся скудные данные: лишь одна строка `node-check` с общим пингом и сырой ошибкой, без этапов DNS/TCP/TLS и без мульти-таргетов.

**Цель:** Оснастить чекер-ноды тем же диагностическим конвейером, что и мастер, расширить wire-протокол передачи отчётов `POST /api/v1/nodes/report` и отображать для ноды точно такой же богатый детальный отчёт в Telegram (DNS, TCP/UDP, TLS, Cloudflare 204, Google 204, Check-Host, вердикт).

---

## 2. Архитектура сбора данных на Ноде

### 2.1. Диагностический конвейер `RunDiagnostics` на ноде
В режиме работы ноды (`REPORT_URL` не пустой) вместо базового однопоточного опроса `CheckProxies()` запускается многоэтапный `proxyChecker.RunDiagnostics(targets)`:

1. **Список таргетов (`targets`):**
   - По умолчанию: `https://cp.cloudflare.com/generate_204` и `https://www.gstatic.com/generate_204`.
   - Если в `ConfigSync` с мастера переданы `TargetURLs`, используются они.
2. **Параллельные замеры каждого прокси (`ProbeNodeHealth`):**
   - **DNS-резолв:** IP-адрес сервера и задержка DNS-запроса (`DNSLatency`, `DNSErr`, `ResolvedIP`).
   - **Сетевой транспорт:** 
     - Прямой TCP-пинг порта сервера (`TCPPing`, `TCPErr`) для TCP-протоколов (VLESS, Trojan, Shadowsocks, VMess).
     - Прямой UDP-пинг / датаграмма (`UDPPing`, `UDPErr`) для QUIC-протоколов (Hysteria, Hysteria2, TUIC, WireGuard).
   - **TLS Handshake:** замер времени TLS-рукопожатия (`TLSLatency`, `TLSErr`).
3. **Параллельная проверка туннеля:**
   - Запросы к каждому целевому сервису через локальный SOCKS5-порт прокси (`CheckSingleTarget`).
4. **Формирование вердикта (`DetermineVerdict`):**
   - Автоматический анализ причин сбоя: «Сбой DNS», «Нода не отвечает на TCP (таймаут)», «Сервер сбросил сессию (EOF/UUID)», «Полностью исправен» и т.д.
5. **Интеграция с Check-Host:**
   - Если прокси получил статус `offline` и в `ConfigSync` включён Check-Host (`CheckHostBgEnabled`), нода может запросить статус доступности хоста из РФ и Мира, либо это поле обогащается мастером при получении отчёта.

---

## 3. Сетевой протокол и структуры данных (Wire Protocol)

### 3.1. Расширение `ReportProxy` в `nodes/report.go`
В структуру `ReportProxy` добавляются опциональные поля глубокой диагностики:

```go
type ReportProxy struct {
    StableID          string                     `json:"stableId"`
    Name              string                     `json:"name"`
    SubName           string                     `json:"subName"`
    GroupName         string                     `json:"groupName"`
    Protocol          string                     `json:"protocol"`
    Address           string                     `json:"address"`
    Online            bool                       `json:"online"`
    Disabled          bool                       `json:"disabled"`
    LatencyMs         float64                    `json:"latencyMs"`
    LastCheck         int64                      `json:"lastCheck"`
    LastErrorCategory int                        `json:"lastErrorCategory"`
    LastErrorMsg      string                     `json:"lastErrorMsg"`
    // Новые поля глубокой диагностики:
    NodeHealth        *checker.NodeHealth        `json:"nodeHealth,omitempty"`
    Targets           []checker.TargetDiagResult `json:"targets,omitempty"`
    Verdict           string                     `json:"verdict,omitempty"`
    CheckHost         *checker.CheckHostSummary  `json:"checkHost,omitempty"`
}
```

Все новые поля имеют тэг `omitempty`, что гарантирует 100% обратную совместимость: старые ноды смогут слать старый payload без ошибок валидации на мастере.

### 3.2. Функция сборки отчёта `BuildReportFromDiag`
В пакете `nodes` добавляется функция:
```go
func BuildReportFromDiag(
    reports []checker.ProxyDiagReport,
    version string,
    intervalSec int,
    checkMethod, hostIP string,
) ReportPayload
```
Она транслирует результаты `[]checker.ProxyDiagReport` в `ReportPayload` с сохранением как базовых плоских полей для системы алертов, так и структур глубокой диагностики.

---

## 4. Хранение и агрегация на Мастере

### 4.1. Реестр нод (`nodes/registry.go`)
1. Структура `nodeState` дополняется полем:
   ```go
   type nodeState struct {
       // ...
       diagReports []checker.ProxyDiagReport
   }
   ```
2. В `HandleReport` входящий отчёт ноды парсит новые поля и сохраняет массив `[]checker.ProxyDiagReport` в `st.diagReports`.
3. Добавляется потокобезопасный метод:
   ```go
   func (r *Registry) NodeDiagReports(name string) []checker.ProxyDiagReport
   ```
   возвращающий копию последнего диагностического отчёта ноды.

### 4.2. Адаптер менеджера нод (`node_manager.go` и `telegram/nodes.go`)
В интерфейс `telegram.NodeManager` добавляется:
```go
NodeDiagReports(node string) []checker.ProxyDiagReport
```
Реализованный в `nodeManagerAdapter` через вызов `reg.NodeDiagReports(node)`.

---

## 5. Представление в Telegram (`telegram/diagnostics.go`)

В функции `getNodeDiagnosticsReports(nodeName string)`:
1. Запрашиваем `reports := b.nodeMgr.NodeDiagReports(nodeName)`.
2. Если `reports` не пустой — сразу возвращаем его.
3. Если пустой (например, нода старой версии без глубокой диагностики) — срабатывает фоллбэк со сборкой базового отчёта из `NodeSnapshot`.

Поскольку существующие методы `buildDiagnosticsDetailsRichMessageWithTarget` и `getDiagnosticsPageTextWithTarget` уже содержат весь код отрисовки DNS, TCP, TLS, Cloudflare, Google, Check-Host и вердикта, для удалённой ноды детальный отчёт автоматически сформирует богатую разметку с полным разбором этапов!

---

## 6. План тестирования и верификации

1. **Unit-тесты сериализации/десериализации (`nodes/report_test.go`):**
   - Проверка, что `BuildReportFromDiag` правильно заполняет `ReportPayload` с `NodeHealth`, `Targets`, `Verdict`.
   - Проверка обратной совместимости (десериализация отчёта без новых полей).
2. **Unit-тесты реестра (`nodes/registry_test.go`):**
   - Проверка метода `NodeDiagReports` (получение копии сохранённых отчётов).
3. **Unit-тесты бота (`telegram/diagnostics_node_test.go`):**
   - Проверка, что `getNodeDiagnosticsReports` возвращает полные отчёты со всеми таргетами и этапами здоровья ноды.
   - Проверка рендеринга страницы детального отчёта для ноды со всеми этапами в Rich и HTML режимах.
4. **Сквозная проверка:**
   - `go test ./... -count=1`
   - `go fmt ./...` и `go vet ./...`
5. **Развёртывание и верификация:**
   - Деплой на `bedolaga`, проверка приёма отчётов и отображения в боте.
