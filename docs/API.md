# 接口与回调协议说明

本文档面向业务接入方与运维人员，描述 `wallet_monitor` 的管理 API 与回调协议。

## 1. 鉴权（管理接口）

除 `/healthz` 外，所有管理接口均可通过 `-admin-token` 启用鉴权。

启用后需要携带以下之一：

- `Authorization: Bearer <ADMIN_TOKEN>`
- `X-Admin-Token: <ADMIN_TOKEN>`

未携带或错误会返回 `401 Unauthorized`。

## 2. 健康检查

`GET /healthz`

返回 `200 ok`（文本）。

## 3. 地址管理

### 3.1 查询地址

`GET /addresses`

可选查询参数：

- `chain`
- `network`
- `asset_type`
- `address`
- `enabled`（`true|false` 或 `1|0`）

响应为 `WatchedAddress` 数组。

### 3.2 新增地址

`POST /addresses`

请求体（JSON）：

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

说明：

- `chain` 默认 `tron`；`network` 默认 `mainnet`（`mock` 则默认 `local`）。
- `asset_type` 支持 `native` / `trc20`。
- `token_contract` 仅 `trc20` 必填。
- `min_confirmations` 用于确认数判断（只回调达到确认数的交易）。
- `start_height` 可选：控制 `last_seen_height` 初始值。  
  `chain=tron` 且不传时，会自动设置为“当前已确认高度”（基于 `min_confirmations` 的 confirmed cutoff），避免历史回填。

响应：`201 Created`，返回创建后的 `WatchedAddress`。

### 3.3 查询单个地址

`GET /addresses/{id}`

### 3.4 更新地址

`PATCH /addresses/{id}`

请求体（JSON，可选字段）：

```json
{
  "callback_url": "https://example.com/new-callback",
  "enabled": true,
  "min_confirmations": 6,
  "last_seen_height": 80738570
}
```

说明：

- `last_seen_height` 常用于回补或回滚扫描高度。

### 3.5 删除地址

`DELETE /addresses/{id}`

### 3.6 启停地址

- `POST /addresses/{id}/enable`
- `POST /addresses/{id}/disable`

## 4. 扫描触发

`POST /scan/once`

返回 `ScanResult`，包含扫描统计与回调处理结果。

## 5. 回调任务队列（CallbackTask）

### 5.1 查询任务

`GET /callback-tasks`

可选查询参数：

- `status`：`pending|retrying|success|dead`
- `chain`
- `address`

### 5.2 查询单条任务

`GET /callback-tasks/{id}`

### 5.3 手动重试

- `POST /callback-tasks/{id}/retry`：重试单条
- `POST /callback-tasks/retry`：批量重试（`retrying` / `dead` -> `retrying`）

## 6. 统计信息

`GET /stats`

返回：

- `watched_total` / `watched_enabled` / `watched_disabled`
- `processed_tx_total`
- `callback_pending` / `callback_retrying` / `callback_success` / `callback_dead`
- `debug_callbacks`

## 7. 指标

`GET /metrics`

Prometheus 指标（默认与管理接口同鉴权）。指标清单见 [OBSERVABILITY.md](OBSERVABILITY.md)。

## 8. 本地联调接口（建议生产隔离）

- `GET/POST/DELETE /mock/transactions`
- `GET/POST/DELETE /debug/callbacks`

## 9. 回调协议（业务侧）

### 8.1 回调请求

- Method: `POST`
- URL: `callback_url`
- Header: `Content-Type: application/json`
- Body: `CallbackPayload`

示例：

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

### 8.2 幂等与事件 ID

每次回调都会带：

```
X-WalletMonitor-Event-ID: <callback_task_id>
```

业务侧建议以该 ID 做幂等键（同一事件重试时 ID 不变）。

### 8.3 签名（可选）

启动参数 `-callback-secret` 不为空时，会额外带签名头：

- `X-WalletMonitor-Timestamp: <unix_seconds>`
- `X-WalletMonitor-Signature: <hex(hmac_sha256(secret, timestamp + "." + payload_json))>`

注意：

- `payload_json` 为 **原始 HTTP body 字节串**（不要二次序列化/重排字段）。
- 业务侧验签失败应返回非 2xx，使其进入重试队列。

验签示例（shell）：

```bash
timestamp="1700000000"
payload='{"a":1}'
secret="s3cr3t"

printf "%s.%s" "$timestamp" "$payload" \
  | openssl dgst -sha256 -hmac "$secret" -hex \
  | awk '{print $2}'
```

### 8.4 成功响应

业务侧返回任意 `2xx` 表示成功；非 2xx 或超时会进入重试队列，超过最大重试次数进入 `dead`。
