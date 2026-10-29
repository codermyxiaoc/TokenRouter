# TokenRouter AI 网关 API 参考

本文只记录当前项目面向模型客户端的 AI 网关接口，依据 `backend/internal/server/routes/gateway.go`、网关 handler 和请求 DTO 整理。面板认证、用户、支付、工单及管理员接口不在本文范围内；上游转换细节见文末专题文档。

## 站内文档页面

前端 `/docs` 提供公开的 AI 网关文档，`/docs/:documentId` 是可分享、可恢复的稳定正文地址；后台模式仍沿用现有公开页面守卫，不绕过站点访问策略。登录用户使用 AppLayout，访客使用同主题的公开页面；首页、模型广场和用户/管理员个人导航均有入口，原有外部 `doc_url` 配置继续保留。

结构化目录位于 `frontend/src/content/api-docs/`，按文本/模型、图片/语音、视频划分协议内容，共用 `types.ts` 和页面渲染组件。目录检索覆盖路径、别名、参数和平台，并按十二个具体上游平台筛选；同一 URL 可以有不同能力的使用指南，平台标签不替代账号能力和分组协议门禁。代码示例使用站内公开 API Base URL 或当前源站地址与占位环境变量，不读取用户 Key，也不在浏览器执行示例请求。复制 MD 与页面渲染读取同一份 Markdown。

<a id="api_docs_markdown"></a>
### Markdown 正文与目录管理

每篇文档都以 Markdown 渲染（`ApiDocMarkdown.vue`，marked 解析后经 DOMPurify 净化，站内 `/…` 链接走前端路由，二级标题生成右侧大纲）。目录采用“内置 + 叠加”：内置分类（含“指南”）与文档由 `content/api-docs/` 从结构化目录生成，数据库只保存管理员的改动与新增，前端在 `content/api-docs/catalog.ts` 合并两者。

- **存储**：迁移 `323_api_docs_catalog.sql` 新建 `api_doc_categories` 与 `api_docs`，每个分类或文档一行。可空的标题、所属分类、正文为 NULL 时沿用内置值，未改动的内置文档随版本升级自动更新；`custom=TRUE` 的自定义条目必须完整。升级时把旧设置键 `api_doc_overrides` 中合法的逐篇改写导入为文档行，损坏的旧值跳过；旧键保留不删，便于回滚。
- **两级目录**：迁移 `325_api_doc_category_parent.sql` 为分类增加 `parent_id`。一级分类（如“视频与异步任务”）下可有二级分类（如 OpenAI Videos、Seedance 等视频协议，内置 ID 为 `video-<协议>`），文档可挂在一级或二级分类下。上级只能是一级分类，目录最多两级；已有子分类的分类不能再挂到其它分类下。内置分类层级固定，自定义分类可在新增或编辑时选择上级。
- **增删改**：管理员在“系统设置 → API 文档”的左侧目录新增分类或文档，右侧逐篇编辑标题、所属分类、是否显示与 Markdown 并实时预览。目录每一行都可直接删除：自定义文档与空的自定义分类被真正删除；内置文档、内置分类以及仍有内容的分类“删除”后隐藏，内容保留，目录中显示“已删除”并可一键恢复。内置条目只保存与内置不同的字段，“恢复默认”清除这些叠加；自定义分类在仍有文档或子分类时不能被彻底删除。文档 ID 即访问地址 `/docs/<ID>`，创建后不可修改，不能与内置或已有 ID 重复。
- **可见性**：公开页面合并目录后，隐藏文档、分类及其下级分支不显示，内容保留并可恢复；一级分类隐藏同时作用于其二级分类。单篇正文 API 校验文档自身以及持久化的显式分类、父分类隐藏状态。历史内置文档改写的 `category_id=NULL` 或内置二级分类没有显式父级叠加时，服务端不能仅凭这些记录完整推断前端默认层级；需要保密的改写应逐篇隐藏或显式保存所属分类，不能仅依赖目录折叠或父级隐藏。
- **排序**：一级分类之间、同一上级下的二级分类之间、分类内文档之间可上下移动，整层顺序一次提交。内置条目没有叠加行时以 `version=0` 的排序占位行保存，不改变内容版本；之后编辑会接管占位行并保留排序。同一排序请求中，文档 ID 不能在同一分类或跨分类列表重复。
- **并发**：每行有 `version`，修改与删除都必须携带编辑起点的版本，不一致返回 409 `API_DOC_CONFLICT`，编辑页保留草稿并刷新最新版本，确认后再次保存才覆盖；新建时 ID 已存在同样返回冲突。保存期间继续输入的内容不会被响应覆盖。
- **接口**：公开 `GET /api/v1/settings/api-docs` 返回不含正文、保留隐藏标记的叠加目录，前端据此过滤内置与自定义条目，`GET /api/v1/settings/api-docs/:id` 只在存在改写正文时返回单篇（隐藏或无改写为 404，页面回退内置正文）。管理端位于 `/api/v1/admin/settings/api-docs`：`GET` 目录（含隐藏）、`GET|PUT|DELETE /documents/:id`、`PUT|DELETE /categories/:id`（删除带 `?version=`）、`PUT /order`。单篇正文上限 20 万字，标题 200 字。

