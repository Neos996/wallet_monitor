# 单机生产版上线清单

本文档用于回答一个更具体的问题：

**如果当前目标是“单机生产版上线”，还有哪些勾没打上，以及每一项怎么验证？**

## 1. 当前判断

结论：

- 代码层已经具备单机生产版所需的核心能力
- 但大部分上线清单仍依赖**实际部署环境、配置值、业务侧联调和线上验收**

因此，当前状态应理解为：

- **代码 ready**
- **环境与验收未全部 ready**

## 2.1 快速执行

仓库已提供一个可自动化检查部分项目的脚本：

```bash
cd wallet_monitor
make bootstrap-prod-env ENV_FILE=.env.production
chmod +x scripts/preflight.sh

WM_BASE_URL=http://127.0.0.1:8080 \
WM_ADMIN_TOKEN='<ADMIN_TOKEN>' \
WM_CALLBACK_SECRET='<CALLBACK_SECRET>' \
WM_TRON_API_KEY='<TRON_API_KEY>' \
WM_DB_PATH='/var/lib/wallet_monitor/wallets.db' \
scripts/preflight.sh
```

或直接使用：

```bash
make preflight
```

需要传自定义参数时：

```bash
make preflight PRECHECK_ARGS="--base-url http://127.0.0.1:8080 --db /var/lib/wallet_monitor/wallets.db"
```

说明：

- 这个脚本会自动检查鉴权、健康检查、`X-Request-ID`、`X-Scan-ID`、调试接口是否关闭、数据库权限等项目
- 它不能替代业务方的幂等/验签联调，也不能替代人工确认防火墙、磁盘加密和备份演练

如需导出报告：

```bash
scripts/preflight.sh \
  --report-md ./preflight-report.md \
  --report-json ./preflight-report.json
```

或直接使用：

```bash
make preflight-report
```

人工验收部分请继续执行 `docs/MANUAL_ACCEPTANCE_SOP.md`。

如果你本身就是通过 `docker-compose.yaml` 部署，并且不想手工把 compose 里的参数再抄一遍，可以直接使用：

```bash
chmod +x scripts/preflight_from_compose.sh
scripts/preflight_from_compose.sh --env-file .env.production
```

或：

```bash
make preflight-compose PRECHECK_ARGS="--env-file .env.production"
make preflight-compose-report PRECHECK_ARGS="--env-file .env.production"
```

如果你已经切到生产 compose：

```bash
make compose-prod-config ENV_FILE=.env.production
make compose-prod-up ENV_FILE=.env.production
make preflight-compose-prod ENV_FILE=.env.production
make preflight-compose-prod-report ENV_FILE=.env.production
```

如果你想用“一条龙工作流”脚本：

```bash
chmod +x scripts/prod_workflow.sh
scripts/prod_workflow.sh full --env-file .env.production
```

或直接使用：

```bash
make prod-full ENV_FILE=.env.production
```

如果只想看将执行什么命令，不要真实启动：

```bash
make prod-full ENV_FILE=.env.production WORKFLOW_ARGS="--dry-run"
```

## 2. 当前仓库已可直接确认

以下项目已经可以根据当前代码仓库直接打勾：

- [x] 管理接口鉴权能力（`-admin-token`）
- [x] 回调签名能力（`-callback-secret`）
- [x] `/readyz` 就绪检查
- [x] `/metrics` 走管理鉴权
- [x] `X-Request-ID` 请求追踪
- [x] `X-Scan-ID` 扫描追踪
- [x] SQLite `WAL` / `busy_timeout` / `max_open_conns` 参数支持
- [x] `callback_url` 白名单能力（`-callback-url-allowlist`）
- [x] 调试接口开关（`-enable-debug-routes=false` 可禁用 `/mock/*` 与 `/debug/*`）

## 3. 上线阻塞项

以下项目如果没打勾，**不建议直接上线**。

### 3.1 配置与主机

验证方式建议：

- 检查 systemd / compose / k8s manifest 的最终启动参数
- 登录主机确认数据库文件权限和磁盘策略

- [ ] 已配置 `-admin-token`，且长度不少于 32 字节
- [ ] 已配置 `-callback-secret`，且长度不少于 32 字节
- [ ] `-tron-api-key` 已通过环境变量注入
- [ ] 已配置 `-ready-max-scan-age`
- [ ] 已配置 `-sqlite-journal-mode WAL`
- [ ] 已配置 `-sqlite-busy-timeout`
- [ ] 已配置 `-callback-url-allowlist`
- [ ] 已配置 `-enable-debug-routes=false`
- [ ] 数据库文件权限为 `600`
- [ ] 已启用磁盘加密

