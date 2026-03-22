# 单机生产版人工验收 SOP

本文档用于补齐 `scripts/preflight.sh` 无法自动完成的上线验收项。

## 1. 使用方式

建议顺序：

1. 先执行 `scripts/preflight.sh`
2. 再按本文档执行人工验收
3. 将结果回填到 `docs/GO_LIVE_CHECKLIST.md`

## 2. 业务联调 SOP

### 2.1 幂等验证

目标：

- 确认业务方按 `X-WalletMonitor-Event-ID` 做幂等

步骤：

1. 让监控服务向业务方发送一笔正常回调
2. 手动对同一 `task_id` 触发一次重试
3. 观察业务系统是否只落一笔业务记录

通过标准：

- 重复回调不会造成重复入账

### 2.2 验签验证

目标：

- 确认业务方正确校验 `X-WalletMonitor-Timestamp` 与 `X-WalletMonitor-Signature`

步骤：

1. 用正确密钥发送一次回调
2. 用错误密钥或篡改 body 重放一次
3. 观察业务系统日志与响应

通过标准：

- 正确签名请求被接受
- 错误签名请求被拒绝

### 2.3 真链成功回调验证

目标：

- 验证生产目标链路的真实成功闭环

步骤：

1. 注册测试地址
2. 发起一笔真实链上入账
3. 等待确认数达到阈值
4. 检查业务方是否收到正确回调

通过标准：

- 回调字段完整
- 幂等键正确
- 业务处理成功

## 3. 失败与恢复 SOP

### 3.1 回调失败重试验证

目标：

- 验证失败回调会自动进入 retrying / dead

步骤：

1. 临时让 callback 目标返回 `500`
2. 触发一笔回调
3. 观察 `callback_tasks` 状态变化
4. 恢复 callback 目标，再执行重试

通过标准：

- 失败任务进入 `retrying`
- 达到最大重试后进入 `dead`
- 恢复后可以被成功重试

### 3.2 死信导出验证

步骤：

```bash
curl -H 'Authorization: Bearer <ADMIN_TOKEN>' \
  'http://127.0.0.1:8080/callback-tasks/dead/export?format=csv'
```

通过标准：

- 可以导出 CSV
- 字段完整可读

### 3.3 数据库恢复演练

目标：

- 确认 SQLite 备份可用

步骤：

1. 备份当前数据库
2. 停止服务
3. 用备份恢复数据库
4. 启动服务
5. 检查 `/readyz`
6. 检查地址、任务、去重记录是否完整

通过标准：

- 服务恢复后能正常扫描
- 已有任务和处理记录未丢失

## 4. 运维观察 SOP

### 4.1 灰度观察

建议：

- 小流量灰度至少 1 天
- 观察前 1 小时、6 小时、24 小时三个窗口

需要观察：

- `wallet_monitor_last_scan_timestamp`
- `wallet_monitor_callback_tasks{status="dead"}`
- `wallet_monitor_callback_failures_total`
- `wallet_monitor_callback_delivery_duration_seconds`

### 4.2 日志观察

建议重点检索：

- `scan completed`
- `scan address failed`
- `callback delivery failed`
- `callback delivery moved to dead state`

### 4.3 异常阈值建议

满足任一条件建议暂停放量：

- 死信任务数持续 > 0
- 扫描连续 2 个周期未推进
- 回调成功率低于 95%
- 目标 RPC 429 持续飙升

## 5. 上线结论记录模板

建议把以下内容记录到发布单或运维日报：

- 部署版本 / 提交号
- 负责人
- 预检脚本结果
- 业务幂等验证结果
- 验签验证结果
- 真链成功回调验证结果
- 失败重试 / 死信导出验证结果
- 灰度观察结论
- 最终 Go / No-Go 判断
