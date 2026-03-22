# 钱包监控服务安全文档

**文档版本**: 1.0
**编制日期**: 2026-03-21
**适用系统**: wallet_monitor（TRON/EVM 钱包入账监控服务）
**密级**: 内部

---

## 1. 系统概述

### 1.1 业务定位
wallet_monitor 是独立部署的区块链钱包入账监控服务，负责扫描 TRON/EVM 链上指定地址的入账交易，并通过 HTTP 回调通知业务系统。

### 1.2 核心功能
- 监控地址注册与管理（支持 TRX、TRC20、ERC20）
- 定时扫链与确认数判断
- 交易去重与幂等保证
- 回调任务队列与指数退避重试
- 管理接口鉴权与回调签名

### 1.3 部署架构
- **部署模式**: 单机部署（SQLite 本地存储）
- **网络边界**: 内网服务，仅对业务系统开放
- **外部依赖**: TronGrid API、EVM RPC 节点、业务方回调接口

---

## 2. 威胁模型

### 2.1 资产识别
| 资产类型 | 具体内容 | 机密性 | 完整性 | 可用性 |
|---------|---------|-------|-------|-------|
| 监控地址列表 | 业务收款地址、token 合约地址 | 高 | 高 | 高 |
| 交易数据 | 入账交易哈希、金额、发送方地址 | 中 | 高 | 高 |
| 回调凭证 | callback_secret、admin_token | 高 | 高 | 高 |
| 业务回调接口 | callback_url | 中 | 高 | 高 |
| 数据库文件 | wallets.db（包含所有监控状态） | 高 | 高 | 高 |

### 2.2 攻击面分析
| 攻击面 | 暴露点 | 威胁类型 |
|-------|-------|---------|
| HTTP 管理接口 | `/addresses`、`/scan/once`、`/callback-tasks` 等 | 未授权访问、数据泄露、恶意操作 |
| 回调发送 | 向业务方 callback_url 发送 POST 请求 | SSRF、信息泄露、中间人攻击 |
| 链上 RPC 依赖 | TronGrid API、EVM RPC 节点 | 数据投毒、DoS、API 密钥泄露 |
| 本地存储 | SQLite 数据库文件 | 文件泄露、篡改、删除 |
| 调试接口 | `/mock/transactions`、`/debug/callbacks` | 生产环境误用、数据污染 |

### 2.3 威胁场景
1. **未授权访问管理接口**: 攻击者通过内网渗透访问管理接口，注册恶意地址或篡改监控配置。
2. **回调劫持**: 攻击者篡改 callback_url，将入账通知重定向到恶意服务器。
3. **重放攻击**: 攻击者截获回调请求，重放至业务系统造成重复入账。
4. **SSRF 攻击**: 攻击者通过 callback_url 探测内网服务或发起攻击。
5. **数据库泄露**: 攻击者获取 wallets.db 文件，泄露所有监控地址与交易记录。
6. **API 密钥泄露**: tron-api-key 泄露导致配额滥用或 API 封禁。
7. **DoS 攻击**: 攻击者通过高频调用 `/scan/once` 或注册大量地址耗尽系统资源。

---

## 3. 已实施的安全措施

### 3.1 认证与授权
| 措施 | 实现方式 | 防护效果 |
|-----|---------|---------|
| 管理接口鉴权 | `-admin-token` 参数启用，支持 `Authorization: Bearer` 或 `X-Admin-Token` 头 | **强制**: 防止未授权访问管理接口 |
| 回调签名 | `-callback-secret` 参数启用，使用 HMAC-SHA256 签名（`timestamp + "." + payload`） | **强制**: 防止回调伪造与重放攻击 |
| 幂等键 | 每次回调携带 `X-WalletMonitor-Event-ID`（callback_task_id） | **强制**: 业务侧去重，防止重复处理 |

