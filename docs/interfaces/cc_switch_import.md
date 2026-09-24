# CC Switch 导入

本文定义 API 密钥页到 CC Switch 的配置确认及深链接字段映射。后端不新增导入接口，实际客户端配置由 CC Switch 接收和保存。

## 页面与模型填写

所有分组的“导入到 CCS”入口先打开 `CcSwitchImportDialog`。用户通过与使用密钥弹窗一致的分段选择样式选择 Claude、Codex 或 Gemini。名称为直接编辑的输入框，分别默认 `My Claude`、`My Codex`、`My Gemini`；主模型必选，Claude 额外提供可选的 Haiku、Sonnet、Opus 模型。所有模型字段使用项目 `Select`，下拉框支持按模型 ID 或展示名称搜索并清空选择，不提供自定义模型输入。各应用保留独立草稿，关闭或更换密钥后重置。只有点击确认才生成深链接并拉起客户端。

弹窗复用 `getMarketplaceModels(signal)` 查询 `/api/v1/marketplace/models`，使用与模型广场相同的全部公开模型列表，无分页。展开所有分组并按原始 `model.id` 精确去重，保留 ID 大小写和名称；展示名仅供搜索，不作为导出的请求值。不按当前密钥或选中的应用过滤模型。该目录不包含专属/停用分组、Key 私有别名及复合前缀；选择模型不扩大密钥已经允许的模型和客户端协议范围，实际请求仍由网关按既有规则校验。

加载过程只读取站内公开目录，不发送待导出的 API Key 或发起推理。请求沿用面板 API 客户端超时，关闭、更换密钥或卸载时取消并忽略旧响应。加载失败提供重新加载操作；空列表不补造默认模型，加载中、失败或空列表均禁止确认导入。

## 深链接契约

`frontend/src/utils/ccswitchImport.ts` 的显式 `app` 优先于历史平台推导。`name` 来自用户填写的配置名称，主模型写入 `model`；Claude 分档分别写入 `haikuModel`、`sonnetModel`、`opusModel`，空字段省略，其他应用不携带这些参数。参数名与 [CC Switch v3.20.3 官方解析器](https://github.com/farion1231/cc-switch/blob/v3.20.3/src-tauri/src/deeplink/parser.rs) 及[配置生成器](https://github.com/farion1231/cc-switch/blob/v3.20.3/src-tauri/src/deeplink/provider.rs)一致。Claude 未填分档模型时，由 CC Switch 自己执行继承或默认规则。

Codex 使用恰好一个 `/v1` 后缀；Claude/Gemini 使用去除末尾 `/v1` 的基础地址。普通 Antigravity Key 的 Claude/Gemini 导入沿用 `/antigravity` 专用入口；智能路由和复合 Key 使用公共入口，由网关解析候选或前缀。保留历史未指定 `app` 的调用兼容及 Grok Build 映射：旧 OpenAI 入口同样规范为单个 `/v1`，旧 Antigravity 入口在追加专用路径前去掉尾斜杠。

导入沿用 `ccswitch://v1/import`、`apiKey`、站点地址及 Base64 UTF-8 用量脚本参数；用量脚本固定读取 API Origin 的 `/v1/usage`。深链接中包含用户确认导出的 Key，不将它写入浏览器持久化、日志或分析事件。网页拉起协议处理器不代表 CC Switch 已保存配置，最终结果以客户端为准。

## 验证

- `ccswitchImport.spec.ts`：应用选择优先级、模型参数、地址规范化及旧导入兼容。
- `CcSwitchImportDialog.spec.ts`、`KeysView.spec.ts`：全站模型去重及搜索、确认前不导入、必填校验、切换应用、加载重试及取消和更换密钥的状态隔离。
- `CcSwitchImportDialog.i18n.spec.ts`：通过正式的中英文语言包入口和真实 JIT 翻译器验证弹窗文案；不能用返回键名的翻译 mock 代替文案校验。开发环境的语言包热更新由 `frontend/src/i18n/index.ts` 接收并同步已加载消息，避免新增界面引用旧语言包快照。

相关文档：[接口目录](index.md)、[模型目录与市场](model_catalog_and_marketplace.md)、[tf CLI 网页导入](tf_cli_web_import.md)。
