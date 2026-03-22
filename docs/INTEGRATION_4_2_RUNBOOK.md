# 4.2 业务联调执行单

本文档专门用于完成 `docs/GO_LIVE_CHECKLIST.md` 中 **4.2 业务联调** 的四项勾选：

- 业务方已按 `X-WalletMonitor-Event-ID` 实现幂等
- 业务方已验证 `X-WalletMonitor-Signature`
- 至少验证过 1 次真实链路成功回调
- 至少验证过 1 次失败重试与死信导出

## 1. 联调前准备

### 1.1 必须先替换 `.env.production`

在正式 4.2 联调前，必须把下面 3 个字段替换为真实值：

- `TRON_API_KEY`
- `CALLBACK_URL`
- `CALLBACK_URL_ALLOWLIST`

当前 `wallet_monitor/.env.production` 中这 3 个字段已经被显式标记为 `TODO(4.2)`。

替换完成后，重新执行：

```bash
make prod-full ENV_FILE=.env.production
```

### 1.2 业务方需准备

业务方回调服务必须满足：

- 可接收 `POST` JSON 请求
- 能记录请求头与请求体
- 能按 `X-WalletMonitor-Event-ID` 做幂等
- 能校验 `X-WalletMonitor-Timestamp` 与 `X-WalletMonitor-Signature`

### 1.3 监控方需准备

- 保存当前 `ADMIN_TOKEN`
- 确认 `/readyz` 为 `200`
- 确认 `callback_url` 域名已命中 allowlist

## 2. 验收项 1：幂等验证

### 目标

确认业务方重复收到同一事件不会重复入账。

### 步骤

1. 让监控服务产生一笔正常回调
2. 记录该次回调中的 `X-WalletMonitor-Event-ID`
3. 对相同任务执行一次重试
4. 检查业务方是否只落一笔业务记录

### 验收标准

- 同一 `Event-ID` 不会造成重复业务入账

## 3. 验收项 2：验签验证

### 目标

确认业务方正确校验回调签名。

### 步骤

1. 使用监控服务发送一笔正常回调
2. 业务方确认验签通过
3. 用错误密钥或篡改 body 进行一次重放测试
4. 业务方确认验签失败并拒绝请求

### 验收标准

- 正常签名请求通过
- 错误签名请求被拒绝

## 4. 验收项 3：真实链路成功回调

### 目标

验证生产目标链路的完整成功闭环。

### 步骤

1. 注册真实测试地址
2. 发起一笔真实链上入账
3. 等待达到确认数
4. 观察业务方是否收到正确回调

### 必查字段

- `X-WalletMonitor-Event-ID`
- `X-WalletMonitor-Timestamp`
- `X-WalletMonitor-Signature`
- `tx_hash`
- `amount`
- `block_height`

### 验收标准

- 回调头和 body 完整
- 业务方处理成功
- 监控方无异常死信

## 5. 验收项 4：失败重试与死信导出

### 目标

验证失败 callback 的 retry / dead / export 流程。

### 步骤

1. 临时让业务方 callback 接口返回 `500`
2. 触发一笔真实或模拟回调
3. 确认任务进入 `retrying`
4. 让其达到 `dead` 或手动推动到死信状态
5. 导出死信 CSV

### 导出命令

```bash
curl -H 'Authorization: Bearer <ADMIN_TOKEN>' \
  'http://127.0.0.1:8080/callback-tasks/dead/export?format=csv'
```

### 验收标准

- 任务能进入 `retrying`
- 可进入 `dead`
- CSV 可导出且字段完整

## 6. 联调完成后要做什么

联调完成后，请回填以下文档：

- `docs/GO_LIVE_CHECKLIST.md`
- `docs/MANUAL_ACCEPTANCE_SOP.md`

建议在发布单中记录：

- 联调时间
- 业务方负责人
- 验签是否通过
- 幂等是否通过
- 成功回调样本 `tx_hash`
- 死信导出样本文件

## 7. 当前状态说明

截至当前仓库状态：

- `4.1 服务可用性与鉴权` 已完成
- `4.2 业务联调` 尚未完成
- 当前阻塞点不是代码能力，而是**真实业务地址、真实回调域名和业务侧配合**

## 8. 本地预演结果（2026-03-22）

以下结果基于本地 `mock` 链 + 本地 callback receiver 预演得到：

### 8.1 幂等预演

- 首次 callback 成功送达
- 重放相同 `X-WalletMonitor-Event-ID` 后，接收端识别为 `duplicate=true`

结论：

- **协议层与接收端示例实现已证明可按 `Event-ID` 做幂等**

### 8.2 验签预演

- 正确签名请求：`signature_valid=true`
- 错误签名重放：接收端返回 `401`

结论：

- **协议层与接收端示例实现已证明可做签名校验**

### 8.3 失败重试 / 死信预演

- 第一次失败后：任务进入 `retrying`
- 第二次失败后：任务进入 `dead`
- `dead/export?format=csv` 成功导出死信

结论：

- **重试、死信、导出链路本地预演通过**

### 8.4 本地假业务回调服务冒烟测试

已完成一轮本地端到端冒烟测试：

- 本地 `wallet_monitor` 测试实例发起 `mock` 入账扫描
- 本地假业务回调服务成功收到 callback
- 返回 `200`
- `signature_valid=true`
- `duplicate=false`

结果示例：

- `detected_txs=1`
- `queued_callbacks=1`
- `callbacks_sent=1`
- `failed_callbacks=0`
- `dead_callbacks=0`

结论：

- **监控服务 -> 本地假业务回调服务** 的最小成功闭环已经通过

### 8.5 当前仍未完成的部分

以下项目仍然不能因本地预演而直接勾选：

- 业务方已按 `X-WalletMonitor-Event-ID` 实现幂等
- 业务方已验证 `X-WalletMonitor-Signature`
- 至少验证过 1 次真实链路成功回调
- 至少验证过 1 次失败重试与死信导出（真实业务环境）

原因：

- 本次仅证明**本地协议与示例接收端**可行
- 还没有完成**真实业务系统**和**真实回调域名**的正式联调
