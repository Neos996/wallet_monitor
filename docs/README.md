# 文档目录

本目录用于说明 `wallet_monitor` 的能力边界、接入方式、部署要求、测试方法和安全要求。

## 建议阅读顺序

### 1. 先看整体
- [README.md](../README.md)：项目总览、启动方式、核心参数、主要接口
- [business_call_flow.md](./business_call_flow.md)：业务系统与监控服务的调用链路

### 2. 接入方重点阅读
- [API.md](./API.md)：管理接口与回调协议
- [TESTING.md](./TESTING.md)：本地闭环、TRON 真链、EVM 本地验证

### 3. 运维与上线前重点阅读
- [DEPLOYMENT.md](./DEPLOYMENT.md)：部署参数、Docker / systemd 示例
- [OBSERVABILITY.md](./OBSERVABILITY.md)：Prometheus 指标与告警建议
- [SECURITY.md](./SECURITY.md)：安全控制、风险项、上线检查清单
- [ENV_PRODUCTION_GUIDE.md](./ENV_PRODUCTION_GUIDE.md)：`.env.production` 填写说明
- [GO_LIVE_CHECKLIST.md](./GO_LIVE_CHECKLIST.md)：单机生产版上线前最终核对项
- [MANUAL_ACCEPTANCE_SOP.md](./MANUAL_ACCEPTANCE_SOP.md)：人工联调、恢复演练与灰度观察 SOP
- [INTEGRATION_4_2_RUNBOOK.md](./INTEGRATION_4_2_RUNBOOK.md)：业务方 4.2 联调执行单

### 4. 设计与实现细节
- [EVM_BLOCK_SCAN.md](./EVM_BLOCK_SCAN.md)：EVM ERC20 按区块日志扫描原理
- [monitor_requirements.md](./monitor_requirements.md)：需求、当前范围与后续演进边界
- [CODE_REFACTOR_PLAN.md](./CODE_REFACTOR_PLAN.md)：代码层去冗余与重构记录

## 文档分工

| 文档 | 受众 | 解决的问题 |
|---|---|---|
| `README.md` | 所有人 | 这个项目是什么，怎么启动，有哪些能力 |
| `API.md` | 接入方、后端 | 如何注册地址、如何接收回调、如何鉴权 |
| `DEPLOYMENT.md` | 运维、后端 | 生产环境怎么部署 |
| `OBSERVABILITY.md` | 运维、SRE | 如何监控、告警、排障 |
| `SECURITY.md` | 管理层、研发、运维 | 当前安全措施、风险点、整改要求 |
| `ENV_PRODUCTION_GUIDE.md` | 运维、负责人 | 生产环境变量怎么填 |
| `GO_LIVE_CHECKLIST.md` | 运维、负责人 | 当前还差哪些上线勾没有打上 |
| `MANUAL_ACCEPTANCE_SOP.md` | 运维、测试、业务方 | 预检脚本之外的人工验收步骤 |
| `INTEGRATION_4_2_RUNBOOK.md` | 运维、业务方、测试 | 如何完成 4.2 业务联调 |
| `TESTING.md` | 开发、测试 | 如何验证 mock / TRON / EVM 场景 |
| `business_call_flow.md` | 业务方、产品、研发 | 业务调用链路是什么 |
| `EVM_BLOCK_SCAN.md` | 研发 | EVM block 模式为什么这样设计 |
| `monitor_requirements.md` | 管理层、研发 | 需求边界、当前完成度、演进方向 |
| `CODE_REFACTOR_PLAN.md` | 研发 | 本轮代码层优化方案、优先级与完成状态 |

## 当前文档结论

`wallet_monitor` 当前已经具备以下能力：
- `mock` 本地闭环联调
- `tron` 已确认 `TRX` / `TRC20` 入账扫描
- `evm` 的 `ERC20` 入账扫描
- 回调持久化队列、指数退避重试、死信管理
- 管理接口鉴权与回调签名
- 请求级 `X-Request-ID`、扫描级 `scan_id`、回调级 `task_id`
- `/readyz` 就绪检查
- 列表接口可选分页
- SQLite WAL / busy_timeout / 连接数控制

**项目定位：它已经是一套可单机部署的入账监控服务，但当前仍处于单机版、MVP 阶段的生产实现，不是多实例高可用的最终形态。**

## 本轮优化状态

以下优化项已经落地并同步到文档：

- [x] 统一访问日志与请求追踪
- [x] 扫描 / 回调结构化关联日志
- [x] 扫描耗时与回调耗时 Histogram
- [x] `/readyz` 就绪检查
- [x] 扫描与回调分发互斥
- [x] 列表接口分页
- [x] SQLite 运行参数优化
- [x] 代码层去冗余重构

## 自动化辅助

当前仓库还提供了以下运维辅助工具：

- `.env.production.example`：生产环境变量模板
- `scripts/bootstrap_prod_env.sh`：复制模板、生成密钥并提示待填项
- `scripts/generate_secrets.sh`：生成 `ADMIN_TOKEN` / `CALLBACK_SECRET`
- `scripts/preflight.sh`：上线前自动化预检
- `scripts/preflight_from_compose.sh`：从 `docker-compose.yaml` / `.env` 推导预检参数
- `scripts/prod_workflow.sh`：生产配置检查、启动、预检的一条龙工作流
- `make bootstrap-prod-env`：初始化 `.env.production`
- `make generate-secrets`：写入生产 env 中的密钥占位
- `make preflight`：执行预检
- `make preflight-report`：执行预检并导出 Markdown / JSON 报告
- `make preflight-compose`：按 compose 配置执行预检
- `make preflight-compose-report`：按 compose 配置执行预检并导出报告
- `docker-compose.prod.yaml`：更收紧的生产 compose 配置
- `make compose-prod-up`：按生产 compose 启动
- `make prod-full`：执行生产工作流（配置检查 + 启动 + 报告预检）
