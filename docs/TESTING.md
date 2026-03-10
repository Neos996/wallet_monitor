# Wallet Monitor 测试说明

## 1. 当前能力

当前服务支持两类验证：

- 本地端到端验证：使用 `mock` 链注入假交易，验证地址注册、扫描、回调、去重是否工作；
- 真链联调验证：当前已支持 `tron` 链的已确认 `TRX` / `TRC20` 入账扫描。

> 当前代码已经具备本地闭环能力，并已落地真实 TRON 扫描能力；其他链仍需继续扩展。

## 2. 本地端到端验证

### 2.1 启动服务

如果你的本机 Go 环境变量（如 `GOROOT` / `GOPATH`）配置异常，建议先临时取消环境变量再启动：

```bash
cd wallet_monitor
env -u GOROOT -u GOPATH go run . \
  -listen :8080 \
  -db ./wallets.db \
  -scan-interval 1m \
  -callback-url http://127.0.0.1:8080/debug/callbacks
```

这里把默认回调地址指向服务自己的 `debug` 接口，这样一台机器、一个进程就能完成完整闭环测试。

如果你启动时开启了 `-admin-token`，则后续所有 `curl` 需要额外带上：

```bash
-H 'Authorization: Bearer <ADMIN_TOKEN>'
```

### 2.2 检查服务存活

```bash
curl http://127.0.0.1:8080/healthz
```

预期返回：

```text
ok
```

### 2.3 清理旧测试数据

```bash
curl -X DELETE http://127.0.0.1:8080/mock/transactions
curl -X DELETE http://127.0.0.1:8080/debug/callbacks
```

### 2.4 注册一个 mock 地址

```bash
curl -X POST http://127.0.0.1:8080/addresses \
  -H 'Content-Type: application/json' \
  -d '{
    "chain": "mock",
    "network": "local",
    "address": "mock_wallet_001"
  }'
```

查询确认地址已注册：

```bash
curl http://127.0.0.1:8080/addresses
```

### 2.5 注入一笔假入账交易

```bash
curl -X POST http://127.0.0.1:8080/mock/transactions \
  -H 'Content-Type: application/json' \
  -d '{
    "chain": "mock",
    "network": "local",
    "address": "mock_wallet_001",
    "tx_hash": "tx_demo_001",
    "from": "payer_a",
    "amount": "12.50"
  }'
```

查看待扫描交易：

```bash
curl http://127.0.0.1:8080/mock/transactions
```

### 2.6 手动触发单次扫描

```bash
curl -X POST http://127.0.0.1:8080/scan/once
```

预期返回类似：

```json
{
  "addresses_scanned": 1,
  "detected_txs": 1,
  "callbacks_sent": 1,
  "duplicate_txs": 0,
  "failed_callbacks": 0,
  "updated_addresses": 1
}
```

### 2.7 验证回调是否真的收到

```bash
curl http://127.0.0.1:8080/debug/callbacks
```

预期看到一条记录，`payload` 里包含：

- `address`
- `tx_hash`
- `from`
- `to`
- `amount`
- `block_height`

### 2.8 验证不会重复推送

再次执行：

```bash
curl -X POST http://127.0.0.1:8080/scan/once
curl http://127.0.0.1:8080/debug/callbacks
```

预期：

- 第二次扫描 `callbacks_sent = 0`；
- `debug/callbacks` 中回调记录数量不再增加；
- 说明 mock 交易已经被消费，不会重复推送。

## 3. TRON 真链联调验证

### 3.1 当前已支持范围

当前 `wallet_monitor` 已支持：

- `chain = tron`
- `network = mainnet | shasta | nile`
- 已确认的 `TRX` 入账交易扫描
- 已确认的 `TRC20` 入账交易扫描
- `min_confirmations`（最小确认数）控制：只回调达到确认数的交易
- 回调持久化队列 `CallbackTask`：失败自动重试 + dead 状态
- 可选回调签名：`-callback-secret` 开启后会带签名请求头，便于业务方验签

当前仍未覆盖（后续生产化增强项）：

