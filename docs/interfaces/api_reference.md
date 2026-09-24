# TokenRouter AI 网关 API 参考

本文只记录当前项目面向模型客户端的 AI 网关接口，依据 `backend/internal/server/routes/gateway.go`、网关 handler 和请求 DTO 整理。面板认证、用户、支付、工单及管理员接口不在本文范围内；上游转换细节见文末专题文档。

## 快速约定

### 地址、鉴权和请求头

设部署地址为 `https://api.example.com`。AI 网关默认使用以下路径前缀：

- `/v1`：OpenAI、Anthropic、Grok、OpenCode 等兼容接口。
- `/v1beta`：Gemini 原生 GenerateContent 接口。
- `/api/v3`、`/v3`、`/v1` 或无前缀的 `/contents/generations/tasks`：Seedance（火山方舟）原生视频任务接口。
- `/backend-api/codex`：Codex 兼容的 Responses、实时会话和搜索别名。

除 Gemini 兼容请求外，推荐使用：

```http
Authorization: Bearer <tokenrouter_api_key>
Content-Type: application/json
Accept: application/json
```

兼容客户端也可以使用 `x-api-key`；Gemini 原生入口额外接受 `x-goog-api-key`。通用网关拒绝 URL 中的 `key`、`api_key` 参数；Gemini 的 `/v1beta` 和 `/antigravity/v1beta` 入口兼容 `?key=`，但仍拒绝 `api_key` 参数，建议优先使用请求头。支持流式的接口可设置 `stream: true` 并读取 `text/event-stream`；Gemini 则使用 `streamGenerateContent` 动作。

可选的关联请求头：

```http
X-Client-Request-ID: client-generated-id
X-Request-ID: optional-client-id
```

### 网关响应和错误

