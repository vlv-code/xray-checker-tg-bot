# Xray Checker

[![GitHub Release](https://img.shields.io/github/v/release/vlv-code/xray-checker-tg-bot?style=flat&color=blue)](https://github.com/vlv-code/xray-checker-tg-bot/releases/latest)
[![DockerHub](https://img.shields.io/badge/Docker-ready-blue)](https://github.com/vlv-code/xray-checker-tg-bot)
[![License](https://img.shields.io/badge/License-GPL%20v3-green)](https://github.com/vlv-code/xray-checker-tg-bot/blob/main/LICENSE)
[![ru](https://img.shields.io/badge/lang-ru-blue)](README_RU.md)
[![en](https://img.shields.io/badge/lang-en-red)](README.md)

> [!NOTE]
> **Данный репозиторий является форком проекта [kutovoys/xray-checker](https://github.com/kutovoys/xray-checker)** с добавлением полнофункционального Telegram-бота на официальной библиотеке `mymmrac/telego`:
> - ⚡ **Многоуровневая пошаговая диагностика**: раздельный анализ DNS, TCP ping, TLS handshake, Xray туннеля и целевых сервисов.
> - 🌐 **Внешняя валидация через Check-Host.net**: автоматическая проверка доступности из РФ (Москва, СПб) и мира при сбоях + целенаправленная команда `/checkhost`.
> - ⏱️ **Динамический интервал проверок**: смена интервала на лету (`/interval` или меню) без перезапуска контейнера.
> - 🔔 **Умный жизненный цикл алертов**: Live-режим (редактирование сообщения с указанием даунтайма) и Clean-режим (автоочистка).
> - 🌙 **Тихие ночные часы и сводки**: глушение звуков ночью, утренняя и периодические дневные сводки, паузы (snooze).
> - 📈 **Постоянная статистика аптайма**: журнал инцидентов и учет надежности (`/stats`).
> - 📋 **Динамическое управление подписками**: добавление и удаление подписок на лету (`/subs`, `/addsub`, `/delsub`).
> - 📦 **Headless-режим**: работа без веб-панели (`WEB_ENABLED=false`).

---

## 🚀 Основные возможности

* 🔍 **Мониторинг Xray-прокси**: поддержка VLESS (включая Reality и Vision), VMess, Trojan, Shadowsocks, WireGuard и Hysteria2.
* 🤖 **Telegram-бот на `mymmrac/telego`**: интерактивное меню, кнопки, мгновенные уведомления о сбоях и подъемах.
* ⚡ **Многоуровневая пошаговая диагностика (`/diag`)**: определение точного этапа сбоя (DNS, TCP, TLS, сессия Xray, бан на сайте).
* 🌐 **Интеграция с Check-Host.net**: автоматическая внешняя проверка доступности из России и других стран при авариях.
* ⏱️ **Настройка интервала на лету**: команда `/interval <секунды>` и инлайн-меню пресетов.
* 🔄 **Автообновление подписок**: поддержка нескольких URL подписок (Base64, JSON, прямые ссылки) с автоперезагрузкой.
* 📊 **Метрики Prometheus**: экспорт метрик `/metrics` для Grafana и поддержка Prometheus Pushgateway.
* 🌐 **Веб-интерфейс**: панель мониторинга с светлой/темной темой и публичной страницей статуса.
* 🔒 **Безопасность**: защита метрик и дашборда с помощью Basic Auth.
* 🐳 **Docker & Docker Compose**: запуск в одну команду и минимальное потребление ресурсов.

---

## 🤖 Команды Telegram-бота

Все функции доступны через интерактивное меню **`/menu`** или прямыми командами в чате:

| Команда | Описание |
|---|---|
| `/menu` или `/start` | Главное интерактивное меню со всеми разделами и кнопками |
| `/status` | Текущий статус всех настроенных прокси (онлайн/оффлайн, задержка) |
| `/diag` | Детальная экспресс-диагностика всех нод (DNS, TCP RTT, TLS, сайты + Check-Host) |
| `/checkhost <хост[:порт]>` | Глобальная проверка любого хоста/IP по всей сети узлов Check-Host.net |
| `/interval [секунды]` | Просмотр или изменение интервала проверок на лету (например, `/interval 60`) |
| `/stats` | Статистика аптайма (%), журнал последних падений и топ проблемных нод |
| `/quiet` | Меню тихого ночного режима и кнопок паузы (1ч, 4ч, до утра) |
| `/targets` | Просмотр списка целевых сайтов проверки доступности |
| `/subs` | Список активных подписок (из конфига и добавленных через бота) |
| `/addsub <URL>` | Добавить новую подписку на лету без перезапуска |
| `/delsub <URL>` | Удалить ранее добавленную подписку |
| `/digest` | Принудительно отправить текущую сводку в чат |
| `/help` | Краткая справка по всем командам |

---

## ⚡ Диагностика и проверка доступности

### 1. Многоуровневая диагностика (`/diag`)
Параллельно тестирует каждый узел по 5 независимым ступеням и выводит нейтральный факт-вердикт:
* **DNS Lookup**: резолв IP и задержка DNS-запроса.
* **TCP Ping (RTT)**: прямое измерение сетевого пинга до `IP:Port` ноды (выявляет таймауты и закрытые порты).
* **TLS Handshake Probe**: проверка рукопожатия TLS и SNI (выявляет блокировки по имени сервера).
* **Xray Tunnel & Targets**: пропуск трафика через туннель к целевым сайтам (Cloudflare, Google и др.).
* **Check-Host Fallback**: если нода не отвечает локально, бот автоматически проверяет доступность порта из РФ (Москва, СПб) и мира (Германия, США и др.).

```text
⚡ Результаты детальной диагностики (2 прокси):

🟢 NL-Amsterdam (VLESS)
  • DNS: ✅ 185.120.45.10 (18 ms)
  • TCP (443): ✅ 42 ms
  • TLS: ✅ 58 ms
  • Cloudflare 204: ✅ 65 ms
  • Google 204: ✅ 71 ms
  💡 Вердикт: Полностью исправен

🔴 DE-Frankfurt (VLESS)
  • DNS: ✅ 45.132.18.2 (20 ms)
  • TCP (443): ❌ Нода не отвечает на TCP (таймаут: хост недоступен с сервера чекера)
  • Check-Host (TCP): РФ ❌ недоступен, Мир ✅ отвечает
    🔗 отчет
  💡 Вердикт: Нода не отвечает на TCP (таймаут) | Check-Host: хост недоступен из узлов РФ, но отвечает из зарубежных сетей
```

### 2. Целенаправленный глобальный аудит (`/checkhost`)
Команда `/checkhost <хост[:порт]>` или кнопка `[ 🌐 Check-Host ]` в `/menu` опрашивает узлы по всему миру:
```text
🌐 Результаты Check-Host для 185.120.45.10:443 (TCP):

🇷🇺 Россия:
  • Moscow (RU): ❌ Connection timed out
  • Saint Petersburg (RU): ❌ Connection timed out

🇪🇺 Европа:
  • Nuremberg (DE): ✅ 18 ms
  • Amsterdam (NL): ✅ 22 ms
  • Helsinki (FI): ✅ 26 ms

🇺🇸 Америка:
  • Los Angeles (US): ✅ 115 ms
  • New York (US): ✅ 95 ms

💡 Вывод: Хост недоступен из узлов РФ, но отвечает из зарубежных сетей (Европа/США)
🔗 Постоянная ссылка на отчет
```

---

## 🛠️ Быстрый старт

### 1. Клонирование репозитория
```bash
git clone https://github.com/vlv-code/xray-checker-tg-bot.git
cd xray-checker-tg-bot
```

### 2. Настройка конфигурации
```bash
cp .env.example .env
nano .env
```
Заполните обязательные параметры:
* `SUBSCRIPTION_URL` — ссылка на подписку с вашими прокси.
* `TELEGRAM_BOT_TOKEN` — токен бота от [@BotFather](https://t.me/BotFather).
* `TELEGRAM_CHAT_IDS` — ваш Telegram ID или ID группы для уведомлений.

### 3. Запуск через Docker Compose
```bash
cp docker-compose.example.yml docker-compose.yml
docker compose up -d --build
```
Веб-панель и метрики станут доступны по адресу: `http://<IP_сервера>:2112`.

---

## 🔄 Обновление на сервере

Чтобы обновить чекер до последней версии с сохранением всех данных:
```bash
cd /opt/remnawave-bedolaga-telegram-bot   # или директория вашего проекта
git pull origin main
docker compose up -d --build
```

---

## ⚙️ Основные переменные окружения (`.env`)

| Переменная | По умолчанию | Описание |
|---|---|---|
| `SUBSCRIPTION_URL` | `""` | Ссылка на подписку (Base64/JSON URL, share-ссылки) |
| `SUBSCRIPTION_STORE_PATH` | `/app/data/subscriptions.json` | Файл сохранения подписок, добавленных через `/addsub` |
| `TELEGRAM_BOT_TOKEN` | `""` | Токен Telegram-бота от @BotFather (включает бота) |
| `TELEGRAM_CHAT_IDS` | `""` | Список Chat ID через запятую, допущенных к боту |
| `TELEGRAM_ALERT_MODE` | `live` | Режим алертов: `live` (обновление сообщения) или `clean` (автоочистка) |
| `TELEGRAM_RICH_MODE`  | `false` | Использовать Telegram Bot API 10.1 Rich-формат (таблица + спойлеры деталей) |
| `TELEGRAM_QUIET_HOURS_ENABLED`| `true` | Включить тихий ночной режим (без звуковых алертов) |
| `TELEGRAM_QUIET_HOURS_START`  | `23:00` | Начало тихого режима (HH:MM) |
| `TELEGRAM_QUIET_HOURS_END`    | `08:00` | Окончание тихого режима (HH:MM) и утренняя сводка |
| `TELEGRAM_DAY_DIGEST_ENABLED` | `true` | Периодические дневные сводки |
| `STATS_STORE_PATH` | `/app/data/stats.json` | Файл сохранения истории инцидентов и аптайма |
| `BOT_CONFIG_STORE_PATH` | `/app/data/bot_config.json` | Файл сохранения настроек бота (интервал и тихий режим) |
| `PROXY_CHECK_INTERVAL` | `300` | Интервал проверок в секундах (настраивается также через `/interval`) |
| `PROXY_TARGET_URLS` | Cloudflare/Google 204 | Резервные целевые сервисы для проверки прокси |
| `WEB_ENABLED` | `true` | Включить веб-панель (установите `false` для headless-режима) |
| `METRICS_PORT` | `2112` | Порт дашборда и Prometheus-метрик |
| `METRICS_PROTECTED` | `true` | Включить Basic Auth защиту веб-панели |
| `METRICS_USERNAME` | `admin` | Логин для веб-панели |
| `METRICS_PASSWORD` | — | Пароль для веб-панели |
| `HTTP_PROXY` / `ALL_PROXY` | `""` | Исходящий прокси (`socks5://...` или `http://...`) для Telegram и API |

### Работа через исходящий прокси (для серверов в РФ)
Если Telegram API заблокирован на сервере (например, на российских VPS), укажите SOCKS5/HTTP-прокси в `.env`:
```env
HTTP_PROXY=socks5://xray-client:1080
HTTPS_PROXY=socks5://xray-client:1080
ALL_PROXY=socks5://xray-client:1080
NO_PROXY=localhost,127.0.0.1
```
Все исходящие запросы бота к Telegram и Check-Host будут безопасно маршрутизироваться через прокси.

### Headless-режим (без веб-панели)
Если веб-интерфейс не нужен (нужны только Telegram-бот и метрики):
Укажите в `.env`:
```env
WEB_ENABLED=false
```
Метрики `/metrics` продолжат работать, а веб-дашборд будет отключен для экономии памяти.
