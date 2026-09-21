# OpenCode Zen / GO 上游

本文定义 `opencode_go` 平台的账号、模型协议、缓存会话与用量边界。实现参考固定的上游 [sub2api v0.2.5](https://github.com/Wei-Shaw/sub2api/tree/v0.2.5)，并适配 TokenRouter 的智能路由、渠道计费和统一用量快照。

<a id="opencode_accounts_and_protocols"></a>
## 账号与协议

OpenCode 只支持 `type=apikey`。Zen 与 GO 共用平台身份，由 `credentials.account_mode=zen|go` 区分；创建表单默认 Zen，历史缺省模式按 GO 读取。`api_protocol` 默认 `adaptive`，也可固定 `responses`、`chat_completions` 或 `anthropic`。Jev 的 System One 是按模型选择的专用协议，不作为账号级 `api_protocol` 选项写入旧账号。创建、编辑、批量更新和账号服务写入都会校验模式与规则，不能把 CN 的 payg/coding 值用于 OpenCode。

分组允许 Messages、Responses、Chat 三种客户端协议，新建时默认全选。自适应按**账号映射后的模型**选择上游，与客户端入口独立；选协议只观察映射结果，实际请求由既有转换器改写一次，原始模型和渠道定价来源继续保留。

| 模式 | Responses | Anthropic Messages | 其余模型 |
| --- | --- | --- | --- |
| Zen | `gpt-*`、`grok-*`、`muse-spark-*` | `claude-*`、`qwen*` | Chat Completions |
| GO | `gpt-*`、`grok-*`、`muse-spark-*` | `minimax-*`、`qwen*` | Chat Completions |

Zen 另有两个结构化决策模型：`jev-1.13` 与 `jev-1.13-free` 只能发送到
`https://opencode.ai/zen/v1/systemone`（或账号配置的同等中继路径）。请求保留
`state` 与 `questions` 对象，响应为同步 JSON，包含 `answers` 和 `usage`；该协议不支持
流式、图片、音频或视频。System One 由专用 `/v1/systemone` 入口处理，不能从普通
Messages、Chat Completions 或 Responses 入口转换；GO 账号和其它平台不会被调度到 Jev。

`credentials.protocol_rules` 最多 64 条，按顺序首条命中；pattern 支持精确值或末尾 `*`，最长 128 字符，不接受空白和中间通配符。缺失/null 使用当前模式默认表，显式 `[]` 表示全部兜底 Chat。固定协议优先于规则；OpenAI Responses 探测和文本路由配置不覆盖 OpenCode 的选择。

Zen 默认根地址为 `https://opencode.ai/zen/v1`，GO 为 `https://opencode.ai/zen/go/v1`。Anthropic 默认根地址移除末尾 `/v1`，端点拼接识别已有版本路径，不能生成 `/v1/v1/messages`。自适应 `api_base_urls` 的对应项优先，随后是管理员显式 `base_url`，最后才是模式默认值；自定义中继主机及路径不得被替换为官方地址。代理、TLS 指纹、受保护请求头覆写继续复用现有传输层。

连接测试只访问所选模型的实际原生端点；空测试模型使用 `glm-5.3`。模型同步和列表沿现有可见目录与白名单机制，静态目录只是未同步时的有限回退，不证明账号能访问所有型号。智能路由仍先按具体目录排除不支持的分组；空模型、未知型号和媒体接口不能因为新增平台被放行。除上述 Jev System One 外，OpenCode 不扩展 Images、视频、Embeddings、Realtime 或 Responses WebSocket 能力。

<a id="opencode_session_and_billing"></a>
## 缓存会话、错误与计费

推理请求派生 `X-OpenCode-Session`，顺序为调用方 OpenCode/已有会话头、原始入站 `prompt_cache_key` 或 `metadata.user_id`、账号覆写。协议转换前保存原始请求，防止转换后丢失会话字段。GO 在全部线索缺失时为同一次请求生成稳定随机回退值；持续缓存命中仍需要客户端提供稳定会话。OpenCode 会话按本站 API Key 隔离，两个用户的同名会话不会共用上游标识。

GO 自定义中继可接收该会话头；Zen 与历史普通兼容账号沿官方 `https://opencode.ai` 主机边界。真实上游端点写入请求上下文和用量结果，错误日志不再依靠入站 URL 推测。输出前的可重试错误继续交给当前账号故障转移及智能路由协调器；已经产生输出或用量后不重放。后台错误记录和用户最终错误沿原有规则保留。

OpenCode 的 Messages→Responses 转换保留完整会话历史，不进入 OpenAI 专用的摘要注入、`previous_response_id` 续接、历史截断与 Todo 提示分支。缓存会话标识用于供应商提示词缓存，不表示服务端已经存储了被省略的消息。

原生 Anthropic 响应由 OpenCode 独立处理器校验终态，三个客户端入口都要求有效 `message_stop`（非流式 JSON 要求完整 message 与停止原因）才算成功；HTTP 200 内的 JSON/SSE 错误同样登记。流式仅暂存最多 64 KiB 的无正文、无用量前导，产生正文或用量即正常发送，不全量缓冲长响应。此前明确的 429/5xx 可按原策略故障转移；随后发生错误、EOF 或超时则保留部分用量、记录 Ops 并向仍连接的客户端写入失败终态，不补成功事件或重放工具调用。客户端断连继续排水计量，纯取消不伪造上游错误。

Zen 402 沿已有余额不足临时冷却处理。GO 429 优先使用身份有效且明确耗尽的用量窗口，多个耗尽窗口取最晚恢复时间；不足以确认时使用上游恢复时间或通用短冷却。没有把 Zen 402 改成新的智能路由跨组错误类型。

结算沿现有渠道模型映射、显式价格、订阅/余额、分组倍率和 Key 限额流程。Jev 1.13 的默认输入价为 `$0.042/M tokens`、输出免费，Jev 1.13 Free 的输入和输出均免费；上游返回的 `usage.input_tokens/output_tokens` 直接用于用量记录。真实上游为 Claude 时仍可使用系统默认 Claude 价格；客户端 Claude 别名映射到其它家族时，不能让无价 OpenCode 模型错误套用 Claude 兜底价。用户平台额度归属 `opencode_go`，不拆成 Zen/GO 两个账本。增量迁移 `277_allow_opencode_user_platform_quotas.sql` 仅扩展平台约束，不新增用户额度记录、不修改余额或订阅。

<a id="opencode_usage_and_scheduling"></a>
## GO 用量与调度

GO 自动使用内部 `opencode_go` 只读适配器，在配置的 GO API 根地址请求 `/usage`，保留中继主机和路径。解析 rolling（五小时）、weekly、monthly 窗口并归一化为百分比。Zen 没有接入余额查询协议，明确返回不支持，不发送猜测的请求。

手动查询只用于展示。既有 `gateway.cn_providers.monitor_enabled` 默认关闭；开启后可监控 GO 并保存统一、身份绑定的 `cn_usage_monitor_snapshot`。GO 的五小时/周/月阈值读取该快照，耗尽或多个阈值同时命中时选择最晚恢复时间。查询失败、凭据/代理/模式/端点变化后的旧快照不得用于停调；GO 调度元数据保留完整查询身份以维持与完整账号相同的校验。

相关文档：[上游账号能力矩阵](upstream_account_matrix.md)、[上游用量查询](upstream_usage.md)、[智能路由 API Key](../domains/smart_routing_api_keys.md)、[用户平台额度](../domains/platform_quotas.md)。
