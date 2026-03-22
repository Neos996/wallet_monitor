# 部署说明

本文档说明如何在单机生产环境部署 `wallet_monitor`，并覆盖本轮已落地的运行时优化项。

## 1. 当前部署结论

当前版本已经具备以下运维增强能力：

- 访问日志 + `X-Request-ID`
- `/healthz` + `/readyz`
- 扫描互斥与回调分发互斥
- SQLite `WAL` / `busy_timeout` / 连接数控制
- 列表接口可选分页

**项目仍然定位为单机版服务。互斥机制用于避免单进程重入，不等同于多实例分布式调度。**

## 2. 核心参数

### 2.1 必配参数

| 参数 | 说明 | 建议 |
|---|---|---|
| `-admin-token` | 管理接口鉴权 | 生产必须配置 |
| `-callback-secret` | 回调签名密钥 | 生产必须配置 |
| `-callback-url` | 默认回调地址 | 可按地址覆盖 |
| `-scan-interval` | 扫描间隔 | TRON 建议 30s 起 |

### 2.2 可观测性与运行时参数

| 参数 | 说明 | 建议 |
|---|---|---|
| `-ready-max-scan-age` | `/readyz` 允许的最大扫描陈旧时间 | 默认 `2m` |
| `-ready-max-dead-tasks` | `/readyz` 允许的死信阈值 | 默认 `-1`（关闭） |
| `-sqlite-journal-mode` | SQLite journal mode | 默认 `WAL` |
| `-sqlite-busy-timeout` | SQLite busy timeout | 默认 `5s` |
| `-sqlite-max-open-conns` | SQLite 连接池上限 | 默认 `1` |
| `-callback-url-allowlist` | 允许的 callback host 白名单 | 生产强烈建议配置 |
| `-enable-debug-routes` | 是否注册 `/mock/*`、`/debug/*` | 生产必须设为 `false` |
| `-scan-workers` | 并发扫描地址数 | 默认 `4` |
| `-callback-workers` | 回调并发数 | 默认 `4` |
| `-callback-qps` | 回调全局限速 | 默认 `0`（不限制） |

完整参数列表见 `../README.md` 与 `docs/API.md`。

## 3. 启动示例

### 3.1 直接运行

```bash
cd wallet_monitor

env -u GOROOT -u GOPATH go run . \
  -listen :8080 \
  -db /var/lib/wallet_monitor/wallets.db \
  -rpc-url https://api.trongrid.io \
  -tron-api-key "${TRON_API_KEY}" \
  -callback-url https://business.example.com/wallet/callback \
  -admin-token "${ADMIN_TOKEN}" \
  -callback-secret "${CALLBACK_SECRET}" \
  -scan-interval 30s \
  -scan-workers 4 \
  -callback-workers 4 \
  -ready-max-scan-age 2m \
  -ready-max-dead-tasks -1 \
  -sqlite-journal-mode WAL \
  -sqlite-busy-timeout 5s \
  -sqlite-max-open-conns 1 \
  -callback-url-allowlist business.example.com,*.internal.example.com \
  -enable-debug-routes=false
```

### 3.2 Docker / Compose

项目根目录提供了 `Dockerfile`、`docker-compose.yaml` 与 `docker-compose.prod.yaml`。

建议先生成生产环境变量文件：

```bash
cp .env.production.example .env.production
```

变量填写说明见：

- `docs/ENV_PRODUCTION_GUIDE.md`

```bash
docker compose --env-file .env.production -f docker-compose.prod.yaml up -d --build
```

数据库目录建议挂载到持久化盘。

也可以使用：

```bash
make compose-prod-config ENV_FILE=.env.production
make compose-prod-up ENV_FILE=.env.production
```

## 4. systemd 示例

```ini
[Unit]
Description=wallet_monitor
After=network.target

[Service]
Type=simple
User=wallet_monitor
Group=wallet_monitor
WorkingDirectory=/opt/wallet_monitor
Environment="TRON_API_KEY=..."
Environment="ADMIN_TOKEN=..."
Environment="CALLBACK_SECRET=..."
ExecStart=/opt/wallet_monitor/wallet_monitor \
  -listen :8080 \
  -db /var/lib/wallet_monitor/wallets.db \
  -scan-interval 30s \
  -scan-workers 4 \
  -callback-workers 4 \
  -rpc-url https://api.trongrid.io \
  -tron-api-key "${TRON_API_KEY}" \
  -callback-url https://business.example.com/wallet/callback \
  -admin-token "${ADMIN_TOKEN}" \
  -callback-secret "${CALLBACK_SECRET}" \
  -ready-max-scan-age 2m \
  -ready-max-dead-tasks -1 \
  -sqlite-journal-mode WAL \
  -sqlite-busy-timeout 5s \
  -sqlite-max-open-conns 1 \
  -callback-url-allowlist business.example.com,*.internal.example.com \
  -enable-debug-routes=false
Restart=always
RestartSec=3
PrivateTmp=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/wallet_monitor
LimitNOFILE=1024
OOMScoreAdjust=-500

[Install]
WantedBy=multi-user.target
```