建议命令：

```bash
ps -ef | grep wallet_monitor
ls -l /var/lib/wallet_monitor/wallets.db
```

### 3.2 网络与安全

验证方式建议：

- 检查监听地址
- 检查防火墙 / 安全组 / 反向代理规则
- 做一次非白名单 callback URL 的负向测试

- [ ] 监听地址仅绑定内网 IP
- [ ] 已配置防火墙，禁止公网直接访问服务
- [ ] 已通过网关或运维配置禁用调试接口访问
- [ ] 已确认 callback 目标域名在 allowlist 中

建议命令：

```bash
ss -ltnp | grep 8080
curl -i http://<host>:8080/mock/transactions
```

### 3.3 运维接入

验证方式建议：

- Prometheus target 为 `UP`
- Grafana / 日志平台可检索 `request_id`、`scan_id`、`task_id`
- 已存在自动备份和恢复说明

- [ ] 已接入 Prometheus 抓取 `/metrics`
- [ ] 已配置告警规则（扫描卡住、死信、回调失败、回调延迟）
- [ ] 已接入日志平台，可按 `request_id` / `scan_id` / `task_id` 检索
- [ ] 已配置数据库定时备份
- [ ] 已做至少一次恢复演练

## 4. 上线验收项

以下项目建议在预发或正式环境上线前逐项执行并打勾。

### 4.1 服务可用性与鉴权

当前状态（2026-03-22，本机通过 `docker-compose.prod.yaml` 验证）：

建议命令：

```bash
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
curl -i http://127.0.0.1:8080/metrics
curl -i http://127.0.0.1:8080/addresses
curl -i -X POST http://127.0.0.1:8080/scan/once -H 'Authorization: Bearer <ADMIN_TOKEN>'
```

- [x] `GET /healthz` 返回 `ok`
- [x] `GET /readyz` 返回 `200`
- [x] `GET /metrics` 未带 token 时无法访问
- [x] 管理接口未带 token 时返回 `401`
- [x] 任意请求返回 `X-Request-ID`
- [x] 手动 `POST /scan/once` 返回 `X-Scan-ID`

### 4.2 业务联调

以下项目需要业务接收方配合，不能只看监控服务本身。

执行参考：

- `docs/INTEGRATION_4_2_RUNBOOK.md`

当前状态（2026-03-22）：

- 本地 `mock` + 本地 callback receiver 预演已完成
- 已本地验证：协议幂等可行、签名校验可行、重试/死信/导出可行
- 但以下四项**仍然必须由真实业务方联调后再勾选**

- [ ] 业务方已按 `X-WalletMonitor-Event-ID` 实现幂等
- [ ] 业务方已验证 `X-WalletMonitor-Signature`
- [ ] 至少验证过 1 次真实链路成功回调
- [ ] 至少验证过 1 次失败重试与死信导出

### 4.3 运行观察

建议观察窗口：

- 小流量灰度至少 1 天
- 正式放量后重点观察前 1 周

- [ ] 当前无死信任务积压
- [ ] 最近扫描时间正常推进
- [ ] 回调成功率达到目标（建议 > 95%）
- [ ] 无明显 RPC 429 / callback 非 2xx 持续飙升

## 5. 建议但可短期延期项

以下项目不建议长期缺失，但如果当前目标只是“小流量单机上线”，可以短期延期：

- [ ] 完成数据库恢复演练文档固化
- [ ] 补充值班手册（死信处理、扫描卡住、429 处理）
- [ ] 完成季度密钥轮换流程
- [ ] 完成 callback 目标域名变更审批流程

## 6. Go / No-Go 规则

### 可以 `GO`

满足以下条件时，可以按“单机生产版”上线：

- 第 3 节所有项目已完成
- 第 4.1 节与第 4.2 节已完成
- 监控、告警、备份至少已具备基础能力

### 暂时 `NO-GO`

出现以下任一情况时，不建议上线：

- 未开启 `-admin-token`
- 未开启 `-callback-secret`
- 未设置 `-enable-debug-routes=false`
- 未配置 `-callback-url-allowlist`
- 无监控、无日志、无备份
- 业务方未完成验签与幂等

## 7. 一句话结论

`wallet_monitor` 当前已经具备**单机生产版**上线所需的代码基础；真正还没打勾的，主要是**部署环境、安全基线、业务侧配合和上线验收**。
