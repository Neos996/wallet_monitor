# 部署说明（TRON 生产第一阶段）

## 1. 二进制部署（单机）

建议通过 flags 明确配置，并开启管理接口鉴权与回调验签：

```bash
./wallet_monitor \
  -listen :8080 \
  -db /var/lib/wallet_monitor/wallets.db \
  -scan-interval 30s \
  -scan-workers 4 \
  -rpc-url https://api.trongrid.io \
  -tron-api-key <TRON_PRO_API_KEY> \
  -callback-url https://business.example.com/wallet/callback \
  -callback-batch 100 \
  -callback-workers 4 \
  -callback-qps 0 \
  -callback-retry-4xx false \
  -callback-retry-statuses 409,425 \
  -admin-token <ADMIN_TOKEN> \
  -callback-secret <CALLBACK_SECRET>
```

说明：

- `-admin-token` 建议生产必开，并确保监听地址只对内网开放。
- `-callback-secret` 建议生产必开，业务方需做验签与幂等。
- `-tron-api-key` 建议生产必配，避免 TronGrid 429。

## 2. Docker / Compose

项目根目录提供了 `Dockerfile` 与 `docker-compose.yaml`，默认把数据库挂载到 `./data`。

```bash
export TRON_API_KEY=...
export CALLBACK_URL=https://business.example.com/wallet/callback
export ADMIN_TOKEN=...
export CALLBACK_SECRET=...

docker compose up --build
```

## 3. systemd 示例

```ini
[Unit]
Description=wallet_monitor
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/wallet_monitor
ExecStart=/opt/wallet_monitor/wallet_monitor -listen :8080 -db /var/lib/wallet_monitor/wallets.db -scan-interval 30s -scan-workers 4 -rpc-url https://api.trongrid.io -tron-api-key <TRON_PRO_API_KEY> -callback-url https://business.example.com/wallet/callback -callback-batch 100 -callback-workers 4 -callback-qps 0 -callback-retry-4xx false -callback-retry-statuses 409,425 -admin-token <ADMIN_TOKEN> -callback-secret <CALLBACK_SECRET>
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

生产环境建议：

- `WorkingDirectory`/数据库目录设置到持久化盘。
- 配合防火墙或反向代理限制管理接口访问来源。
- 监控 `/stats` 与日志中的 429/回调失败，及时调整扫描间隔与回调重试参数。