## 5. 健康检查建议

### 5.1 Kubernetes / 容器环境

- `livenessProbe`：使用 `GET /healthz`
- `readinessProbe`：使用 `GET /readyz`

建议：

- `livenessProbe` 保持简单，只判断进程存活
- `readinessProbe` 结合 `-ready-max-scan-age` 与死信阈值判断是否对外提供服务

### 5.2 反向代理

建议通过 Nginx / Caddy 转发，并保留：

- `X-Request-ID`
- `X-Forwarded-For`

这样可以把上游链路日志和服务内部日志串起来。

## 6. SQLite 建议

当前实现已默认支持以下优化：

- `journal_mode=WAL`
- `busy_timeout`
- 可配置 `max_open_conns`

推荐值：

- `-sqlite-journal-mode WAL`
- `-sqlite-busy-timeout 5s`
- `-sqlite-max-open-conns 1`

说明：

- `WAL` 适合当前读多写少的单机模型
- `busy_timeout` 可降低短暂锁竞争导致的立即失败
- `max_open_conns=1` 能减少 SQLite 文件锁争用
- `-callback-url-allowlist` 建议与业务回调域名保持一致
- `-enable-debug-routes=false` 建议作为生产默认值

## 7. 日志接入建议

服务默认输出 JSON 日志，建议：

- 使用 logrotate 或容器日志回收策略
- 接入 ELK / Loki / Datadog
- 以 `request_id`、`scan_id`、`task_id` 为检索键

建议重点关注：

- `http request completed`
- `scan completed`
- `scan address failed`
- `callback delivery failed`
- `callback delivery moved to dead state`

## 8. 部署检查清单

### 8.1 部署前

- [ ] 已配置 `-admin-token`
- [ ] 已配置 `-callback-secret`
- [ ] `-tron-api-key` 通过环境变量传递
- [ ] 监听地址仅绑定内网 IP
- [ ] 数据库文件权限为 `600`
- [ ] 已启用磁盘加密
- [ ] 已配置 `-ready-max-scan-age`
- [ ] 已配置 SQLite 参数（至少 `WAL` 与 `busy_timeout`）
- [ ] 已配置 `-callback-url-allowlist`
- [ ] 已配置 `-enable-debug-routes=false`
- [ ] 已接入 Prometheus
- [ ] 已接入日志平台

### 8.2 部署后

- [ ] `GET /healthz` 返回 `ok`
- [ ] `GET /readyz` 返回 `200`
- [ ] `GET /metrics` 需要鉴权
- [ ] 管理接口需要 `Authorization` 或 `X-Admin-Token`
- [ ] 任意请求返回 `X-Request-ID`
- [ ] 手动执行 `POST /scan/once` 返回 `X-Scan-ID`
- [ ] 业务方可按 `X-WalletMonitor-Event-ID` 做幂等

建议额外执行：

```bash
make preflight
make preflight-report
```

## 9. 常见问题

### 9.1 `scan skipped: already running`

原因：

- 定时扫描与人工扫描重叠
- 单轮扫描耗时过长

处理：

- 降低手动触发频率
- 排查 RPC 或数据库慢点
- 关注 `wallet_monitor_scan_skipped_total`

### 9.2 `callback dispatch skipped: already running`

原因：

- 自动分发与人工重试重叠
- 单批次回调耗时过长

处理：

- 关注 `wallet_monitor_callback_dispatch_skipped_total`
- 提升 `-callback-workers`
- 检查业务方接口延迟

### 9.3 `/readyz` 失败

常见原因：

- 数据库不可用
- 最近成功扫描时间过旧
- 死信任务超过阈值

处理：

- 查看 `/readyz` JSON 返回中的 `checks`
- 查看 `docs/OBSERVABILITY.md`
