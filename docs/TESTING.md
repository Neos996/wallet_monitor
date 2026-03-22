# 测试与验收说明

本文档说明如何验证 `wallet_monitor` 的核心能力，包括本地 mock 闭环、TRON 真链联调和 EVM 本地 ERC20 验证。

## 1. 测试目标

当前版本需要验证的核心能力包括：
- 地址注册是否生效
- 扫描是否能识别新入账
- 回调是否能成功投递
- 去重是否有效
- 回调失败后是否进入重试与死信流程
- TRON / EVM 适配器是否按预期工作

## 2. 当前支持的验证范围

当前服务支持三类验证：
- 本地 mock 闭环验证
- TRON 真链联调验证
- EVM 本地 ERC20 闭环验证

**当前系统已经支持 mock、TRON 和 EVM 三类验证路径。**

## 3. 本地 mock 闭环验证

本节用于验证最小闭环：
- 注册地址
- 注入假交易
- 扫描识别
- 回调写入
- 重复扫描不重复推送

### 3.1 启动服务

```bash
cd wallet_monitor
env -u GOROOT -u GOPATH go run . \
  -listen :8080 \
  -db ./wallets.db \
  -scan-interval 1m \
  -callback-url http://127.0.0.1:8080/debug/callbacks
```

如果启用了 `-admin-token`，后续所有请求都需要额外带上：

```bash
-H 'Authorization: Bearer <ADMIN_TOKEN>'
```

### 3.2 健康检查

```bash
curl http://127.0.0.1:8080/healthz
```

预期结果：

```text
ok
```

补充验证：

```bash
curl -i http://127.0.0.1:8080/readyz
```

预期结果：

- 返回 `200 OK`
- Header 中包含 `X-Request-ID`
- Body 中 `status = "ok"`

### 3.3 清理旧测试数据

```bash
curl -X DELETE http://127.0.0.1:8080/mock/transactions
curl -X DELETE http://127.0.0.1:8080/debug/callbacks
```

### 3.4 注册 mock 地址

```bash
curl -X POST http://127.0.0.1:8080/addresses \
  -H 'Content-Type: application/json' \
  -d '{
    "chain": "mock",
    "network": "local",
    "address": "mock_wallet_001"
  }'
```

验证地址是否已注册：

```bash
curl http://127.0.0.1:8080/addresses
```

### 3.5 注入一笔 mock 入账交易

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

### 3.6 触发一次扫描

```bash
curl -i -X POST http://127.0.0.1:8080/scan/once
```

预期结果：
- `detected_txs > 0`
- `callbacks_sent > 0`
- `updated_addresses > 0`
- 响应头包含 `X-Request-ID`
- 响应头包含 `X-Scan-ID`

### 3.7 验证回调结果

```bash
curl http://127.0.0.1:8080/debug/callbacks
```

预期结果：
- 有一条回调记录
- `payload` 中包含 `address`、`tx_hash`、`from`、`to`、`amount`、`block_height`

### 3.8 验证不会重复推送

再次执行：

```bash
curl -i -X POST http://127.0.0.1:8080/scan/once
curl http://127.0.0.1:8080/debug/callbacks
```

预期结果：
- 第二次扫描 `callbacks_sent = 0`
- `debug/callbacks` 记录数不增加

如果在首轮扫描尚未完成时再次触发 `POST /scan/once`，预期可能返回：

- `409 Conflict`

表示服务已经拦截了同进程内的重复扫描。

## 4. TRON 真链联调验证

本节用于验证：
- TRON 已确认 `TRX` 入账扫描
- TRON 已确认 `TRC20` 入账扫描
- `min_confirmations` 是否生效
- 回调是否可正常投递

### 4.1 当前支持范围
当前已支持：
- `chain=tron`
- `network=mainnet | shasta | nile`
- 已确认 `TRX` 入账扫描
- 已确认 `TRC20` 入账扫描
- 回调持久化队列与自动重试
- 可选回调签名

### 4.2 启动方式

```bash
cd wallet_monitor
env -u GOROOT -u GOPATH go run . \
  -listen :8080 \
  -db ./wallets.db \
  -scan-interval 30s \
  -rpc-url https://api.trongrid.io \
  -callback-url http://127.0.0.1:8080/debug/callbacks
```

建议同时配置：

```bash
-tron-api-key <TRON_PRO_API_KEY>
```

### 4.3 start_height 规则
TRON 场景默认不回填历史入账：
- 未传 `start_height` 时，系统会把 `last_seen_height` 初始化为当前已确认高度
- 如需历史回填，必须显式传 `start_height`

### 4.4 注册 TRX 监控地址

```bash
curl -X POST http://127.0.0.1:8080/addresses \
  -H 'Content-Type: application/json' \
  -d '{
    "chain": "tron",
    "network": "mainnet",
    "address": "TE6tpVvcdAn1Sg7fYjkgeaWnabnzzxCdir"
  }'
```

### 4.5 注册 TRC20 监控地址

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