网关成功响应保持对应协议的原生格式，不使用面板 API 的 `{code,data}` envelope。常见错误响应如下：

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "invalid_request_error",
    "message": "model is required"
  }
}
```

Gemini 错误遵循 Google 格式：

```json
{
  "error": {
    "code": 403,
    "message": "This group does not allow Gemini GenerateContent requests",
    "status": "PERMISSION_DENIED"
  }
}
```

常见状态码：`400` 参数或模型校验失败，`401` Key 无效，`403` 分组协议/功能不允许，`404` 模型、任务或子路径不存在，`413` 请求体超限，`429` 限流，`500` 服务端错误，`503` 暂无可调度账号或上游暂不可用。流式响应开始后发生错误时，会以当前协议允许的 SSE 错误事件结束，不能再改写成普通 JSON。

### 模型和分组选择

模型目录会根据 API Key 可访问分组、可见模型目录、渠道映射和账号能力过滤。智能路由会按 Key 保存的候选顺序尝试；模型不在某个候选分组目录中时会跳过该分组。分组协议门禁在首次请求和智能路由重试时都会复核。

## AI 网关接口

### 端点总览

| 方法 | 路径 | 协议/用途 |
| --- | --- | --- |
| `GET` | `/v1/models`、`/v1/models/{model}`；无前缀 `/models...`；`/antigravity/models...` | OpenAI/Anthropic 兼容模型目录 |
| `GET` | `/v1/usage`、`/antigravity/v1/usage` | 网关用量查询 |
| `POST` | `/v1/messages`、`/antigravity/v1/messages` | Anthropic Messages 兼容 |
| `POST` | `/v1/messages/count_tokens`、无前缀 `/messages/count_tokens`、`/antigravity/v1/messages/count_tokens` | Anthropic/OpenAI 桥接 token 估算 |
| `POST` | `/v1/chat/completions`、无前缀 `/chat/completions` | OpenAI Chat Completions |
| `POST` | `/v1/responses`、无前缀 `/responses`、`/backend-api/codex/responses` | OpenAI Responses |
| `POST` | 上述 Responses 路径的 `/{subpath}` | 可转发的 Responses 子路径，如 `input_tokens` |
| `GET` | `/v1/responses`、`/responses`、`/backend-api/codex/responses` | Responses WebSocket 升级入口 |
| `POST` | `/v1/embeddings`、无前缀 `/embeddings` | OpenAI Embeddings；仅 OpenAI 账号组 |
| `POST` | `/v1/images/generations`、`/v1/images/edits` 及无前缀别名 | OpenAI/Grok 图片 |
| `POST` | `/v1/images/generations/async`、`/v1/images/edits/async` 及无前缀别名 | 提交异步图片，需管理员启用图片对象存储 |
| `GET` | `/v1/images/tasks/{task_id}` 及无前缀别名 | 使用原 Key 查询异步图片结果 |
| `POST/GET/DELETE` | `/v1/images/batches...` | Gemini / Vertex 批量图片任务 |
| `POST/GET` | `/v1/videos...` 及无前缀别名 | Grok 视频创建、编辑、扩展、查询和内容下载 |
| `POST/GET/PATCH/DELETE` | `/v1/tts`、`/v1/stt`、`/v1/custom-voices...` | Grok Voice |
| `POST` | `/v1/systemone`、无前缀 `/systemone` | OpenCode Zen Jev System One |
| `POST` | `/v1/alpha/search`、无前缀 `/alpha/search`、`/backend-api/codex/alpha/search` | OpenAI/Codex 搜索兼容入口 |
| `POST` | `/v1/web_search`、`/v1/x_search` 及无前缀别名 | Grok 搜索入口 |
| `GET` | `/v1/realtime` 及无前缀别名 | Grok Realtime WebSocket 升级入口 |
| `GET/POST` | `/v1beta/models...`、`/antigravity/v1beta/models...` | Gemini 原生 GenerateContent |
| `POST/GET/DELETE` | `/api/v3/contents/generations/tasks...`、`/v3/...`、`/v1/...`、`/contents/...` | Seedance/火山方舟原生视频任务 |

### OpenAI Chat Completions

#### `GET /v1/models`

模型列表会根据 API Key 的分组、可见模型目录、渠道映射和账号能力过滤；复合 Key/智能路由 Key 返回候选分组合并后的可请求模型。常见响应结构为：

```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-6-astra",
      "object": "model",
      "type": "model",
      "created": 1704067200,
      "owned_by": "openai",
      "display_name": "gpt-6-astra"
    }
  ]
}
```

`GET /v1/models/{model}` 使用相同目录规则返回单模型结果；模型不存在或不可见时统一返回 `404`，错误码为 `model_not_found`。

#### `POST /v1/chat/completions`

```json
{
  "model": "gpt-6-astra",
  "messages": [
    {"role": "system", "content": "You are concise."},
    {"role": "user", "content": "Hello"}
  ],
  "stream": false,
  "temperature": 0.2,
  "max_tokens": 512,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get weather",
        "parameters": {"type": "object", "properties": {}}
      }
    }
  ],
  "tool_choice": "auto"
}
```

非流式成功响应保持 OpenAI 形状：

```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion",
  "created": 1760000000,
  "model": "gpt-6-astra",
  "choices": [
    {
      "index": 0,
      "message": {"role": "assistant", "content": "Hello"},
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 12,
    "completion_tokens": 8,
    "total_tokens": 20
  }
}
```

`stream=true` 时返回 `text/event-stream`，发送 OpenAI `chat.completion.chunk` 事件并以 `data: [DONE]` 结束。上游工具调用会转换为客户端协议对应的 tool call；流式开始后发生错误时发送协议允许的错误事件。

### OpenAI Responses

#### `POST /v1/responses`

```json
{
  "model": "gpt-6-astra",
  "input": [
    {"role": "user", "content": [{"type": "input_text", "text": "Hello"}]}
  ],
  "stream": false,
  "tools": [
    {
      "type": "function",
      "name": "get_weather",
      "description": "Get weather",
      "parameters": {"type": "object", "properties": {}}
    }
  ],
  "tool_choice": "auto",
  "metadata": {"user_id": "client-session-1"}
}
```

非流式响应保持 Responses 原生对象，常见字段如下：

```json
{
  "id": "resp_...",
  "object": "response",
  "status": "completed",
  "model": "gpt-6-astra",
  "output": [
    {
      "type": "message",
      "role": "assistant",
      "content": [{"type": "output_text", "text": "Hello"}]
    }
  ],
  "usage": {
    "input_tokens": 12,
    "output_tokens": 8,
    "total_tokens": 20
  }
}
```

Responses 子路径只有代码明确允许的路径才会转发；未知子路径返回 `404`。`GET /v1/responses` 用于 WebSocket，是否支持取决于最终平台和分组能力。

### Anthropic Messages

#### `POST /v1/messages`

```json
{
  "model": "claude-sonnet-4",
  "max_tokens": 1024,
  "system": "You are helpful.",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "Hello"}]}
  ],
  "stream": false,
  "tools": [
    {
      "name": "get_weather",
      "description": "Get weather",
      "input_schema": {"type": "object", "properties": {}}
    }
  ]
}
```

成功响应遵循 Anthropic Messages 形状：

```json
{
  "id": "msg_...",
  "type": "message",
  "role": "assistant",
  "model": "claude-sonnet-4",
  "content": [{"type": "text", "text": "Hello"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 12, "output_tokens": 8}
}
```

### Gemini 原生接口

#### `GET /v1beta/models`

返回 Gemini 模型目录。生成请求采用 Google 原生路径：

```http
POST /v1beta/models/gemini-2.5-pro:generateContent
POST /v1beta/models/gemini-2.5-pro:streamGenerateContent
POST /v1beta/models/gemini-2.5-pro:countTokens
x-goog-api-key: <tokenrouter_api_key>
```

请求体使用 Gemini 原生 `contents`、`systemInstruction`、`generationConfig`、`tools` 和 `safetySettings` 字段，响应使用 Google `candidates`、`usageMetadata` 和 `promptFeedback` 字段。`streamGenerateContent` 返回 Gemini SSE/JSON 流，不转换为 OpenAI envelope。

### OpenCode Zen Jev System One

#### `POST /v1/systemone`

只允许 OpenCode Zen 分组和 `jev-1.13`、`jev-1.13-free` 模型，不支持 `stream=true`。请求体：

```json
{
  "model": "jev-1.13",
  "state": "classify",
  "questions": {
    "is_urgent": {"type": "noul", "instructions": "Is this urgent?"}
  }
}
```

`state` 必须存在，`questions` 必须是非空对象。示例采用项目回归测试中的结构；具体问题类型由 System One 上游校验。成功响应保留原生 JSON，包含 `answers` 和 `usage`，例如：

```json
{
  "model": "jev-1.13",
  "answers": {
    "is_urgent": {"type": "noul", "value": false}
  },
  "usage": {
    "input_tokens": 12,
    "output_tokens": 1
  }
}
```

该端点不经过普通 Messages、Chat Completions 或 Responses 转换，也不支持图片、音频、视频和流式响应。OpenCode 账号会派生 `X-OpenCode-Session` 以保持提示词缓存命中。

### OpenAI / Grok 异步图片

#### `POST /v1/images/generations/async`

使用原有图片请求字段，`model` 必须是当前分组支持的图片模型，`size`/`quality`/`n` 的支持范围与同步接口一致。`prompt` 必须是非空字符串，缺失、空白或类型错误会在创建任务前返回 400。不接受 `stream: true`。

```json
{
  "model": "gpt-image-2",
  "prompt": "一幅山间日出的水彩画",
  "size": "3840x2160",
  "n": 1,
  "quality": "auto"
}
```

启用对象存储并通过请求校验后立即返回 **HTTP 202**，`Location` 指向轮询地址，建议按 `Retry-After: 3` 间隔查询：

```json
{
  "id": "imgtask_example",
  "task_id": "imgtask_example",
  "object": "image.generation.task",
  "status": "processing",
  "created_at": 1750000000,
  "expires_at": 1750086400,
  "poll_url": "/v1/images/tasks/imgtask_example"
}
```

`POST /v1/images/edits/async` 使用同步编辑接口的 JSON 或 multipart 形状，包括模型和图片、mask 等字段。异步只是改变提交和获取结果的方式，不改变图片参数、路由或计费规则。没有启用 S3 图片存储时返回 404；不要将它当作可无条件替换同步接口的 SDK 协议。

#### `GET /v1/images/tasks/{task_id}`

继续使用创建任务的同一个 TokenRouter API Key。处理中返回 `status: processing`；完成后示例如下，`image_url` 是第一张图片，完整结果在 `result`：

```json
{
  "id": "imgtask_example",
  "task_id": "imgtask_example",
  "object": "image.generation.task",
  "status": "completed",
  "http_status": 200,
  "image_url": "https://images.example.com/images/imgtask_example/0.png",
  "result": {
    "created": 1750000120,
    "data": [{"url": "https://images.example.com/images/imgtask_example/0.png"}]
  },
  "created_at": 1750000000,
  "completed_at": 1750000120,
  "expires_at": 1750086520
}
```

失败任务的查询仍返回 HTTP 200，任务内 `status: failed`、`http_status` 和 `error` 表示执行失败原因。任务不存在、过期或归属不匹配返回 404。创建和完成时分别保留 24 小时查询记录；S3 图片链接有效期独立于任务保留期。已落库的完成结果可跨重启和 Redis 丢失继续查询。执行实例失联后会登记 `failed / 503`、`error.type: execution_interrupted`，表示上游结果不确定且可能已经计费；不会静默一直处理中。轮询和状态补偿不再次生成或收费；图片已经生成但结果存储失败时可能仍有真实用量扣费，不要看到失败就自动重复生成。未完成上游调用无法在进程重启后安全续跑。

该方式让长耗时生成在后台执行，客户端通过短请求取结果；客户端需要实现提交、轮询与保存返回链接。详细权限、结果存储和计费边界见[异步图片与任务记录](../domains/media_tasks.md)。

### Seedance 原生视频任务

以下路径等价：

```text
/api/v3/contents/generations/tasks
/v3/contents/generations/tasks
/v1/contents/generations/tasks
/contents/generations/tasks
```

创建：

```http
POST /api/v3/contents/generations/tasks
Authorization: Bearer <tokenrouter_api_key>
Content-Type: application/json
```

```json
{
  "model": "seedance-model-or-endpoint-id",
  "content": [
    {"type": "text", "text": "A cinematic city at sunset"}
  ]
}
```

查询和删除：

```http
GET    /api/v3/contents/generations/tasks/{task_id}
DELETE /api/v3/contents/generations/tasks/{task_id}
```

请求和响应保持方舟原生格式，不包装成 TokenRouter envelope。任务查询固定原账号和任务归属，完成任务的真实 usage 才进入结算。

## 网关限制和安全

- 网关请求必须经过 API Key、用户/团队归属、分组协议、模型目录和账号能力检查。模型不存在于候选分组时不会发送到该分组。
- 智能路由按 Key 中保存的候选顺序尝试；遇到可恢复的上游错误会切换候选分组，所有候选都处于冷却或不可调度时返回 `503`。
- `Authorization`、`x-api-key`、`x-goog-api-key`、Cookie、OAuth 凭据和代理凭据不要写入客户端日志。
- 请求体受网关启动配置的大小限制；超限返回 `413`。流式请求开始后发生错误时，服务端只能通过当前 SSE 协议发送终止错误事件。
- 图片、视频、音频、工具调用和多模态字段是否可用，取决于最终分组平台、模型目录和账号能力；应先调用模型目录或查阅平台专题文档。
- 具体模型、价格和协议能力以当前部署的分组配置为准，不应仅根据本文示例模型名推断部署实例一定启用该模型。

## 相关文档

- [HTTP 接口边界](http_api.md)
- [OpenAI 上游](openai_upstream.md)
- [Anthropic 上游](anthropic_upstream.md)
- [Gemini 上游](gemini_upstream.md)
- [OpenCode Zen / GO 上游](opencode_upstream.md)
- [Seedance / 火山方舟原生视频任务](seedance_upstream.md)
