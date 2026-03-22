# `.env.production` 填写指南

本文档说明如何基于 `.env.production.example` 生成真正可用于单机生产版上线的 `.env.production`。

## 1. 推荐流程

```bash
cd wallet_monitor
cp .env.production.example .env.production
```

然后按下面的说明逐项填写。

如果想先自动生成安全随机值：

```bash
chmod +x scripts/generate_secrets.sh
scripts/generate_secrets.sh --env-file .env.production
```

或直接使用：

```bash
make generate-secrets ENV_FILE=.env.production
```

如果想一键完成“复制模板 + 生成密钥 + 提示待填项”：

```bash
chmod +x scripts/bootstrap_prod_env.sh
scripts/bootstrap_prod_env.sh --env-file .env.production
```

或：

```bash
make bootstrap-prod-env ENV_FILE=.env.production
```

## 2. 字段说明

### 2.1 必填密钥

#### `ADMIN_TOKEN`

- 用途：保护管理接口
- 是否必填：是
- 要求：长度至少 32 字节，建议使用随机字符串

示例生成方式：

```bash
openssl rand -hex 32
```

#### `CALLBACK_SECRET`

- 用途：生成 `X-WalletMonitor-Signature`
- 是否必填：是
- 要求：长度至少 32 字节，必须与业务方验签配置保持一致

#### `TRON_API_KEY`

- 用途：TronGrid 生产访问
- 是否必填：TRON 生产场景必填
- 要求：使用真实生产 key，不要使用测试 key

### 2.2 网络与回调

#### `HOST_PORT`

- 用途：宿主机暴露端口
- 是否必填：建议填写
- 建议：保持 `8080`，并通过反向代理转发

#### `CALLBACK_URL`

- 用途：默认回调地址
- 是否必填：通常必填
- 要求：必须是业务方真实接收地址

#### `CALLBACK_URL_ALLOWLIST`

- 用途：限制允许配置的 callback host
- 是否必填：生产强烈建议必填
- 支持格式：
  - 精确 host：`wallet-callback.internal.example.com`
  - 通配 host：`*.internal.example.com`

示例：

```dotenv
CALLBACK_URL_ALLOWLIST=wallet-callback.internal.example.com,*.internal.example.com
```

### 2.3 链 RPC

#### `RPC_URL`

- 用途：TRON RPC 地址
- 默认：`https://api.trongrid.io`
- 建议：如有企业级网关或私有代理，请替换为内部地址

#### `EVM_RPC_URL`

- 用途：EVM 扫描
- 是否必填：仅当你启用 `chain=evm` 时必填

### 2.4 扫描与回调并发

#### `SCAN_INTERVAL`

- 用途：扫描周期
- 建议起点：`30s`
- 建议：地址量较大或 RPC 限流明显时适当调大

#### `SCAN_WORKERS`

- 用途：扫描并发
- 建议起点：`4`

#### `CALLBACK_BATCH`

- 用途：单轮扫描后最多处理多少 callback task
- 建议起点：`100`

#### `CALLBACK_WORKERS`

- 用途：回调并发
- 建议起点：`4`

#### `CALLBACK_QPS`

- 用途：回调全局限速
- 默认：`0`（不限制）
- 建议：如果业务方有限流，按业务方 SLA 调整

### 2.5 重试策略

#### `CALLBACK_RETRY_4XX`

- 默认：`false`
- 建议：保持默认，除非你明确知道某些 4xx 需要重试

#### `CALLBACK_RETRY_STATUSES`

- 用途：指定总是重试的 HTTP 状态码
- 示例：`409,425`

### 2.6 TRON 限流

#### `TRON_QPS`

- 默认：`8`
- 建议：根据 TronGrid 套餐和实际 429 情况调整

#### `TRON_RETRY_429`

- 默认：`3`
- 建议：若公网 TronGrid 429 偏多，可适当上调

### 2.7 就绪检查

#### `READY_MAX_SCAN_AGE`

- 默认：`2m`
- 含义：最近成功扫描超过该时间则 `/readyz` 失败

#### `READY_MAX_DEAD_TASKS`

- 默认：`-1`
- 含义：关闭死信阈值检查
- 建议：上线稳定后可改为 `0`

### 2.8 SQLite

#### `SQLITE_JOURNAL_MODE`

- 建议：`WAL`

#### `SQLITE_BUSY_TIMEOUT`

- 建议：`5s`

#### `SQLITE_MAX_OPEN_CONNS`

- 建议：`1`

### 2.9 生产安全

#### `ENABLE_DEBUG_ROUTES`

- 生产必须：`false`

## 3. 推荐起始配置

```dotenv
ADMIN_TOKEN=<32+ random chars>
CALLBACK_SECRET=<32+ random chars>
TRON_API_KEY=<real production key>
HOST_PORT=8080
CALLBACK_URL=https://wallet-callback.internal.example.com/wallet/callback
CALLBACK_URL_ALLOWLIST=wallet-callback.internal.example.com,*.internal.example.com
RPC_URL=https://api.trongrid.io
EVM_RPC_URL=
SCAN_INTERVAL=30s
SCAN_WORKERS=4
CALLBACK_BATCH=100
CALLBACK_WORKERS=4
CALLBACK_QPS=0
CALLBACK_RETRY_4XX=false
CALLBACK_RETRY_STATUSES=
TRON_QPS=8
TRON_RETRY_429=3
READY_MAX_SCAN_AGE=2m
READY_MAX_DEAD_TASKS=-1
SQLITE_JOURNAL_MODE=WAL
SQLITE_BUSY_TIMEOUT=5s
SQLITE_MAX_OPEN_CONNS=1
ENABLE_DEBUG_ROUTES=false
```

## 4. 填写完成后怎么做

### 4.1 检查 compose 配置

```bash
make compose-prod-config ENV_FILE=.env.production
```

### 4.2 启动服务

```bash
make compose-prod-up ENV_FILE=.env.production
```

或者使用一条龙工作流：

```bash
scripts/prod_workflow.sh up --env-file .env.production
```

### 4.3 执行自动预检

```bash
make preflight-compose-prod ENV_FILE=.env.production
```

### 4.4 导出预检报告

```bash
make preflight-compose-prod-report ENV_FILE=.env.production
```

### 4.5 一条龙执行

```bash
make prod-full ENV_FILE=.env.production
```

说明：

- 会依次执行 compose 配置检查、启动、预检报告导出
- 完成后仍需继续执行 `docs/MANUAL_ACCEPTANCE_SOP.md`

如果只想预览命令顺序：

```bash
make prod-full ENV_FILE=.env.production WORKFLOW_ARGS="--dry-run"
```

### 4.6 继续做人工验收

参考：

- `docs/GO_LIVE_CHECKLIST.md`
- `docs/MANUAL_ACCEPTANCE_SOP.md`

## 5. 常见错误

### 5.1 `ADMIN_TOKEN` / `CALLBACK_SECRET` 太短

现象：

- 预检脚本直接报 `expected >= 32`

处理：

- 重新生成随机值

### 5.2 `CALLBACK_URL_ALLOWLIST` 没覆盖真实域名

现象：

- 创建地址或更新地址时报 `callback_url host ... is not in allowlist`

处理：

- 把真实业务回调域名加入 allowlist

### 5.3 `ENABLE_DEBUG_ROUTES=true`

现象：

- 预检发现调试接口仍可访问

处理：

- 生产环境改成 `false`
