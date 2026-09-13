# MiniMax 上游

本文记录 MiniMax 账号、分组和文本协议在 TokenRouter 中的接入边界。MiniMax 复用当前国产供应商的账号、调度、协议桥、用量和计费流程；参考项目的其它平台或管理功能不属于此接入范围。

<a id="minimax_account_protocols"></a>
## 账号与协议

平台标识为 `minimax`，仅支持 `type=apikey`，凭据保存于 `credentials.api_key`。`account_mode` 支持 `payg` 与 `coding`，缺省为 `payg`；两种模式使用相同推理端点，由实际 API Key 的套餐决定上游扣费。

`credentials.api_protocol` 支持 `chat_completions`、`anthropic`、`responses`、`adaptive`，缺省为 `chat_completions`。管理员可选择固定协议，也可通过 `adaptive` 按客户端入口使用对应原生协议：

| 客户端入口 | 自适应模式的默认上游 | 鉴权 |
| --- | --- | --- |
| `/v1/chat/completions` | `https://api.minimax.io/v1/chat/completions` | Bearer API Key |
| `/v1/messages` | `https://api.minimax.io/anthropic/v1/messages` | 默认 `x-api-key`，可沿现有账号设置选择 Bearer |
| `/v1/responses` | `https://api.minimax.io/v1/responses` | Bearer API Key |

固定协议下复用现有跨协议转换器。分组支持并默认开启 Messages、Responses、Chat 三种客户端协议，仍须通过分组协议准入。此接入不新增图片生成、视频生成、音频、Embeddings 或 OAuth 能力；token 计数沿用当前 CN 兼容路径的能力与本地估算边界。

原生或转换后的 Anthropic 请求使用既有 MiniMax 思考兼容规则，将 `thinking.type=enabled` 转为 `adaptive`，历史思考与工具内容沿现有传输流程保留。原生 Responses 请求保留客户端的 `max_output_tokens`。

`base_url` 和自适应模式的 `api_base_urls` 沿用当前凭据格式。前端提供国际 `.io`、国内 `.cn` 及旧 `.minimaxi.com` 地址预设；自定义中继保留主机和路径前缀，不从 URL 反推供应商身份。代理、TLS、URL allowlist 与受保护请求头规则继续生效。

## 模型与请求资格

默认管理/连通性测试模型为 `MiniMax-M2.7`。基础目录包含 `MiniMax-M3`、`MiniMax-M2.7`、`MiniMax-M2.7-highspeed`、`MiniMax-M2.5`、`MiniMax-M2.5-highspeed`、`MiniMax-M2.1`、`MiniMax-M2.1-highspeed` 和 `MiniMax-M2`。目录条目只提供可配置基线，实际模型可用性仍由套餐、账号模型映射、状态及上游决定。

模型同步使用同一账号的 OpenAI 兼容 `/v1/models`。MiniMax 的大小写模型 ID 与 Qoder 内部的小写别名各自保持原有语义。普通与智能路由均保留 `minimax` 平台身份，候选账号必须属于对应平台和分组；不能回退为 OpenAI 平台挑选账号。

MiniMax 模型价格复用已有模型目录和静态价格，保留分组、渠道价格的现有优先级；未知价格继续返回未定价，不填零价或借用 Claude 价格。

<a id="minimax_usage_and_limits"></a>
## 用量与额度

Coding Plan 使用自动适配器 `minimax_coding`，固定只读查询 `/v1/api/openplatform/coding_plan/remains`，将上游窗口归一为 `PERCENT` 的 `5h` 与 `weekly` 限额。按量付费模式不提供余额接口适配，明确返回不支持，不向其它供应商接口尝试查询。

手动查询、后台监控、身份绑定快照、失败保留最近成功结果及阈值停调均沿用 [API Key 上游用量查询](upstream_usage.md) 的规则。MiniMax 的上游套餐窗口与用户的 USD 平台额度分开处理。

迁移 `270_allow_minimax_user_platform_quotas.sql` 仅扩展用户平台额度的数据库 CHECK 约束，允许 `minimax`。已有用户不回填 MiniMax 限额，缺失行继续表示无限额；新用户和管理员保存时沿用当前平台额度默认值与全量保存规则。

## 参考与验证边界

接入参考 [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api/tree/bdb42e22f81fcb633ff0a060961211dd2bcb515b) 的 MiniMax 能力，代码组织、凭据校验、平台隔离和监控身份采用 TokenRouter 当前规范。

公开协议来源：[MiniMax Anthropic SDK](https://platform.minimax.io/docs/api-reference/text-anthropic-api)、[OpenAI SDK](https://platform.minimax.io/docs/api-reference/text-openai-api)、[官方文档目录](https://platform.minimax.io/docs/llms.txt)。本地模拟上游测试可以验证请求路径、鉴权、转换、用量与失败行为；不能替代实际 MiniMax 账号的套餐权限和线上可用性验证。

相关文档：[上游账号能力矩阵](upstream_account_matrix.md)、[模型目录与市场](model_catalog_and_marketplace.md)、[用户平台额度](../domains/platform_quotas.md)、[智能路由 API Key](../domains/smart_routing_api_keys.md)。
