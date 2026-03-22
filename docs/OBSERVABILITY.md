# 可观测性与排障

本文档说明 `wallet_monitor` 当前已经落地的可观测性能力，包括日志、Prometheus 指标、健康检查和排障流程。

## 1. 当前落地状态

本轮优化已经完成以下能力：

- 请求级访问日志，统一输出 `request_id`
- 扫描链路日志，统一输出 `scan_id`
- 回调投递日志，统一输出 `task_id`、`retry_count`、`latency_ms`
- 回调失败保留 HTTP 状态码与响应体摘要，便于对端问题排查
- `/readyz` 就绪检查，覆盖数据库、最近成功扫描时间、死信阈值
- 扫描互斥与回调分发互斥，避免同进程内并发重入
- 列表接口可选分页，返回总量头
- 扫描耗时与回调耗时已升级为 Prometheus Histogram

## 2. 日志

### 2.1 日志格式

服务使用 `slog` 输出 JSON 日志，默认写到标准输出。

### 2.2 关键关联字段

| 字段 | 来源 | 说明 |
|---|---|---|
| `request_id` | HTTP 中间件 | 每个 HTTP 请求唯一 ID；若上游传入 `X-Request-ID` 则透传 |
| `scan_id` | 扫描管理器 | 每轮扫描唯一 ID，用于串联扫描开始、扫描完成、地址失败日志 |
| `task_id` | 回调任务 | 每次回调任务的唯一 ID |
| `retry_count` | 回调任务 | 当前已重试次数 |
| `tx_hash` / `log_index` | 扫描与回调 | 对应具体链上事件 |
| `duration_ms` / `latency_ms` | HTTP / callback | 排查慢请求、慢回调 |

### 2.3 关键日志事件

| 事件 | 日志关键字 | 说明 |
|---|---|---|
| HTTP 请求完成 | `http request completed` | 统一访问日志，包含方法、路径、状态码、耗时、request_id |
| 扫描开始 | `scan started` | 包含 `scan_id` 与触发来源（`startup` / `ticker` / `manual`） |
| 扫描完成 | `scan completed` | 包含地址数、命中数、入队数、发送数、失败数、死信数 |
| 扫描跳过 | `scan skipped: already running` | 同进程内已有扫描执行中 |
| 地址扫描失败 | `scan address failed` | 单地址或单 bucket 的扫链错误 |
| 回调成功 | `callback delivered` | 包含 `task_id`、状态码、耗时 |
| 回调失败 | `callback delivery failed` | 包含错误类型、HTTP 状态码、响应体摘要、下次重试时间 |
| 回调死信 | `callback delivery moved to dead state` | 任务最终进入死信 |
| 地址创建 | `address created` | 新增监控地址 |
| 地址更新 | `address updated` | 监控地址参数变更 |
| 地址删除 | `address deleted` | 监控地址删除 |

### 2.4 日志平台检索建议

- 按 `request_id="<id>"` 追查单次接口调用
- 按 `scan_id="<id>"` 追查单轮扫描
- 按 `task_id="<id>"` 或 `tx_hash="<hash>"` 追查单次回调事件
- 按 `error_kind="non_2xx"` 聚合业务方回调错误

## 3. Prometheus 指标

服务暴露 `GET /metrics`，默认与管理接口共用鉴权。

### 3.1 扫描指标

| 指标 | 类型 | 说明 |
|---|---|---|
| `wallet_monitor_last_scan_duration_seconds` | Gauge | 最近一次成功扫描耗时 |
| `wallet_monitor_scan_duration_seconds` | Histogram | 成功扫描耗时分布 |
| `wallet_monitor_last_scan_timestamp` | Gauge | 最近一次成功扫描的 Unix 时间戳 |
| `wallet_monitor_scan_addresses_total` | Counter | 累计扫描地址数 |
| `wallet_monitor_scan_detected_txs_total` | Counter | 累计检测到的入账事件数 |
| `wallet_monitor_scan_queued_callbacks_total` | Counter | 累计入队回调数 |
| `wallet_monitor_scan_duplicate_txs_total` | Counter | 累计去重事件数 |
| `wallet_monitor_scan_failed_callbacks_total` | Counter | 扫描过程中观测到的失败回调数 |
| `wallet_monitor_scan_dead_callbacks_total` | Counter | 扫描过程中进入死信的回调数 |
| `wallet_monitor_scan_updated_addresses_total` | Counter | 累计推进 `last_seen_height` 的地址数 |
| `wallet_monitor_scan_skipped_total` | Counter | 因已有扫描执行而跳过的扫描次数 |
| `wallet_monitor_scan_address_failures_total` | Counter | 单地址 / 单 bucket 扫描失败次数 |

### 3.2 回调指标

| 指标 | 类型 | 说明 |
|---|---|---|
| `wallet_monitor_callback_delivery_duration_seconds` | Histogram | 回调投递耗时分布 |
| `wallet_monitor_callback_attempts_total` | Counter | 回调尝试总数 |
| `wallet_monitor_callback_success_total` | Counter | 回调成功总数 |
| `wallet_monitor_callback_dead_total` | Counter | 最终进入死信的回调总数 |
| `wallet_monitor_callback_dispatch_skipped_total` | Counter | 因已有回调分发执行而跳过的分发次数 |
| `wallet_monitor_callback_failures_total{kind="..."}` | Counter | 按失败类型聚合的回调失败总数 |
| `wallet_monitor_callback_http_status_total{status="..."}` | Counter | 按 HTTP 状态码聚合的非 2xx 回调数 |

