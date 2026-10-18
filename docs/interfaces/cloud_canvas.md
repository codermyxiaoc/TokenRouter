# 云端无限画布接口

> 上级目录：[接口文档目录](index.md)；领域约束：[云端画布](../domains/cloud_canvas.md)

## 认证和资源归属

以下接口均要求登录后的用户 JWT，并且所有查询、更新、删除和素材读取都以认证用户作为资源拥有者。客户端不能通过路径参数访问其他用户的画布、素材或执行记录；服务端返回统一错误，不泄露资源是否存在。

## 画布和素材

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/api/v1/creative/canvases` | 分页列出当前用户的画布 |
| `POST` | `/api/v1/creative/canvases` | 创建画布，提交 `title` 和受限 `document` |
| `GET` | `/api/v1/creative/canvases/{id}` | 读取画布和 revision |
| `PUT` | `/api/v1/creative/canvases/{id}` | 按 `revision` 乐观锁更新标题和文档 |
| `DELETE` | `/api/v1/creative/canvases/{id}` | 删除画布元数据 |
| `POST` | `/api/v1/creative/assets` | 上传素材，使用 `multipart/form-data` 的 `file`、`canvas_id` 字段 |
| `GET` | `/api/v1/creative/assets?canvas_id={id}` | 分页列出当前画布素材 |
| `GET` | `/api/v1/creative/assets/{id}` | 读取当前用户私有素材元数据 |
| `GET` / `HEAD` | `/api/v1/creative/assets/{id}/content` | 读取私有素材内容 |
| `DELETE` | `/api/v1/creative/assets/{id}` | 删除当前用户素材 |

文档只允许画布节点、位置、尺寸、连接和受控参数；脚本、远程 URL、凭据、外部插件和任意 HTML 会被拒绝。上传文件按魔数校验 MIME，不能通过文件名伪装类型。素材七天后立即不可读，后台每周清理已过期物理文件；元数据保留为过期记录，便于画布显示和审计。

## 模型和执行

目录支持 `media_type=image|text|video|audio`。文本项由管理员按分组和模型开启，只返回可调用并有明确 Token 价格的候选，`operations=[generate]`。`text_capabilities` 返回 `max_input_chars=8000`、`max_output_tokens=4096`、`default_max_output_tokens=1024`、`max_reference_images=0`。`kind=text` 的单执行使用 `group_id`、`model`、`prompt`、可选 `operation=generate` 与 `max_output_tokens`；本期不接受媒体参考或图片/视频/音频专用参数。

文本执行通过 `output_text` 返回纯文本结果；它与执行记录一起受用户归属校验，不是公开素材 URL。`CREATIVE_TEXT_SUBMISSION_UNCONFIRMED` 表示受理核对，`CREATIVE_TEXT_SETTLEMENT_PENDING` 表示已返回但用量结算仍在确认；两者保持 `running`，不能据此创建新任务。账务证据和资金回执只在后端保存，不出现在用户执行响应。相同幂等键仍只恢复原参数和原执行，不再次请求文本模型。

`GET /api/v1/creative/models` 返回管理员在创作台设置中启用的图片、文本、视频和音频模型。视频条目返回端点与 `video_capabilities`：`durations` 为 1–60 整数秒，`resolutions` 为 480p/720p/1080p 与原配置档位的并集，`aspect_ratios` 为常见比例、`adaptive` 与原配置的并集。`allow_custom_resolution` 为 true，允许数字 p 分辨率（1–65536）；比例必须来自 `aspect_ratios`，不接受任意自由比例。`default_duration` 保留原时长最小值，`default_resolution`、`default_aspect_ratio` 保留原配置首项。上述扩展仅在运行时派生，不回写设置；参考图数量限制、模型权限和端点校验不变。旧目录未声明自由输入能力时，前端仍按其原白名单处理。图片条目的 `output_count` 继续表示单任务固定输出数，`output_counts` 表示画布可选总张数：普通模型为 1–8，MJ 为 4/8。

音频条目为 `media_type=audio`、`model=grok-voice-tts`、`operations=[generate]`，固定一个输出。`audio_capabilities` 返回 `voices=[eve,ara,rex,sal,leo]`、`formats=[mp3,wav]`、`speed_min=0.7`、`speed_max=1.5`、`default_voice=eve`、`default_format=mp3`、`default_speed=1`、`default_language=auto` 和 `max_input_chars=15000`。候选与目录同时受管理员白名单、用户分组权限和 Grok Voice 账号能力约束。

`kind=audio` 的单执行 `params` 使用 `group_id`、`model`、`prompt`，以及可选 `operation=generate`、`voice`、`audio_format`、`speed`；声音、格式和语速分别默认 Eve、MP3、1。文本在服务端去首尾空白后按 Unicode 字符数限制，非空且不超过 15000。媒体引用、图片参数、视频参数和图片批次参数不适用于 TTS；没有任意脚本、声音指令或客户端上游密钥字段。站内模型名只用于目录与任务，上游 `/tts` 实际发送 `text`、`voice_id`、`language=auto`、`speed`、`output_format.codec`，不发送 `model`。

自定义视频参数经现有协议适配后按实际分辨率、时长和素材计数报价，不隐式降档或替换参数；模型或协议不支持以及缺价时由原任务链路拒绝。界面允许输入不代表供应商保证支持，已受理任务继续使用创建时的参数与冻结价卡。

`video_capabilities.max_reference_image_bytes` 为可选的本站单张参考图大小上限（字节）。仅当模型/端点的可用候选全部使用秘塔 H3 适配时返回 `2097152`；省略不表示任意上游保证支持大图。前端仅对有可信大小信息、实际将发送的参考图提前提示，未知大小由后端检查。H3 新画布请求超限返回稳定错误码 `CREATIVE_VIDEO_REFERENCE_TOO_LARGE`，用户需手动更换较小参考图；原素材不修改，原视频任务未创建且没有预扣。该策略不应用于正式 API 或已有执行的恢复。

画布请求选择自动比例时，只有实际账号使用秘塔 H3 适配才按已校验参考图方向解析成 16:9 或 9:16；无参考图则使用合法的模型默认比例或 16:9。解析提示只存在于服务端内部，浏览器不能伪造；普通 API 与其他适配的自动比例行为不变。报价与预扣仍使用实际转换的尺寸，旧任务恢复不重算或重新提交。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/api/v1/creative/executions` | 创建单执行，正文包含 `canvas_id`、`node_id`、`kind`、`params`；须带 `Idempotency-Key` |
| `POST` | `/api/v1/creative/execution-batches` | 原子登记图片批次，正文包含 `canvas_id`、`node_id`、`kind=image`、总张数 `count`、`params`；须带 `Idempotency-Key` |
| `GET` | `/api/v1/creative/executions?canvas_id={id}` | 分页读取当前画布执行 |
| `GET` | `/api/v1/creative/executions/{id}` | 查询单个执行和输出素材 |

