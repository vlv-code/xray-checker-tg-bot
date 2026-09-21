---
title: مرجع API
description: مرجع API Xray Checker
---

## نمای کلی

Xray Checker هم endpointهای HTTP عمومی و هم محافظت شده ارائه می‌دهد. endpointهای محافظت شده وقتی `METRICS_PROTECTED=true` تنظیم شده باشد نیاز به احراز هویت خواهند داشت.

## نقاط پایانی عمومی

نقاط پایانی عمومی نیاز به احراز هویت ندارند یا به‌طور مشروط برای صفحات وضعیت عمومی فعال می‌شوند.

### بررسی سلامت

```http
GET /health
```

endpoint ساده بررسی سلامت. همیشه بدون نیاز به احراز هویت در دسترس است (برای لود بالانسرها).

**پاسخ:** `200 OK` با بدنه `OK`

### وضعیت عمومی پروکسی

```http
GET /api/v1/public/proxies
```

وضعیت پروکسی را بدون داده‌های حساس (بدون IP/پورت سرور) برمی‌گرداند. توسط رابط وب برای به‌روزرسانی خودکار استفاده می‌شود.

:::note[نحوه احراز هویت]
این نقطه پایانی زمانی که `METRICS_PROTECTED=false` یا `WEB_PUBLIC=true` باشد بدون احراز هویت در دسترس است. اگر `METRICS_PROTECTED=true` و `WEB_PUBLIC=false` باشد، دسترسی به این نقطه پایانی نیاز به احراز هویت پایه (Basic Auth) دارد.
:::

**پاسخ:**
```json
{
  "success": true,
  "data": [
    {
      "stableId": "a1b2c3d4e5f67890",
      "name": "US-Server-1",
      "groupName": "",
      "online": true,
      "latencyMs": 150,
      "lastCheck": 1751130000
    }
  ]
}
```

- `groupName`: نام متعادل‌کننده/گروه (برای پروکسی‌های بدون گروه خالی است).
- `lastCheck`: مهر زمانی Unix (ثانیه) آخرین بررسی؛ اگر هنوز بررسی نشده باشد `0` است.

## نقاط پایانی محافظت شده

وقتی `METRICS_PROTECTED=true`، این نقاط پایانی نیاز به احراز هویت پایه دارند.

### رابط وب

```http
GET /
```

داشبورد HTML با نمای کلی وضعیت پروکسی، جستجو، فیلتر، مرتب‌سازی و به‌روزرسانی خودکار.

### متریک‌های Prometheus

```http
GET /metrics
```

نقطه پایانی متریک‌های Prometheus.

**مثال متریک‌ها:**
```text
# HELP xray_proxy_status Status of proxy connection (1: success, 0: failure)
# TYPE xray_proxy_status gauge
xray_proxy_status{protocol="vless",address="example.com:443",name="proxy1",stable_id="a1b2c3d4e5f67890",sub_name="Premium VPN",group_name=""} 1

# HELP xray_proxy_latency_ms Latency of proxy connection in milliseconds
# TYPE xray_proxy_latency_ms gauge
xray_proxy_latency_ms{protocol="vless",address="example.com:443",name="proxy1",stable_id="a1b2c3d4e5f67890",sub_name="Premium VPN",group_name=""} 156
```

### وضعیت پروکسی تکی

```http
GET /config/{stableId}
```

نقطه پایانی وضعیت برای پروکسی فردی، ایده‌آل برای نظارت بر آپتایم.

**پارامترها:**
- `stableId`: شناسه هش پایدار ۱۶ کاراکتری برای پروکسی

**پاسخ:**
- `200 OK` با بدنه `OK` اگر پروکسی کار می‌کند
- `503 Service Unavailable` با بدنه `Failed` اگر پروکسی کار نمی‌کند

:::tip[پیدا کردن Stable ID‌ها]
Stable ID‌ها در URL رابط وب هنگام کلیک روی نام پروکسی قابل مشاهده هستند، یا از طریق نقطه پایانی `/api/v1/proxies`.
:::

### لیست همه پروکسی‌ها

```http
GET /api/v1/proxies
```

اطلاعات کامل همه پروکسی‌ها را برمی‌گرداند.

**پاسخ:**
```json
{
  "success": true,
  "data": [
    {
      "index": 0,
      "stableId": "a1b2c3d4e5f67890",
      "name": "US-Server-1",
      "subName": "Premium VPN",
      "groupName": "",
      "server": "192.168.1.1",
      "port": 443,
      "protocol": "vless",
      "proxyPort": 10000,
      "online": true,
      "latencyMs": 150,
      "lastCheck": 1751130000,
      "metricsLabels": {
        "location": "Netherlands, Amsterdam",
        "hoster": "FreeVDS"
      }
    }
  ]
}
```