改写正文中的 `{{BASE_URL}}` 替换为当前网关地址。接口总览未改写时按可见接口与目录搜索、平台筛选生成表格，标题采用改名后的值；指南与自定义文档按标题检索，不受平台筛选影响。读取目录失败时页面回退到内置目录。

维护路由或协议时同时核对这里的工程参考与对应前端内容，包括 HTTP 方法、任务归属、别名、请求头、参数、响应与错误条件。不能把已注册的通配路径当作任意上游能力，也不能把厂商文档中的未注册接口加入可用清单；原生视频按各协议保真，统一入口不代表自动转换所有厂商参数。

视频目录按协议与模型家族细分为 OpenAI Videos、Seedance / 火山方舟、Kling、万相、MiniMax、Vidu 和 Grok 子标题；同一真实端点可以有不同接入方案的模型指南，不代表新增服务端路由。视频检索覆盖型号示例、参数说明与使用注意事项，能够按实际模型名找到对应指南；其它分类维持原有检索范围。搜索和平台筛选后隐藏空子分类，保留原文档地址与跨平台接口归属。

## 快速约定

### 地址、鉴权和请求头

设部署地址为 `https://api.example.com`。AI 网关默认使用以下路径前缀：

- `/v1`：OpenAI、Anthropic、Grok、OpenCode 等兼容接口。
- `/v1beta`：Gemini 原生 GenerateContent 接口。
- `/api/v3`、`/v3`、`/v1` 或无前缀的 `/contents/generations/tasks`：Seedance（火山方舟）原生视频任务接口。
- `/ent/v2`：Vidu 原生视频创建和产物查询，要求 Video 账号显式启用 `vidu` 与对应协议方案。
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
| `POST/GET` | `/v1/videos`、`/v1/videos/{video_id}`、`/v1/videos/{video_id}/content` 及无前缀别名 | Video 分组唯一视频入口：统一请求格式创建、查询和下载，见 [Video 平台统一视频任务](#video-平台统一视频任务) |
| — | `/v1/video/generations`、`/video/generations`、`/v1/tasks/{task_id}` 及各家原生视频路径 | Video 分组已下线，返回 `410 VIDEO_ENDPOINT_RETIRED`；请改用 [Video 平台统一视频任务](#video-平台统一视频任务) |
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

HTTP 200 只表示查到了任务，不能据此认定生成成功。客户端应按以下规则维护待查询队列：

| 查询结果 | 客户端处理 |
| --- | --- |
| HTTP 200，`status=processing`，且未到 `expires_at` | 继续查询原任务，遵循 `Retry-After`（当前为 3 秒） |
| HTTP 200，`status=completed` | 保存 `result` 中的图片，停止轮询并移出队列 |
| HTTP 200，`status=failed` | 读取任务内的 `http_status` 和 `error`，停止轮询并移出队列；不得仅因失败自动重新生成 |
| HTTP 404，`error.code=IMAGE_TASK_NOT_FOUND` | 停止该 ID 的轮询并移出队列，核对任务 ID 和创建时的 Key；不自动重新提交生成 |
| 已达到最近响应中的 `expires_at` | 停止轮询并移出队列，即使客户端仍把任务记为处理中 |
| 查询网络失败或临时服务错误 | 只对原查询做有最大尝试次数或截止时间的退避重试；不能恢复已终止的任务轮询，也不能重发生成请求 |

不存在、过期和用户或 API Key 归属不匹配均返回同样的 404，不能仅凭该错误判定任务过期。实际错误响应如下，`error.type` 与 `error.code` 均为 `IMAGE_TASK_NOT_FOUND`：

```json
{
  "error": {
    "type": "IMAGE_TASK_NOT_FOUND",
    "code": "IMAGE_TASK_NOT_FOUND",
    "message": "image task not found"
  }
}
```

`expires_at` 是 Unix 秒时间戳。创建时保留 24 小时查询记录，完成或失败时重新起算 24 小时；任务历史、R2/S3 图片保存期和链接有效期与查询记录期限独立。后台历史仍存在不代表已过期任务结果还能从查询接口取回。任务查询服务未初始化的 404 仍使用 `not_found_error`；存储暂不可用返回 `503 / IMAGE_TASK_UNAVAILABLE`，可有界退避重试原查询。

已落库的完成结果可跨重启和 Redis 丢失继续查询。执行实例失联后会登记 `failed / 503`、`error.type: execution_interrupted`，表示上游结果不确定且可能已经计费；不会静默一直处理中。轮询和状态补偿不再次生成或收费；图片已经生成但结果存储失败时可能仍有真实用量扣费，不要看到失败就自动重复生成。未完成上游调用无法在进程重启后安全续跑。

该方式让长耗时生成在后台执行，客户端通过短请求取结果；客户端需要实现提交、轮询与保存返回链接。详细权限、结果存储和计费边界见[异步图片与任务记录](../domains/media_tasks.md)。

### Video 平台统一视频任务

使用绑定 Video 分组的站内 API Key。无论上游是 Seedance、Kling、Wan、MiniMax、Vidu 还是中转服务，客户端都只用一种请求格式；站内按账号为该模型绑定的协议转换请求、查询任务并取回成品。模型及分辨率价档或兜底价需要预先配置。

```bash
curl https://api.example.com/v1/videos \
  -H "Authorization: Bearer $TOKENROUTER_API_KEY" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: my-request-001" \
  -d '{"model":"video-model","prompt":"A city at sunset","duration":5,"resolution":"720p","aspect_ratio":"16:9","images":[{"url":"https://media.example.com/first-frame.jpg","role":"first_frame"}]}'
```

字段：`model`（必填）、`prompt`、`negative_prompt`、`duration`（秒）、`resolution`、`aspect_ratio`、`images`（URL 或 `{url, role}`，角色 `first_frame`/`last_frame`/`reference_image`）、`videos`（`{url, role: "reference_video"}`）、`generate_audio`、`watermark`、`seed`，以及放置厂商特有参数的 `extra` 对象。兼容 OpenAI 写法 `seconds`、`size`、`input_reference: {"image_url": ...}`，以及比例别名 `ratio`。未知顶层字段、冲突的别名、所绑定协议无法表达的参数（例如对不支持的模型提交参考视频或尾帧）都会在预扣前返回 400，不会被静默丢弃。旧 `/v1/video/generations` 与各家原生路径已下线，返回 `410`。

任务返回本站 `vid_` 开头的 ID，成功受理响应包含 `object: "video"`、`model`、`created_at`、`status` 和 `billing_status`。已知时输出 `seconds/size` 并保留 `duration/resolution` 兼容字段；按秒结算读取可信终态秒数。用原 Key 查询或下载：

```http
GET /v1/videos/{video_id}
Authorization: Bearer <tokenrouter_api_key>

GET /v1/videos/{video_id}/content
Authorization: Bearer <tokenrouter_api_key>
Range: bytes=0-1048575
```

`Range` 可省略。状态为 `queued`、`in_progress`、`completed`、`failed`；失败附带 `error`。`completed` 只表示上游生成完成，`billing_status: "settled"` 才表示结算成功。受理不明等待核对时额外返回 `task_status`，不要自动重新提交；上游过期、长时间未完成或没有可取回成品的任务会失败并释放预扣，不会扣费。内容接口仅允许已完成且已结算任务，其他状态返回 `409`；不存在或不属于当前用户/Key 返回 `404`。查询与下载复用原任务，不再次生成。统一入口不提供 `DELETE`（取消）、集合列表或 multipart。

### 视频模型与接入方案调用细节

所有 Video 分组模型都通过 `POST /v1/videos` 提交统一请求，站内按账号对该模型绑定的协议转换。选择模型前先确认当前 Key 的模型目录；示例中的上游型号可以经站内模型映射提供给用户，但限制由映射后的实际型号与其绑定协议决定。创作台 1–60 秒及自定义分辨率是界面可编辑范围，不能据此承诺所有供应商型号支持这些参数。模型的使用说明也不替代接口参数与价格校验。

| 绑定协议 | 统一请求如何转换 | 参考图和参数注意事项 |
| --- | --- | --- |
| 可美预置模板（`profile:kemei-*`），含 Seedance 2.5 `doubao-seedance-2-5-260628` | `prompt`、`images`（保留 `role`）、`duration`、`resolution`、`aspect_ratio`→`ratio` 按可美各端点模板转换 | 文生与普通参考图可指定比例；首帧/首尾帧必须 `adaptive`。2.5 为 480p/720p/1080p、4–30 整数秒或 -1；仅说明可美合同，不推广到其他适配 |
| Seedance 官方（`seedance.seedance_v1`） | `prompt` 与 `images`/`videos` 转为 `content`，角色保留 | 首帧和参考图角色显式区分。官方 2.5 首尾帧比例只能省略或 adaptive |
| MiniMax H3 V2（`minimax.minimax_v2`） | 同上转为 `content`；`duration`、`resolution` 保留 | MiniMax V2 必填 resolution；不支持参考视频 |
| 秘塔 H3（`profile:metaso-h3`） | `duration`→`seconds`，`resolution`+`aspect_ratio`→像素 `size`，单图→`input_reference.image_url` | 1280x720/720x1280 对应 768p；1792x1024/1024x1792 对应 2k。不能按图片短边或数学比例猜测档位 |
| Yezonghui（`profile:yezonghui`） | `duration`→`seconds`，`aspect_ratio`→比例 `size`，图片→`image_urls` | 参考媒体只用 HTTP(S) 公共链接，不接受 Data URL；不能照搬 OpenAI 像素 size |
| Kling（`kling.kling_v1`、`profile:kemei-kling-*`） | 操作路径来自绑定：文生/图生/Omni；官方方案的 `images` 与比例写入 `settings` | 文生路径不接受参考图、图生路径要求参考图；Omni 只有可美模板，参考图与参考视频改名为原生角色，至多一个参考视频 |
| Wan（`wan.*`） | `prompt`/`negative_prompt`/首帧→`input`，时长、分辨率、比例→`parameters` | 只接受一张首帧图；不支持参考视频 |
| Vidu（`vidu.*`） | 操作路径来自绑定（img2video、reference2video、text2video、start-end2video） | 单图、参考、无图、首尾帧对应不同绑定路径；可美模板默认 5 秒/540p/16:9 及 Q4 限制不套用到官方方案 |
| 声明式模板（`profile:*`） | 按管理员配置的映射规则生成请求体 | 模板未映射的字段一律拒绝；参数范围以模板声明的能力为准 |

Grok 分组的视频不属于 Video 平台，继续使用 `/v1/videos/generations` 的 xAI 合同（`duration`、`resolution`、`aspect_ratio`、`image.url`），见 [Grok / xAI 上游](grok_upstream.md)。

可美当前[统一视频文档](https://token.xinhankr.com/docs/openai/doc0019)区分图片角色。单个字符串图片通常推断为首帧，两张推断为首尾帧；需要普通参考语义时应显式提供 `reference_image`，不能靠提示词中的“参考图”代替角色。首尾帧示例：

```json
{
  "model": "doubao-seedance-2-5-260628",
  "prompt": "人物沿海岸缓步前行，镜头平稳跟随",
  "images": [
    {"url": "https://media.example.com/first-frame.png", "role": "first_frame"},
    {"url": "https://media.example.com/last-frame.png", "role": "last_frame"}
  ],
  "ratio": "adaptive",
  "resolution": "720p",
  "duration": 5
}
```

只有首帧时移除 last_frame 项，仍保留 adaptive。需要 16:9 输出时先把首帧裁剪为 16:9；普通参考生则使用 `images: [{"url":"https://media.example.com/character.png","role":"reference_image"}]`、模型支持的固定比例及 `extra: {"omni_reference_task_type": "auto"}`。视频编辑 edit 与延长 extend 不是首帧模式：可美 2.5 两者都要求 adaptive，edit 还要求 duration=-1。媒体 URL、素材数量及内容限制依具体模型和本站安全门禁；不要混入另一套原生 content。

`/docs/kemei-seedance25-text`、`/docs/kemei-seedance25-frames`、`/docs/kemei-seedance25-reference` 分别提供可复制的完整请求；`/docs/video-metaso-h3`、`/docs/video-yezonghui-create` 分开说明同一 `/v1/videos` 路径下的尺寸与参考图合同。既有 Kling、Wan、MiniMax、Vidu、Grok 文档补充各自结构和查询边界。新增说明描述调用者应遵守的合同，不改变网关参数适配或生成行为。

创建成功后保存任务 ID，并以同一 Key 轮询对应查询接口。统一视频接口使用本站 `vid_…`，原生接口使用供应商 ID；`X-Video-Task-ID` 可关联本站账务查询。只有明确 completed 且 billing_status=settled 才能依独立 Video 的内容接口下载；HTTP 200 的 failed 不是生成成功。失败释放需检查 billing_status=released，不能从上游 error 单独推断退款。已确认失败后修正参数可使用新的幂等键创建新任务；创建超时、断线或状态 unknown 时先核对原任务，不能为刷新进度再次 POST。`duration=-1` 仍有预算和实际用量结算要求，比例不替代分辨率价格档位。

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
