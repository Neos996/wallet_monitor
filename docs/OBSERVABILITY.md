# 可观测性与告警建议

本服务已暴露 Prometheus 指标（`GET /metrics`，可被 `-admin-token` 保护）。

## 1. 指标列表

扫描相关：

- `wallet_monitor_scan_duration_seconds` (Histogram)
- `wallet_monitor_scan_addresses_total` (Counter)
- `wallet_monitor_scan_detected_txs_total` (Counter)
- `wallet_monitor_scan_queued_callbacks_total` (Counter)
- `wallet_monitor_scan_duplicate_txs_total` (Counter)
- `wallet_monitor_scan_failed_callbacks_total` (Counter)
- `wallet_monitor_scan_dead_callbacks_total` (Counter)
- `wallet_monitor_scan_updated_addresses_total` (Counter)
- `wallet_monitor_last_scan_timestamp` (Gauge)

队列与地址：

- `wallet_monitor_callback_tasks{status="pending|retrying|success|dead"}` (Gauge)
- `wallet_monitor_watched_addresses{state="enabled|disabled"}` (Gauge)

## 2. 告警建议（PromQL 参考）

扫描卡住：

```
time() - wallet_monitor_last_scan_timestamp > 120
```

回调死信积压：

```
wallet_monitor_callback_tasks{status="dead"} > 0
```

回调失败飙升（5 分钟内）：

```
increase(wallet_monitor_scan_failed_callbacks_total[5m]) > 5
```

回调堆积增长：

```
increase(wallet_monitor_callback_tasks{status="pending"}[10m]) > 0
```

扫链压力过大（扫描耗时持续偏高）：

```
histogram_quantile(0.95, rate(wallet_monitor_scan_duration_seconds_bucket[5m])) > 10
```

## 3. 日志建议

日志使用 JSON 结构化输出，关键事件包含：

- 扫描完成：扫描地址数、命中数、回调队列/成功/失败/死信数
- 扫描异常：链 RPC 错误、429、解析失败
- 回调异常：非 2xx、超时、死信

建议业务侧与日志平台建立以下规则：

- `scan error` 或 `scan address failed` 连续出现
- `callback` 错误与 `dead` 计数同步上升
- 扫描耗时显著增加（对比常态）

