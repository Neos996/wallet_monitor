# wallet_monitor

独立部署的钱包入账监控服务（当前优先面向 TRON 生产收款场景）。

业务系统只需要做两件事：

1. 注册需要监控的收款地址（HTTP API）。
2. 接收入账回调（HTTP POST）。

监控服务负责：扫链、确认数判断、去重、回调重试与失败记录。

## 当前能力

- `chain=mock`：本地闭环联调（注入假交易 -> 扫描 -> 回调 -> 去重）。
- `chain=tron`：`mainnet | shasta | nile` 的已确认 `TRX(native)` / `TRC20` 入账扫描。
- `chain=evm`：`ERC20` 按地址日志查询入账（回调金额为原始整数，需业务侧按 token decimals 处理）。
- `min_confirmations`：最小确认数控制（只回调达到确认数的交易）。
- 回调持久化任务队列：`CallbackTask` + 指数退避重试 + `dead` 状态。
- 幂等去重：回调成功后写入 `ProcessedTx`，避免重复通知。
- 可选回调签名：`-callback-secret` 开启后自动带签名请求头。
- 可选管理接口鉴权：`-admin-token` 开启后，除 `/healthz` 外接口都需要带 token。

## 快速开始（本地 mock 闭环）

如果你的本机 Go 环境变量（`GOROOT` / `GOPATH`）有污染，建议先临时取消环境变量：

```bash
cd wallet_monitor
env -u GOROOT -u GOPATH go run . \
  -listen :8080 \
  -db ./wallets.db \
  -scan-interval 1m \
  -callback-url http://127.0.0.1:8080/debug/callbacks
```

验证：

```bash
curl http://127.0.0.1:8080/healthz
```

完整联调步骤见 [docs/TESTING.md](docs/TESTING.md)。

## 快速开始（TRON 真链联调）

```bash
cd wallet_monitor
env -u GOROOT -u GOPATH go run . \
  -listen :8080 \
  -db ./wallets.db \
  -scan-interval 30s \
  -rpc-url https://api.trongrid.io \
  -tron-api-key <TRON_PRO_API_KEY> \
  -callback-url http://127.0.0.1:8080/debug/callbacks
```

注册地址（TRX）：

```bash
curl -X POST http://127.0.0.1:8080/addresses \
  -H 'Content-Type: application/json' \
  -d '{
    "chain": "tron",
    "network": "mainnet",
    "address": "TE6tpVvcdAn1Sg7fYjkgeaWnabnzzxCdir",
    "asset_type": "native",
    "min_confirmations": 3
  }'
```

注册地址（TRC20，例如 USDT）：

```bash
curl -X POST http://127.0.0.1:8080/addresses \
  -H 'Content-Type: application/json' \
  -d '{
    "chain": "tron",
    "network": "mainnet",
    "address": "TYo5GrzZGGnzrSMp2eQ8QR352RDTrwrgkQ",
    "asset_type": "trc20",
    "token_contract": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
    "min_confirmations": 3
  }'
```

触发一次扫描并查看回调：

```bash
curl -X POST http://127.0.0.1:8080/scan/once
curl http://127.0.0.1:8080/debug/callbacks
```

### `start_height`（避免历史回填）

TRON 生产收款场景一般不需要回填历史入账。

- 当 `chain=tron` 且注册请求不传 `start_height` 时，服务会自动把该地址的 `last_seen_height` 初始化为“当前已确认高度”（基于 `min_confirmations` 的 confirmed cutoff）。
- 如果你确实需要回填，请显式传 `start_height`（例如 `0` 或某个区块高度）。

## 配置参数（Flags）