**代码位置**:
- 鉴权中间件: `main.go:829-852` (`requireAdmin`)
- 回调签名: `production.go:370-376` (`signCallbackPayload`)

### 3.2 数据完整性
| 措施 | 实现方式 | 防护效果 |
|-----|---------|---------|
| 交易去重 | `ProcessedTx` 表唯一索引（chain + network + address + asset_type + token_contract + tx_hash + log_index） | **强制**: 防止同一交易重复回调 |
| 回调任务唯一性 | `CallbackTask` 表唯一索引（同上） | **强制**: 防止重复入队 |
| 确认数判断 | `min_confirmations` 参数控制，只回调达到确认数的交易 | **可配置**: 防止链重组导致的错误通知 |

**代码位置**:
- 去重检查: `main.go:805-816` (`isProcessed`)
- 确认数计算: `production.go:96-105` (`calculateConfirmedCutoff`)

### 3.3 输入验证
| 措施 | 实现方式 | 防护效果 |
|-----|---------|---------|
| 地址格式校验 | TRON 地址 Base58Check 校验（`internal/tron/address.go`） | **强制**: 防止无效地址注册 |
| 参数类型校验 | JSON 反序列化自动校验类型 | **强制**: 防止类型混淆攻击 |
| SQL 注入防护 | 使用 GORM ORM，参数化查询 | **强制**: 防止 SQL 注入 |

**代码位置**:
- TRON 地址校验: `internal/tron/address.go:73-82` (`AddressToHex`)
- GORM 参数化查询: 全局使用 `Where("field = ?", value)` 模式

### 3.4 网络安全
| 措施 | 实现方式 | 防护效果 |
|-----|---------|---------|
| HTTP 超时控制 | 回调请求 10 秒超时，RPC 请求 20 秒超时 | **强制**: 防止慢速攻击与资源耗尽 |
| 限流保护 | `-tron-qps`、`-callback-qps` 参数控制全局 QPS | **可配置**: 防止 API 限流与回调风暴 |
| 429 重试 | `-tron-retry-429` 参数控制指数退避重试 | **可配置**: 应对 TronGrid 限流 |

**代码位置**:
- HTTP 超时: `main.go:479` (`httpClient`), `internal/tron/http.go:90-91`
- 限流器: `limiter.go`, `internal/tron/limiter.go`

### 3.5 日志与审计
| 措施 | 实现方式 | 防护效果 |
|-----|---------|---------|
| 结构化日志 | 使用 `slog` 输出 JSON 格式日志 | **强制**: 便于日志分析与告警 |
| 关键事件记录 | 扫描完成、回调失败、死信任务 | **强制**: 支持事后审计 |
| Prometheus 指标 | `/metrics` 接口暴露扫描与回调指标 | **强制**: 支持实时监控 |

**代码位置**:
- 指标定义: `metrics.go`
- 日志输出: 全局使用 `slog.Info/Error`

---

## 4. 风险点与缓解建议

### 4.1 高风险项（需立即处理）

#### 风险 1: 管理接口未强制鉴权
**现状**: `-admin-token` 为可选参数，默认不启用。
**影响**: 内网攻击者可直接访问管理接口，注册恶意地址、篡改配置、触发扫描。
**缓解措施**:
- **强制要求**: 生产环境必须配置 `-admin-token`，建议使用 32 字节随机字符串。
- **部署检查**: 在启动脚本中检查 `-admin-token` 是否为空，为空则拒绝启动。
- **网络隔离**: 确保监听地址仅绑定内网 IP，禁止公网访问。

#### 风险 2: 回调签名未强制启用
**现状**: `-callback-secret` 为可选参数，默认不启用。
**影响**: 业务方无法验证回调来源，攻击者可伪造回调请求。
**缓解措施**:
- **强制要求**: 生产环境必须配置 `-callback-secret`，建议使用 32 字节随机字符串。
- **业务侧验签**: 业务方必须实现 HMAC-SHA256 验签逻辑，验签失败返回非 2xx。
- **时间戳校验**: 业务方应检查 `X-WalletMonitor-Timestamp`，拒绝超过 5 分钟的请求。