### 4.6 触发真实扫描

```bash
curl -X POST http://127.0.0.1:8080/scan/once
curl http://127.0.0.1:8080/debug/callbacks
```

预期结果：
- 如果 `last_seen_height` 之后存在新的已确认入账，则 `detected_txs > 0`
- `debug/callbacks` 中能看到真实链交易数据
- 同一笔交易再次扫描时不会重复回调

### 4.7 常见问题

#### TronGrid 429
原因：公共端点限流。

处理：
- 配置 `-tron-api-key`
- 调大 `-scan-interval`
- 调低 `-scan-workers`
- 调低 `-tron-qps`

## 5. EVM 本地 ERC20 验证

本节用于验证：
- ERC20 Transfer 扫描
- `tx_hash + log_index` 去重
- `token_decimals` 缓存

### 5.1 目标
在本机不依赖公网 RPC，完整验证：
- ERC20 入账扫描
- 同一笔 tx 内多条 Transfer 是否分别回调
- decimals 是否缓存到本地数据库

### 5.2 启动本地 EVM（anvil）

终端 A：

```bash
cd wallet_monitor
docker run --rm -it --name wm-anvil -p 8545:8545 \
  -v "$PWD/tools/evmtest:/evmtest" -w /evmtest \
  ghcr.io/foundry-rs/foundry:latest \
  anvil --host 0.0.0.0 --port 8545
```

### 5.3 启动 wallet_monitor

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

清理 debug 回调记录：

```bash
curl -X DELETE http://127.0.0.1:8080/debug/callbacks
```

### 5.4 部署测试 ERC20

终端 C：

```bash
ANVIL_PK=<填 anvil 输出的第一个 private key>

docker exec -e ANVIL_PK="$ANVIL_PK" wm-anvil sh -lc '
  forge create src/FakeERC20.sol:FakeERC20 \
    --rpc-url http://127.0.0.1:8545 \
    --private-key "$ANVIL_PK" \
    --constructor-args "FakeUSDT" "fUSDT" 6
'
```

记录输出中的 `Deployed to: 0x...`，作为 `TOKEN_CONTRACT`。

### 5.5 注册 EVM 监控地址

```bash
TOKEN_CONTRACT=<上一步合约地址>
TO_ADDR=<anvil 输出中的任一收款地址>

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

### 5.6 发送一笔包含两条 Transfer 的交易

```bash
docker exec -e ANVIL_PK="$ANVIL_PK" wm-anvil sh -lc "
  cast send \"$TOKEN_CONTRACT\" \"mintTwo(address,uint256,uint256)\" \"$TO_ADDR\" 111 222 \
    --rpc-url http://127.0.0.1:8545 \
    --private-key \"$ANVIL_PK\"
"
```

### 5.7 触发扫描并验证结果

```bash
curl -X POST http://127.0.0.1:8080/scan/once
curl http://127.0.0.1:8080/debug/callbacks
```

预期结果：
- 能看到 2 条回调记录
- 两条记录 `tx_hash` 相同，但 `log_index` 不同
- `payload` 包含 `token_decimals: 6`
- `amount` 为原始整数 `111`、`222`

### 5.8 验证 decimals 缓存

```bash
sqlite3 ./wallets_evm_test.db ".tables"
sqlite3 ./wallets_evm_test.db "select name from sqlite_master where type='table' and name like 'token_%';"
```

再查询，例如：

```bash
sqlite3 ./wallets_evm_test.db "select chain, network, token_contract, decimals from token_metadata;"
```

预期结果：
- 能查到 `evm / local / <token_contract> / 6`

## 6. 回调失败与死信验证

本节用于验证失败补偿机制。

### 6.1 验证目标
- 回调失败是否进入 `retrying`
- 达到最大重试次数后是否进入 `dead`
- 是否支持手动重试

### 6.2 建议方法
- 将 `callback_url` 指向一个返回 `500` 的测试接口
- 执行扫描并观察 `CallbackTask` 状态变化
- 通过 `/callback-tasks` 查询任务状态
- 通过 `/callback-tasks/retry` 触发手动重试

预期结果：
- 初次失败后状态变为 `retrying`
- 超过最大重试次数后状态变为 `dead`
- 手动重试后任务重新进入可投递状态

## 7. 验收标准

满足以下条件，才能认为当前版本验证通过：
- 地址注册后能够稳定被扫描
- 新入账能在预期时间内被发现
- 回调字段完整，且可对应真实交易
- 服务重启后不会重复回调历史交易
- 单次回调失败不会丢单
- 超过最大重试次数后能形成死信
- 死信任务可查询、导出、手动重试

## 8. 文档边界

本文档只说明验证方法与验收标准：
- 接口协议见 [API.md](./API.md)
- 部署见 [DEPLOYMENT.md](./DEPLOYMENT.md)
- 监控见 [OBSERVABILITY.md](./OBSERVABILITY.md)
- 安全见 [SECURITY.md](./SECURITY.md)