- `-listen`：HTTP 监听地址，默认 `:8080`
- `-db`：SQLite 文件路径，默认 `wallets.db`
- `-scan-interval`：扫描间隔，默认 `15s`（TRON 生产建议从 `30s` 起，根据地址量调整）
- `-scan-workers`：每轮并发扫描地址数，默认 `4`（地址量大时可增加，但注意 RPC 限流）
- `-rpc-url`：链上 RPC/网关 URL（TRON 可用 `https://api.trongrid.io` / `https://api.shasta.trongrid.io` / `https://nile.trongrid.io`）
- `-tron-api-key`：TronGrid API key（建议生产必配，降低 429 风险）
- `-tron-qps`：Tron API 全局 QPS 限制，默认 `8`（`0` 表示不限制）
- `-tron-retry-429`：遇到 HTTP 429 时的指数退避重试次数，默认 `3`
- `-evm-rpc-url`：EVM JSON-RPC 地址（`chain=evm` 必填）
- `-evm-log-range`：EVM 单次日志查询最大区块范围，默认 `2000`
- `-callback-url`：默认回调地址（可被单地址 `callback_url` 覆盖）
- `-callback-secret`：回调签名 HMAC secret（不为空则启用签名）
- `-callback-retry-base`：回调失败重试基准间隔（指数退避基数），默认 `10s`
- `-callback-max-retries`：最大重试次数，默认 `5`
- `-callback-batch`：每轮扫描后处理的回调任务上限，默认 `100`
- `-callback-workers`：回调发送并发数，默认 `4`
- `-callback-qps`：回调全局限速（每秒），`0` 表示不限制
- `-callback-retry-4xx`：是否对 4xx 回调响应进行重试，默认 `false`
- `-callback-retry-statuses`：额外需要重试的状态码（逗号分隔），例如 `409,425`
- `-admin-token`：管理接口鉴权 token（不为空则启用；支持 `Authorization: Bearer ...` 或 `X-Admin-Token`）

## API 概览

- `GET /healthz`：健康检查（不鉴权）
- `GET /addresses` / `POST /addresses`：查询/新增监控地址
- `GET /addresses/{id}` / `PATCH /addresses/{id}` / `DELETE /addresses/{id}`：按 ID 管理（支持更新 `callback_url` / `enabled` / `min_confirmations` / `last_seen_height`）
- `POST /addresses/{id}/enable` / `POST /addresses/{id}/disable`：启停监控
- `POST /scan/once`：手动触发一轮扫描
- `GET /callback-tasks` / `GET /callback-tasks/{id}`：查看回调任务队列
- `POST /callback-tasks/{id}/retry` / `POST /callback-tasks/retry`：手动重试
- `GET /callback-tasks/dead/export`：导出死信任务（支持 `?format=csv`）
- `GET /stats`：统计信息
- `GET /metrics`：Prometheus 指标（默认与管理接口同鉴权）
- `GET/POST/DELETE /mock/transactions`、`GET/POST/DELETE /debug/callbacks`：本地联调接口（生产环境建议通过 `-admin-token` + 网络隔离保护）

完整接口与回调协议见 [docs/API.md](docs/API.md)。
可观测性与告警建议见 [docs/OBSERVABILITY.md](docs/OBSERVABILITY.md)。

## 回调协议（业务方需要实现）

回调为 HTTP POST，Body 为 JSON（`CallbackPayload`），字段包括：

- `chain` / `network`
- `asset_type` / `token_contract` / `token_symbol` / `token_decimals`
- `address`
- `tx_hash`
- `from` / `to`
- `amount`
- `block_height`

每次回调会带一个事件 ID：

- `X-WalletMonitor-Event-ID: <callback_task_id>`

业务方应将其作为幂等键（避免重复处理）。

若启动时设置了 `-callback-secret`，则额外带签名头：

- `X-WalletMonitor-Timestamp: <unix_seconds>`
- `X-WalletMonitor-Signature: <hex(hmac_sha256(secret, timestamp + "." + payload_json))>`

业务方验签通过且返回 `2xx` 才会被认为回调成功；否则将进入重试队列，直到成功或进入 `dead`。

## TRON 生产部署建议（第一阶段目标）

- 强烈建议开启 `-admin-token`，并确保监听地址只对内网开放。
- 强烈建议开启 `-callback-secret`，并在业务侧做验签与幂等。
- TronGrid 公共端点容易 429：建议配置 `-tron-api-key`，并将 `-scan-interval` 调大到合理范围。
- SQLite 适合单机部署与 MVP；如果要做多实例/高可用，需要引入共享数据库与分布式锁（后续多链版本可一起演进）。

更多部署细节见 [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)。
