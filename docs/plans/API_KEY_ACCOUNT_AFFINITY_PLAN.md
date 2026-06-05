# API Key 账号亲和路由实施方案（中级工程师版）

## Summary
在现有多 API Key 鉴权基础上，增加“API Key 绑定账号集合”的路由能力，让请求优先使用该 key 绑定的账号；绑定账号不可选时，再按配置决定是否回退到全局账号池。实现必须保持当前账号池的权重、模型过滤、冷却、token 过期跳过、配额判断、失败重试和错误返回形状不变。

本次目标是“亲和路由”，不是租户级强隔离。多个 key 允许共享同一账号。

## Implementation Changes

### 1. 配置模型与保存语义
扩展 `config.ApiKeyEntry`：
- 新增 `BoundAccountIDs []string \`json:"boundAccountIds,omitempty"\``
- 新增 `StrictBinding bool \`json:"strictBinding,omitempty"\``

新增统一的规范化/校验逻辑，供创建和更新共用：
- 去除空字符串账号 ID
- 去重并保留输入顺序
- 校验每个账号 ID 必须存在于 `config.GetAccounts()`
- 若规范化后 `BoundAccountIDs` 为空，则强制 `StrictBinding=false`
- 若请求显式传入 `StrictBinding=true` 且规范化后绑定集合为空，则返回 400

保存语义明确为：
- 创建 API Key
  - `boundAccountIds` 省略：视为空数组
  - `strictBinding` 省略：默认 `false`
- 更新 API Key
  - `boundAccountIds` 使用 `*[]string`
  - `strictBinding` 使用 `*bool`
  - 字段省略：保留现值
  - `boundAccountIds: []`：表示显式清空绑定；清空后结果中的 `strictBinding` 必须变为 `false`
  - 若请求同时发送 `boundAccountIds: []` 和 `strictBinding: true`，返回 400，不做隐式纠正

`config.UpdateApiKey` 保持“补丁式更新”，但新增字段的补丁规则必须写入函数注释并按上述语义实现，避免前端无法清空绑定或误保留旧值。

### 2. API Key 鉴权上下文
调整 API Key context 传递方式：
- 认证成功后，将**完整命中的 `ApiKeyEntry` 副本**放入 request context，而不是只存 `apiKeyID`
- 保留 `apiKeyIDFromContext` 兼容现有 usage 归因逻辑，但其值应从 context 中的 entry 提取
- 新增 `apiKeyEntryFromContext(ctx)` helper，后续路由直接读 entry，不再在每条 handler 中重复查配置

这样可以保证：
- 单次请求在整个生命周期内使用同一个 API Key 快照
- 路由与 usage 归因基于同一份匹配结果
- 避免中途重复查询导致行为分裂

### 3. 账号池：新增“限定集合”选择能力
不要新写一套账号选择循环；要把现有选择逻辑抽成内部共享实现，保留当前语义。

账号池新增两个公开方法：
- `GetNextWithinExcluding(allowedIDs map[string]bool, excluded map[string]bool) *config.Account`
- `GetNextForModelWithinExcluding(model string, allowedIDs map[string]bool, excluded map[string]bool) *config.Account`

实现要求：
- 复用现有 `GetNextExcluding` / `GetNextForModelExcluding` 的完整逻辑
- 保留**权重轮询**语义：绑定选择必须仍然基于 `p.accounts` 这个加权展开后的切片，而不是退回到原始账号列表
- 保留现有 fallback 语义：当没有立即可用账号时，仍返回“冷却时间最短的候选账号”；只有在限定集合内完全无候选时才返回 `nil`
- `allowedIDs` 仅作为额外过滤条件，不改变其余判断顺序
- `allowedIDs` 为空时直接返回 `nil`，不做全局回退

推荐做法：
- 抽一个私有共享 selector，接受两个额外 predicate：
  - 是否在 allowed 集合内
  - 是否支持指定模型
- 让现有 4 个公开选择方法都走这个私有 selector，避免复制粘贴和行为漂移

### 4. Handler 路由策略
在 Claude、OpenAI Chat、Responses 的自动选账号路径中，统一接入 API Key 绑定路由。

新增 handler 级 helper：
- 输入：`model string`、`excluded map[string]bool`、`apiKeyEntry *config.ApiKeyEntry`
- 输出：`account *config.Account`、`bindingPhaseActive bool`

