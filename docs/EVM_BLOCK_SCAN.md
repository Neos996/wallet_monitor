# EVM 按区块高度爬块（Block Scan Mode）原理

本项目 `chain=evm` 的 ERC20 入账扫描支持两种模式：

- `address`（默认）：对每个 watched address 单独调用 `eth_getLogs` 扫该地址的入账。
- `block`：按区块高度推进游标，合并多个 watched address 的日志查询（批量 OR topics），以降低 RPC 调用量。

该文档解释 `-evm-scan-mode block` 的工作原理、正确性约束和调参要点。

## 1. 基本概念

### 1.1 游标：`last_seen_height`

每个监控地址（`watched_addresses`）都有一个游标 `last_seen_height`，表示该地址“已扫描到的最高已确认区块高度”。每次扫描只处理：

- `block_height in (last_seen_height, confirmed_cutoff]`

### 1.2 确认数：`min_confirmations` 和 `confirmed_cutoff`

为了避免短链重组导致的回滚，扫描会先取当前区块高度 `current_block`，并计算：

```
confirmed_cutoff = current_block - (min_confirmations - 1)
```

只处理 `<= confirmed_cutoff` 的事件，并在无错误的情况下将游标推进到 `confirmed_cutoff`。

### 1.3 事件唯一键：`(tx_hash, log_index)`

EVM 同一笔交易里可能包含多条 `Transfer` 日志（例如批量 mint/airdrop）。因此本项目在 EVM 下使用：

- `tx_hash + log_index`

作为“入账事件”的唯一标识，并写入：

- `callback_tasks`（避免重复入队）
- `processed_txes`（回调成功后的最终幂等标记）

业务侧也建议用同样的唯一键做对账/幂等（或直接使用 `X-WalletMonitor-Event-ID` 作为幂等键）。

## 2. 为什么 block 模式更省 RPC

### 2.1 address 模式

对 N 个地址，基本形态是每个地址单独 `eth_getLogs`：

- RPC 次数约为：`O(N * (区块跨度/evm_log_range))`

地址越多，RPC 压力线性增长。

### 2.2 block 模式

block 模式按 `token_contract` 分桶，把多个 watched address 合并进一个查询：

- 同一个合约、同一段区块范围：一次 `eth_getLogs` 就能拿到多地址的入账日志（通过 OR topics）
- RPC 次数约为：`O(合约数 * (区块跨度/evm_log_range) * (地址数/evm_topic_batch))`

当“地址数远大于合约数”时收益明显（例如同一个 USDT 合约下监控大量充值地址）。

## 3. block 模式的核心算法

实现入口在：

- `scanOnce()`：当 `-evm-scan-mode=block` 时，会把 `chain=evm & asset_type=erc20` 的地址单独拿出来走 block 模式。
- `scanEVMByBlockLogs()` / `scanEVMLogBucket()`：按桶拉取 logs，路由到具体 watched address。

### 3.1 分桶

把 watched 地址按以下维度分桶：

- `token_contract`
- `network`（用于 token_metadata 缓存 key；当前只有一个 `-evm-rpc-url`，不会真的切换 RPC）
- `min_confirmations`（确认数不同会导致 `confirmed_cutoff` 不同，不能混扫）

### 3.2 计算扫描区间

对一个桶内的地址集合：

1. 取 `current_block`
2. 算 `confirmed_cutoff`
3. 只对 `confirmed_cutoff > last_seen_height` 的地址参与本轮扫描
4. 扫描起点 `fromBlock` 取 **桶内最小的** `(last_seen_height + 1)`
5. 扫描终点 `toBlock = confirmed_cutoff`

这样能避免为每个地址重复扫大量重叠区间。

### 3.3 批量 OR topics 查询

对 ERC20 Transfer：

- `topic0 = keccak256("Transfer(address,address,uint256)")`
- `topic2` 是 `to`（收款方）

block 模式构造 `eth_getLogs` filter：

- `address = token_contract`
- `topics = [topic0, nil, [toTopic1, toTopic2, ...]]`

其中第三个位置传数组表示 OR 语义：匹配任意一个 `to`。

为了避免一次查询 topics 过大，`to` 会按 `-evm-topic-batch` 分批；同时区块范围按 `-evm-log-range` 分段。

### 3.4 路由与去重

对查询得到的每条 log：

1. 解析 `blockNumber`、`transactionHash`、`logIndex`、`topics[1]/topics[2]`、`data(value)`
2. 通过 `to` 地址找到对应 watched address
3. 若 `blockNumber <= watched.last_seen_height` 则忽略（防止桶级 fromBlock 较小带来的重扫）
4. 生成事件 `Tx{Hash, LogIndex, From, To, Amount, BlockHeight, TokenContract}`
5. 通过 `ProcessedTx(chain, network, address, asset_type, token_contract, tx_hash, log_index)` 判断是否已经成功投递
6. 通过 `CallbackTask` 唯一键防止重复入队（即使进程重启也能幂等）

### 3.5 decimals 缓存

当桶内命中任何事件时，会在该桶维度解析一次 token `decimals()`：

- 先查本地 `token_metadata`
- 未命中则 `eth_call` 合约 `decimals()` 并 upsert 到 `token_metadata`

然后把 `token_decimals` 填入回调 payload（EVM 的 `amount` 仍保持原始整数）。

### 3.6 推进游标

当一个桶的扫描与入队逻辑没有发生致命错误时，才会将桶内地址的 `last_seen_height` 更新到 `confirmed_cutoff`。

如果某个地址缺少 callback_url，本实现会跳过该地址的游标推进，避免“没有回调地址但游标前进导致永久漏单”。

## 4. 参数调优建议

- `-evm-log-range`：单次 `eth_getLogs` 覆盖的区块跨度。RPC 提供方若有返回条数上限/超时风险，调小它。
- `-evm-topic-batch`：每次查询合并的 `to` 地址数。过大可能触发 RPC 提供方限制；过小则 RPC 次数上升。

经验上：先从 `evm_log_range=2000`、`evm_topic_batch=100` 起，根据 RPC 失败率与延迟调参。

## 5. 已知限制与风险

- `eth_getLogs` 的返回条数/响应体大小在不同 RPC 提供方可能有限制；若某个区间内 Transfer 非常密集，可能需要降低 `evm_log_range` 或 `evm_topic_batch`。
- 仅通过 “确认数 cutoff” 来降低 reorg 风险；不做区块 hash 回溯校验，因此不覆盖极端深 reorg。
- 当前 EVM 只实现 ERC20 Transfer 入账（topic0 固定），不处理更复杂的事件语义（比如特殊代币的非标准事件）。