#### 风险 3: 数据库文件无加密
**现状**: SQLite 数据库文件明文存储，包含所有监控地址与交易记录。
**影响**: 攻击者获取文件后可直接读取敏感数据。
**缓解措施**:
- **文件权限**: 设置 `wallets.db` 文件权限为 `600`（仅 owner 可读写）。
- **磁盘加密**: 使用操作系统级磁盘加密（如 LUKS、BitLocker）。
- **备份加密**: 数据库备份必须加密存储，密钥独立管理。

#### 风险 4: API 密钥硬编码风险
**现状**: `-tron-api-key` 通过命令行参数传递，可能被 `ps` 命令泄露。
**影响**: 攻击者获取 API 密钥后可滥用配额或发起攻击。
**缓解措施**:
- **环境变量**: 改用环境变量传递（如 `TRON_API_KEY`），避免命令行泄露。
- **密钥轮换**: 定期轮换 TronGrid API 密钥（建议每季度一次）。
- **权限最小化**: 使用只读权限的 API 密钥（如 TronGrid 支持）。

### 4.2 中风险项（建议处理）

#### 风险 5: SSRF 攻击风险
**现状**: `callback_url` 由业务方提供，服务会向该 URL 发送 POST 请求。
**影响**: 攻击者可通过恶意 callback_url 探测内网服务或发起攻击。
**缓解措施**:
- **URL 白名单**: 限制 callback_url 只能使用预定义的域名或 IP 段。
- **协议限制**: 禁止 `file://`、`ftp://` 等非 HTTP(S) 协议。
- **内网隔离**: 禁止回调至 `127.0.0.1`、`169.254.0.0/16`、`10.0.0.0/8` 等内网地址。
- **代码实现**: 在 `createAddress` 中增加 URL 校验逻辑。

#### 风险 6: 调试接口生产误用
**现状**: `/mock/transactions`、`/debug/callbacks` 接口未隔离。
**影响**: 生产环境误用可能导致数据污染或信息泄露。
**缓解措施**:
- **编译时隔离**: 使用 Go build tag 将调试接口编译为独立二进制。
- **运行时检查**: 增加 `-enable-debug` 参数，默认禁用调试接口。
- **网络隔离**: 通过防火墙规则禁止生产环境访问调试接口。

#### 风险 7: 回调重试风暴
**现状**: 回调失败会无限重试（最多 5 次），可能导致业务方过载。
**影响**: 业务方服务不可用时，回调任务堆积导致资源耗尽。
**缓解措施**:
- **熔断机制**: 增加回调失败率熔断，连续失败超过阈值时暂停回调。
- **死信告警**: 监控 `wallet_monitor_callback_tasks{status="dead"}` 指标，及时告警。
- **手动重试**: 通过 `/callback-tasks/{id}/retry` 接口手动恢复。

### 4.3 低风险项（可选处理）

#### 风险 8: TLS 证书校验
**现状**: HTTP 客户端未显式配置 TLS 证书校验。
**影响**: 中间人攻击风险（Go 默认启用证书校验，风险较低）。
**缓解措施**:
- **显式配置**: 在 `http.Client` 中显式设置 `TLSClientConfig`，禁用 `InsecureSkipVerify`。
- **证书固定**: 对关键 RPC 节点（如自建节点）启用证书固定。

#### 风险 9: 日志敏感信息泄露
**现状**: 日志中可能包含完整的回调 payload（包含金额、地址）。
**影响**: 日志泄露可能导致业务数据泄露。
**缓解措施**:
- **日志脱敏**: 对日志中的金额、地址进行脱敏（如只显示前 6 位）。
- **日志权限**: 限制日志文件访问权限为 `600`。
- **日志轮转**: 配置日志轮转与自动清理（建议保留 30 天）。