选择策略固定为两阶段：
1. 若 `apiKeyEntry == nil` 或 `len(BoundAccountIDs)==0`，直接走现有全局选择
2. 若存在绑定账号，先走“绑定阶段”
3. 绑定阶段选不到账号时：
   - `StrictBinding=true`：直接返回 `nil`
   - `StrictBinding=false`：切换到“全局阶段”
4. 全局阶段走现有全局选择逻辑

重试策略固定为：
- 沿用当前 `maxAccountRetryAttempts`
- 尝试次数是**整个请求总预算**，不是“绑定 3 次 + 全局 3 次”
- 从绑定阶段切换到全局阶段时，不消耗一次 attempt；只在真正拿到账号并尝试使用它时消耗预算
- 使用同一个 `excluded` map 跨阶段共享
- 绑定阶段失败过的账号进入 `excluded` 后，切换到全局阶段也不能再被选中

错误行为固定为：
- 严格绑定失败或绑定阶段耗尽后，不新增新的错误结构
- 完全复用当前各 handler 的“无可用账号”分支和状态码：
  - Claude / OpenAI Chat / Responses 继续返回当前已有的 `503 No available accounts` 分支
- 不新增 `no bound accounts` 或其他新错误类型，保证客户端兼容

### 5. 接入范围
只修改客户端推理流量的自动选账号路径：
- Claude `/v1/messages`
- OpenAI `/v1/chat/completions`
- OpenAI `/v1/responses`

不改动以下路径：
- 管理员手工测试账号
- 管理员手工刷新 token / models
- 账号导入
- 账号详情查询
- 账号导出
- 后台其他显式指定账号的操作

### 6. 后台 API 与 UI
扩展 API Key 管理接口返回与入参：
- `GET /admin/api/api-keys`
- `GET /admin/api/api-keys/{id}`
- `POST /admin/api/api-keys`
- `PUT /admin/api/api-keys/{id}`

返回字段新增：
- `boundAccountIds`
- `strictBinding`

前端要求：
- API Key 创建/编辑弹窗增加账号多选控件
- 增加“严格绑定”开关
- 未选择任何账号时：
  - 前端保存请求应发送 `strictBinding=false`
  - 严格绑定开关置灰或自动关闭，避免发出无效请求
- API Key 列表显示：
  - 未绑定：`Global routing`
  - 已绑定：`Bound to N accounts`
- 编辑态展示账号名称摘要，使用现有账号列表中的 `email/nickname` 显示，不新增后端展开字段

## Test Plan

### Config / API Key tests
- 创建带绑定账号的 API Key 成功
- 创建时绑定不存在账号 ID 返回 400
- 创建时重复账号 ID 被去重且顺序保留
- 创建时 `strictBinding=true` 且无绑定账号返回 400
- 更新时省略 `boundAccountIds` 不改变现值
- 更新时发送 `boundAccountIds: []` 可以清空绑定，并将 `strictBinding` 置为 false
- 更新时发送 `boundAccountIds: []` + `strictBinding: true` 返回 400

### Pool tests
- `GetNextWithinExcluding` 只会返回 allowed 集合内账号
- `GetNextForModelWithinExcluding` 同时遵守 allowed 集合和模型过滤
- 新 helper 保留权重行为
- 新 helper 在集合内无立即可用账号时，仍保留当前“最短冷却 fallback”语义
- `allowedIDs` 为空时返回 `nil`

### Request routing tests
- 绑定 key 请求优先命中绑定账号
- 非严格绑定下，绑定阶段拿不到账号时会切换全局池
- 严格绑定下，绑定阶段拿不到账号时走现有“无可用账号”错误分支
- 绑定账号不支持模型时，非严格绑定可回退，全局阶段可成功选中其他账号
- 绑定阶段失败账号在全局阶段不会被重复尝试
- API Key usage 统计行为保持不变

### UI/API tests
- 列表和详情能读写 `boundAccountIds` / `strictBinding`
- 未绑定 key 显示为全局路由
- 编辑已有 key 时可清空绑定
- 多选绑定账号后再次打开弹窗能正确回显

## Assumptions
- `legacy` 单 `ApiKey` 模式不支持绑定账号，本次不扩展。
- 多个 API Key 绑定同一账号是合法配置，不做冲突拦截。
- 本次不实现“账号只能被一个 key 独占”或“按 key 拆分独立冷却/配额视图”。
- 现有对外错误形状与状态码必须保持兼容。
