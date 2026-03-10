# Wallet Monitor 测试说明

## 1. 当前能力

当前服务支持三类验证：

- 本地端到端验证：使用 `mock` 链注入假交易，验证地址注册、扫描、回调、去重是否工作；
- 真链联调验证：当前已支持 `tron` 链的已确认 `TRX` / `TRC20` 入账扫描；
- 本地闭环验证：支持 `evm` 的 `ERC20` 按日志入账扫描（配合本地 EVM dev chain 验证 `log_index` 去重与 `decimals` 缓存）。

> 当前代码已经具备 mock 闭环、TRON 真链扫描、EVM ERC20 日志扫描能力；其他链仍需继续扩展。

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
- 更多链适配（BTC 等）
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

## 4. EVM 本地闭环验证（ERC20）

目标：在本机不依赖公网 RPC，完整验证：

- ERC20 Transfer logs 扫描入账（按 `address + token_contract`）
- 同一笔 tx 内多条 Transfer 能被分别回调（`tx_hash + log_index` 唯一键）
- token decimals 会被缓存到本地 SQLite（`token_metadata` 表）

### 4.1 启动本地 EVM（anvil）

终端 A：

```bash
cd wallet_monitor
docker run --rm -it --name wm-anvil -p 8545:8545 \
  -v "$PWD/tools/evmtest:/evmtest" -w /evmtest \
  ghcr.io/foundry-rs/foundry:latest \
  anvil --host 0.0.0.0 --port 8545
```

`anvil` 启动后会打印一组测试账户和私钥。下文用到的部署私钥请替换成输出里的第一个 private key。

### 4.2 启动 wallet_monitor

终端 B：

```bash
cd wallet_monitor
rm -f ./wallets_evm_test.db

env -u GOROOT -u GOPATH go run . \
  -listen :8080 \
  -db ./wallets_evm_test.db \
  -scan-interval 1m \
  -evm-rpc-url http://127.0.0.1:8545 \
  -evm-scan-mode block \
  -evm-topic-batch 100 \
  -callback-url http://127.0.0.1:8080/debug/callbacks
```

清理 debug 回调缓存：

```bash
curl -X DELETE http://127.0.0.1:8080/debug/callbacks
```

### 4.3 部署测试 ERC20（FakeERC20）

终端 C（通过 `docker exec` 在 anvil 容器内执行 `forge/cast`，避免额外的网络地址问题）：

```bash
ANVIL_PK=<填 anvil 输出的第一个 private key>

docker exec -e ANVIL_PK="$ANVIL_PK" wm-anvil sh -lc '
  forge create src/FakeERC20.sol:FakeERC20 \
    --rpc-url http://127.0.0.1:8545 \
    --private-key "$ANVIL_PK" \
    --constructor-args "FakeUSDT" "fUSDT" 6
'
```

记下输出里的 `Deployed to: 0x...`，作为 `TOKEN_CONTRACT`。

### 4.4 注册 EVM 监控地址

建议按这个顺序执行：先注册 watcher，再发起链上交易；否则需要显式传 `start_height` 才能回填历史。

```bash
TOKEN_CONTRACT=<上一步 Deployed to 地址>
TO_ADDR=<收款地址，从 anvil 输出里任选一个账户地址即可>

curl -X POST http://127.0.0.1:8080/addresses \
  -H 'Content-Type: application/json' \
  -d "{
    \"chain\": \"evm\",
    \"network\": \"local\",
    \"address\": \"$TO_ADDR\",
    \"asset_type\": \"erc20\",
    \"token_contract\": \"$TOKEN_CONTRACT\",
    \"min_confirmations\": 1
  }"
```

### 4.5 发起入账交易（验证 log_index）

发送一笔包含两条 `Transfer` 日志的交易：

```bash
docker exec -e ANVIL_PK="$ANVIL_PK" wm-anvil sh -lc "
  cast send \"$TOKEN_CONTRACT\" \"mintTwo(address,uint256,uint256)\" \"$TO_ADDR\" 111 222 \
    --rpc-url http://127.0.0.1:8545 \
    --private-key \"$ANVIL_PK\"
"
```

### 4.6 触发扫描并验证回调

```bash
curl -X POST http://127.0.0.1:8080/scan/once
curl http://127.0.0.1:8080/debug/callbacks
```

预期：

- 能看到 2 条回调记录（同一个 `tx_hash`，不同 `log_index`）
- payload 里包含 `token_decimals: 6`
- `amount` 为原始整数（`111`、`222`），业务侧需自行按 `decimals` 换算

### 4.7 验证 decimals 缓存（SQLite）

`TokenMetadata` 表名可能会因 ORM 复数规则略有差异，建议先看 `.tables` 或直接查 `sqlite_master`：

```bash
sqlite3 ./wallets_evm_test.db ".tables"
sqlite3 ./wallets_evm_test.db "select name from sqlite_master where type='table' and name like 'token_%';"
```

然后对你看到的表名执行查询，例如表名为 `token_metadata`：

```bash
sqlite3 ./wallets_evm_test.db "select chain, network, token_contract, decimals from token_metadata;"
```

预期能查到一行 `evm / local / <token_contract> / 6`。

## 5. 真实有效的验收标准

满足以下条件，才能认为监控服务“真实有效”：

- 地址注册后，能够稳定被扫描；
- 真实链上有新入账时，服务能在预期时间内发现；
- 回调字段完整，且能对应到真实交易；
- 服务重启后不会重复回调历史交易；
- 单次回调失败时，`CallbackTask` 会进入重试队列，后续扫描仍能重试，而不会直接丢单；
- 超过最大重试次数后进入 `dead` 状态，能够通过管理接口手动重试或排查失败原因。