---

## 5. 合规性考量

### 5.1 数据保护
| 合规要求 | 当前状态 | 建议措施 |
|---------|---------|---------|
| 个人数据最小化 | ✅ 仅存储业务必需的地址与交易哈希 | 无需额外措施 |
| 数据加密存储 | ❌ 数据库明文存储 | 启用磁盘加密 |
| 数据访问控制 | ⚠️ 依赖文件权限 | 增加数据库访问审计 |
| 数据删除权 | ✅ 支持 `DELETE /addresses/{id}` | 增加级联删除逻辑（删除地址时同步删除相关交易记录） |

### 5.2 审计与日志
| 合规要求 | 当前状态 | 建议措施 |
|---------|---------|---------|
| 操作审计 | ⚠️ 仅记录扫描与回调事件 | 增加管理接口操作审计（谁在何时注册/删除了哪个地址） |
| 日志完整性 | ❌ 无防篡改机制 | 集成日志中心（如 ELK、Loki），启用日志签名 |
| 日志保留期 | ❌ 无自动清理 | 配置日志轮转，保留 30-90 天 |

### 5.3 业务连续性
| 合规要求 | 当前状态 | 建议措施 |
|---------|---------|---------|
| 数据备份 | ❌ 无自动备份 | 配置定时备份（每日增量 + 每周全量） |
| 灾难恢复 | ❌ 无恢复预案 | 编写恢复手册，定期演练 |
| 高可用部署 | ❌ 单机部署 | 后续版本支持多实例 + 共享数据库 |

---

## 6. 运维安全建议

### 6.1 部署清单
**必须执行**:
- [ ] 配置 `-admin-token`（32 字节随机字符串）
- [ ] 配置 `-callback-secret`（32 字节随机字符串）
- [ ] 使用环境变量传递 `-tron-api-key`
- [ ] 设置 `wallets.db` 文件权限为 `600`
- [ ] 确保监听地址仅绑定内网 IP（如 `127.0.0.1:8080` 或内网 IP）
- [ ] 配置防火墙规则，禁止公网访问
- [ ] 启用磁盘加密（LUKS/BitLocker）

**建议执行**:
- [ ] 配置 `-callback-url-allowlist`
- [ ] 配置 `-enable-debug-routes=false`，禁用调试接口（`/mock/*`、`/debug/*`）
- [ ] 配置日志轮转与自动清理
- [ ] 集成 Prometheus 监控与告警
- [ ] 配置数据库定时备份

### 6.2 监控与告警
**关键指标**:
- `wallet_monitor_last_scan_timestamp`: 扫描卡住（超过 2 分钟未更新）
- `wallet_monitor_callback_tasks{status="dead"}`: 死信任务积压（> 0）
- `wallet_monitor_scan_failed_callbacks_total`: 回调失败飙升（5 分钟内 > 5 次）
- `wallet_monitor_callback_tasks{status="pending"}`: 回调堆积增长（10 分钟内持续增长）

**告警规则**（PromQL）:
```promql
# 扫描卡住
time() - wallet_monitor_last_scan_timestamp > 120

# 死信积压
wallet_monitor_callback_tasks{status="dead"} > 0

# 回调失败飙升
increase(wallet_monitor_scan_failed_callbacks_total[5m]) > 5

# 回调堆积增长
increase(wallet_monitor_callback_tasks{status="pending"}[10m]) > 0
```

### 6.3 应急响应
**场景 1: 管理接口未授权访问**
1. 立即重启服务并配置 `-admin-token`
2. 检查 `/addresses` 接口是否有异常地址注册
3. 检查 `wallets.db` 文件是否被篡改（对比备份）
4. 分析访问日志，定位攻击来源

**场景 2: 回调签名泄露**
1. 立即轮换 `-callback-secret`
2. 通知业务方更新验签密钥
3. 检查 `CallbackTask` 表是否有异常任务
4. 分析日志，定位泄露途径

