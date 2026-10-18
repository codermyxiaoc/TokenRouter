# 独立 Video 上游

本文拥有 `platform=video` 的账号配置、原生协议和 HTTP 兼容边界。任务持久化、预算预留与结算由[媒体任务](../domains/media_tasks.md)和[路由与计费](../domains/routing_and_billing.md)拥有。旧 [Grok 视频](grok_upstream.md)与 [OpenAI 账号的 Seedance 能力](seedance_upstream.md)继续使用原链路；新增 Video 不改变其计价或准入。

## 章节导航

- [账号与模型端点](#video_accounts)：配置独立平台、多个端点、模型绑定与预算上限。
- [版本化协议适配](#video_adapters)：端点默认、模型覆盖、官方参数和旧任务兼容。
- [入站和上游协议](#video_protocols)：核对原生路径、参数保真、任务 ID 与取消语义。
- [OpenAI 视频入口与内容](#video_openai_content)：核对 `/v1/videos`、共享 Grok 分派及凭据隔离的流式下载。
- [管理员账号测试](#video_account_tests)：选择模型及其已配置端点，提交一次测试视频并轮询预览。
- [验证边界](#验证边界)：理解本地协议测试的保证范围。

<a id="video_accounts"></a>
## 账号与模型端点

Video 仅接受 API Key 账号，且 Video 账号与 Video 分组双向隔离。创建、编辑、批量更新和导入共用校验；跳过混合渠道提示不能绕过隔离。分组使用 `basic` 调度，显式提交 `advanced` 返回客户端错误，隐藏的文本调度配置不参与视频请求。

账号 `credentials` 示例：

```json
{
  "api_key": "供应商密钥",
  "base_url": "https://relay.example",
  "video_endpoints": ["compat", "seedance", "kling", "vidu"],
  "video_model_bindings": {"ark-model": ["compat", "seedance"], "kling-model": "kling", "vidu-model": "vidu"},
  "video_base_urls": {"seedance": "https://ark.example/api/v3"},
  "video_model_paths": {"kling-model": "/omni-video/{model}"},
  "video_vidu_model_paths": {"vidu-model": "/ent/v2/img2video"},
  "video_endpoint_adapters": {"seedance": "seedance_v1", "kling": "legacy_v1", "vidu": "kemei_v1"},
  "video_model_adapters": {"ark-model": {"seedance": "legacy_v1"}},
  "video_max_pending_tasks": 10,
  "video_max_duration_seconds": 15
}
```

`video_endpoints` 至少包含一项，可选 `compat`、`openai_videos`、`seedance`、`kling`、`wan`、`minimax`、`vidu`。创建表单不默认勾选端点，管理员必须明确选择；编辑保留已保存的选择，缺失配置不自动补为 `compat`。`compat` 保持上游 `/v1/video/generations`，`openai_videos` 使用上游 `/v1/videos`，旧账号不自动增加或迁移端点。`video_base_urls` 按端点覆盖共享 `base_url`；地址必须是没有内嵌凭据、查询参数或 fragment 的 HTTP(S) URL，出站继续经过现有上游 URL 安全校验、代理和 TLS 传输边界。受保护的 Header Override 可用于 Video API Key。

模型先经过客户端别名、分组/渠道映射和账号模型映射，再用**最终上游模型名**查询 `video_model_bindings`。该字段同时接受旧字符串单端点和新字符串数组，例如 `"ark-model": ["compat", "seedance"]`。数组必须非空、无重复，且全部属于账号已勾选端点；模型未配置绑定时继承账号端点。创建和编辑表单允许同一模型填写多行不同端点，保存时按模型聚合；只填写一条仍保存旧字符串格式。模型目录继续使用配置的绑定、白名单和映射，不补聊天模型默认值。

自适应选择在实际转发、调度资格判断和模型广场中共用同一规则：原生入口只选择该请求对应的已启用协议；统一 `/v1/video/generations` 必须明确启用 `compat`，`/v1/videos` 必须明确启用 `openai_videos`，且均须属于最终模型的允许端点集合。两个 OpenAI 入口互不授权，也不回退到唯一原生端点。没有合格账号时在建任务、预扣和上游 POST 之前拒绝请求，端点不匹配返回 `VIDEO_ENDPOINT_MODEL_MISMATCH`。不会按模型名称、列表顺序或请求字段猜测供应商，错误恢复也不能改走未授权端点；只有下述明确未受理条件允许同端点换号。

未显式配置新适配器的两个 OpenAI 入口继续只替换实际模型，未知扩展字段保持原行为。可选账号凭据 `video_openai_profile` 接受空值、`passthrough`（默认保真）或 `metaso_h3`；只有实际选中 `openai_videos` 时才生效，不通过主机或模型名称猜测适配。取消该端点时可以保留合法配置供下次启用，其它端点不读取该配置。创建任务把配置冻结到上游路由快照；后续查询与恢复不能按账号的新配置重新解释旧任务。内部未提供协议的旧调用只视为 `unified`，仍须明确启用 `compat`；它不是账号默认能力。

例如同一个模型配置 `["compat", "seedance"]` 后，客户端的 `/v1/video/generations` 转发到上游同路径，`/api/v3/contents/generations/tasks` 转发到上游 Seedance 原生路径，`/v1/videos` 不可用。历史 `"model": "seedance"` 只允许 Seedance 原生入口；需要开放兼容入口时，必须同时在账号端点和该模型绑定中增加相应协议。已受理任务仍使用冻结协议查询、取消和结算，不受后来取消端点勾选影响。

Kling 原生入口直接提供 `/text-to-video/{model}`、`/image-to-video/{model}`、`/omni-video/{model}`、`/v1/videos/text2video` 或 `/v1/videos/omni-video` 操作路径，无需从请求体猜测文生或图生模式。遗留 `video_model_paths` 继续受受控模板校验，但不能借此开放任何未勾选的 OpenAI 入口。

Vidu 使用独立的 `video_vidu_model_paths`，以最终上游模型精确名称保存 `/ent/v2/img2video`、`/ent/v2/reference2video`、`/ent/v2/text2video` 或 `/ent/v2/start-end2video`。该映射用于管理员测试默认操作；正式原生请求由入站路径明确操作。它不复用 Kling 的 `video_model_paths`，同一模型绑定两个端点时路径不会相互覆盖；路径配置也不授予端点权限。可美方案中的 Vidu Q4 不接受 `text2video` 和 `start-end2video`，不会自动改成另一种操作；供应商特定限制不套用到保留原有协议的账号。

`video_max_pending_tasks` 默认 10、范围 1–1000。可选的正数 `video_max_duration_seconds` 支持省略或用 `null` 显式清空；按秒计费或已开启 Token 秒价预扣的自动时长任务必须配置它，显式零和非法值仍拒绝。账号不再提供 Token 预留上限；遗留 `video_max_output_tokens` 对新任务无效，保存时清理，已受理任务仍按自身快照结算。Token 预扣由模型价卡的 `video_token_prepay` 决定，固定秒价不乘倍率，完成后按实际 Token 退补差；关闭则完成后原子检查资金和额度并扣费。预算不改写供应商参数或裁剪用量，缺少可信用量不能作零费用结算。详见[任务结算](../domains/video_tasks.md#video_task_billing)。

<a id="video_adapters"></a>
## 版本化协议适配

`video_endpoint_adapters` 为每个端点选择独立协议；`video_model_adapters` 以最终上游模型的精确名称按端点覆盖。生效顺序是模型覆盖、端点默认、旧 `video_openai_profile` 或原有协议。覆盖不能增加端点权限，不支持模型通配符、域名推断、任意脚本或动态 HTTP 模板。协议配置与模型绑定分离；API 导入的无绑定模型覆盖在编辑保存时保留，不自动添加限制模型端点的绑定。

| 端点 | 新建表单默认方案 | 其他可选方案 |
| --- | --- | --- |
| compat | `legacy_v1`：保留原有协议 | `openai_v1`：OpenAI 官方参数格式，保持兼容路径；`kemei_v1`：可美视频 |
| openai_videos | `openai_v1`：OpenAI JSON 协议 | `legacy_v1`；`metaso_h3_v1` 保留已有秘塔 H3 适配 |
| seedance | `seedance_v1`：Seedance 原生协议 | `legacy_v1`、`kemei_v1` |
| minimax | `minimax_v2`：MiniMax H3 V2 协议 | `legacy_v1`、`kemei_v1` |
| wan | `wan_v1`：万相异步视频协议 | `legacy_v1`、`kemei_v1` |
| kling | `kling_v1`：Kling 官方文生/图生协议 | `legacy_v1`、`kemei_v1` |
| vidu | `vidu_v2`：Vidu 官方参数 | `legacy_v1`：保留原有协议；`kemei_v1`：可美视频 |

每个端点至少提供“保留原有协议”和对应官方参数方案，也可继续选择已有供应商适配。新建表单只预选协议方案，不自动勾选端点。原有六类端点的已有账号缺失新字段时始终保留旧行为，API 创建或导入缺字段也按旧合同处理；不会凭创建时间推断适配器。Vidu 必须在 `video_endpoint_adapters.vidu` 显式选择受支持的方案，不能仅靠模型覆盖代替端点配置；原有 `kemei_v1` 账号继续保留该值，新增加的显式 `legacy_v1` 也保存版本二快照，版本一 Vidu 仍拒绝。编辑未改适配选项不会自动升级。旧 `metaso_h3` 仍有效，新映射在对应端点显式存在时优先；“继承端点设置”会删除模型覆盖。未知方案或跨端点选择在账号保存时拒绝，不能静默降级。

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

`kemei_v1` 为 token.xinhankr.com 的明确合同，适用于 `compat`、`seedance`、`minimax`、`wan`、`kling` 和新增 `vidu`。`openai_videos` 没有该选项，不声明供应商支持 `/v1/videos`。管理员按端点选择，也可按最终模型覆盖；系统不根据域名、模型名或参考图存在与否替用户更换协议。可美选项不改变上述原有默认方案。

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

供应商合同来源：[统一兼容接口](https://token.xinhankr.com/docs/openai/doc0019)、[MiniMax H3](https://token.xinhankr.com/docs/examples/doc0015)、[Seedance](https://token.xinhankr.com/docs/examples/doc0010)、[Kling](https://token.xinhankr.com/docs/examples/doc0012)、[Wan](https://token.xinhankr.com/docs/examples/doc0014)、[Vidu](https://token.xinhankr.com/docs/examples/doc0041)。供应商方案的实现入口为 `video_protocol_kemei*.go`，Vidu 操作与响应在 `video_protocol_kemei_vidu.go`；添加新的供应商合同应使用新方案 ID，不改变已有方案语义。

<a id="video_protocols"></a>
## 入站和上游协议

统一创建为 `POST /v1/video/generations`，裸别名为 `/video/generations`。**请求体遵循实际选中协议的 JSON 参数约定**；原有协议不互转 `content`、`input`、`parameters`、`images`，显式适配器按上述固定版本处理已声明的别名。上游端点由入站协议、账号能力和最终模型的允许端点集合共同决定。创建必须能从规范的唯一 `model` 字段或受支持的模型路径解析模型；路径与正文同时提供时必须一致。

| 上游端点 | 创建 | 原生查询 | 原生取消 |
| --- | --- | --- | --- |
| compat | `/v1/video/generations` | `/v1/video/generations/:id` | 同路径 `DELETE` |
| openai_videos | `/v1/videos` | `/v1/videos/:id`；内容为 `/:id/content` | 不支持 |
| seedance | `/api/v3/contents/generations/tasks` | 同根路径 `/:id` | 同路径 `DELETE` |
| kling | 配置的受控模型路径，或两个 `/v1/videos/*` 操作 | `/tasks?task_ids=:id` | 不支持 |
| wan | `/api/v1/services/aigc/video-generation/video-synthesis` | `/api/v1/tasks/:id` | 不支持 |
| minimax | `/v2/video_generation` | `/v2/query/video_generation/:id` | 不支持 |
| vidu | `/ent/v2/img2video`、`/ent/v2/reference2video`、`/ent/v2/text2video`、`/ent/v2/start-end2video` | `/ent/v2/tasks/:id/creations` | 不支持 |

Ark 同时保留 `/v3`、`/v1` 和裸路径别名。Video 分组进入新任务服务；OpenAI 分组进入旧 Seedance 处理器。共享 `/v1/videos` 创建入口按分组平台分派，Grok 继续原处理器；其 `/v1/videos/generations` 等复数操作路径保持原行为，Kling 的两个明确操作路径优先精确分类。

统一入口使用本地任务 ID；统一查询为 `GET /v1/video/generations/:id` 或 `GET /v1/tasks/:id`，两条路径都支持 `DELETE` 取消。原生创建和原生查询保留供应商任务 ID 与原始响应体，另用 `X-Video-Task-ID` 返回本地 ID。Kling 查询只接受一个 `task_ids`，不允许批量查询越过逐任务归属判断。查询/取消校验原用户、原 Key 和原端点归属；已经创建的任务不再要求当前映射仍包含原分组，当前 Key 仍须通过身份、状态和 IP 等鉴权。任务使用冻结的版本化 URL/操作路径及原账号，不重新调度。原生 ID 歧义不能以其它账号试探解决。普通 Key 改到 OpenAI 分组后，Ark 原生路径继续按旧规则分流，可使用本地任务 ID 从统一路径取回新 Video 任务。

原有协议转发只替换认证及实际模型字段/模型路径；其它 JSON 参数和未知扩展字段保持原样。旧 `metaso_h3` / 新 `metaso_h3_v1` 适配的参数转换见下文，不扩展账号允许的端点。重复 JSON 键、大小写歧义和超过 64 层的嵌套会被拒绝，避免网关与上游解析值不同。通过任务归属校验的响应保留上游 HTTP 状态、原始 JSON/错误正文及允许透传的响应头，不执行客户端模型别名还原或套面板 envelope；任务响应强制 `Cache-Control: no-store`。创建请求禁止自动重定向，3xx 的 `Location` 不下发客户端，其他共享上游请求的重定向策略不受影响。Wan 的 `request_id` 是追踪号，任务 ID 从 `output.task_id` 读取。

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

独立 Video 的退款策略区分创建与查询：安全换号耗尽或不满足重提条件时，完整收到创建 HTTP 4xx/5xx 且没有任务 ID，返回原错误并将本地任务记为失败、幂等释放预扣；平台承担上游可能已经受理的成本。退款规则不能用于推断允许再次生成。带任务 ID 的错误保留归属供原账号查询，明确失败/取消终态按任务规则释放。明确的创建超时或本版提交进程中断，只有没有持久响应证据且具备起算时点时，才适用[15 分钟核对后释放规则](../domains/video_tasks.md#video_task_lifecycle)。其它网络中断、响应读取失败或缺乏故障分类的旧任务继续待核对，查询 HTTP 错误不会触发退款；恢复不使用后续查询包推断创建失败。

取消必须得到供应商确认。显式 `cancelled`/`canceled`/`deleted` 才是取消观测；空响应的 Ark 成功删除和 compat `204` 可确认取消。HTTP 200 中的业务错误、处理中或已经成功状态不能直接退款。`expired` 没有足够契约证明未产生费用，保留该观测并等待对账。

<a id="video_openai_content"></a>
## OpenAI 视频入口与内容

Video 分组使用 `POST /v1/videos` 创建、`GET /v1/videos/:id` 查询、`GET /v1/videos/:id/content` 播放或下载，兼容不带 `/v1` 的别名。创建要求账号与模型明确允许 `openai_videos`，只接受 JSON，使用站内 Bearer Key；参数以实际选中上游与显式适配配置为准，支持识别 `seconds`、`size`、`input_reference.image_url` 及原有 `duration`、`resolution`、`ratio`、`images`，不隐式互相转换。已创建任务的查询和内容读取继续遵循原任务归属与冻结协议，不依赖当前账号勾选。这是所提供异步 JSON 合同的接入，不自动开放集合枚举、multipart、remix 或未声明的取消 API。

该入口返回本站 `vid_…` ID、`object=video` 和 `queued / in_progress / completed / failed` 状态，另保留 `billing_status`、实际已知的时长、分辨率及 Token 用量；已知时长同时提供兼容旧客户端的 `duration` 和规范 `seconds` 字符串，已知像素尺寸提供 `size`。失败提供安全错误摘要。受理不明仍保留预算待对账，不因显示四态而退款或重新提交。旧统一和供应商原生响应格式保持不变。查询沿用持久后台轮询、预扣与幂等结算，同一任务重复查询或下载不产生新的生成请求。

共享路径仅创建 Video 分组任务，Grok 分组继续原链路；管理请求通过本地 `vid_` ID 精确查找新任务，后续允许原有效 Key 切换到其它有效分组后取回。复合/智能 Key 查询不要求重新选组，但仍核对原行为用户和原 Key。普通 Key 仅当前 Video 分组的本地任务查询可免消费准入，切换到其它平台后继续其原消费门禁，不能让碰巧以 `vid_` 开头的旧 Grok ID 绕过原检查；原有分组、状态、IP 与身份门禁保持不变。智能路由仍允许 Grok 与 Video 各自符合条件的候选，模型字段处理按本次所选平台区分，不能让新路径改变旧 Grok 的别名恢复。

`content` 是只读流，必须任务生成完成且账务已结算；未完成或待对账返回冲突，不触发上游状态查询或补扣。优先读取持久化的安全公共视频地址；若上游是 `openai_videos` 且未返回直接地址，则使用冻结端点、原账号和原供应商任务 ID 请求固定 `/v1/videos/:id/content`。带凭据请求禁止自动重定向；重定向目标单独经过公共地址与 DNS 拨号校验，以无凭据请求访问，不转发调用方 Key、上游 Authorization 或 Cookie。

面板预览通过自身的对象权限签发任务专属票据，与站内 Key 的 `content` 入口分开鉴权。独立 Video 的签票和每次播放都读取持久任务，核对任务 ID、原用户、原 Key、`completed`、`settled` 及完成后 24 小时期限；Redis 媒体快照不替代持久状态检查。无直接地址且冻结端点为 `openai_videos` 的任务同样可签发最长十分钟的票据，票据期限不超过结果期限。预览元数据及签票不访问上游；实际播放才复用上述只读内容传输，服务端凭据仅发往冻结的供应商内容路径，不进入浏览器、票据或公共 CDN 请求。图片、Grok 和 Seedance 预览的原有公共地址边界不变，详见[任务结果预览](../domains/media_tasks.md#media_task_preview)。

流式下载支持单段 `Range`，最多 512 MiB、两分钟，API 下载与面板播放共享本机最多 16 条下载流；只输出允许的视频类型和必要的内容头，供应商错误、跳转、Cookie 等不透传。响应禁止缓存并带 `nosniff` 和 `no-referrer`，内容传输不调用生成、任务状态查询、恢复或结算。新端点未声明删除语义，因此不为 `/v1/videos/:id` 注册 DELETE，账号旧统一取消入口也不能把未支持的操作伪装为退款成功。视频价卡、参考图固定收费、倍率和失败释放规则继续由已有任务账本负责。

<a id="video_account_tests"></a>
## 管理员账号测试

Video API Key 账号在账号管理中使用与 Grok 相同的测试弹窗、进度和媒体预览组件，只提供视频测试。先选择模型，再选择该模型允许的端点；`GET /api/v1/admin/accounts/:id/models` 的 Video 模型条目携带 `video_endpoints` 和 `video_protocol_adapters`，后者为最终映射模型的生效方案；可选 `video_vidu_operation` 返回该模型已配置的 Vidu 默认操作。目录来自账号模型映射、最终白名单和视频模型绑定，不补聊天默认模型，也不默认开放任何兼容端点。端点集合根据映射后的上游模型解析，前端选择不能替代服务端的最终校验。

测试复用 [管理员账号连接测试](http_api.md#account_connection_tests)，请求显式发送 `test_type=video` 与 `video_endpoint=compat|openai_videos|seedance|kling|wan|minimax|vidu`。可提供 `prompt`、`image_data_url`、`duration`、`resolution` 和 `ratio`。历史协议时长省略默认 5 秒，OpenAI 官方协议默认 4 秒，显式值必须是 1–60 的整数且满足所选协议约束，这是主动试跑的限制，不改写账号预算上限或正常网关参数。历史协议分辨率不设置默认值；新 MiniMax 官方测试界面预填 `768P`，OpenAI 官方使用像素尺寸提示且比例默认留空。Seedance、Vidu 官方测试比例也默认留空。默认值仅在字段未被手动修改时随方案调整；其余方案比例预填 `16:9`，可清空。参考图沿用 Grok 测试的真实图片格式和 4 MiB 限制，不接受任意素材 URL 下载。

可美测试时长默认 5 秒，MiniMax 分辨率默认 `768P`，Wan 为 `720P`，Seedance/Kling 为 `720p`，Vidu 为 `540p`，compat 不预填分辨率。MiniMax 上传图片时省略未被用户编辑过的默认比例，保留上游图生自适应行为；用户明确修改的比例仍按输入提交。Vidu 用 `video_operation` 显式选择四种受控操作之一，优先回填模型的 `video_vidu_operation`，没有配置时要求选择；临时选择不写回账号。当前测试弹窗只上传一张图，`img2video`/`reference2video` 要求图片，`text2video` 禁止图片，`start-end2video` 提示使用 API 提交两张图，不把单图伪装为首尾帧。服务端再次校验操作、模型限制和图片数量。

兼容端点显式选择 `openai_v1` 时，测试默认值与 `/v1/videos` 的同方案一致，保持 4 秒、像素尺寸提示和空比例；端点仍是 `/v1/video/generations`。Vidu 官方 `vidu_v2` 的主动测试时长预填 5 秒并允许按模型修改，分辨率与比例留空，由管理员按该模型要求填写；该主动测试值不是所有 Vidu 模型的官方默认值，不改变正式 API 的缺省行为。

Seedance 官方账号测试的分辨率保持空值，输入示例为 `720p`，不再沿用通用 `768p` 示例；未编辑的比例也默认留空，上传参考图时不自动填入 `16:9`，由官方选择当前模型的默认比例。两套测试弹窗共用官方档位提示和输入校验，用户手填或明确清空的值不会被切换模型或方案覆盖；模型级限制由后端在模型映射和协议覆盖之后统一检查，非法输入不会触发付费创建。

测试载荷按所选协议构造，模型映射、端点 Base URL 覆盖、受控 Kling/Vidu 路径、代理、TLS 与受保护 Header Override 均沿用原有账号配置。上游创建最多一次，后续只用该账号、端点和任务 ID 轮询；不因错误或等待超时重新提交。最多等待 10 分钟。成功必须取得明确完成状态，以及安全媒体链接或已下载的视频内容；OpenAI 内容端点下载最多 64 MiB，带凭据请求禁止自动跟随跳转，公共跳转另走无凭据安全下载。失败、未知状态、冲突任务 ID 或内容下载错误不能伪装成成功。

上游生成完成与浏览器预览可用性分别展示。公共产物链接由浏览器直接读取，仍可能受到本机网络、链接有效期或编码支持限制；播放器加载失败会显示独立提示，可仅重新加载预览或在新页打开安全作品链接。等待视频元数据超过 30 秒仅提示媒体连接缓慢，仍可继续等待，加载成功后自动恢复；换源、重载和卸载时清理等待计时器。预览重载不会再次提交生成、查询上游任务或产生新的测试费用。生产默认 CSP 明确允许受支持的 HTTPS、同源、Data URL 和 Blob 媒体来源，管理员自定义 CSP 仍按其配置生效。

账号显式选择 `video_openai_profile=metaso_h3` 且使用 `openai_videos` 时，测试复用正式网关适配器：时长转为 `seconds`，768p/2k 结合 `16:9` 或 `9:16` 转为供应商 `size`，单张参考图转为 `input_reference.image_url`。不支持的分辨率、比例和冲突参数在发送前拒绝；完成响应的计费元数据异常也不能显示测试成功。原有协议配置沿用旧行为，不根据模型名或 Base URL 推断 H3。每个启用端点显示独立的协议选择框，模型绑定行可以继承或覆盖。

主动测试不经过站内用户计费，不新增用户使用记录或持久化媒体任务；真实生成仍可能消耗上游额度。浏览器关闭、切换账号或停止测试会取消本地等待，上游已经受理的任务可能继续生成。测试选项不写入账号，不改变普通网关请求或原有计划测试流程。

上游返回 HTTP 错误时，管理员测试保留状态码，并从已知错误字段提取安全摘要，便于区分参数限制、模型权限和内容审核。摘要会遮罩凭据、完整素材地址及 Base64 数据，不回显原始响应、HTML 或嵌套调试对象；无法安全提取时仅显示状态码。这项诊断不改变正式网关错误、任务状态、重试或计费策略。

创建响应未识别到任务 ID，或已提取 ID 不能安全用于查询路径时，测试分别提示原因，并提供 HTTP 状态与该协议固定字段的 JSON 类型摘要。摘要不包含原始响应、任意上游字段名、任务 ID 值、素材地址或凭据；它只用于管理员排查返回结构，不扩大任务 ID 的接受范围。这两种情况都可能已在上游开始生成，测试停止本地轮询且不自动重复提交。

## 验证边界

协议 fixtures 使用本地 HTTP 替身验证已支持端点的路径、认证、参数保真、原生响应、ID 提取和取消状态，不产生真实付费请求。可美请求与响应、Vidu 操作、模型覆盖及版本快照也使用独立 fixture 验证；这些检查不证明线上供应商当前可用。用户提供的附件包含中继协议示例，不能自动等同于已验证的供应商官方完整契约；未给出的取消、缺失用量和过期费用语义保持保守处理。部署到具体上游前应核对其支持的模型、原生操作路径和终态 usage 字段。

相关入口：[接口目录](index.md)、[HTTP 边界](http_api.md)、[账号矩阵](upstream_account_matrix.md)、[账号调度](../architecture/account_scheduling_and_cache.md)。