### 3.3 队列与地址存量

| 指标 | 类型 | 说明 |
|---|---|---|
| `wallet_monitor_callback_tasks{status="pending"}` | Gauge | 待处理回调任务数 |
| `wallet_monitor_callback_tasks{status="retrying"}` | Gauge | 重试中回调任务数 |
| `wallet_monitor_callback_tasks{status="success"}` | Gauge | 已成功回调任务数 |
| `wallet_monitor_callback_tasks{status="dead"}` | Gauge | 死信任务数 |
| `wallet_monitor_watched_addresses{state="enabled"}` | Gauge | 启用中的监控地址数 |
| `wallet_monitor_watched_addresses{state="disabled"}` | Gauge | 禁用中的监控地址数 |

## 4. 告警建议

### 4.1 扫描卡住

```promql
time() - wallet_monitor_last_scan_timestamp > 120
```

处理建议：

- 检查 `/readyz`
- 检查日志中的 `scan failed` / `scan address failed`
- 检查 RPC 可用性与数据库状态

### 4.2 扫描并发冲突

```promql
increase(wallet_monitor_scan_skipped_total[5m]) > 0
```

处理建议：

- 检查是否频繁人工触发 `POST /scan/once`
- 检查单轮扫描耗时是否持续偏高

### 4.3 地址扫描失败增加

```promql
increase(wallet_monitor_scan_address_failures_total[5m]) > 0
```

处理建议：

- 检查失败日志中的 `chain` / `network` / `address`
- 检查对应 RPC 配置与限流状态

### 4.4 回调失败飙升

```promql
sum(increase(wallet_monitor_callback_failures_total[5m])) > 5
```

处理建议：

- 查看 `wallet_monitor_callback_failures_total{kind=...}`
- 查看日志中的 `callback delivery failed`
- 检查业务方服务与验签逻辑

### 4.5 回调延迟升高

```promql
histogram_quantile(0.95, rate(wallet_monitor_callback_delivery_duration_seconds_bucket[5m])) > 5
```

处理建议：

- 检查业务方接口 RT
- 检查 `-callback-workers` 与 `-callback-qps`

### 4.6 回调分发冲突

```promql
increase(wallet_monitor_callback_dispatch_skipped_total[5m]) > 0
```

处理建议：

- 检查是否频繁调用 `POST /callback-tasks/retry`
- 检查单批次回调量与下游耗时

### 4.7 死信积压

```promql
wallet_monitor_callback_tasks{status="dead"} > 0
```

处理建议：

- 查询 `GET /callback-tasks?status=dead`
- 查看 `last_error`、`last_error_type`、`last_status_code`
- 导出 `GET /callback-tasks/dead/export?format=csv`

## 5. 健康检查

### 5.1 `GET /healthz`

- 不鉴权
- 返回纯文本 `ok`
- 用于 liveness probe

### 5.2 `GET /readyz`

- 不鉴权
- 返回 JSON
- 用于 readiness probe

当前检查项：

- 数据库连接是否正常
- 最近一次成功扫描是否超过 `-ready-max-scan-age`
- 死信任务数是否超过 `-ready-max-dead-tasks`

失败时返回 `503 Service Unavailable`。

## 6. 排障流程

### 6.1 扫描异常

1. 访问 `/readyz` 确认数据库与扫描新鲜度
2. 查看 `wallet_monitor_last_scan_timestamp`
3. 按 `scan_id` 检索 `scan started`、`scan completed`、`scan address failed`
4. 检查具体链 RPC 响应与限流

### 6.2 回调失败

1. 查看 `wallet_monitor_callback_failures_total{kind=...}`
2. 查询 `GET /callback-tasks?status=retrying` 或 `GET /callback-tasks?status=dead`
3. 按 `task_id` 检索 `callback delivery failed`
4. 根据 `response_body` 摘要与 `last_status_code` 排查对端

### 6.3 接口报错

1. 记录响应头中的 `X-Request-ID`
2. 按 `request_id` 检索访问日志与业务日志
3. 根据状态码定位是鉴权、参数还是数据库/RPC 异常

## 7. 仪表盘建议

建议至少包含以下面板：

- 扫描耗时：`histogram_quantile(0.95, rate(wallet_monitor_scan_duration_seconds_bucket[5m]))`
- 最近扫描时间：`time() - wallet_monitor_last_scan_timestamp`
- 地址扫描失败：`increase(wallet_monitor_scan_address_failures_total[5m])`
- 回调耗时：`histogram_quantile(0.95, rate(wallet_monitor_callback_delivery_duration_seconds_bucket[5m]))`
- 回调失败分布：`sum by (kind) (increase(wallet_monitor_callback_failures_total[5m]))`
- 队列状态：`wallet_monitor_callback_tasks`
- 地址状态：`wallet_monitor_watched_addresses`
