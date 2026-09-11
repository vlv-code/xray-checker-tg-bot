# Xray Checker

[![GitHub Release](https://img.shields.io/github/v/release/kutovoys/xray-checker?style=flat&color=blue)](https://github.com/kutovoys/xray-checker/releases/latest)
[![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/kutovoys/xray-checker/build-publish.yml)](https://github.com/kutovoys/xray-checker/actions/workflows/build-publish.yml)
[![DockerHub](https://img.shields.io/badge/DockerHub-kutovoys%2Fxray--checker-blue)](https://hub.docker.com/r/kutovoys/xray-checker/)
[![Documentation](https://img.shields.io/badge/docs-xray--checker.kutovoy.dev-blue)](https://xray-checker.kutovoy.dev/)
[![Live Demo](https://img.shields.io/badge/demo-live-brightgreen)](https://demo-xray-checker.kutovoy.dev/)
[![Telegram Chat](https://img.shields.io/badge/Telegram-Chat-blue?logo=telegram)](https://t.me/+uZCGx_FRY0tiOGIy)
[![GitHub License](https://img.shields.io/github/license/kutovoys/xray-checker?color=greeen)](https://github.com/kutovoys/xray-checker/blob/main/LICENSE)
[![ru](https://img.shields.io/badge/lang-ru-blue)](https://github.com/kutovoys/xray-checker/blob/main/README_RU.md)
[![en](https://img.shields.io/badge/lang-en-red)](https://github.com/kutovoys/xray-checker/blob/main/README.md)

> [!NOTE]
> **Данный репозиторий является форком оригинального проекта [kutovoys/xray-checker](https://github.com/kutovoys/xray-checker)** с добавлением интеграции с Telegram-ботом (мгновенные оповещения об отвале и восстановлении, команды `/status` и `/help`, динамическое управление подписками `/subs`, `/addsub`, `/delsub`), поддержкой headless-режима (`WEB_ENABLED`) и удобного управления через `.env`.

Xray Checker - это инструмент для мониторинга доступности прокси-серверов с поддержкой протоколов VLESS, VMess, Trojan и Shadowsocks. Он автоматически тестирует соединения через Xray Core и предоставляет метрики для Prometheus, а также API-эндпоинты для интеграции с системами мониторинга.

<div align="center">
  <img src=".github/screen/xray-checker.webp" alt="Dashboard Screenshot">
</div>

> [!TIP]
> **Попробуйте демо:** Посмотрите Xray Checker в действии на [demo-xray-checker.kutovoy.dev](https://demo-xray-checker.kutovoy.dev/)

## 🚀 Основные возможности

- 🔍 Мониторинг работоспособности Xray-прокси серверов (VLESS, VMess, Trojan, Shadowsocks)
- 🤖 Интегрированный Telegram-бот: оповещения об отвале и восстановлении в реальном времени, /status и управление подписками (/subs, /addsub, /delsub)
- 🔄 Автоматическое обновление конфигурации из подписки (поддержка нескольких подписок)
- 📊 Экспорт метрик в формате Prometheus с поддержкой Pushgateway
- 🌐 REST API с документацией OpenAPI/Swagger
- 🌓 Веб-интерфейс с темной/светлой темой
- 🎨 Полная кастомизация веб-интерфейса (свой логотип, стили или весь шаблон)
- 📄 Публичная страница статуса для VPN-сервисов (без аутентификации)
- 📥 Эндпоинты для интеграции с системами мониторинга (Uptime Kuma и др.)
- 🔒 Защита метрик и веб-интерфейса с помощью Basic Auth
- 🐳 Поддержка Docker и Docker Compose
- 🌍 Автоматическое управление geo-файлами (geoip.dat, geosite.dat)
- 📝 Гибкая загрузка конфигурации:
  - URL-подписки (base64, JSON)
  - Share-ссылки (vless://, vmess://, trojan://, ss://)
  - JSON-файлы конфигурации
  - Папки с конфигурациями

Полный список возможностей доступен в [документации](https://xray-checker.kutovoy.dev/ru/intro/features).

## 🤖 Telegram-бот

Xray Checker включает встроенного Telegram-бота для оперативных оповещений и управления подписками на лету:

### Возможности
* **Оповещения в реальном времени**: Мгновенные уведомления об отвале прокси и о восстановлении (с замером пинга/задержки). При старте текущее состояние фиксируется тихо без спама алертами.
* **Интерактивные команды** (доступны только разрешённым `chat_id`):
  * `/status` — Текущий статус и задержка всех проверяемых прокси
  * `/subs` — Список активных подписок (🔒 статические из env/флагов, ➕ добавленные через бота)
  * `/addsub <URL>` — Проверить подписку на работоспособность, сохранить, применить новую конфигурацию Xray и перезапустить проверки
  * `/delsub <URL>` — Удалить динамически добавленную подписку и обновить конфигурацию
  * `/help` — Справка по командам бота

### Параметры конфигурации

| Переменная окружения | Флаг CLI | По умолчанию | Описание |
|---|---|---|---|
| `TELEGRAM_BOT_TOKEN` | `--telegram-bot-token` | `""` | Токен Telegram-бота от [@BotFather](https://t.me/BotFather) (включает бота) |
| `TELEGRAM_CHAT_IDS` | `--telegram-chat-id` | `""` | Список Chat ID, которым разрешено взаимодействовать с ботом и получать алерты (через запятую в env или повторением флага) |
| `TELEGRAM_NOTIFY_ON_RECOVERY` | `--telegram-notify-on-recovery` | `true` | Присылать уведомление, когда прокси снова поднимается в онлайн |
| `TELEGRAM_COMMANDS_ENABLED` | `--telegram-commands` | `true` | Включить обработку команд (`/status`, `/help` и др.) |
| `TELEGRAM_MANAGE_SUBSCRIPTIONS` | `--telegram-manage-subscriptions` | `true` | Разрешить управление подписками через `/addsub`, `/delsub`, `/subs` |
| `SUBSCRIPTION_STORE_PATH` | `--subscription-store-path` | `subscriptions.json` | Путь к файлу сохранения добавленных через бота подписок (для переживания рестартов) |
| `WEB_ENABLED` | `--web-enabled` | `true` | Включить веб-панель (дашборд). Значение `false` отключает веб-интерфейс |

### Работа без веб-панели (Headless режим)

Если вам нужны только метрики Prometheus или оповещения в Telegram без веб-интерфейса:
* Установите `WEB_ENABLED=false` в `.env` или передайте флаг `--web-enabled=false`. Эндпоинты `/metrics` и `/health` продолжают работать.
* Чтобы полностью отключить HTTP-сервер (только Telegram-бот и проверки прокси), установите `METRICS_PORT=0`.
* Чтобы собрать Docker-образ без веб-панели по умолчанию:
  ```bash
  docker build --build-arg WEB_ENABLED=false -t xray-checker:headless .
  ```

## 🚀 Быстрый старт
 
### 1. Клонируйте репозиторий
```bash
git clone https://github.com/vlv-code/xray-checker-tg-bot.git
cd xray-checker-tg-bot
```

### 2. Настройте конфигурацию в `.env`
Скопируйте шаблон файла настроек:
```bash
cp .env.example .env
```
Откройте `.env` в любом редакторе и укажите свои параметры:
```bash
nano .env
```
Основные параметры для старта:
* `SUBSCRIPTION_URL`: Ссылка на вашу подписку с прокси
* `TELEGRAM_BOT_TOKEN`: Токен бота от [@BotFather](https://t.me/BotFather)
* `TELEGRAM_CHAT_IDS`: Ваш Telegram ID или ID группы для алертов и команд
* `SUBSCRIPTION_STORE_PATH`: Путь для сохранения подписок бота (по умолчанию `/app/data/subscriptions.json` в контейнере)
* `WEB_ENABLED`: `true` (по умолчанию) или `false` для работы без веб-панели

### 3. Запуск через Docker Compose
```bash
cp docker-compose.example.yml docker-compose.yml
docker compose up -d --build
```
Веб-интерфейс и метрики станут доступны по адресу `http://<IP_сервера>:2112` (или только `/metrics` при `WEB_ENABLED=false`).

## 🤝 Участие в разработке

Мы рады любому вкладу в развитие Xray Checker! Если вы хотите помочь:

1. Сделайте форк репозитория
2. Создайте ветку для ваших изменений
3. Внесите изменения и протестируйте их
4. Создайте Pull Request

Подробнее о том, как внести свой вклад, читайте в [руководстве для контрибьюторов](https://xray-checker.kutovoy.dev/ru/contributing/development-guide).

<p align="center">
Спасибо всем контрибьюторам, которые помогли улучшить Xray Checker:
</p>
<p align="center">
<a href="https://github.com/kutovoys/xray-checker/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=kutovoys/xray-checker" />
</a>
</p>
<p align="center">
  Сделано с помощью <a rel="noopener noreferrer" target="_blank" href="https://contrib.rocks">contrib.rocks</a>
</p>

---

## Рекомендация VPN

Для безопасного и надежного доступа в интернет мы рекомендуем [bye-bye-home](https://cabinet.bbhome.xyz/). Используйте промокод `PIPISKA1337` на 14 дней бесплатно.
