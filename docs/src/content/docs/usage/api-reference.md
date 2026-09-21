---
title: API Reference
description: API Reference of Xray Checker
---

## Overview

Xray Checker provides both public and protected HTTP endpoints. Protected endpoints require authentication when `METRICS_PROTECTED=true`.

## Public Endpoints

Public endpoints do not require authentication or are conditionally opened for status pages.

### Health Check

```http
GET /health
```

Simple health check endpoint. Always accessible without authentication (for load balancers and orchestrator probes).

**Response:** `200 OK` with body `OK`

### Public Proxy Status

```http
GET /api/v1/public/proxies
```

Returns proxy status without sensitive data (no server IPs/ports). Used by the web UI for auto-refresh.

:::note[Authentication Behavior]
This endpoint is unauthenticated when `METRICS_PROTECTED=false` OR when `WEB_PUBLIC=true`. If `METRICS_PROTECTED=true` and `WEB_PUBLIC=false`, accessing this endpoint requires HTTP Basic Authentication.
:::

**Response:**
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

- `groupName`: balancer/group name (empty for ungrouped proxies).
- `lastCheck`: Unix timestamp (seconds) of the last check; `0` if not checked yet.

## Protected Endpoints

When `METRICS_PROTECTED=true`, these endpoints require Basic Authentication.

### Web Interface

```http
GET /
```

HTML dashboard with proxy status overview, search, filtering, sorting, and auto-refresh.

### Prometheus Metrics

```http
GET /metrics
```

Prometheus metrics endpoint.

**Example metrics:**
```text
# HELP xray_proxy_status Status of proxy connection (1: success, 0: failure)
# TYPE xray_proxy_status gauge
xray_proxy_status{protocol="vless",address="example.com:443",name="proxy1",stable_id="a1b2c3d4e5f67890",sub_name="Premium VPN",group_name=""} 1

# HELP xray_proxy_latency_ms Latency of proxy connection in milliseconds
# TYPE xray_proxy_latency_ms gauge
xray_proxy_latency_ms{protocol="vless",address="example.com:443",name="proxy1",stable_id="a1b2c3d4e5f67890",sub_name="Premium VPN",group_name=""} 156
```

### Individual Proxy Status

```http
GET /config/{stableId}
```

Status endpoint for individual proxy, perfect for uptime monitoring.

**Parameters:**
- `stableId`: 16-character stable identifier hash for the proxy

**Response:**
- `200 OK` with body `OK` if proxy is working
- `503 Service Unavailable` with body `Failed` if proxy is not working

:::tip[Finding Stable IDs]
Stable IDs are visible in the web UI URL when clicking on a proxy name, or via the `/api/v1/proxies` endpoint.
:::

### List All Proxies

```http
GET /api/v1/proxies
```

Returns full information for all proxies.

**Response:**
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

- `groupName`: balancer/group name (empty for ungrouped proxies).
- `lastCheck`: Unix timestamp (seconds) of the last check; `0` if not checked yet.
- `metricsLabels`: operator-defined labels from the outbound JSON (see [Custom Metric Labels](/configuration/subscription#9-custom-metric-labels)); omitted when none are set.

:::note[Generated config]
When `WEB_SHOW_DETAILS` is enabled (and, in public mode, `WEB_TRUSTED_EXTERNAL_AUTH`), each item also includes a `generatedConfig` object — the Xray outbound generated for that proxy, with secrets (uuid, password, auth, kcp seed) masked. It is omitted otherwise.
:::

### Get Proxy by ID

```http
GET /api/v1/proxies/{stableId}
```

Returns information for a specific proxy.

**Response:** Same structure as single item from `/api/v1/proxies`

### System Status

```http
GET /api/v1/status
```

Returns summary statistics.

**Response:**
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

### Configuration

```http
GET /api/v1/config
```

Returns current checker configuration.

**Response:**
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

### System Info

```http
GET /api/v1/system/info
```

Returns version and uptime information.

**Response:**
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

### Current IP

```http
GET /api/v1/system/ip
```

Returns the server's current detected IP address.

**Response:**
```json
{
  "success": true,
  "data": {
    "ip": "203.0.113.1"
  }
}
```

### Remote Nodes Health

```http
GET /api/v1/nodes
```

Returns health status, network metadata (IP, ASN) and check statistics for all configured remote checker nodes.

**Response:**
```json
{
  "success": true,
  "data": [
    {
      "name": "node-spb",
      "up": true,
      "everReported": true,
      "version": "v2.0.1",
      "hostIP": "95.173.136.1",
      "asn": "AS12389 PJSC Rostelecom",
      "online": 8,
      "total": 8,
      "lastReport": "2026-09-17T21:23:17Z",
      "checkIntervalSec": 60
    }
  ]
}
```

### Ingest Node Check Report (Push Reporting)

```http
POST /api/v1/nodes/report
```

Master ingest endpoint for receiving check report snapshots from remote headless checker nodes.

**Authentication:** `Authorization: Bearer <REPORT_TOKEN>`. This endpoint is dedicated to remote node push reports; it completely bypasses Basic Authentication and validates solely the Bearer token against the registered nodes on the master.

**Request Body (JSON):**
```json
{
  "node": "node-spb",
  "version": "v2.0.1",
  "checkIntervalSec": 60,
  "proxies": [
    {
      "name": "SPB-01",
      "address": "1.2.3.4:443",
      "protocol": "vless",
      "stableId": "spb_01",
      "status": 1,
      "latencyMs": 42
    }
  ]
}
```

**Response:** `200 OK` with list of desired managed subscriptions:
```json
{
  "managedSubs": ["https://sub.example.com/spb"]
}
```

### API Documentation

```http
GET /api/v1/docs
```

Swagger UI for interactive API documentation.

```http
GET /api/v1/openapi.yaml
```

OpenAPI specification file.

## Authentication

When enabled (`METRICS_PROTECTED=true`), protected endpoints require Basic Authentication:

```bash
curl -u username:password http://localhost:2112/metrics
```

**Notes on exceptions:**
- `/health` is always accessible without authentication (for load balancer and orchestrator health checks).
- `/api/v1/public/proxies` is open without authentication when `WEB_PUBLIC=true` (or when `METRICS_PROTECTED=false`).
- `/api/v1/nodes/report` authenticates exclusively via `Authorization: Bearer <token>` and bypasses Basic Authentication.

## Integration Examples

### Uptime Kuma

```bash
# Monitor URL (use stableId from web UI or API)
http://localhost:2112/config/a1b2c3d4e5f67890

# With authentication
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

## Error Responses

All API endpoints return consistent error format:

```json
{
  "success": false,
  "error": "Error message"
}
```

HTTP Status Codes:
- `200 OK`: Request successful
- `400 Bad Request`: Invalid parameters
- `401 Unauthorized`: Authentication required
- `404 Not Found`: Resource not found
- `500 Internal Server Error`: Server error
- `503 Service Unavailable`: Proxy check failed
