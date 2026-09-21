---
title: راهنمای توسعه
description: راهنمای توسعه
---

### راه‌اندازی محیط توسعه

1. پیش‌نیازها:

   - Go ۱.۲۶ یا بالاتر
   - Git
   - Make (اختیاری، برای استفاده از Makefile)

2. کلون کردن مخزن:

```bash
git clone https://github.com/vlv-code/xray-checker-tg-bot.git
cd xray-checker-tg-bot
```

3. نصب وابستگی‌ها:

```bash
go mod download
```

4. ساخت پروژه:

```bash
make build
# یا
go build -o xray-checker
```

### ساختار پروژه

```
.
├── asn/           # پایگاه‌داده MaxMind ASN و منطق جستجو
├── checker/       # منطق بررسی پروکسی و تشخیص چندمرحله‌ای
├── config/        # مدیریت پیکربندی و متغیرهای محیطی
├── geo/           # فایل‌های Geo (geoip.dat, geosite.dat)
├── logger/        # لاگ ساختاریافته
├── metrics/       # متریک‌های Prometheus
├── models/        # مدل‌های داده
├── nodes/         # ثبت نودهای توزیع‌شده، احراز هویت و گزارش‌دهی
├── subscription/  # تجزیه و مدیریت اشتراک
├── telegram/      # ربات تلگرام، دستورات، کال‌بک‌ها و هشدارها
├── web/           # رابط وب، API و فایل‌های استاتیک
├── xray/          # یکپارچه‌سازی و اجراکننده Xray
├── go.mod         # فایل ماژول‌های Go
└── main.go        # نقطه ورود برنامه
```

### ایجاد تغییرات

1. ایجاد branch جدید:

```bash
git checkout -b feature/your-feature-name
```

2. تغییرات خود را ایجاد کنید
3. تست‌ها را اجرا کنید
4. در صورت نیاز مستندات را به‌روز کنید
5. یک pull request ارسال کنید

### تست محلی

1. راه‌اندازی پیکربندی تست:

```bash
export SUBSCRIPTION_URL=your_test_subscription
```

2. اجرا در حالت توسعه:

```bash
go run main.go
```

3. اجرا با ویژگی‌های خاص:

```bash
go run main.go --proxy-check-method=status --metrics-protected=true
```
