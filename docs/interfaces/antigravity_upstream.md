# Antigravity 上游

本文描述 Antigravity 账号接入、专用 Claude/Gemini 端点、协议转换、混合调度与上游失败语义。它用于修改 Antigravity 适配器时保持平台隔离，不记录终端用户的 Claude Code 操作技巧，也不承诺上游未验证的模型能力。

## 章节导航

- [账号与凭据](#账号与凭据)：修改 OAuth 导入、刷新或账号资格时读取。
- [专用端点](#专用端点)：修改强制平台路由时读取。
- [协议适配](#协议适配)：修改 Claude/Gemini/OpenAI 转换时读取。
- [混合调度](#混合调度)：修改与 Anthropic/Gemini 分组共用账号时读取。
- [模型与额度](#模型与额度)：修改可见模型、配额或价格归属时读取。
- [失败与恢复](#失败与恢复)：修改限流、重试、切换或凭据错误时读取。

<a id="antigravity_account_contract"></a>
## 账号与凭据

Antigravity 账号的 `platform` 为 `antigravity`。管理端通过 `/api/v1/admin/antigravity/oauth/*` 生成授权 URL、交换 code 或验证/刷新 refresh token，再使用通用账号创建/更新路径保存凭据。OAuth access token 由 token provider 在使用前刷新；project ID、subscription/tier、privacy mode、额度和上游 user agent 等属于账号/运行时元数据。

管理端还提供“静态上游”表单，但当前把它保存为 `type=apikey`；Antigravity Claude 直连和 token provider 的历史静态分支只识别 `type=upstream`。这两种类型目前不能视为等价，新的静态账号不构成完整正式支持。历史 `upstream` 账号仅用于 Claude 直连兼容。

原生兼容转发要求 `type=oauth` 的 Antigravity 账号。setup token、upstream 或 API-key 类型不能被当成原生 OAuth 兼容账号；服务应返回可操作错误而不是把不匹配凭据发送给上游。standard-tier 账号缺少必要 project ID 时也要显式拒绝。完整平台/账号分类和已知冲突见[上游账号能力矩阵](upstream_account_matrix.md)。

账号导入、凭据刷新和批量导入后会检查/设置适用的隐私状态。凭据、refresh token、project ID 和上游响应中的内部标识不得出现在客户端错误或模型列表中。

通用账号刷新接口可能已成功更新 token，但后续账号元数据补全失败。此时仍返回刷新后的账号，并附带可选 `warning`；后台显示警告并照常失效上游用量缓存，不能把部分成功显示为完全成功或撤销已保存凭据。

访问 token 的缓存键和刷新锁统一使用 `ag:account:<账号ID>`，同 project 的不同账号不能共用 token。失效操作同时清理当前账号键和历史 `ag:<project>` 键；project 不再作为凭据身份。多实例须全部升级才能完成隔离，旧实例仍可能按 project 写旧缓存；不需要清空整个 Redis。

## 专用端点

专用路由在 API Key 鉴权前写入 `ForcePlatform=antigravity`，因此只选择 Antigravity 账号，不受混合调度开关影响：

| 入口 | 客户端协议 | 处理 |
| --- | --- | --- |
| `GET /antigravity/models` | Claude 风格模型列表 | 返回当前 Key/分组可请求的 Antigravity 模型 |
| `POST /antigravity/v1/messages` | Anthropic Messages | 转换并转发 Claude 请求 |
| `POST /antigravity/v1/messages/count_tokens` | Anthropic token count | 路由入口保留；当前明确返回 `404`，客户端应回退本地估算 |
| `GET /antigravity/v1/models`、`/usage` | Claude 风格自省 | 模型与 Key 用量 |
| `/antigravity/v1beta/models/*` | Gemini v1beta | list/get/generate/stream/countTokens 等 Gemini 形状 |

Claude Code 可把 base URL 指向部署地址的 `/antigravity`，认证值仍是 TokenRouter API Key。Gemini 入口中的 `x-goog-api-key` 同样接收 TokenRouter Key，不是 Google 上游 key。

专用路由仍执行请求体限制、client request ID、Ops error logger、endpoint 归一化、分组和订阅/余额准入。强制平台只限制候选账号，不能绕过 Key、团队、分组或模型权限。

## 协议适配

Antigravity 分组支持 Messages、Responses、Chat 和 Gemini GenerateContent，新建时默认启用 Messages 与 Gemini GenerateContent；四项都可关闭，迁移前已有分组启用四项。通用入口和 `/antigravity/*` 别名都按最终分组执行对应协议门禁；Gemini 模型列表 GET 不受生成协议开关影响。

Anthropic Messages 经过 Antigravity request transformer 生成上游 Gemini/内部请求形状，响应和 SSE 再恢复为 Anthropic 协议。工具定义、tool choice、thinking、缓存断点、图片输入、token 用量和停止原因都需要双向转换；schema cleaner 会移除上游不接受的 JSON Schema 表达。

转换 system 指令时，字符串或文本块开头的 `x-anthropic-billing-header:` 元数据行会被移除，后续真实指令继续保留；正文中间的同名文本不作为元数据清理。该规则只作用于 Antigravity 转换，原生 Anthropic 的计费指纹同步仍遵守其独立契约。

通用 OpenAI Chat Completions 和 Responses 在选到原生 Antigravity OAuth 账号时走兼容适配器：

```text
Chat Completions / Responses
  -> OpenAI-compatible normalized form
  -> Anthropic request
  -> Antigravity/Gemini upstream
  -> Anthropic response/stream
  -> original OpenAI protocol
```

每次 failover attempt 都必须从原始请求重新转换，并刷新工具名回程映射。流开始后遵守共同不可切换边界。Gemini v1beta 专用入口保持 Google 错误/流形状，不经过 OpenAI envelope。

Gemini 原生 SSE 转发会拆开上游包装并重新写出 data 帧分隔，因此不再重复转发上游空分隔行；LF 和 CRLF 输入都只生成一次帧分隔。普通客户端仍可收到网关的空闲注释心跳；`User-Agent` 或 `X-Goog-Api-Client` 识别为 `google-genai-sdk` 且使用 `gl-go` 或 `gl-python` 时，网关关闭本地产生的注释心跳，并过滤上游注释行，避免这类 SDK 将 `:` 判定为非法流块。该兼容处理不改写正常 data 事件，也不改变普通客户端的注释转发。

兼容层把 Chat 请求中的正数 `max_completion_tokens`（缺省时使用 `max_tokens`）在转换为 Anthropic 请求前封顶为 64000；零、负数或缺省值不会覆盖转换器已有的默认上限，避免超大客户端参数被上游拒绝。

## 混合调度

Antigravity 账号 `extra.mixed_scheduling` 为布尔 `true` 时，可以作为 Anthropic 或 Gemini 原生分组的候选账号。缺失、`false` 或字符串 `"true"` 都视为未启用。候选账号还必须属于目标分组、状态 active/schedulable，并满足模型、额度、并发、资格和 endpoint 能力。

混合调度只扩大账号候选集，不改变请求平台的产品语义：

- Anthropic 分组的请求仍按 Anthropic 入口、分组倍率和错误形状处理。
- Gemini 分组的请求仍按 Gemini 入口和模型 URL 处理。
- 粘性会话命中已关闭混合调度的 Antigravity 账号时，必须丢弃旧绑定并重新选择原生或合格混合账号。
- 专用 `/antigravity/*` 入口永远强制平台；普通 Antigravity 分组也不因该开关混入其它平台账号。

账号的混合调度状态或分组关系变化后要重建原生平台和 Antigravity 相关调度快照，清理旧粘性状态。Anthropic 与 Antigravity Claude 不能在同一显式会话里无约束切换；会话隔离、粘性和缓存计费规则用于防止上下文跨账号语义漂移。

Gemini 混合分组的账号目录纳入开启混合调度的 Antigravity 账号，但只展示其 Gemini 模型，不混入 Claude 模型。Antigravity 系统提示只规范化开头固定的 Claude Agent SDK/Claude Code 身份句，不重写用户正文；tuple schema 的 `prefixItems` 被转换为 Gemini 支持的 `items`，并递归规范化对象属性、联合类型和数组。

客户端函数工具与内置搜索/执行工具混合时，只保留客户端函数，移除不兼容的内置工具；纯搜索请求仍保留搜索能力。兼容入口不因已经移除的搜索工具再切换搜索 fallback 模型。本次工具清理步骤保留收到的未知字段和数字精度；没有混合冲突时该步骤原样返回。这不是整条原生转换链均不重新编码的承诺。

## 模型与额度

Antigravity 同时提供 Claude 与 Gemini 模型族。Gemini 3.6、3.7、3.8 Flash 的基础、high、low、medium 与 tiered 五种模型 ID 均进入默认模型目录和身份映射；账号存在自定义映射时，只要没有覆盖它们的通配符，这些精确直通映射仍会自动保留。

裸 Gemini 模型通过统一最终模型解析器处理：原生请求使用 `generationConfig.thinkingConfig.thinkingLevel` 或 `thinkingBudget` 选择账号映射中存在的思考变体；Messages/OpenAI 兼容请求根据转换后的 `thinking.budget_tokens` 使用同一档位规则，缺省配置按 `high` 选择。调度上下文和重试冷却键保存相同档位，避免实际请求与限流 scope 不一致。首选档位不可用时依次尝试 `high`、`medium`、`low`、`tiered`，不重复尝试首选项。管理员配置的精确映射和命中的通配符映射始终优先，包括显式原样透传；运行时自动补齐的裸名自映射不视为管理员的显式选择。请求已有思考后缀时保持常规模型映射，找不到任何变体也回到既有映射流程。

可见模型来自默认映射、分组/渠道限制、账号资格和当前可请求解析；API Key 精确别名可投影到列表，目标不可请求时不展示。模型能力不能只由名称前缀推断，thinking/image 等能力由适配器与账号详情共同约束。

额度查询按账号和模型 scope 保存上游 reset/remaining 状态，并可包含 AI Credits。429/503 分类区分模型限流、credits 耗尽和共享容量不足；请求结算的 `QuotaPlatform` 必须保留 Antigravity，即使客户端从 Anthropic/OpenAI 兼容入口进入。账号成本和用户扣费仍遵守渠道计价与分组倍率边界。

## 失败与恢复

- 短 `RetryInfo` 可以在同账号做一次受限等待；长模型限流标记模型/账号并请求调度层切换。
- 单账号模式允许有总等待上限的退避重试，多账号模式优先切换；Context 取消立即停止。
- `MODEL_CAPACITY_EXHAUSTED` 被视为共享模型容量，使用全局去重和有界重试，不能通过快速轮换账号放大上游压力。
- 粘性会话切换账号时可把普通输入按 cache-read 计费，反映缓存失效；该标志必须进入结算输入。
- OAuth credential 被刷新后仍遭拒绝时返回要求重新授权并检查 project ID 的脱敏提示，同时把账号标记为可恢复错误。
- 只有白名单中的安全上游提示可以透传；响应体日志受开关、字节上限和脱敏约束。

修改适配器时应覆盖非流/流、Claude/Gemini/OpenAI 三种客户端形状、工具/thinking、单/多账号限流、混合调度关闭后的快照失效和用量归属测试。

相关文档：[上游账号能力矩阵](upstream_account_matrix.md)、[网关请求生命周期](../architecture/gateway_request_lifecycle.md)、[路由与结算](../domains/routing_and_billing.md)、[HTTP 接口边界](http_api.md)、[接口目录](index.md)。
