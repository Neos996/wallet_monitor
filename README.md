# wallet_monitor

独立部署的钱包入账监控服务。

业务系统只需要做两件事：

1. 注册需要监控的收款地址（HTTP API）
2. 接收入账回调（HTTP POST）

监控服务负责：

- 周期性扫链
- 按确认数判断入账是否成立
- 幂等去重
- 回调持久化
- 失败重试与死信管理

## 当前定位

当前版本的准确定位是：

- **单机部署、可落地使用的入账监控服务**
- **适合作为单机生产版 / MVP 生产版上线**
- **不是最终态的多实例高可用平台**

当前明确不承诺：

- 多实例高可用
- 共享数据库下的分布式调度
- 深度链重组恢复
- BTC 等其他链适配

## 当前能力

当前版本支持：

- `chain=mock`：本地闭环联调
- `chain=tron`：`mainnet | shasta | nile` 的已确认 `TRX` / `TRC20` 入账扫描
- `chain=evm`：`ERC20` 入账扫描（支持 `address` 和 `block` 两种扫描模式）
- 最小确认数控制
- 回调持久化队列、指数退避重试、死信管理
- 幂等去重
- 可选回调签名（`-callback-secret`）
- 可选管理接口鉴权（`-admin-token`）
- `/readyz` 就绪检查
- 请求级 `X-Request-ID`
- 扫描级 `X-Scan-ID`
- Prometheus 指标
- callback URL 白名单（`-callback-url-allowlist`）
- 调试接口开关（`-enable-debug-routes=false`）

## 快速开始

### 1. 本地 mock 闭环

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
curl http://127.0.0.1:8080/readyz
```

### 2. TRON 真链联调

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

### 3. 单机生产版推荐流程

```bash
cd wallet_monitor

# 1) 初始化生产 env
make bootstrap-prod-env ENV_FILE=.env.production

# 2) 补完真实生产值
# - TRON_API_KEY
# - CALLBACK_URL
# - CALLBACK_URL_ALLOWLIST

# 3) 先 dry-run 看流程
make prod-full ENV_FILE=.env.production WORKFLOW_ARGS="--dry-run"

# 4) 正式执行
make prod-full ENV_FILE=.env.production
```

## 生产相关关键参数

| 参数 | 说明 | 生产建议 |
|---|---|---|
| `-admin-token` | 管理接口鉴权 | 必须配置，长度至少 32 字节 |
| `-callback-secret` | 回调签名密钥 | 必须配置，长度至少 32 字节 |
| `-tron-api-key` | TronGrid API key | TRON 生产场景必须配置 |
| `-callback-url-allowlist` | callback host 白名单 | 强烈建议配置 |
| `-enable-debug-routes` | 是否注册 `/mock/*`、`/debug/*` | 生产必须设为 `false` |
| `-ready-max-scan-age` | `/readyz` 扫描新鲜度阈值 | 默认 `2m` |
| `-sqlite-journal-mode` | SQLite journal mode | 建议 `WAL` |
| `-sqlite-busy-timeout` | SQLite busy timeout | 建议 `5s` |
| `-sqlite-max-open-conns` | SQLite 连接池上限 | 建议 `1` |
| `-scan-interval` | 扫描间隔 | TRON 建议 `30s` 起 |
| `-scan-workers` | 扫描并发 | 默认 `4` |
| `-callback-workers` | 回调并发 | 默认 `4` |

完整参数说明见 `docs/API.md` 与 `docs/DEPLOYMENT.md`。

## 主要接口

### 健康与可观测性

- `GET /healthz`：存活检查
- `GET /readyz`：就绪检查
- `GET /metrics`：Prometheus 指标
- `GET /stats`：统计信息

### 地址与扫描

- `POST /addresses`：注册监控地址
- `GET /addresses`：查询监控地址
- `GET /addresses/{id}`：查询单个地址
- `PATCH /addresses/{id}`：更新地址
- `DELETE /addresses/{id}`：删除地址
- `POST /addresses/{id}/enable`：启用地址
- `POST /addresses/{id}/disable`：禁用地址
- `POST /scan/once`：手动触发一轮扫描

### 回调任务

- `GET /callback-tasks`：查看任务
- `GET /callback-tasks/{id}`：查看单个任务
- `POST /callback-tasks/retry`：批量重试
- `POST /callback-tasks/{id}/retry`：单任务重试
- `GET /callback-tasks/dead/export`：导出死信

完整接口说明见 `docs/API.md`。

## 回调协议

业务方接收 HTTP POST 回调，Body 为 JSON，例如：

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

每次回调固定携带：

- `X-WalletMonitor-Event-ID`
- `X-WalletMonitor-Timestamp`
- `X-WalletMonitor-Signature`

业务方必须：

- 返回 `2xx` 表示成功
- 按 `X-WalletMonitor-Event-ID` 做幂等
- 验证签名（如启用）

完整协议见 `docs/API.md`。

## 当前上线状态

当前仓库层面已经完成：

- 生产 env 模板
- 自动生成密钥
- 自动预检脚本
- 从 compose 自动读取配置并预检
- 一条龙生产工作流脚本
- 4.1 服务可用性与鉴权验收
- 4.2 本地假业务回调预演

当前尚未自动完成、仍需真实业务/环境参与的内容：

- 真实 `TRON_API_KEY`
- 真实 `CALLBACK_URL`
- 真实 `CALLBACK_URL_ALLOWLIST`
- 真实业务方幂等与验签联调
- 真实链路成功回调验收

## 自动化辅助

当前仓库提供以下生产辅助工具：

- `.env.production.example`：生产环境变量模板
- `scripts/bootstrap_prod_env.sh`：复制模板、生成密钥并提示待填项
- `scripts/generate_secrets.sh`：生成 `ADMIN_TOKEN` / `CALLBACK_SECRET`
- `scripts/preflight.sh`：上线前自动化预检
- `scripts/preflight_from_compose.sh`：从 compose / env 推导预检参数
- `scripts/prod_workflow.sh`：配置检查、启动、预检的一条龙工作流
- `docker-compose.prod.yaml`：更收紧的生产 compose 配置
- `make bootstrap-prod-env`：初始化 `.env.production`
- `make generate-secrets`：写入生产 env 中的密钥占位
- `make preflight`：执行预检
- `make preflight-report`：导出预检报告
- `make preflight-compose-prod`：按生产 compose 做预检
- `make prod-full`：执行生产工作流

## 文档索引

### 接入与验证

- `docs/API.md`：接口与回调协议
- `docs/TESTING.md`：测试与验收
- `docs/INTEGRATION_4_2_RUNBOOK.md`：4.2 业务联调执行单

### 运维与上线

- `docs/DEPLOYMENT.md`：部署说明
- `docs/OBSERVABILITY.md`：监控与告警
- `docs/SECURITY.md`：安全要求与检查清单
- `docs/ENV_PRODUCTION_GUIDE.md`：`.env.production` 填写说明
- `docs/GO_LIVE_CHECKLIST.md`：单机生产版上线前最终核对项
- `docs/MANUAL_ACCEPTANCE_SOP.md`：人工联调、恢复演练与灰度观察 SOP

### 设计与演进

- `docs/business_call_flow.md`：业务调用流程
- `docs/monitor_requirements.md`：需求与范围
- `docs/EVM_BLOCK_SCAN.md`：EVM 扫描原理
- `docs/CODE_REFACTOR_PLAN.md`：代码重构与去冗余记录

完整文档导航见 `docs/README.md`。
