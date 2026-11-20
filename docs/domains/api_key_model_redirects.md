# API Key 模型重定向

每个 API Key 可以保存一组独立的 `model_mapping`，把客户端提交的模型别名重定向到内部真实模型。规则只对当前 Key 生效，不会修改渠道、分组或账号的全局配置。

本文覆盖规则配置、匹配顺序、请求/响应模型链和模型列表投影，不覆盖渠道映射、账号映射或供应商自身的模型别名。

## 章节导航

- [配置接口](#配置接口)：修改 Key 创建、更新或校验时读取。
- [规则约束](#规则约束)：核对可接受的来源和目标。
- [匹配顺序](#匹配顺序)：修改精确/通配符解析时读取。
- [请求与响应](#请求与响应)：修改中间件、日志或响应恢复时读取。
- [复合 Key](#复合-key)：核对与前缀选组的先后顺序。
- [模型列表](#模型列表)：修改可见别名投影时读取。

## 配置接口

创建 API Key 时可传入：

```json
{
  "name": "review-key",
  "group_id": 12,
  "model_mapping": {
    "codex-auto-review": "gpt-5.6-luna",
    "team-preview-*": "gpt-5.6-luna"
  }
}
```

更新 API Key 时：

- 省略 `model_mapping`：保留已有规则。
- 传入 `{}`：清空全部规则。
- 传入非空对象：用新对象替换全部规则。

API Key 查询与列表响应会返回完整的 `model_mapping` 对象。

## 规则约束

- 最多 100 条规则。
- 来源和目标去除首尾空格后必须包含 1 至 100 个字符。
- 来源支持精确名称，或一个位于末尾的 `*` 通配符。
- 目标必须是具体模型，不能包含通配符。
- 来源区分大小写，去除首尾空格后不能重复。
- 来源与目标不能相同。

非法规则返回 HTTP `400`，错误码为 `INVALID_API_KEY_MODEL_MAPPING`。

<a id="redirect_order"></a>
## 匹配顺序

每个模型在一次请求中只匹配一次，不会对映射结果继续执行下一条规则：

1. 精确规则优先。
2. 多个通配符命中时，来源前缀最长的规则优先。
3. 未命中时保留原模型。

Anthropic 分组的 Messages 与 count_tokens 将输入模型末尾的 `[1m]` 作为窗口选择器。完整名称的精确规则优先（选择器识别大小写及重复写法），随后依次匹配基础名称的精确规则与完整名称的最长通配前缀。目标始终保留一个 `[1m]`。复合 Key 先剥离组前缀，智能 Key 按每个候选分组的平台分别解析，选组与正式改写使用同一规则。

例如以下配置：

```json
{
  "codex-*": "gpt-5.6-sol",
  "codex-auto-*": "gpt-5.6-luna",
  "codex-auto-review": "gpt-5.6-luna",
  "gpt-5.6-luna": "another-model"
}
```

`codex-auto-review` 会得到 `gpt-5.6-luna`，不会继续变成 `another-model`。

<a id="gateway_field_validation"></a>
## 请求与响应

网关在基础鉴权后、复合 Key 或智能 Key 选组及模型改写前，先检查解压后的请求体。JSON 对象与 multipart 标量有重复声明时返回协议原生的 400，Responses WebSocket 的首帧及后续应用帧执行同一检查。工具参数及 JSON Schema 的自定义键保留大小写，多图字段允许重复。纯文本端点在首次读体时就使用各自的大小上限，超过上限返回 413。

重定向发生在 API Key 鉴权和复合 Key 选组之后、渠道与账号映射之前。JSON、multipart、Gemini URL、Responses 工具模型、异步媒体、批量图片、WebSocket 每轮模型及 Live `session.model` 使用相同规则。

客户端响应中的协议模型元数据会恢复为原始别名。正文中的同名文本不会被替换。用量记录中：

- `requested_model` 保存客户端提交的模型别名。
- `upstream_model` 保存实际上游模型。
- `model_mapping_chain` 保存去重后的映射链。

模型权限、账号资格与计费从 Key 重定向后的目标模型开始计算，不会使用客户端别名匹配价格。

## 复合 Key

复合 Key 先使用完整的 `前缀/模型` 选择分组，再对去掉前缀的模型应用该 Key 的共享规则。

例如客户端请求 `GPT/codex-auto-review`，规则为 `codex-auto-review -> gpt-5.6-luna`，则选中 `GPT` 分组后向内部传递 `gpt-5.6-luna`，客户端响应仍展示 `GPT/codex-auto-review`。

<a id="model_list_projection"></a>
## 模型列表

`/v1/models`、`/models`、Gemini、Antigravity 与批量图片模型列表保留原有模型，并追加目标当前可请求的精确别名。

- `codex-auto-review -> gpt-5.6-luna` 会同时展示 `gpt-5.6-luna` 和 `codex-auto-review`。
- 目标当前不可请求时不展示别名。
- Anthropic 目录对已有具体模型和精确 Key 别名计算 `[1m]` 变体。只有 Key 映射后的实际目标在该组的可用长窗口集合里才展示；同名原始条目也会复核。例如 `review → long` 且 `long[1m]` 可请求时展示 `review[1m]`。
- 通配符来源不会被枚举为具体模型 ID。
- 复合 Key 返回带分组前缀的别名。
- 智能路由 Key 按重定向后的目标匹配候选分组，模型列表合并并去重原始 ID 与可请求的精确别名，不添加分组前缀。
- Gemini 原生 `/v1beta/models` 开启分组自定义列表时，也以返回列表中的目标模型追加 Key 精确别名；别名继承目标能力元数据，原模型与顺序保留。

保存规则时只校验格式，不要求目标当时已有可用渠道或账号；实际请求继续使用现有路由错误语义。

相关文档：[路由与结算](routing_and_billing.md)、[网关请求生命周期](../architecture/gateway_request_lifecycle.md)、[领域目录](index.md)。
