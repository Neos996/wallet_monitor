# 代码层优化与去冗余计划

本文档记录 `wallet_monitor` 代码层的收敛方案，目标是在**不改变现有业务行为**的前提下，减少重复逻辑、降低维护成本并提升后续迭代效率。

## 1. 优化目标

本轮代码层优化聚焦以下问题：

- 同一类分页查询在多个 handler 中重复实现
- 扫描结果与 worker 数量计算在多个执行路径中重复实现
- 扫描入账后“去重 → 解析回调地址 → 入队”逻辑存在重复
- 回调任务重试流程在单任务 / 批量任务接口中重复

## 2. 优先级

### P1：收敛交易入队与回调重试逻辑

状态：`completed`

目标：

- 提取统一的“交易入队” helper
- 提取统一的 callback task 重试 helper

预期收益：

- 减少 `scanOneAddress` 与 `scanEVMLogBucket` 的重复分支
- 减少单任务 / 批量重试接口的重复代码
- 降低未来修改回调策略时的遗漏风险

### P2：收敛分页查询逻辑

状态：`completed`

目标：

- 提取统一的分页查询与响应 helper

覆盖范围：

- `GET /addresses`
- `GET /callback-tasks`
- `GET /mock/transactions`
- `GET /debug/callbacks`

预期收益：

- 减少 count / query / headers / JSON 输出的重复代码
- 统一分页行为，避免后续接口行为漂移

### P3：收敛 worker 计算与结果聚合逻辑

状态：`completed`

目标：

- 提取统一的 worker 数量规范化 helper
- 提取统一的扫描结果 / 地址结果聚合 helper

预期收益：

- 减少扫描主流程、EVM bucket 扫描和回调分发中的重复累加代码
- 让并发控制与结果合并更集中、更易验证

### P4：收敛路径参数解析与按 ID 查询逻辑

状态：`completed`

目标：

- 提取统一的路径参数解析 helper
- 提取统一的“按 ID 查询 + not found / db error 响应” helper

覆盖范围：

- `GET/PATCH/DELETE /addresses/{id}`
- `GET/POST /callback-tasks/{id}`

预期收益：

- 减少 `strings.TrimPrefix` / `strings.Split` / `strconv.ParseUint` 重复代码
- 统一非法 ID、资源不存在和数据库异常的处理方式

### P5：收敛回调任务 CSV 导出逻辑

状态：`completed`

目标：

- 提取 callback task CSV 写出 helper

预期收益：

- 缩短 handler 体积
- 降低后续导出字段调整时的遗漏风险

### P6：收敛 method guard 与方法分发逻辑

状态：`completed`

目标：

- 提取统一的单方法校验 helper
- 提取统一的多方法分发 helper

覆盖范围：

- `GET /readyz`
- `POST /scan/once`
- `GET /metrics`
- `GET /stats`
- `GET/POST/DELETE` 风格资源路由

预期收益：

- 减少 `if r.Method != ...` 与 `switch r.Method` 的重复
- 统一 `405 Method Not Allowed` 行为

### P7：收敛路由注册与 stats 统计组装逻辑

状态：`completed`

目标：

- 提取统一的路由注册 helper
- 提取统一的 stats count helper

预期收益：

- `main` 更聚焦启动流程
- `collectStats` 更数据驱动，降低字段新增时的重复修改成本

### P8：收敛请求解码与基础参数校验逻辑

状态：`completed`

目标：

- 提取统一的 JSON body 解码 helper
- 提取基础必填字段校验 helper

覆盖范围：

- 创建地址
- 创建 mock 交易
- 更新地址

预期收益：

- 减少 `json.NewDecoder(...).Decode(...)` 与 `http.Error(..., 400)` 的重复
- 统一坏请求处理方式

### P9：收敛地址与 mock 交易的参数归一化逻辑

状态：`completed`

目标：

- 提取地址创建默认值 / 规范化 helper
- 提取地址更新 `updates map` 构造 helper
- 提取 mock 交易默认值 / 高度推导 helper

预期收益：

- handler 只保留流程控制
- 参数归一化与校验集中在 helper 中，便于后续扩展

### P10：按职责拆分超大源码文件

状态：`completed`

目标：

- 将 `main.go` 中的扫描流程、API handler、通用 helper 拆到独立文件

预期收益：

- 降低单文件认知负担
- 提升按职责定位代码的效率
- 为后续继续拆分 model / adapter / api 奠定结构基础

### P11：同步重构文档状态并验证结构稳定性

状态：`completed`

目标：

- 在文档中记录拆分结果
- 用测试与静态检查验证拆分后未改变行为

### P12：按职责拆分生产回调与后台管理代码

状态：`completed`

目标：

- 将原 `production.go` 中的回调投递、后台管理 handler、统计接口拆到独立文件

预期收益：

- 避免 callback delivery、后台接口、统计逻辑继续挤在同一文件
- 提高排障与扩展时的代码定位效率

### P13：验证生产代码拆分后的稳定性并同步文档

状态：`completed`

目标：

- 完成拆分后的回归验证
- 记录新的文件职责划分

## 3. 实施原则

- 不改变现有接口协议
- 不改变现有指标名与日志语义
- 优先做“小步重构 + 测试验证”
- 优先抽象**重复业务逻辑**，不为抽象而抽象

## 4. 状态跟踪

