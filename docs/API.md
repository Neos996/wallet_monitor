# 接口与回调协议

本文档面向接入方、后端开发和运维人员，说明 `wallet_monitor` 的管理接口、回调协议和当前已落地的排障约束。

## 1. 鉴权与请求跟踪

### 1.1 管理接口鉴权

除 `GET /healthz` 与 `GET /readyz` 外，管理接口可通过 `-admin-token` 开启鉴权。

启用后，请求必须携带以下任一请求头：

- `Authorization: Bearer <ADMIN_TOKEN>`
- `X-Admin-Token: <ADMIN_TOKEN>`

未携带或 token 错误时返回：

- `401 Unauthorized`

### 1.2 请求跟踪头

服务会为每个 HTTP 请求生成并返回：

- `X-Request-ID`

如果调用方已经传入 `X-Request-ID`，服务会原样透传，便于和上游网关、业务系统串联日志。

### 1.3 分页响应头

列表接口支持可选分页。分页场景下会返回：

- `X-Total-Count`
- `X-Limit`
- `X-Offset`

## 2. 通用说明

### 2.1 基础地址

默认监听地址由 `-listen` 指定，例如：

- `http://127.0.0.1:8080`

### 2.2 内容类型

除下载类接口外，请求与响应默认使用：

- `Content-Type: application/json`

### 2.3 主要资源

系统主要管理以下资源：

- `WatchedAddress`：监控地址
- `CallbackTask`：回调任务
- `ProcessedTx`：成功处理过的交易事件

## 3. 健康检查

### 3.1 `GET /healthz`

- 不需要鉴权
- 返回纯文本 `ok`
- 用于 liveness probe

### 3.2 `GET /readyz`

- 不需要鉴权
- 返回 JSON
- 用于 readiness probe

当前检查项：

- 数据库连通性
- 最近一次成功扫描是否超过 `-ready-max-scan-age`
- 死信任务数是否超过 `-ready-max-dead-tasks`

失败时返回：

- `503 Service Unavailable`

响应示例：

```json
{
  "status": "ok",
  "request_id": "req_7b3f8f7a9b71",
  "last_successful_scan_unix": 1710000000,
  "last_successful_scan_age_seconds": 4.2,
  "dead_callback_tasks": 0,
  "checks": [
    {"name": "database", "status": "ok"},
    {"name": "scan_freshness", "status": "ok", "detail": "last successful scan age 4s"},
    {"name": "dead_callbacks", "status": "skipped", "detail": "disabled (current=0)"}
  ]
}
```

## 4. 地址管理接口

### 4.1 查询地址列表

- `GET /addresses`

可选查询参数：

- `chain`
- `network`
- `asset_type`
- `address`
- `enabled=true|false|1|0`
- `limit`
- `offset`

响应：

- `200 OK`
- 返回 `WatchedAddress[]`

说明：

- 不传 `limit` / `offset` 时保持兼容，返回全部结果
- 建议生产排障时显式带分页参数

### 4.2 新增监控地址

- `POST /addresses`

请求示例：

```json
{
  "chain": "tron",
  "network": "mainnet",
  "address": "TE6tpVvcdAn1Sg7fYjkgeaWnabnzzxCdir",
  "asset_type": "native",
  "token_contract": "",
  "callback_url": "https://example.com/callback",
  "min_confirmations": 3,
  "start_height": 0
}
```

字段说明：

| 字段 | 必填 | 说明 |
|---|---|---|
| `chain` | 否 | 默认 `tron` |
| `network` | 否 | 默认 `mainnet`；`mock` 默认 `local` |
| `address` | 是 | 待监控地址 |
| `asset_type` | 否 | `native` / `trc20` / `erc20` |
| `token_contract` | 条件必填 | `trc20` / `erc20` 时必填 |
| `callback_url` | 否 | 单地址回调地址；不传则使用全局 `-callback-url` |
| `min_confirmations` | 否 | 最小确认数 |
| `start_height` | 否 | 初始扫描高度 |

规则说明：

- `chain=evm` 时仅支持 `asset_type=erc20`
- `chain=tron` 且未传 `start_height` 时，系统会把 `last_seen_height` 初始化为当前已确认高度，默认不回填历史
- `chain=evm` 同理，但要求已配置 `-evm-rpc-url`
- 如果配置了 `-callback-url-allowlist`，则 `callback_url` 的 host 必须命中白名单

响应：

- `201 Created`
- 返回创建后的 `WatchedAddress`

### 4.3 查询单个地址

- `GET /addresses/{id}`

### 4.4 更新地址

- `PATCH /addresses/{id}`

请求示例：

```json
{
  "callback_url": "https://example.com/new-callback",
  "enabled": true,
  "min_confirmations": 6,
  "last_seen_height": 80738570
}
```

可更新字段：