**场景 3: 数据库文件泄露**
1. 立即隔离受影响服务器
2. 评估泄露范围（哪些地址、交易记录）
3. 通知业务方风险地址，建议更换
4. 从备份恢复数据库，重新部署

**场景 4: API 密钥泄露**
1. 立即在 TronGrid 控制台撤销密钥
2. 生成新密钥并更新配置
3. 检查 API 调用日志，定位异常请求
4. 评估是否需要更换监控地址

### 6.4 安全加固
**操作系统层**:
- 禁用不必要的服务与端口
- 配置 iptables/firewalld 规则，仅允许必要流量
- 启用 SELinux/AppArmor 强制访问控制
- 定期更新操作系统补丁

**应用层**:
- 使用非 root 用户运行服务
- 配置 systemd 沙箱（`PrivateTmp=yes`、`NoNewPrivileges=yes`）
- 限制文件描述符数量（`LimitNOFILE=1024`）
- 配置 OOM 保护（`OOMScoreAdjust=-500`）

**网络层**:
- 使用反向代理（Nginx/Caddy）处理 HTTPS
- 配置 TLS 1.3 + 强加密套件
- 启用 HSTS、CSP 等安全头
- 配置 rate limiting 防止 DoS

---

## 7. 安全检查清单

### 7.1 部署前检查
- [ ] 已配置 `-admin-token` 且长度 ≥ 32 字节
- [ ] 已配置 `-callback-secret` 且长度 ≥ 32 字节
- [ ] `-tron-api-key` 通过环境变量传递
- [ ] 监听地址仅绑定内网 IP
- [ ] 数据库文件权限为 `600`
- [ ] 已启用磁盘加密
- [ ] 已配置防火墙规则
- [ ] 已配置 `-callback-url-allowlist`
- [ ] 已配置 `-enable-debug-routes=false`
- [ ] 已配置日志轮转
- [ ] 已集成 Prometheus 监控

### 7.2 运行时检查
- [ ] `/healthz` 接口正常响应
- [ ] `/metrics` 接口需要鉴权
- [ ] 管理接口需要 `Authorization` 头
- [ ] 回调请求携带 `X-WalletMonitor-Signature` 头
- [ ] 业务方验签成功
- [ ] 无死信任务积压
- [ ] 扫描间隔正常（无卡住）
- [ ] 回调成功率 > 95%

### 7.3 定期检查（每季度）
- [ ] 轮换 `-admin-token`
- [ ] 轮换 `-callback-secret`
- [ ] 轮换 `-tron-api-key`
- [ ] 检查数据库备份完整性
- [ ] 演练灾难恢复流程
- [ ] 审计访问日志
- [ ] 更新操作系统补丁
- [ ] 更新 Go 依赖库

---

## 8. 附录

### 8.1 密钥生成示例
```bash
# 生成 32 字节随机字符串（Base64 编码）
openssl rand -base64 32

# 生成 32 字节随机字符串（Hex 编码）
openssl rand -hex 32
```

### 8.2 回调验签示例（Go）
```go
func verifyCallback(secret, timestamp, payload, signature string) bool {
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(timestamp))
    mac.Write([]byte("."))
    mac.Write([]byte(payload))
    expected := hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(expected), []byte(signature))
}
```

### 8.3 systemd 沙箱配置示例
```ini
[Service]
User=wallet_monitor
Group=wallet_monitor
PrivateTmp=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/wallet_monitor
LimitNOFILE=1024
OOMScoreAdjust=-500
```

### 8.4 参考文档
- [OWASP Top 10](https://owasp.org/www-project-top-ten/)
- [CWE Top 25](https://cwe.mitre.org/top25/)
- [NIST Cybersecurity Framework](https://www.nist.gov/cyberframework)
- [TronGrid API 文档](https://developers.tron.network/docs/trongrid)

---

**文档结束**