- [x] P1 已完成
- [x] P2 已完成
- [x] P3 已完成
- [x] P4 已完成
- [x] P5 已完成
- [x] P6 已完成
- [x] P7 已完成
- [x] P8 已完成
- [x] P9 已完成
- [x] P10 已完成
- [x] P11 已完成
- [x] P12 已完成
- [x] P13 已完成
- [x] `go test ./...` 通过
- [x] `go vet ./...` 通过

## 5. 已完成结果

### 5.1 P1 结果

已抽取：

- 统一交易入队 helper：`enqueueDetectedTx`
- 统一回调地址解析 helper：`resolveCallbackURL`
- 统一 callback task 重试 helper：`retryCallbackTasks`

效果：

- `scanOneAddress` 与 `scanEVMLogBucket` 共享同一套“去重 / 解析 callback / 入队”逻辑
- 单任务 / 批量任务重试共享同一套重试入口

### 5.2 P2 结果

已抽取：

- 通用分页响应 helper：`respondWithPaginatedQuery`

已替换接口：

- `GET /addresses`
- `GET /callback-tasks`
- `GET /mock/transactions`
- `GET /debug/callbacks`

### 5.3 P3 结果

已抽取：

- worker 数量规范化 helper：`normalizeWorkerCount`
- 地址结果聚合 helper：`addressResult.merge`
- 扫描结果聚合 helper：`ScanResult.mergeAddressResult`
- 回调结果聚合 helper：`ScanResult.mergeCallbackTaskResult`

效果：

- 扫描主流程、EVM bucket 扫描与回调分发的聚合逻辑更集中
- 并发 worker 计算不再散落在多个函数中

### 5.4 P4 结果

已抽取：

- 通用路径 ID 解析 helper：`parseResourcePathID`
- 通用按 ID 查询 helper：`loadModelByID`

效果：

- `/addresses/{id}` 与 `/callback-tasks/{id}` 的路径解析不再重复
- `not found` / `database error` 响应行为更统一

### 5.5 P5 结果

已抽取：

- callback task CSV 导出 helper：`writeCallbackTasksCSV`

效果：

- 死信导出 handler 更短
- CSV 头与字段映射集中在一个位置，后续维护成本更低

### 5.6 P6 结果

已抽取：

- 单方法校验 helper：`requireMethod`
- 多方法分发 helper：`dispatchMethods`

效果：

- `GET /readyz`、`GET /metrics`、`GET /stats`、`POST /scan/once` 等 handler 的 method guard 更统一
- `GET/POST/DELETE` 风格资源路由不再重复写 `switch r.Method`

### 5.7 P7 结果

已抽取：

- 路由注册 helper：`registerRoutes`
- 统计计数 helper：`countModel`、`applyStatCountSpecs`

效果：

- `main` 中的启动流程更聚焦
- `collectStats` 从“多段手写 count”改为数据驱动组装，后续加统计项更集中

### 5.8 P8 结果

已抽取：

- 统一 JSON 解码 helper：`decodeJSONBody`
- 基础必填字段校验 helper：`requireNonEmptyField`

效果：

- `createAddress`、`createMockTransaction`、`updateAddress` 的坏请求处理更统一
- 减少重复的 `Decode` 与 `400 Bad Request` 分支

### 5.9 P9 结果

已抽取：

- 地址创建默认值 helper：`applyCreateAddressDefaults`
- 地址创建校验与规范化 helper：`validateAndNormalizeCreateAddressRequest`
- 地址更新 map 构造 helper：`buildAddressUpdates`
- mock 交易默认值 / 高度推导 helper：`normalizeMockTxRequest`

效果：

- handler 更聚焦流程控制
- 参数默认值、校验和规范化集中在 helper 中，后续扩展更容易

### 5.10 P10 结果

已完成按职责拆分：

- `app_runtime.go`：运行时流程、路由注册、健康检查、管理员鉴权
- `scanner_flow.go`：扫描主流程、单地址扫描、去重相关 DB 操作
- `api_main_handlers.go`：地址、扫描、mock、debug callback 的主 handler
- `app_common.go`：JSON 输出、链地址/hex 辅助、迁移兼容 helper

效果：

- `main.go` 从“大杂烩文件”收敛为“类型定义 + 启动入口”
- 代码定位更直接：扫链看 `scanner_flow.go`，接口看 `api_main_handlers.go`

### 5.11 P11 结果

已完成：

- 重构方案文档状态同步
- `go test ./...` 验证通过
- `go vet ./...` 验证通过

结论：

- 文件拆分没有改变现有行为
- 后续如果继续演进，可以按同样方式把 model / adapter / production handler 再进一步分层

### 5.12 P12 结果

已完成按职责拆分：

- `callback_delivery.go`：回调任务模型、投递、重试判断、签名、幂等落库
- `api_production_handlers.go`：地址管理与 callback task 管理接口
- `stats_handlers.go`：统计接口与统计组装

效果：

- 原 `production.go` 的多职责已完全拆散
- 看回调重试链路时只需关注 `callback_delivery.go`
- 看后台管理接口时只需关注 `api_production_handlers.go`

### 5.13 P13 结果

已完成：

- `go test ./...` 验证通过
- `go vet ./...` 验证通过
- 文档状态同步完成

当前结构结果：

- `main.go`：类型定义 + 启动入口
- `app_runtime.go`：运行时与路由
- `scanner_flow.go`：扫描主流程
- `api_main_handlers.go`：主接口
- `callback_delivery.go`：回调投递
- `api_production_handlers.go`：后台管理接口
- `stats_handlers.go`：统计接口
- `app_common.go` / `code_refactor_helpers.go`：通用 helper