同一用户、同一画布和同一幂等键只能创建一个执行；不同请求体使用同键返回冲突。图片执行复用创作台原有 provider、预扣、扣费和退款账本，视频执行复用视频任务服务及其异步结算，画布层不重复记账。任务重试会先恢复已有上游任务 ID，避免重复生成；删除画布不会取消已经提交的上游任务，也不会绕过既有结算流程。

新执行登记沿用每用户 2 GiB 的素材与在途任务预算，视频子执行预留 512 MiB，其它子执行预留 128 MiB。已有任务预留导致暂时无法接收整批时返回 HTTP `429 CANVAS_EXECUTION_LIMIT`，提示等待已有任务完成；实际素材加本次所需预算本身已经超限时仍返回 `413 CANVAS_STORAGE_LIMIT`。拒绝发生在执行登记和生成预扣前，不留下半批任务、不自动重发；同一幂等键的已受理任务仍优先恢复，不受新的容量检查阻断。

音频执行复用创作台任务引擎及其资金账本，声音、格式、语速与文本进入参数指纹。同步上游返回不明确时以 `running` 和 `CREATIVE_AUDIO_SUBMISSION_UNCONFIRMED` 提示核对，不能另建请求或直接当作已退款；重启恢复保留提交栅栏，不盲目重发。成功结果通过同一私有素材接口归档、播放和下载。

视频执行的 `status=running` 不一定表示上游正在生成。`error_code` 可为 `CREATIVE_VIDEO_SUBMISSION_UNCONFIRMED`、`CREATIVE_VIDEO_SETTLEMENT_PENDING`、`CREATIVE_VIDEO_RELEASE_PENDING` 或 `CREATIVE_VIDEO_RECONCILIATION`，分别表示受理待确认、结算待处理、预扣释放待处理和其它任务费用核对；`error_message` 提供安全说明。这些非终态提示不表示任务失败或已经退款，客户端继续按原执行轮询，不能另建任务。正常进度和终态会更新或清除提示，终态判断仍以 `status` 为准。

图片批次返回 `{batch_id, executions}`，批次 ID 为该请求幂等键。每个子执行的 `params` 包含服务端生成的 `batch_id`、从零开始的 `batch_index`、子任务总数 `batch_size` 和总输出数 `batch_output_count`，不允许客户端伪造这些内部字段。一个 MJ 四图任务对应一个子执行，八图对应两个。服务端按用户锁检查整批容量和幂等后一次提交，失败不会留下半批；响应丢失后使用原参数与原键可恢复整批，修改张数会返回冲突。轮询按批次键与节点 ID 聚合，成功图分别归档，失败子任务按原账务规则释放。

## 前端交付

`/canvas-app/index.html` 是与 Vue 宿主同源的 React 子应用。宿主只向 iframe 发送当前用户的运行时 JWT，子应用不把令牌写入 localStorage、sessionStorage 或画布文档。父子窗口同时校验消息来源和消息发送方，登出、切换用户或组件卸载时清理内存会话和对象 URL。