- 全链路限流与更细粒度的 RPC 429 退避策略（大地址量场景）
- 多链适配（EVM/BTC 等）
- 更复杂的 reorg/回滚处理策略（TRON 以 confirmed + confirmations 为主，已可满足大多数收款场景）

注意：

- `TRC20` 扫描需要额外查询交易详情来拿区块高度；
- 如果直接使用公共 `TronGrid`，可能会遇到 `429 Too Many Requests`；
- 当前代码已加入基础节流和重试，但生产环境仍建议配置专用 API Key 或私有节点。

### 3.2 启动方式

```bash
cd wallet_monitor
env -u GOROOT -u GOPATH go run . \
  -listen :8080 \
  -db ./wallets.db \
  -scan-interval 30s \
  -rpc-url https://api.trongrid.io \
  -callback-url http://127.0.0.1:8080/debug/callbacks
```

建议生产联调时配置专用 API Key：

```bash
-tron-api-key <TRON_PRO_API_KEY>
```

### 3.3 关于 start_height（避免历史回填）

TRON 生产收款场景通常不需要“回填该地址历史所有入账”。因此当前逻辑为：

- `chain=tron` 且请求不传 `start_height` 时，服务会自动把 `last_seen_height` 初始化为“当前已确认高度”（基于 `min_confirmations` 计算的 confirmed cutoff）。
- 如果你确实需要回填历史入账，请在注册时显式传入 `start_height`（例如 `0` 或某个区块高度）。

### 3.3 注册一个 TRON 地址

```bash
curl -X POST http://127.0.0.1:8080/addresses \
  -H 'Content-Type: application/json' \
  -d '{
    "chain": "tron",
    "network": "mainnet",
    "address": "TE6tpVvcdAn1Sg7fYjkgeaWnabnzzxCdir"
  }'
```

如果你要监控 TRC20（例如 USDT），则注册方式改成：

```bash
curl -X POST http://127.0.0.1:8080/addresses \
  -H 'Content-Type: application/json' \
  -d '{
    "chain": "tron",
    "network": "mainnet",
    "address": "TYo5GrzZGGnzrSMp2eQ8QR352RDTrwrgkQ",
    "asset_type": "trc20",
    "token_contract": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
  }'
```

### 3.4 触发一次真实扫描

```bash
curl -X POST http://127.0.0.1:8080/scan/once
curl http://127.0.0.1:8080/debug/callbacks
```

如果该地址在 `last_seen_height` 之后存在新的已确认 `TRX` 入账，预期结果是：

- `scan/once` 返回的 `detected_txs` 和 `callbacks_sent` 大于 0；
- `debug/callbacks` 中能看到真实链上的交易哈希和金额；
- 同一笔交易再次扫描时不会重复回调。

### 3.5 我本地已经验证过的结果

我本地已实际跑通过一轮真实 TRON 扫描：

- 地址：`TE6tpVvcdAn1Sg7fYjkgeaWnabnzzxCdir`
- 扫描结果：检测到 4 笔新的已确认 `TRX` 入账
- 回调结果：成功写入 4 条 `/debug/callbacks` 记录

我也实际跑通过一轮真实 TRON TRC20 扫描：

- 地址：`TYo5GrzZGGnzrSMp2eQ8QR352RDTrwrgkQ`
- 代币合约：`TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t`（USDT）
- 增量区块高度：`80738570 -> 80738571`
- 扫描结果：检测到 1 笔新的已确认 `USDT` 入账
- 回调结果：成功写入 1 条 `/debug/callbacks` 记录

这说明当前代码已经不是“只有 mock 能跑”的状态，而是已经具备一版可工作的真实 TRON 主币 / TRC20 入账监控能力。

## 4. 真实有效的验收标准

满足以下条件，才能认为监控服务“真实有效”：

- 地址注册后，能够稳定被扫描；
- 真实链上有新入账时，服务能在预期时间内发现；
- 回调字段完整，且能对应到真实交易；
- 服务重启后不会重复回调历史交易；
- 单次回调失败时，`CallbackTask` 会进入重试队列，后续扫描仍能重试，而不会直接丢单；
- 超过最大重试次数后进入 `dead` 状态，能够通过管理接口手动重试或排查失败原因。
