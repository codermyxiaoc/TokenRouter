# 独立 Video 上游

本文拥有 `platform=video` 的统一入口、账号协议绑定、声明式协议模板、内置原生协议和 HTTP 兼容边界。任务持久化、预算预留与结算由[独立视频平台任务](../domains/video_tasks.md)、[媒体任务](../domains/media_tasks.md)和[路由与计费](../domains/routing_and_billing.md)拥有。旧 [Grok 视频](grok_upstream.md)与 [OpenAI 账号的 Seedance 能力](seedance_upstream.md)继续使用原链路；Video 平台的重构不改变其计价或准入。

## 章节导航

- [统一入口 /v1/videos](#video_unified_api)：客户端唯一的创建、查询与内容接口，以及统一请求格式。
- [模型协议绑定](#video_accounts)：账号按“上游模型 → 协议”绑定内置协议或模板，旧端点配置的迁移规则与预算上限。
- [声明式协议模板](#video_protocol_profiles)：用配置描述创建端点、请求体映射、任务 ID、查询路径、状态与产物，以及版本、快照和调试器。
- [AI 生成模板](#video_protocol_ai)：在系统设置配置模型后，由上游文档生成模板草稿并自动校验修正。
- [预置模板](#video_protocol_presets)：Metaso H3、Yezonghui、可美与通用扁平格式的模板化版本、旧方案自动改绑与等价验证。
- [版本化协议适配](#video_adapters)：内置协议只保留各家官方格式，以及旧方案在途任务的兼容。
- [Yezonghui 视频兼容](#video_yezonghui)：Yezonghui 预置模板的分辨率、参考图交付和异步下载合同。
- [上游协议与已下线入口](#video_protocols)：内置协议的上游路径、参数保真、任务 ID 规则，以及旧入口的 410 行为。
- [视频内容](#video_openai_content)：凭据隔离的流式下载与面板预览。
- [管理员账号测试](#video_account_tests)：按模型绑定的协议提交一次测试视频并轮询预览。
- [验证边界](#验证边界)：理解本地协议测试的保证范围。

<a id="video_unified_api"></a>
## 统一入口 /v1/videos

Video 平台对客户端只保留三项接口，均使用站内 Bearer Key：

| 方法与路径 | 用途 |
| --- | --- |
| `POST /v1/videos` | 创建任务，请求体为统一格式，只接受 `application/json` |
| `GET /v1/videos/{task_id}` | 按本地 `vid_…` ID 查询任务；只读本地记录，上游轮询由后台恢复器完成 |
| `GET /v1/videos/{task_id}/content` | 读取已完成且已结算任务的成品，见[视频内容](#video_openai_content) |

不对外开放取消；不带 `/v1` 的别名继续可用。同一路径在 Grok 分组下仍由原 Grok 链路处理，分派依据分组平台而非请求内容。

统一请求由 `ParseVideoCanonicalRequest` 严格解析，未知顶层字段、类型错误和别名冲突在选号与预扣前返回 400：

```json
{
  "model": "kling-v2",
  "prompt": "海边日落，镜头缓慢推进",
  "negative_prompt": "模糊",
  "duration": 5,
  "resolution": "720p",
  "aspect_ratio": "16:9",
  "images": [{"url": "https://... 或 data:image/png;base64,...", "role": "first_frame"}],
  "videos": [{"url": "https://...", "role": "reference_video"}],
  "generate_audio": false,
  "watermark": false,
  "seed": 42,
  "extra": {"camera_control": {"type": "zoom_in"}}
}
```

- `model` 必填，经分组/渠道映射与账号模型映射后得到最终上游模型。
- 时长使用 `duration`（数字秒），兼容 OpenAI 的 `seconds`（字符串或数字）；比例使用 `aspect_ratio`，兼容旧客户端的 `ratio`。别名与规范字段同时出现时必须一致。
- 参考图使用 `images`，元素可为 URL 字符串或 `{url, role}`，角色取 `first_frame`、`last_frame`、`reference_image`；兼容 OpenAI 的 `input_reference: {image_url}`，但不能与 `images` 同时出现。参考视频使用 `videos`，角色仅 `reference_video`。参考素材合计最多 16 项。
- `size` 仅作为 OpenAI 像素尺寸别名，只有 compat / openai_videos 内置协议或模板显式映射时才接受；其它协议应使用 `resolution` 与 `aspect_ratio`。
- 厂商特有参数放入 `extra`；内置协议把它合并到请求体顶层且不能覆盖规范字段，模板协议只接受映射规则显式引用的 `extra.<key>`。
- 可选请求头 `Idempotency-Key` 沿用原规则：最多 128 字符，同一 Key 内按原始请求字节比较。

选号时对每个候选账号按其对该模型绑定的协议转换请求：内置协议由 `videoBuiltinRequestBody` 转为端点格式，再交给原有协议适配器校验并从**实际发送的请求体**提取时长、分辨率和参考图数量；模板协议由引擎生成请求体并提取同样的维度。某个候选的协议不接受参数时可以尝试其它合格账号，全部失败才返回错误，此阶段尚未报价或预扣。端点无法表达的字段直接拒绝，不能静默丢弃参考素材或改变语义。

创建与查询响应为 OpenAI Video 对象：本站 `vid_…` ID、`object=video`、`queued / in_progress / completed / failed` 四态，另附 `billing_status`、已知时长（`duration` 与 `seconds`）、分辨率、像素尺寸和 Token 用量；受理不明或过期时附加 `task_status`。`X-Video-Task-ID` 响应头同样返回本地 ID。任何版本创建的本地任务都可以通过 `GET /v1/videos/{task_id}` 查询。

<a id="video_accounts"></a>
## 模型协议绑定

Video 仅接受 API Key 账号，且 Video 账号与 Video 分组双向隔离。创建、编辑、批量更新和导入共用校验；跳过混合渠道提示不能绕过隔离。分组使用 `basic` 调度，显式提交 `advanced` 返回客户端错误，隐藏的文本调度配置不参与视频请求。

账号 `credentials` 示例：

```json
{
  "api_key": "供应商密钥",
  "base_url": "https://relay.example",
  "video_model_protocols": {
    "kling-v2":   {"protocol": "kling.kling_v1", "path": "/text-to-video/{model}"},
    "seedance-1": {"protocol": "seedance.seedance_v1", "base_url": "https://ark.example/api/v3"},
    "vendor-x":   {"protocol": "profile:vendor_x"},
    "*":          {"protocol": "openai_videos.openai_v1"}
  },
  "video_max_pending_tasks": 10,
  "video_max_duration_seconds": 15
}
```

`video_model_protocols` 是唯一的协议来源，键为**最终上游模型**的精确名称或 `*`（账号其余模型的默认协议，唯一允许的通配符），至少一项。查找顺序为精确模型、再 `*`；没有绑定的模型在转发、智能路由、模型广场和画布目录中一致不可用，返回 `VIDEO_ENDPOINT_MODEL_MISMATCH`。客户端从哪个入口进来不参与协议选择，不按模型名称、列表顺序或请求字段猜测供应商。

`protocol` 取值：

- 内置协议 `端点.方案`：只保留各家官方格式（`compat.openai_v1`、`openai_videos.openai_v1`、`seedance.seedance_v1`、`kling.kling_v1`、`wan.wan_v1`、`minimax.minimax_v2`、`vidu.vidu_v2`），见[版本化协议适配](#video_adapters)，非法组合在保存时拒绝。Kling 必须提供 `/text-to-video/{model}` 或 `/image-to-video/{model}`，Vidu 必须提供四种 `/ent/v2/*` 操作之一，其它内置协议不得提供 `path`。
- 厂商与中转格式（Metaso H3、Yezonghui、可美、原样协议）由[预置模板](#video_protocol_presets)提供。保存账号时旧方案绑定 `*.legacy_v1`、`*.kemei_v1`、`openai_videos.metaso_h3_v1`、`openai_videos.yezonghui_v1` 自动改绑到对应模板或官方协议；没有等价替代的旧绑定（原样协议的旧版 Kling 路径）保存时拒绝，需管理员手动改绑。
- 模板协议 `profile:<键名>` 使用该键名当前启用的版本，`profile:<键名>@<版本>` 锁定版本；模板不存在、未启用或内容哈希不符时，该候选在创建前被拒绝。模板定义自身路径，不得提供 `path`。

`base_url` 覆盖账号共享 `base_url`，必须是没有内嵌凭据、查询参数或 fragment 的 HTTP(S) URL；出站继续经过现有上游 URL 安全校验、代理和 TLS 传输边界。受保护的 Header Override 可用于 Video API Key。

创建与编辑表单不为 Video 账号预填任何 Base URL 或 API Key 示例：账号 Base URL 与 API Key 都必须由管理员填写（编辑时 API Key 留空表示保留原值）。账号调度快照的精简凭据必须保留 `api_key`、`base_url`、`video_model_protocols`、`video_max_pending_tasks` 与 `video_max_duration_seconds`，否则新建账号在选号时没有任何模型绑定、待处理上限按零计算，直到再次编辑才恢复（见[账号调度与缓存](../architecture/account_scheduling_and_cache.md)）。

**旧配置迁移。** 旧字段 `video_endpoints`、`video_model_bindings`、`video_base_urls`、`video_model_paths`、`video_vidu_model_paths`、`video_openai_profile`、`video_endpoint_adapters`、`video_model_adapters` 由 `convertLegacyVideoConfiguration` 换算：

- `video_model_bindings` 中的每个模型生成精确绑定；多个端点按 `openai_videos`、`compat`、配置顺序中首个原生端点的优先级取一个。
- 账号端点集合按同一优先级生成 `*` 默认绑定；默认端点为 Kling 或 Vidu 时无法表达按模型保存的路径，只为配置过路径的模型生成精确绑定。
- 仅有模型级方案覆盖或操作路径、没有显式绑定的模型同样生成精确绑定，方案按“模型覆盖 → 端点默认 → 旧 `metaso_h3` profile → `legacy_v1`”解析，再按[预置模板](#video_protocol_presets)的改绑规则换为模板或官方协议。
- 旧绑定指向未启用端点、非法方案等含糊配置拒绝迁移，不静默改选。

账号保存时写入 `video_model_protocols` 并删除全部旧字段；服务启动时先由 `EnsureVideoProtocolPresets` 补齐预置模板，再由 `BackfillVideoAccountProtocols` 一次性迁移仍保存旧字段或仍绑定旧厂商方案的账号，无法换算的账号保留原样并记录日志，运行时读取仍会即时换算，不影响服务。原先按入站入口在多个端点间选择的账号迁移后每个模型只保留一个协议，需要不同协议时应拆分模型或账号。

`video_max_pending_tasks` 默认 10、范围 1–1000。可选的正数 `video_max_duration_seconds` 支持省略或用 `null` 显式清空；按秒计费或已开启 Token 秒价预扣的自动时长任务必须配置它，显式零和非法值仍拒绝。账号不再提供 Token 预留上限；遗留 `video_max_output_tokens` 对新任务无效，保存时清理，已受理任务仍按自身快照结算。Token 预扣由模型价卡的 `video_token_prepay` 决定，固定秒价不乘倍率，完成后按实际 Token 退补差；关闭则完成后原子检查资金和额度并扣费。预算不改写供应商参数或裁剪用量，缺少可信用量不能作零费用结算。详见[任务结算](../domains/video_tasks.md#video_task_billing)。

<a id="video_protocol_profiles"></a>
## 声明式协议模板

模板让管理员不改代码接入新上游，例如“创建走 `/v1/video/generations`、查询走 `/v1/tasks/{task_id}`、任务 ID 位于 `data.task_id`”。模板保存在 `video_protocol_profiles`（迁移 `322_video_protocol_profiles.sql`），每次保存生成同键名的新版本，同一键名最多一个启用版本；规格以 sha256 哈希保护。管理接口：

| 方法与路径 | 用途 |
| --- | --- |
| `GET /api/v1/admin/video-protocol-profiles` | 列出全部版本 |
| `POST /api/v1/admin/video-protocol-profiles` | 校验并保存新版本，可同时启用 |
| `PUT /api/v1/admin/video-protocol-profiles/:key/versions/:version/status` | 启用、停用或改回草稿；启用时该键名原启用版本降为草稿，锁定该版本的绑定仍可使用；停用版本不能用于新任务 |
| `POST /api/v1/admin/video-protocol-profiles/preview` | 用请求与响应样例离线调试，不访问任何上游 |

规格示例：

```json
{
  "auth": {"scheme": "bearer"},
  "capabilities": {"durations": [5, 10], "resolutions": ["720p", "1080p"], "max_images": 1, "image_roles": ["first_frame"]},
  "create": {
    "path": "/v1/video/generations",
    "defaults": {"duration": 5},
    "body": [
      {"from": "model", "to": "model"},
      {"from": "prompt", "to": "input.prompt", "required": true},
      {"from": "duration", "to": "parameters.duration", "type": "int"},
      {"from": "resolution", "to": "parameters.resolution", "map": {"720p": "720P", "1080p": "1080P"}},
      {"from": "images[role=first_frame]", "to": "input.img_url"},
      {"const": true, "to": "parameters.prompt_extend"}
    ],
    "response": {"task_id": ["data.task_id"], "rejected_when": [{"path": "code", "not_in": [0]}]}
  },
  "poll": {
    "path": "/v1/tasks/{task_id}",
    "interval_seconds": 15,
    "max_processing_minutes": 120,
    "response": {
      "root": "data", "task_id": ["task_id"], "status": "task_status",
      "status_map": {"SUCCEEDED": "completed", "FAILED": "failed", "RUNNING": "processing", "PENDING": "queued"},
      "video_url": ["output.video_url"], "seconds": ["usage.duration"], "tokens": ["usage.completion_tokens"]
    }
  },
  "content": {"mode": "url"}
}
```

- **鉴权**：`bearer`（`Authorization: Bearer`）、`token`（`Authorization: Token`）或 `header`（自定义请求头原样携带账号密钥）。凭据只来自账号，模板不能携带密钥；静态请求头禁止 Authorization、Host、Cookie、Content-Length 等。
- **请求体规则**：`from` 可读 `model`、`prompt`、`negative_prompt`、`duration`、`resolution`、`aspect_ratio`、`size`、`generate_audio`、`watermark`、`seed`、`images`/`videos`、`images[0]`、`images[role=x]`（多项同角色视为冲突）、`extra.<key>`；`const` 写入常量。媒体列表用 `each` 生成对象数组（`$url`/`$role` 取素材值，空值字段省略）或用 `item: "$url"` 生成字符串数组，`roles` 改写角色名（设置后未列出的角色拒绝），`append` 追加到已有数组。标量规则也可用 `each` 包装成对象（`$value` 取值），例如把提示词追加为 `contents` 的文本项。`from_all` 联合多个标量查表，`map` 键为各值用 `|` 连接，字段须全部给出或全部缺省（如 `768p|16:9` → `1280x720`）。`type` 支持 `string`、`int`、`number`、`bool`、`string_number`、`string_int`（整数字符串）、`resolution`/`resolution_upper`（校验可报价后统一小写或大写）、`resolution_tier`（只接受档位，拒绝像素尺寸）和 `ratio`（正整数 `W:H`），只做无损转换；`map` 的键对布尔与数字取 `true`/`false` 与最短十进制，未命中时拒绝而不原样透传；`required` 缺失时拒绝；`default` 在来源缺省时写入 JSON 值。两条规则向同一路径写入不同值时拒绝（`VIDEO_PARAMETER_CONFLICT`），`default` 可被显式值覆盖。`defaults` 只为时长、分辨率、比例补默认值，补出的值同样写入请求并参与计价；`image_role_default` 为未写角色的参考图补角色：`frames`（一张首帧、两张首尾帧、三张以上参考图）、`single_first` 或 `reference`。
- **字段使用核对**：统一请求中非空但没有被任何规则读取的字段（包括每一张参考图、参考视频和 `extra` 键）直接返回 `VIDEO_PARAMETER_UNSUPPORTED`，防止静默丢参与少计费。计价维度只取被读取的规范值；像素尺寸经 `capabilities.size_resolutions` 换算为计价档位并保留尺寸。`capabilities` 声明时同时校验时长（`durations`、`min_duration`/`max_duration`、`integer_duration`）、分辨率、比例、参考图数量（`min_images`/`max_images`）与角色、参考视频数量、`exclusive_frames`（首尾帧不能与参考素材混用、尾帧需首帧）、`require_ratio_without_media`（文生视频必须给出非 adaptive 比例）、`deny_models`（精确或以 `*` 结尾的前缀）以及 `public_media`（素材必须是公网 HTTP(S) 链接）。`max_reference_bytes` 与 `adaptive_ratio`（`orientation` 按首图方向换算 16:9/9:16，`omit` 不发送比例）只作用于云画布。
- **路径**：只允许以 `/` 开头的相对路径，禁止 `//`、`..`、查询串和片段；占位符只有 `{model}`（创建与查询）、`{task_id}` 和 `{ref.<名称>}`（查询与内容），统一转义。查询参数用 `poll.query` 声明，任务 ID 位于查询参数时（如 `/tasks?task_ids={task_id}`）路径本身可以不含占位符。`create.response.refs` 把创建响应中的额外字段保存为查询上下文，只在首次受理时写入。
- **身份**：`create.response.task_id` 的多个路径取值不一致时判为冲突，进入受理不明；查询响应的 `task_id` 必须与冻结 ID 一致，`list` 模式下数组只能包含本任务（`match` 可列多个 ID 路径），否则整包拒绝；`list.optional` 时数组缺失或各项都没有任务 ID（例如产物数组）则使用当前节点。`root` 可列多个候选，取第一个存在的对象（如 MiniMax 中继的 `task` 封装）。数字 ID 按原文保存。
- **状态**：`status` 可列多个路径，取第一个非空值；`status_map` 键不区分大小写，必须至少映射一个 `completed` 和一个 `failed`；未识别的状态不推进终态。`max_processing_minutes` 覆盖平台默认的最长处理时间，超时按[未交付即释放](../domains/video_tasks.md#video_task_delivery_release)处理。`rejected_when` 只在没有任何任务 ID 时用于确认未受理并释放；条件中 JSON `null` 视为字段不存在。
- **可信观测**：响应含重复键或声明路径上仅大小写不同的同名字段时标记元数据错误；秒数接受正数或数字字符串，Token 必须为非负整数且零值视为未知，分辨率可为字符串或正整数（如万相 `SR: 720` 记为 `720p`），非法取值一律标记元数据错误。`strict` 要求秒数、Token、分辨率的多个路径取值一致；否则取第一个有效值。`outputs` 声明产物数组与上限（超过即不可计价），可用 `filter` 只统计指定类型（如 `type=video`），并用项内 `url`/`seconds` 路径取唯一产物的直链与实际秒数，此时完成态缺少任一项都标记元数据错误。`completed_invalid_when` 在上游声称完成时校验（如同时带错误包或非零错误码）；`public_url` 要求直链是公网地址。元数据错误不交付、不按请求估算扣费，未交付的任务按[未交付即释放](../domains/video_tasks.md#video_task_delivery_release)处理。
- **内容**：`url` 模式要求声明 `video_url` 路径；`path` 模式用原账号凭据请求声明的内容路径，禁止跟随重定向，同时声明的 `video_url` 取到直链时优先直链。

保存时校验 schema、路径与占位符、请求头、状态映射，并用一份样例请求空跑映射规则。创建任务时解析启用版本并把整份规格、哈希与 `profile:<键名>@<版本>` 冻结为版本三路由快照；之后编辑、停用或新增版本都不影响在途任务的查询、内容与结算，快照哈希不符时拒绝使用。启用版本解析在进程内缓存 10 秒。调试接口返回生成的请求路径与请求体、计价维度、解析出的任务 ID、refs、状态、产物地址、用量、查询与内容路径以及请求错误，供启用前核对。编辑器打开、AI 回填或点击“按模板生成样例”时，调试样例请求按模板读取的字段与 `capabilities`（图片角色与数量、时长范围、分辨率、比例、`map` 允许的取值）生成，避免固定样例因能力不匹配而在映射前报错。

管理页面（菜单“协议模板”）的“编辑”在弹窗中锁定键名并以该版本内容打开编辑器，保存始终生成同键名的新版本（源版本启用时可直接“保存修改并启用”），历史版本保留可回退；“基于此版本新建”可改键名复制。`create.headers` 声明只随创建请求发送的静态请求头（如万相 `X-DashScope-Async: enable`），校验规则与 `headers` 相同。

删除：`DELETE /api/v1/admin/video-protocol-profiles/:key/versions/:version` 删除单个版本，`DELETE /api/v1/admin/video-protocol-profiles/:key` 删除整个模板。启用中的版本须先停用或启用其它版本；仍被视频账号绑定的模板（删除整个模板或最后一个版本时检查全部绑定，删除单个版本时检查锁定该版本的绑定）返回 409 并列出账号。已受理任务冻结了模板快照，删除不影响其查询与结算。删除预置模板的全部版本后记入设置键 `video_protocol_presets_deleted`，启动补齐与内置回退都不再恢复该模板。

<a id="video_protocol_ai"></a>
### AI 生成模板

系统设置“视频协议 AI”保存一个 OpenAI 兼容的对话模型（设置键 `video_profile_ai_config`：Base URL、模型、API Key、单轮超时 10–600 秒）；API Key 只写不读，接口只返回是否已配置，留空保存保留原值。模板页面的“AI 生成模板”把管理员粘贴的上游文档（最多 12 万字）、补充说明和可选的当前编辑器内容发送给该模型（`POST /api/v1/admin/video-protocol-profiles/ai-generate`）。系统提示描述统一请求字段与模板规格，并附带若干预置模板作为范例；模型输出的 `spec` 按保存规则（含未知字段与样例空跑）校验，失败时把错误反馈给模型修正，最多 3 轮。结果只回填编辑器并显示模型说明与校验状态，不会自动保存或启用；未通过校验时返回最后一版草稿与错误供手动修正。上游模型错误摘要会遮罩密钥。配置接口为 `GET`/`PUT /api/v1/admin/video-protocol-profiles/ai-config`。设置页的“使用本站模型”可一键填入本站 API 地址（系统设置的 API Base URL，否则当前域名，补齐 `/v1`）并从当前管理员自己的启用 API Key 中选择一个填入，按该 Key 的分组与倍率计费，仍需点击保存。

<a id="video_protocol_presets"></a>
## 预置模板

内置协议只保留各家官方格式；厂商与中转格式以系统预置模板提供（`video_protocol_presets.go`），管理员可在模板页面查看、复制为新键名后修改，或为同键名保存新版本。服务启动时 `EnsureVideoProtocolPresets` 为数据库中尚不存在的键名创建第 1 版并启用；最高版本仍由系统创建（`created_by` 为空）且处于启用状态、内容与当前内置定义不同时，按新定义升为新版本并启用。管理员保存过、停用或改为草稿的键名一律不覆盖。数据库中完全没有某个预置键名时（例如补齐失败），`Resolve` 使用内置定义作为第 1 版，避免已改绑账号中断；该键名一旦入库，停用与草稿状态照常生效。

| 键名 | 对应旧方案 | 要点 |
| --- | --- | --- |
| `metaso-h3` | `openai_videos.metaso_h3_v1` | 整数秒 `seconds`；`resolution`+`aspect_ratio` 联合查表为四种像素 `size`（768p/2k × 16:9/9:16），尺寸按档位计价；单张参考图写入 `input_reference.image_url`；画布参考图上限 2 MiB，自动比例按首图方向换算 |
| `yezonghui` | `openai_videos.yezonghui_v1` | 必填提示词、1–3600 整数秒字符串与分辨率档位；比例写入 `size`；素材只收公网链接（画布先发布短期链接）；查询间隔 60 秒；完成包带错误或旧格式封装时不可交付 |
| `generic-flat` / `generic-flat-videos` | `compat.legacy_v1` / `openai_videos.legacy_v1` | 通用扁平格式：顶层 `duration`、`resolution`、`ratio`、`size`，图片为 URL 数组，通用状态词表与产物路径；后者走 `/v1/videos` 并可回退内容路径 |
| `kemei-compat` | `compat.kemei_v1` | 扁平字段，素材保留 `url` 与 `role` |
| `kemei-seedance` / `kemei-minimax` | `seedance.kemei_v1` / `minimax.kemei_v1` | 提示词与素材写入 `content`，未写角色按 `frames` 补角色；MiniMax 另有大写分辨率、`aigc_watermark`、4–15 秒、图 ≤9 / 视频 ≤3、首尾帧互斥、文生须比例与 `task` 封装 |
| `kemei-wan` | `wan.kemei_v1` | `input.media` 按角色写入，参数进 `parameters`（大写分辨率）；查询读取 `output.*` 与 `usage.SR` |
| `kemei-kling-text` / `-image` / `-omni` | `kling.kemei_v1` + 对应路径 | 模型在路径中；`settings` 默认 5 秒、720p、16:9、`audio=off`；图生只收首尾帧，全能视频把参考图改名 `refer_image`、参考视频为 `feature_video`；查询 `/tasks?task_ids=`，只认唯一 `type=video` 产物及其秒数 |
| `kemei-vidu-img2video` / `-reference2video` / `-text2video` / `-start-end2video` | `vidu.kemei_v1` + 对应路径 | 默认 5 秒、540p、16:9；图片数按操作限制；Vidu Q4 不能用文生与首尾帧；成功但错误码非零或多个作品时不可交付 |

**自动改绑。** 账号保存与启动迁移按 `videoDeprecatedProtocolReplacement` 改绑：上表的旧厂商方案换为对应模板（保留 `base_url`，清除 `path`）；原样协议在原生端点换为官方协议（`seedance`、`minimax`、`wan`、`vidu` 与 Kling 文生/图生路径）。原样协议的旧版 Kling 路径（`/omni-video/{model}`、`/v1/videos/*`）没有等价替代，运行时仍可使用，但再次保存账号时必须手动改绑。旧方案代码只保留给在途版本一/二任务的查询、内容与结算，不能用于新绑定。

**创作台兼容。** 改绑后模型在画布目录中归入 `unified`；该模型没有 `unified` 设置时沿用原端点（如 `openai_videos`、`seedance`）下已保存的创作台模型设置，已有画布节点按原端点继续匹配，不会从创作台消失。参考图大小提示读取模板 `max_reference_bytes`（全部候选都声明时取最小值），提交时仍按实际选中的模板校验；`unified` 设置允许多张参考图，数量由模板能力在预扣前校验。`video_protocol_followup_test.go` 用各预置模板验证画布请求可生成上游请求且计价维度与画布参数一致。

**等价验证。** `video_protocol_presets_test.go` 对每个模板与对应旧方案使用相同的规范请求和上游响应对照：请求体逐字段一致，时长、计价档位、尺寸、参考图数量与参考视频标记一致；响应的任务 ID、状态、直链、秒数、档位与 Token 一致，旧方案判为不可信的响应模板同样不可信。已知差异：

- 旧方案对原生透传结构（`contents`、`input.media`、`content` 等）的兼容不再提供，统一入口只接受规范字段，厂商扩展需在模板中以 `extra.<key>` 显式映射。
- 旧可美 Vidu 方案在统一入口下无法消费带角色的图片对象，带图请求会被拒绝；模板按 URL 数组发送，修复了该问题。对照以旧客户端的原生扁平请求为基准。
- Kling 原生响应完成却没有 `outputs` 时，旧方案标记元数据错误，模板视为尚无可交付产物；两者都不扣费，后者在截止后按未交付释放。
- 计价档位大小写（如 MiniMax 的 `768P`）与旧方案相同，报价前统一归一。

<a id="video_adapters"></a>
## 版本化协议适配

内置协议在[模型协议绑定](#video_accounts)中写作 `端点.方案`，每个绑定只选一个方案，不支持模型通配符以外的通配、域名推断、任意脚本或动态 HTTP 模板；厂商格式使用[预置模板](#video_protocol_presets)，其它上游使用[声明式协议模板](#video_protocol_profiles)。可选的内置协议只保留官方格式：

| 端点 | 内置协议 |
| --- | --- |
| compat | `openai_v1`：OpenAI 官方参数格式，保持兼容路径 |
| openai_videos | `openai_v1`：OpenAI JSON 协议 |
| seedance | `seedance_v1`：Seedance 原生协议 |
| minimax | `minimax_v2`：MiniMax H3 V2 协议 |
| wan | `wan_v1`：万相异步视频协议 |
| kling | `kling_v1`：Kling 官方文生/图生协议 |
| vidu | `vidu_v2`：Vidu 官方参数 |

已由模板取代的 `legacy_v1`、`kemei_v1`、`metaso_h3_v1`、`yezonghui_v1` 不再出现在选择框中，也不能用于新绑定；它们的实现只用于升级前已受理任务按自身快照查询与结算，保存账号时按[预置模板](#video_protocol_presets)的规则自动改绑。新建的内置协议绑定一律冻结为版本二快照并携带显式方案；升级前按旧配置创建的版本一任务仍按自身快照查询与结算，版本一 Vidu 仍拒绝。

`/v1/video/generations` 本身不是 OpenAI 官方视频路径，其 `openai_v1` 仅复用 OpenAI JSON 参数及响应的 `seconds`、`size`、`input_reference` 合同。创建和查询仍使用冻结的 `/v1/video/generations` 路径，不改为 `/v1/videos`，也不因此授予另一端点的权限。保留原有协议时继续原样发送已有字段，不自动转换为官方参数。

适配器同时负责请求参数、查询路径、响应任务身份、状态、媒体和可信用量。正式生成与管理员测试使用相同转换入口；先验证并提取真正发送的计费维度，再由原有价格与预算链路预扣。转换不得静默丢弃参考图、改变时长或使用无法判断的图片数量作零张计价。旧原样协议继续接受既有参数；显式官方方案的别名冲突和不支持的参数会在上游 POST 之前失败。一个候选账号的协议不接受请求时，可以在发送前检查其它合格账号；这不是对已发送创建请求的重试。

OpenAI JSON 方案使用 `seconds` 字符串、像素 `size` 和单张 `input_reference`；MiniMax H3 V2 使用 `content`、`resolution`、`duration`、`ratio`，必填分辨率不凭模型名称猜测。Seedance 使用 `content`；Wan 使用 `input`/`parameters` 和异步 Header；Kling 新版使用模型路径与 `settings`。Kling 官方方案当前仅适用于已核对的 `/text-to-video/{model}`、`/image-to-video/{model}`，不推测 Omni 转换。`/omni-video/{model}` 可明确选择下述可美方案或保留原有协议；旧 `/v1/videos/*` 操作仅保留原有协议。方案表示选定的协议合同，不保证任意第三方实现或任意模型支持所有参数。

Seedance 官方方案单独检查显式 `resolution`：接受小写规范化后的 `480p`、`720p`、`1080p`、`4k`，其中已核对的 2.0 Mini/Fast 只允许前两档，2.5 与 1.0 Pro/Pro Fast 不允许 `4k`。型号限制使用映射后的官方名称及六位日期版本，不从自定义别名或 `ep-` 接入点猜测实际模型；其余型号的具体档位仍由方舟校验。省略分辨率时不注入参数，测试继续使用上游默认，正式请求仍遵守原有报价维度门禁。该校验在发送前执行，不静默降档，不改变可美或原有协议的参数合同。核对来源：[方舟创建视频任务](https://docs.volcengine.com/docs/ark/create-video-generation-task-api?lang=zh)。

Seedance 官方帧数模式按 `frames` 优先于 `duration` 提取计费时长，固定为 `frames / 24`，保留小数秒及原始发送参数。帧数必须为 29–289 内满足 `25+4n` 的整数；已知 2.0/2.5 型号拒绝该参数，未知接入点不猜测型号。该时长同时用于按秒预算和 Token 秒价预扣，不改变各自最终计价规则。官方完成响应带 `frames` 时，必须同时提供数值型 `framespersecond: 24`，且不能再带 `duration`；缺失、冲突、非规范字段名和非法用量进入待核对，不退回较短的请求时长结算。官方 Seedance 创建与查询的状态响应必须是完整的单个 JSON 对象，尾随其它内容的伪终态不能触发扣费或退款，只保留受理证据并继续查询；已支持的成功 DELETE 空响应仍按原取消合同处理。失败或取消仍沿用原退款规则。按次计价仍只收一次，旧原样与可美方案不启用上述帧数语义。查询合同见[方舟任务查询](https://docs.volcengine.com/docs/ark/get-video-generation-task-api?lang=zh)。

Seedance 官方 2.5 首帧/尾帧图生视频只接受省略比例或显式 `ratio: adaptive`；该判断在最终模型映射及素材别名转换后执行，非自适应比例在创建与预扣前拒绝。普通多参考图、其它型号及旧原样/可美方案不套用此限制，也不静默覆盖用户手填比例。

Vidu 官方 `vidu_v2` 在创建和查询中使用 `Authorization: Token`，可美与保留原有协议继续使用既有 Bearer。鉴权方式按任务冻结方案读取，受保护 Header Override 规则保持不变。官方参数沿用四种原生操作、`images`、`duration`、`resolution` 和 `aspect_ratio`，不强加各模型不同的默认时长、分辨率或比例；无法确定的计费维度继续由原有报价门禁处理。主体库素材不能被当作零张参考图，明确完成态的单一产物才可用于预览；未声明的取消和 Token 用量不推测为退款或免费完成。官方来源见 [Vidu API](https://platform.vidu.com/docs/api-reference/video-models/vidu-q3/reference-to-video)。

Vidu 三种方案中，明确完成但暂缺作品或地址的任务继续查询；同一任务后续补齐唯一、可用的作品地址后，可基于冻结的可信计费维度恢复结算。该恢复不要求按次计费返回无关时长，也不能清除先前真实的计费维度异常或把未知 Token 当作零用量；多产物、非法结构和冲突响应不属于缺产物恢复白名单。

Kling 官方响应从本任务的唯一视频产物读取实际时长；空产物、非法时长和多视频结果进入待核对。后续产物补齐且没有分辨率冲突时可以恢复，查询未回显 `settings` 不会永久卡住任务；已有真实档位冲突仍须返回明确分辨率才能解除。没有受理 ID 的非零业务错误码沿用失败释放，即使 `data` 为空也不改变这一判断。官方合同未定义的外层 Token 扩展不用于 Kling 结算，按 Token 定价缺少可靠用量时继续待核对。

新任务只有显式选择方案时才保存 `VideoUpstreamTarget.version=2` 和带版本的 `protocol_adapter`；原有端点缺配置继续保存版本一及原 profile。查询、取消、内容下载、创建响应恢复、原包重放和结算解析都读取任务快照，不读取账号后来修改的协议。版本二清空旧 `openai_profile`，不能叠加旧 profile；非法的混合快照、不支持的任务版本或方案拒绝处理，不能降级为默认协议。可美与 Vidu 同样冻结实际端点、Base URL 和创建操作，改账号配置或模型路径不迁移在途任务。已有版本一任务与其冻结的价卡、预扣和退款规则保持不变。

已明确拒绝、无任务 ID 的安全换号仍受原有条件限制，并要求前后协议版本等价和计费元数据一致。跨协议不自动重提付费创建；网络超时、身份冲突、已有任务 ID 或受理不明也不因此获得重试资格。适配器只返回事实，不修改价格、倍率、资金来源、账本、退款或统计幂等规则。部署需要所有处理视频任务的实例支持版本二；旧程序不具备处理新快照的能力。

官方合同核对来源：[OpenAI Videos](https://developers.openai.com/api/reference/resources/videos/methods/create)、[MiniMax V2 创建](https://platform.minimax.cn/docs/api-reference/video-generation-v2-create)与[查询](https://platform.minimax.cn/docs/api-reference/video-generation-v2-query)、[Seedance](https://docs.volcengine.com/docs/ark/video-generation-tutorial?lang=en)、[万相](https://help.aliyun.com/zh/model-studio/text-to-video-api-reference)、[Kling](https://kling.ai/document-api/apiReference/model/textToVideo)。OpenAI 文档目前标记该 API 服务已停用，本项目保留其 JSON 协议以对接实现该合同的上游。后续供应商特殊格式应增加独立版本方案，不修改已发布方案的语义。

### 可美视频协议

可美（token.xinhankr.com）合同现由 `kemei-*` [预置模板](#video_protocol_presets)提供，旧 `kemei_v1` 方案只用于在途任务。本节描述该合同的语义，模板按同样语义转换规范请求；`openai_videos` 没有可美模板，不声明供应商支持 `/v1/videos`。系统不根据域名、模型名或参考图存在与否替用户更换协议。

| 端点 | 适配后的请求结构 | 边界 |
| --- | --- | --- |
| compat | 扁平 `prompt`、`images`、`videos`、`audios` 等供应商字段 | 同义字段必须一致，混用不同结构且无法明确解释时拒绝 |
| seedance / minimax | 文本与媒体转换为 `content` 项 | 保留媒体角色；MiniMax 文生需要明确比例，图生不强加默认 `16:9`，不转换为 OpenAI `input_reference` |
| wan | `input` 与 `parameters` | 保留异步请求边界和实际计费维度，不按模型猜协议 |
| kling | 操作路径、`settings` 与相应媒体字段 | 仅现代 `/text-to-video/{model}`、`/image-to-video/{model}`、`/omni-video/{model}`；旧 `/v1/videos/*` 仅旧协议可用 |
| vidu | `images`、`sounds`、`duration`、`resolution`、`aspect_ratio` | 创建操作显式选择，缺省写入 5 秒、540p、16:9 后再提取预算维度 |

Kling Omni 的原生角色使用 `refer_image`、`feature_video`、`base_video`，不能把通用 `reference_image`、`reference_video` 直接当作原生角色发送。媒体别名转换不下载或重新编码参考素材；同义字段冲突和明确异常的输入结构在提交前拒绝。Kling `element` 无法可靠统计图片数量时保留未知，由原有图片附加费报价与用量规则决定是否拒绝或等待用量，不能当作零张。上传 Data URL 是否受具体模型支持仍由供应商合同决定，配置适配器不保证任意媒体形式均被接受。

Vidu 的 `img2video` 恰好一张图，`reference2video` 至少一张，`start-end2video` 恰好两张，`text2video` 不接收图且要求提示词；非空 `sounds` 仅用于 `reference2video`。`audio`、`off_peak` 为布尔值，`seed` 为非负整数。`image_urls`/`audios`/`ratio`/`generate_audio` 分别映射到 `images`/`sounds`/`aspect_ratio`/`audio`，双方同时出现时只接受等值。原生响应只从外层 `task_id` 或 `id` 识别任务，`creations[].id` 是产物 ID。`created`/`queueing`/`queued` 归为排队，`processing` 为处理中，`success` 为完成，`failed` 为失败；完成必须有唯一可用产物，状态与错误码冲突或多产物不能取第一项直接结算。

可美的零 Token 占位不证明免费用量；Vidu 不凭空生成 Token 用量。选用 Token 价格但没有可信用量的任务仍待核对，不能借适配改按秒结算。协议只提供归属、状态、产物和计费事实，继续使用已有预算、捕获、释放及统计幂等事务。

供应商合同来源：[统一兼容接口](https://token.xinhankr.com/docs/openai/doc0019)、[MiniMax H3](https://token.xinhankr.com/docs/examples/doc0015)、[Seedance](https://token.xinhankr.com/docs/examples/doc0010)、[Kling](https://token.xinhankr.com/docs/examples/doc0012)、[Wan](https://token.xinhankr.com/docs/examples/doc0014)、[Vidu](https://token.xinhankr.com/docs/examples/doc0041)。预置模板定义在 `video_protocol_presets.go`，旧方案实现（`video_protocol_kemei*.go`）只服务在途任务；新的供应商合同应新增模板，不修改已发布模板版本的语义。

<a id="video_yezonghui"></a>
## Yezonghui 视频兼容

在 Video API Key 账号把模型绑定到预置模板 `profile:yezonghui`，Base URL 按供应商配置（当前文档为 `https://new.yezonghui.org`）；旧 `openai_videos.yezonghui_v1` 绑定保存时自动改绑。不凭域名或模型名称自动切换，不新增平台或视频端点。新任务冻结模板快照，已有任务继续使用原快照。供应商合同来源：[对接文档](https://new.yezonghui.org/docs)及其[原始文档接口](https://new.yezonghui.org/api/docs)，本版只接入其中视频协议。

创建与查询分别复用 `POST /v1/videos`、`GET /v1/videos/{id}`。要求非空 `prompt`、1–3600 的整数时长和明确的可报价分辨率档位，出站 `seconds` 为整数字符串；`duration` 是数值别名。`resolution` 与 `size` 分离：前者是 `720p`、`1080p`、`2k`、`4k` 等本站可报价档位，后者是可省略的正整数比例，如 `16:9`，不是像素尺寸。`ratio`/`aspect_ratio` 合并为 `size`；同义字段同时存在必须等值。`metadata.resolution` 可提供档位，但与顶层冲突时拒绝，不能绕过报价。供应商实际支持档位和时长由具体模型决定，适配器不降档、不改价、不承诺所有档位可生成。

`images` 转为 `image_urls`，`generate_audio` 转为 `audio`；`image_urls`、`video_urls`、`audio_urls` 只接受 HTTP(S) 公共链接，不接受 Base64、Data URL、内网字面量或登录态素材。`audio_urls` 是否可用由上游模型决定，不扩大模型能力声明。`negative_prompt`、`seed`、`fps`、`camera_fixed` 和 `audio` 按文档类型校验，厂商扩展放入 `metadata` 并保留数字精度；不支持的顶层参数（包括 `watermark`）明确拒绝。当前一个任务只交付一个视频，`n` 只允许 1，不把多个作品合并成单次收费。

创作台和画布沿用已有视频模型目录、1–60 秒编辑范围、参考图上限和站内价格。已经鉴权的私有图片在实际选中本方案后转换为[短期访问链接](cloud_canvas.md#video_reference_delivery)，原素材不改写、不公开桶；缺少可用公网站点地址或参考图交付失败时在创建和预扣前拒绝。幂等摘要使用原始画布请求，同次选号重用已签发的链接，恢复已有任务不再发布或生成。画布的“自动”比例只在本方案下省略 `size`，不强制变为横屏；旧模型继续原来的自动比例逻辑。统一入口 `/v1/videos` 的调用者（包括外部画布脚本）直接提交 Base64 图片（`data:image/png|jpeg|webp`）时，若实际选中的协议只收公网素材（本方案或声明 `capabilities.public_media` 的模板），同样只把这些 data URL 发布为短期访问链接，已是公网 URL 的图片原样转发；发布的只是调用者自己上传的字节，不能借此公开站内其它文件。参考视频不做转换，仍需公网链接。发布需要系统设置中可用的公网站点地址，否则在创建和预扣前拒绝。

响应仅按顶层 `id`/`task_id` 识别任务，二者冲突或查询 ID 不匹配时保留核对，不任选 ID 或释放预算。`queued`、`in_progress`、`completed`、`failed` 分别映射排队、处理中、完成和失败；`unknown` 与未识别状态保持非终态。创建没有可靠受理结果时沿用原状态机，不盲目重建任务。后台为该冻结方案使用至少 60 秒查询间隔，查询失败继续原 ID 退避，既有模型间隔不变。完成响应可无 `url`，由原账号凭据请求 `/v1/videos/{id}/content`，不让上游凭据进入浏览器。当前供应商文档没有取消合同，不新增取消能力。

站内仍使用原有按秒、按次或 Token 价卡及图片固定附加费，分辨率按实际发送的 `resolution` 报价，不把比例当分辨率。预扣、成功捕获、失败释放、并发幂等和内容下载复用原账本；上游价率 Header 不覆盖站内价格。文档未保证 Token 用量，选用 Token 价却缺少可信正数用量时保持待核对，零占位不能推断免费。仅返回完成而没有尺寸/时长时可沿用创建时冻结维度；显式异常、冲突或不同价档继续待核对，不自动改费。图片异步端点和其存储、保留策略不在本适配范围内。

<a id="video_protocols"></a>
## 上游协议与已下线入口

客户端只通过[统一入口](#video_unified_api)提交规范请求；下表是内置协议实际访问的**上游**路径，由模型绑定的协议决定，冻结在任务快照中。内置协议的请求体由规范请求转换而来，官方适配器按上述固定版本处理已声明的别名；原样协议已由通用扁平格式模板取代。模板协议的路径见[声明式协议模板](#video_protocol_profiles)。

| 上游端点 | 创建 | 上游查询 |
| --- | --- | --- |
| compat | `/v1/video/generations` | `/v1/video/generations/:id` |
| openai_videos | `/v1/videos` | `/v1/videos/:id`；内容为 `/:id/content` |
| seedance | `/api/v3/contents/generations/tasks` | 同根路径 `/:id` |
| kling | 绑定的受控模型路径，或两个 `/v1/videos/*` 操作 | `/tasks?task_ids=:id` |
| wan | `/api/v1/services/aigc/video-generation/video-synthesis` | `/api/v1/tasks/:id` |
| minimax | `/v2/video_generation` | `/v2/query/video_generation/:id` |
| vidu | 绑定的 `/ent/v2/img2video`、`/ent/v2/reference2video`、`/ent/v2/text2video` 或 `/ent/v2/start-end2video` | `/ent/v2/tasks/:id/creations` |

<a id="video_retired_entries"></a>
**已下线入口。** Video 分组的 Key 访问旧统一入口 `/v1/video/generations*`、`/video/generations*`、`/v1/tasks/:id`，以及 Seedance（`/api/v3`、`/v3`、`/v1` 与裸路径的 `/contents/generations/tasks`）、Kling、Wan、MiniMax、Vidu 原生创建与查询入口时，一律返回 `410 VIDEO_ENDPOINT_RETIRED` 并提示改用 `/v1/videos`；不再提供任何取消入口。旧入口创建的本地任务继续由后台按冻结协议查询与结算，客户端用其 `vid_…` ID 通过 `GET /v1/videos/{task_id}` 取回，原生供应商 ID 查询随入口一并取消。OpenAI 分组的 Seedance 能力与 Grok 分组的 `/v1/videos*` 不受影响；智能路由与模型广场对 Video 平台只展示和选择 `/v1/videos`。

任务使用冻结的版本化 URL/操作路径及原账号查询，不重新调度；查询校验原用户与原 Key，已经创建的任务不再要求当前映射仍包含原分组，当前 Key 仍须通过身份、状态和 IP 等鉴权。普通 Key 改到其它分组后仍可使用本地任务 ID 取回 Video 任务。

内置协议转发只替换认证及实际模型字段/模型路径；Metaso H3 等厂商格式的参数转换见[预置模板](#video_protocol_presets)，不扩展账号允许的端点。重复 JSON 键、大小写歧义和超过 64 层的嵌套会被拒绝，避免网关与上游解析值不同。通过任务归属校验的响应保留上游 HTTP 状态、原始 JSON/错误正文及允许透传的响应头，不执行客户端模型别名还原或套面板 envelope；任务响应强制 `Cache-Control: no-store`。创建请求禁止自动重定向，3xx 的 `Location` 不下发客户端，其他共享上游请求的重定向策略不受影响。Wan 的 `request_id` 是追踪号，任务 ID 从 `output.task_id` 读取。

已有任务的查询和取消统一核对协议规范 ID 字段；明确不匹配、冲突 ID 或包含其他任务的任务列表不得作为本任务观测。重复的规范 ID 键或其父容器同样视为归属歧义，避免不同 JSON 解析器分别读取首值、末值；此消歧不扩展到普通厂商扩展或非 JSON 错误正文。整包拒绝意味着状态、用量、媒体和原始响应均不落入当前任务，也不触发捕获或释放；后台按查询失败退避，取消及已知污染的历史原包读取返回安全错误，不暴露外来正文。合法无 ID 查询响应仍可接受，供应商追踪号及未知扩展字段不被猜测为任务 ID。

创建响应若包含多个不同的有效规范任务 ID，首次返回及幂等重放使用安全 `502 / VIDEO_TASK_RESPONSE_MISMATCH`，不透出冲突原包。内部保存诊断响应，但不选择存疑 ID 发起查询，按受理不明保留预算；即使创建 HTTP 为 4xx/5xx，也不能将这种冲突当作完全无 ID 的拒绝退款。

MiniMax 原生响应兼容平铺字段及 `task` 对象封装。H3 创建响应的顶层 `TaskId` 按精确大小写识别为字符串任务 ID，后续可用同一个 ID 读取 `task.id` 查询包；不把这一别名扩展为其它平台的查询 ID。它与其它规范 ID 同时出现时必须一致，重复字段或查询归属不符仍整包拒绝。即使创建 HTTP 为错误状态，存在该受理 ID 也不能当作无任务退款或自动重提。已持久保存这种创建包的未知任务可沿用既有恢复器重新提取 ID 并继续查询；管理员测试没有持久创建包，无法据此找回历史丢失的 ID。封装存在时从 `task.id`、`task.status`、`task.content.url` 及 `task.usage` 提取任务、状态、产物和用量，保留通过归属校验的原始响应体；`usage.output_seconds` 优先于普通时长字段，只接受有限正数，不用输入秒数或合计秒数代替。该封装和新增字段只用于 MiniMax，其他协议的同名扩展不参与状态或计费。封装查询包中的任务 ID 明确不匹配时整包拒绝，不能保存或返回外来原包，也不能据此捕获或释放当前任务预算。完成、失败和取消仍复用原有幂等结算规则，过期继续待对账。已观测到该封装用正数输出秒数配合 `completion_tokens`、`prompt_tokens`、`total_tokens` 三项明确零值表示 Token 占位；该组合的零 Token 保持未知，Token 模型须待可靠用量后结算，按秒模型仍使用真实输出秒数。平铺原生明确零及其他原生端点的既有零值规则保持不变。

MiniMax 创建包的 `TaskId` 若为数字、布尔值、对象或数组，且没有其它可解析的受理 ID，保持受理不明、保留预扣待核对；即使同时返回失败状态或 HTTP 错误，也不自动退款或重提。该限制仅作用于新增字段的异常类型；合法嵌套 ID 与空值或异常类型的别名并存时仍可沿用嵌套 ID。

新封装只读取本任务的规范字段，不遍历其中的 `data`/`tasks`，也不借用外层或扩展 `output` 的状态、产物及用量。缺少状态时保持未知观测；任务 ID 沿用 `id` 与 `task_id` 只有一个有效值或两者一致的兼容，两者都缺少时仍保留旧平铺位置明确返回的受理 ID，避免把已受理响应误判为无 ID 拒绝。嵌套 `id`/`task_id` 冲突、提供的 ID 均无法解析或查询 ID 不符时不应用该观测，已知调用方或外层受理 ID 仍保留。空封装中的外层业务错误只有未取得任何任务 ID 才按既有规则认定创建拒绝，查询 HTTP 错误继续保持原处理方式。

内部提取 `resolution`、`duration`、`has_reference_video` 和可信用量，不给未知分辨率或时长补默认值。选定上游协议后才解析输入：Wan 的 `parameters`/`input` 优先于扁平字段，MiniMax/Ark 的 `content` 存在时只读取该正文的参考视频。未知 compat 厂商混用结构且元数据冲突时拒绝提交；`openai_videos` 保留这些保护并额外读取规范字段。只有实际视频输入（例如 `video_url`、`reference_videos`、`content` 中的 `type=video_url`）设置参考视频标记；图片和音频不设置。该标记保留输入事实和历史兼容，不参与新视频价格选择；每个分辨率统一一个单价。Token 用量缺失与可信零严格区分，终态可信用量才可用于结算；兼容中继可能用零占位缺失 usage，所以 compat 和 openai_videos 的零 Token 仍视为未知，原生明确零可以保留。

`openai_videos` 识别顶层 `seconds` 正整数或规范十进制整数字符串，并与已有 `duration`/`parameters.duration` 校验一致；不能用零、负数、分数、溢出值或冲突别名绕过预扣。默认保真模式不转换这些字段。`size` 接受严格 `宽x高`，每边为 1–65536 的整数，不接受前导零；大小写 `X` 只在计费维度中归一化，原包保留。精确尺寸是独立价格档位，不按短边映射到 720p 等档位；合法未配置的尺寸可使用显式兜底价。`resolution` 与 `size` 同时提供时必须指向同一个已知计费维度，否则拒绝。`input_reference.image_url` 的单张 URL 或 Data URL 计入参考图；未知结构或与其它非空图片表示混用时张数保持未知，启用图片费的报价拒绝继续。

显式 `metaso_h3` 将旧字段 `duration`、`resolution`、`ratio`、`images`/`image_urls` 转为 `seconds`、`size`、`input_reference.image_url`，删除已转换的旧别名并保留其它扩展值的 JSON 精度。只支持单图，不下载或重编码图片；多图、冲突 URL、视频/音频及其它未验证的媒体结构在提交前拒绝。四个明确尺寸映射为：`1280x720` 和 `720x1280` 对应 768p，`1792x1024` 和 `1024x1792` 对应 2k，分别使用逻辑比例 `16:9` 和 `9:16`，不能按像素短边或数学比例推测。已有规范 `size` 时核对选填的档位和比例；只填旧档位时必须明确比例，不能猜测横竖屏。该适配不切换到原生 MiniMax，也不改写默认保真账号。

画布的“自动比例”另由服务端生成内部方向提示：已校验参考图纵向使用 `9:16`，横向或正方形使用 `16:9`；无图时使用模型的明确横竖屏默认值，其它默认比例采用横屏。只有真实选号后确定为秘塔 H3 的候选消费该提示，原转换器再生成规范 `size` 并按实际档位报价。该提示不属于客户端 JSON 或上游字段，其它适配器仍接收原 `adaptive`；直接网关和管理员测试的 H3 参数合同保持不变。受理后恢复使用原任务快照，不因默认值或参考素材状态变化再次提交。

OpenAI Videos 响应按任务冻结配置读取 `seconds`（含规范数字字符串）和 `size`，仅完成态可信秒数用于替换请求预估值。非法值、重复或大小写歧义、冲突时长及不一致尺寸会标记元数据错误，保留任务 ID 和状态并进入待核对，不回退请求预估值扣费。标准尺寸仍作为精确价格档位，H3 四尺寸按冻结映射核对价档；其它原生端点不读取这些新增字段。原始响应继续保留供核查。

参考图片数量仅从已知输入字段提取：MiniMax/Ark 优先 `content`，Wan 优先 `input`；支持图片数组、首尾帧和协议白名单中的参考图。同数组中重复 URL 按输入项数计算，`images`/`image_urls` 明确别名数量一致时只计一次，数量不同或多种表示混用且无法确定优先级时标记为未知。明确无图为零；输出图片及未知扩展字段不参与计数。此步骤不改写请求体，也不额外拒绝原本可转发的请求；只有启用图片附加费且无法确定张数时拒绝报价，避免漏收或重复收取。

创建沿用鉴权、内容策略、用户和账号并发、渠道模型限制及消费资格检查；不以旧“允许图片生成”开关限制 Video。选号失败可以尝试尚未提交的其他合格账号。完整明确拒绝且没有任务 ID 时可在同组、同端点下有界换号，沿用同一个持久任务、价卡和预扣；账号容量与有效执行租约在迁移事务中复核。网络中断、通用网关错误、已有 ID 或受理未知不自动重提，参数和内容审核错误不按故障换号。完整分类、预算和恢复约束见[任务生命周期](../domains/video_tasks.md#video_task_lifecycle)。

独立 Video 的退款策略区分创建与查询：安全换号耗尽或不满足重提条件时，完整收到创建 HTTP 4xx/5xx 且没有任务 ID，返回原错误并将本地任务记为失败、幂等释放预扣；平台承担上游可能已经受理的成本。退款规则不能用于推断允许再次生成。带任务 ID 的错误保留归属供原账号查询，明确失败/取消终态按任务规则释放。明确的创建超时或本版提交进程中断，只有没有持久响应证据且具备起算时点时，才适用[15 分钟核对后释放规则](../domains/video_tasks.md#video_task_lifecycle)。其它网络中断、响应读取失败、缺乏故障分类的旧任务或任务 ID 冲突的创建包在核对期内保留预扣，期限后仍取不到可查询的任务 ID 即按[未交付即释放](../domains/video_tasks.md#video_task_delivery_release)失败释放；查询 HTTP 错误本身不会触发退款，恢复不使用后续查询包推断创建失败。

取消不对外开放；后台查询仍识别显式 `cancelled`/`canceled`/`deleted` 观测。上游 `expired`、超过最长处理时间仍未完成、或完成却没有可取回成品的任务，用户拿不到视频，按[未交付即释放](../domains/video_tasks.md#video_task_delivery_release)失败并释放预扣，不再停留在待对账。

<a id="video_openai_content"></a>
## 视频内容

统一入口的请求格式与响应形状见[统一入口 /v1/videos](#video_unified_api)。创建只接受 JSON，不开放集合枚举、multipart、remix 或取消 API；查询沿用持久后台轮询、预扣与幂等结算，同一任务重复查询或下载不产生新的生成请求。受理不明在核对期内保留预算，不因显示四态而退款或重新提交。

共享路径仅创建 Video 分组任务，Grok 分组继续原链路；管理请求通过本地 `vid_` ID 精确查找新任务，后续允许原有效 Key 切换到其它有效分组后取回。复合/智能 Key 查询不要求重新选组，但仍核对原行为用户和原 Key。普通 Key 仅当前 Video 分组的本地任务查询可免消费准入，切换到其它平台后继续其原消费门禁，不能让碰巧以 `vid_` 开头的旧 Grok ID 绕过原检查；原有分组、状态、IP 与身份门禁保持不变。智能路由仍允许 Grok 与 Video 各自符合条件的候选，模型字段处理按本次所选平台区分，不能让新路径改变旧 Grok 的别名恢复。

`content` 是只读流，必须任务生成完成且账务已结算；未完成或待对账返回冲突，不触发上游状态查询或补扣。优先读取持久化的安全公共视频地址；没有直接地址时，只有冻结协议声明了凭据内容路径（内置 `openai_videos` 的固定 `/v1/videos/:id/content`，或模板 `content.mode=path` 声明的路径）才使用原账号和原供应商任务 ID 读取。带凭据请求禁止自动重定向；重定向目标单独经过公共地址与 DNS 拨号校验，以无凭据请求访问，不转发调用方 Key、上游 Authorization 或 Cookie。

面板预览通过自身的对象权限签发任务专属票据，与站内 Key 的 `content` 入口分开鉴权。独立 Video 的签票和每次播放都读取持久任务，核对任务 ID、原用户、原 Key、`completed`、`settled` 及完成后 24 小时期限；Redis 媒体快照不替代持久状态检查。无直接地址但冻结协议声明了凭据内容路径的任务同样可签发最长十分钟的票据，票据期限不超过结果期限。预览元数据及签票不访问上游；实际播放才复用上述只读内容传输，服务端凭据仅发往冻结的供应商内容路径，不进入浏览器、票据或公共 CDN 请求。图片、Grok 和 Seedance 预览的原有公共地址边界不变，详见[任务结果预览](../domains/media_tasks.md#media_task_preview)。

流式下载支持单段 `Range`，最多 512 MiB、两分钟，API 下载与面板播放共享本机最多 16 条下载流；只输出允许的视频类型和必要的内容头，供应商错误、跳转、Cookie 等不透传。响应禁止缓存并带 `nosniff` 和 `no-referrer`，内容传输不调用生成、任务状态查询、恢复或结算。新端点未声明删除语义，因此不为 `/v1/videos/:id` 注册 DELETE，账号旧统一取消入口也不能把未支持的操作伪装为退款成功。视频价卡、参考图固定收费、倍率和失败释放规则继续由已有任务账本负责。

<a id="video_account_tests"></a>
## 管理员账号测试

Video API Key 账号在账号管理中使用与 Grok 相同的测试弹窗、进度和媒体预览组件，只提供视频测试。管理员只选择模型；`GET /api/v1/admin/accounts/:id/models` 的 Video 模型条目携带 `video_protocol`，即最终映射模型绑定的协议标识，未绑定协议的模型不列出。测试与正式网关一样调用 `resolveTarget` 确定协议，把测试参数构造成统一请求，再经同一转换与协议校验后提交；端点、Kling/Vidu 操作路径和 Base URL 全部来自绑定，前端不再选择端点或操作，旧的 `video_endpoint`、`video_operation` 参数被忽略。

测试复用 [管理员账号连接测试](http_api.md#account_connection_tests)，请求发送 `test_type=video`，可提供 `prompt`、`image_data_url`、`duration`、`resolution` 和 `ratio`。时长省略默认 5 秒，OpenAI 官方协议默认 4 秒，显式值必须是 1–60 的整数且满足绑定协议约束，这是主动试跑的限制，不改写账号预算上限或正常网关参数。参考图沿用 Grok 测试的真实图片格式和 4 MiB 限制，不接受任意素材 URL 下载。Kling 文生路径不接受参考图、图生路径要求参考图；Vidu `start-end2video` 需要两张图，测试弹窗只上传一张图，因此提示改用 API 测试。

Seedance 官方账号测试的分辨率保持空值，输入示例为 `720p`，不再沿用通用 `768p` 示例；未编辑的比例也默认留空，上传参考图时不自动填入 `16:9`，由官方选择当前模型的默认比例。两套测试弹窗共用官方档位提示和输入校验，用户手填或明确清空的值不会被切换模型或方案覆盖；模型级限制由后端在模型映射和协议覆盖之后统一检查，非法输入不会触发付费创建。

Yezonghui 主动测试默认 5 秒、比例留空，要求填写明确分辨率档位，不套用 OpenAI 的像素尺寸限制。参考图沿用单张 4 MiB 校验，签发身份取已登录管理员，不能由 JSON 指定；临时访问失败不会发送上游创建。测试只等待一个上游任务，第一轮立即查询，后续至少 60 秒查询一次，最多等待 30 分钟；等待间隔每 15 秒写 SSE 注释保活，不增加模型或查询请求。临时查询失败继续原任务，明确失败终态立即结束；停止或超时不会重新生成或假定上游取消。

测试载荷按模型绑定的协议构造，模型映射、端点 Base URL 覆盖、受控 Kling/Vidu 路径、代理、TLS 与受保护 Header Override 均沿用原有账号配置。上游创建最多一次，后续只用该账号、端点和任务 ID 轮询；不因错误或等待超时重新提交。除 Yezonghui 方案及查询间隔不少于 60 秒的模板（最多等待 30 分钟并容忍临时查询失败）外最多等待 10 分钟。成功必须取得明确完成状态，以及安全媒体链接或已下载的视频内容；OpenAI 内容端点下载最多 64 MiB，带凭据请求禁止自动跟随跳转，公共跳转另走无凭据安全下载。失败、未知状态、冲突任务 ID 或内容下载错误不能伪装成成功。

上游生成完成与浏览器预览可用性分别展示。公共产物链接由浏览器直接读取，仍可能受到本机网络、链接有效期或编码支持限制；播放器加载失败会显示独立提示，可仅重新加载预览或在新页打开安全作品链接。等待视频元数据超过 30 秒仅提示媒体连接缓慢，仍可继续等待，加载成功后自动恢复；换源、重载和卸载时清理等待计时器。预览重载不会再次提交生成、查询上游任务或产生新的测试费用。生产默认 CSP 明确允许受支持的 HTTPS、同源、Data URL 和 Blob 媒体来源，管理员自定义 CSP 仍按其配置生效。

绑定预置模板 `profile:metaso-h3`（或尚未迁移的 `openai_videos.metaso_h3_v1`）的模型测试时复用正式网关转换：时长转为 `seconds`，768p/2k 结合 `16:9` 或 `9:16` 转为供应商 `size`，单张参考图转为 `input_reference.image_url`。不支持的分辨率、比例和冲突参数在发送前拒绝；完成响应的计费元数据异常也不能显示测试成功。不根据模型名或 Base URL 推断 H3。

主动测试不经过站内用户计费，不新增用户使用记录或持久化媒体任务；真实生成仍可能消耗上游额度。浏览器关闭、切换账号或停止测试会取消本地等待，上游已经受理的任务可能继续生成。测试选项不写入账号，不改变普通网关请求或原有计划测试流程。

上游返回 HTTP 错误时，管理员测试保留状态码，并从已知错误字段提取安全摘要，便于区分参数限制、模型权限和内容审核。摘要会遮罩凭据、完整素材地址及 Base64 数据，不回显原始响应、HTML 或嵌套调试对象；无法安全提取时仅显示状态码。这项诊断不改变正式网关错误、任务状态、重试或计费策略。

创建响应未识别到任务 ID，或已提取 ID 不能安全用于查询路径时，测试分别提示原因，并提供 HTTP 状态与该协议固定字段的 JSON 类型摘要。摘要不包含原始响应、任意上游字段名、任务 ID 值、素材地址或凭据；它只用于管理员排查返回结构，不扩大任务 ID 的接受范围。这两种情况都可能已在上游开始生成，测试停止本地轮询且不自动重复提交。

## 验证边界

协议 fixtures 使用本地 HTTP 替身验证已支持端点的路径、认证、参数保真、原生响应、ID 提取和取消状态，不产生真实付费请求。可美请求与响应、Vidu 操作、模型覆盖及版本快照也使用独立 fixture 验证；这些检查不证明线上供应商当前可用。用户提供的附件包含中继协议示例，不能自动等同于已验证的供应商官方完整契约；未给出的取消、缺失用量和过期费用语义保持保守处理。部署到具体上游前应核对其支持的模型、原生操作路径和终态 usage 字段。

相关入口：[接口目录](index.md)、[HTTP 边界](http_api.md)、[账号矩阵](upstream_account_matrix.md)、[账号调度](../architecture/account_scheduling_and_cache.md)。