- `callback_url`
- `enabled`
- `min_confirmations`
- `last_seen_height`

说明：

- 如果更新了 `callback_url`，同样会校验 `-callback-url-allowlist`

### 4.5 删除地址

- `DELETE /addresses/{id}`

说明：

- 删除后，该地址不再参与后续扫描
- 当前实现会删除该地址下 `pending` / `retrying` / `dead` 的回调任务

### 4.6 启用 / 禁用地址

- `POST /addresses/{id}/enable`
- `POST /addresses/{id}/disable`

## 5. 扫描触发接口

### 5.1 手动触发一轮扫描

- `POST /scan/once`

成功响应：

- `200 OK`
- Header 返回 `X-Scan-ID`
- Body 返回 `ScanResult`

冲突响应：

- `409 Conflict`

冲突含义：

- 当前已有扫描执行中，服务会拒绝新的同进程扫描

示例字段：

- `addresses_scanned`
- `detected_txs`
- `queued_callbacks`
- `callbacks_sent`
- `duplicate_txs`
- `failed_callbacks`
- `dead_callbacks`
- `updated_addresses`
- `scanned_at`

## 6. 回调任务接口

### 6.1 查询任务列表

- `GET /callback-tasks`

可选查询参数：

- `status=pending|retrying|success|dead`
- `chain`
- `address`
- `limit`
- `offset`

### 6.2 查询单个任务

- `GET /callback-tasks/{id}`

### 6.3 手动重试

- `POST /callback-tasks/{id}/retry`：重试单个任务
- `POST /callback-tasks/retry`：批量重试任务

批量重试说明：

- 会把 `retrying` / `dead` 状态任务重新置为可重试状态
- 若当前已有回调分发执行中，返回 `409 Conflict`

### 6.4 导出死信任务

- `GET /callback-tasks/dead/export`

可选参数：

- `format=csv`

默认返回：

- JSON 数组

用途：

- 排障
- 审核
- 死信恢复

## 7. 统计与指标接口

### 7.1 统计信息

- `GET /stats`

返回字段包括：

- `watched_total`
- `watched_enabled`
- `watched_disabled`
- `processed_tx_total`
- `callback_pending`
- `callback_retrying`
- `callback_success`
- `callback_dead`
- `debug_callbacks`

### 7.2 Prometheus 指标

- `GET /metrics`

说明：

- 默认与管理接口共用鉴权
- 详细指标见 `docs/OBSERVABILITY.md`

## 8. 本地联调接口

以下接口用于本地验证，生产环境应通过 token 与网络隔离保护：

- `GET/POST/DELETE /mock/transactions`
- `GET/POST/DELETE /debug/callbacks`

生产建议：

- 配置 `-enable-debug-routes=false` 直接禁用这些调试接口

两个列表接口同样支持可选分页参数：

- `limit`
- `offset`

## 9. 回调协议

### 9.1 请求方式

服务向业务方发起：

- `POST <callback_url>`

### 9.2 Header

固定携带：

- `Content-Type: application/json`
- `X-WalletMonitor-Event-ID: <task_id>`

启用 `-callback-secret` 时额外携带：

- `X-WalletMonitor-Timestamp`
- `X-WalletMonitor-Signature`

### 9.3 Body 示例

```json
{
  "chain": "tron",
  "network": "mainnet",
  "asset_type": "trc20",
  "token_contract": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
  "token_symbol": "USDT",
  "token_decimals": 6,
  "address": "TYo5GrzZGGnzrSMp2eQ8QR352RDTrwrgkQ",
  "tx_hash": "1c8e...",
  "from": "TMx...",
  "to": "TYo...",
  "amount": "12.5",
  "block_height": 80738571
}
```

说明：

- EVM 场景会额外携带 `log_index`
- 同一笔 EVM 交易内多条 `Transfer` 会拆成多次独立回调

### 9.4 业务方要求

业务方必须：

- 返回 `2xx` 表示成功
- 按 `X-WalletMonitor-Event-ID` 做幂等
- 启用签名时验证 `X-WalletMonitor-Signature`

### 9.5 失败处理

- 非 `2xx`、超时、网络错误会进入重试
- 达到最大重试次数后任务进入 `dead`
- 当前实现会记录失败类型、HTTP 状态码和响应体摘要，便于排障

## 10. 常见状态码

| 场景 | 状态码 | 说明 |
|---|---|---|
| 管理接口未鉴权 | `401` | 缺少 token 或 token 错误 |
| 参数错误 | `400` | JSON、ID、分页参数非法 |
| 资源不存在 | `404` | 地址或任务不存在 |
| 扫描 / 回调分发冲突 | `409` | 同进程已有对应执行流在运行 |
| 下游或数据库异常 | `500` | 服务内部错误 |
| 就绪检查失败 | `503` | 数据库、扫描新鲜度或死信阈值不满足 |