- `groupName`: نام متعادل‌کننده/گروه (برای پروکسی‌های بدون گروه خالی است).
- `lastCheck`: مهر زمانی Unix (ثانیه) آخرین بررسی؛ اگر هنوز بررسی نشده باشد `0` است.
- `metricsLabels`: لیبل‌های تعیین‌شده توسط اپراتور از JSON outbound (به [لیبل‌های سفارشی متریک](/fa/configuration/subscription#۹-لیبلهای-سفارشی-متریک) مراجعه کنید)؛ اگر تنظیم نشده باشد حذف می‌شود.

:::note[پیکربندی تولیدشده]
وقتی `WEB_SHOW_DETAILS` فعال باشد (و در حالت عمومی، `WEB_TRUSTED_EXTERNAL_AUTH`)، هر آیتم یک شیء `generatedConfig` نیز شامل می‌شود — یعنی outbound مربوط به Xray که برای آن پروکسی تولید شده است، با مخفی‌سازی اطلاعات حساس (uuid، password، auth، kcp seed). در غیر این صورت حذف می‌شود.
:::

### دریافت پروکسی با ID

```http
GET /api/v1/proxies/{stableId}
```

اطلاعات یک پروکسی خاص را برمی‌گرداند.

**پاسخ:** همان ساختار آیتم تکی از `/api/v1/proxies`

### وضعیت سیستم

```http
GET /api/v1/status
```

آمار خلاصه را برمی‌گرداند.

**پاسخ:**
```json
{
  "success": true,
  "data": {
    "total": 10,
    "online": 8,
    "offline": 2,
    "avgLatencyMs": 200
  }
}
```

### پیکربندی

```http
GET /api/v1/config
```

پیکربندی فعلی checker را برمی‌گرداند.

**پاسخ:**
```json
{
  "success": true,
  "data": {
    "checkInterval": 300,
    "checkMethod": "ip",
    "timeout": 30,
    "startPort": 10000,
    "subscriptionUpdate": true,
    "subscriptionUpdateInterval": 300,
    "simulateLatency": true,
    "subscriptionNames": ["Premium VPN", "Basic VPN"]
  }
}
```

### اطلاعات سیستم

```http
GET /api/v1/system/info
```

اطلاعات نسخه و uptime را برمی‌گرداند.

**پاسخ:**
```json
{
  "success": true,
  "data": {
    "version": "1.0.0",
    "uptime": "1h 30m 45s",
    "uptimeSec": 5445,
    "instance": "prod-1"
  }
}
```

### IP فعلی

```http
GET /api/v1/system/ip
```

آدرس IP فعلی شناسایی شده سرور را برمی‌گرداند.

**پاسخ:**
```json
{
  "success": true,
  "data": {
    "ip": "203.0.113.1"
  }
}
```

### مستندات API

```http
GET /api/v1/docs
```

رابط کاربری Swagger برای مستندات تعاملی API.

```http
GET /api/v1/openapi.yaml
```

فایل مشخصات OpenAPI.

### دریافت گزارش نود (Push Reporting)

```http
POST /api/v1/nodes/report
```

نقطه پایانی سرور اصلی برای دریافت گزارش‌های وضعیت از نودهای راه دور.

**احراز هویت:** `Authorization: Bearer <REPORT_TOKEN>`. این نقطه پایانی اختصاصی نودهاست و احراز هویت Basic Auth را به‌طور کامل دور می‌زند و منحصراً بر اساس توکن Bearer اعتبارسنجی می‌شود.

## احراز هویت

وقتی فعال باشد (`METRICS_PROTECTED=true`)، نقاط پایانی محافظت شده نیاز به احراز هویت پایه (نام کاربری و رمز عبور) دارند:

```bash
curl -u username:password http://localhost:2112/metrics
```

**نکات استثنا:**
- نقطه پایانی `/health` همیشه بدون احراز هویت در دسترس است.
- نقطه پایانی `/api/v1/public/proxies` در صورت `WEB_PUBLIC=true` (یا `METRICS_PROTECTED=false`) بدون احراز هویت باز است.
- نقطه پایانی `/api/v1/nodes/report` منحصراً از طریق توکن Bearer احراز هویت شده و به Basic Auth نیاز ندارد.

## مثال‌های یکپارچه‌سازی

### Uptime Kuma

```bash
# آدرس نظارت (از stableId از رابط وب یا API استفاده کنید)
http://localhost:2112/config/a1b2c3d4e5f67890

# با احراز هویت
http://username:password@localhost:2112/config/a1b2c3d4e5f67890
```

### Prometheus

```yaml
scrape_configs:
  - job_name: "xray-checker"
    metrics_path: "/metrics"
    basic_auth:
      username: "username"
      password: "password"
    static_configs:
      - targets: ["localhost:2112"]
```

## پاسخ‌های خطا

تمام نقاط پایانی API فرمت خطای یکسانی برمی‌گردانند:

```json
{
  "success": false,
  "error": "Error message"
}
```

کدهای وضعیت HTTP:
- `200 OK`: درخواست موفق
- `400 Bad Request`: پارامترهای نامعتبر
- `401 Unauthorized`: احراز هویت مورد نیاز است
- `404 Not Found`: منبع پیدا نشد
- `500 Internal Server Error`: خطای سرور
- `503 Service Unavailable`: بررسی پروکسی ناموفق
