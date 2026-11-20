package service

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	openAIResponsesEndpoint          = "/v1/responses"
	openAIResponsesCompactEndpoint   = "/v1/responses/compact"
	responsesLiteHeader              = "X-OpenAI-Internal-Codex-Responses-Lite"
	responsesLiteHeaderKey           = "x-openai-internal-codex-responses-lite"
	responsesLiteWSMetadataKey       = "ws_request_header_x_openai_internal_codex_responses_lite"
	imageGenerationPermissionMessage = "Image generation is not enabled for this group"
)

// isOpenAIResponsesLiteHeader 判断请求是否来自 Codex Responses Lite 通道。
func isOpenAIResponsesLiteHeader(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

// isOpenAIResponsesLiteWebSocketPayload 从 WS client_metadata 中读取对应的 Lite 标记。
func isOpenAIResponsesLiteWebSocketPayload(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	return isOpenAIResponsesLiteHeader(gjson.GetBytes(body, "client_metadata."+responsesLiteWSMetadataKey).String())
}

// ImageGenerationPermissionMessage returns the stable end-user error text for disabled groups.
func ImageGenerationPermissionMessage() string {
	return imageGenerationPermissionMessage
}

// GroupAllowsImageGeneration preserves ungrouped-key behavior and enforces the flag when a group is present.
func GroupAllowsImageGeneration(group *Group) bool {
	return group == nil || group.AllowImageGeneration
}

var (
	// imageGenLiteralBytes 覆盖扫描侧的全部非模型分支：tool type image_generation
	// 自带该前缀，namespace / tool_choice / 函数引用的名字都是 image_gen。
	imageGenLiteralBytes = []byte("image_gen")
	// imageModelFoldBytes 覆盖 model 分支：gpt-image-* 与 grok-imagine* 都含 mag，
	// 模型名按 ToLower 比较，这里做 ASCII 折叠匹配。
	imageModelFoldBytes = []byte("mag")
)

// 工具名含 image_gen，生图模型含 mag（这三个字母不受特殊 Unicode 大小写折叠影响）。
// JSON Unicode 转义可能隐藏任意标记，此时必须进行完整扫描。
func openAIBodyMayDeclareImageGeneration(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if bytes.Contains(body, imageGenLiteralBytes) {
		return true
	}
	if containsASCIIFold(body, imageModelFoldBytes) {
		return true
	}
	return bytes.Contains(body, jsonUnicodeEscapeBytes)
}

// IsImageGenerationIntent 判断请求是否声明生图能力。
func IsImageGenerationIntent(endpoint string, requestedModel string, body []byte) bool {
	return openAIImageGenerationIntent(endpoint, requestedModel, body, false, true)
}

// IsExplicitImageGenerationIntent 仅检测原生 image_generation 工具、图片模型和显式 tool_choice，
// 不检测被动的 image_gen namespace 声明。用于 capability 路由决策——被动 namespace 不应
// 强制要求原生 Responses 能力，否则 Chat Completions-only 账号会被误过滤（#4476）。
func IsExplicitImageGenerationIntent(endpoint string, requestedModel string, body []byte) bool {
	return openAIImageGenerationIntent(endpoint, requestedModel, body, true, true)
}

// IsImageGenerationIntentForPlatform 按平台区分普通意图与显式生图意图。
func IsImageGenerationIntentForPlatform(endpoint string, requestedModel string, body []byte, platform string) bool {
	return openAIImageGenerationIntentForPlatform(endpoint, requestedModel, body, platform, true)
}

// IsImageGenerationIntentForPlatformValidated 与 IsImageGenerationIntentForPlatform 判定相同，
// 但要求调用方已经确认 body 是合法 JSON（例如 handler 已对同一份字节做过 gjson.ValidBytes）。
// 它跳过这次全量校验——大 body 上这一项占判定耗时的六成以上。非法 JSON 的语义与未校验
// 版本不同，只允许在校验过同一份字节、且后续改写只经由 sjson / json.Marshal 的调用点使用。
func IsImageGenerationIntentForPlatformValidated(endpoint string, requestedModel string, body []byte, platform string) bool {
	return openAIImageGenerationIntentForPlatform(endpoint, requestedModel, body, platform, false)
}

func openAIImageGenerationIntentForPlatform(endpoint string, requestedModel string, body []byte, platform string, validate bool) bool {
	if !strings.EqualFold(strings.TrimSpace(platform), PlatformGrok) {
		return openAIImageGenerationIntent(endpoint, requestedModel, body, false, validate)
	}
	// Grok 移除被动命名空间目录，仅显式选用生图能力的请求属于生图意图。
	return openAIImageGenerationIntent(endpoint, requestedModel, body, true, validate)
}

// openAIImageGenerationIntent 统一入口：先做 O(1) 端点/模型判断，再用字节级预检查
// 挡住不可能命中扫描的 body，最后才按需校验并遍历根对象。
func openAIImageGenerationIntent(endpoint string, requestedModel string, body []byte, explicit bool, validate bool) bool {
	if IsImageGenerationEndpoint(endpoint) || isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if !openAIBodyMayDeclareImageGeneration(body) {
		return false
	}
	if validate && !gjson.ValidBytes(body) {
		return false
	}
	root := parseRawJSONView(body)
	if explicit {
		return explicitImageGenerationDeclarationsFromRoot(root)
	}
	return imageGenerationDeclarationsFromRoot(root)
}

// imageGenerationDeclarationsFromRoot 走一遍根对象，保留「重复键取第一个」语义，
// 覆盖 model、tools、input.additional_tools 与 tool_choice。调用方负责 JSON 合法性。
func imageGenerationDeclarationsFromRoot(root gjson.Result) bool {
	var modelSeen, toolsSeen, inputSeen, toolChoiceSeen bool
	imageIntent := false
	root.ForEach(func(key, value gjson.Result) bool {
		// 根对象只遍历一次，重复字段沿用首个值。
		switch key.Str {
		case "model":
			if !modelSeen {
				modelSeen = true
				imageIntent = isOpenAIImageGenerationModel(strings.TrimSpace(value.String()))
			}
		case "tools":
			if !toolsSeen {
				toolsSeen = true
				imageIntent = openAIJSONToolsContainImageGeneration(value)
			}
		case "input":
			if !inputSeen {
				inputSeen = true
				imageIntent = openAIJSONInputContainsImageGenTool(value)
			}
		case "tool_choice":
			if !toolChoiceSeen {
				toolChoiceSeen = true
				imageIntent = openAIJSONToolChoiceSelectsImageGeneration(value)
			}
		}
		return !imageIntent && (!modelSeen || !toolsSeen || !inputSeen || !toolChoiceSeen)
	})
	return imageIntent
}

// explicitImageGenerationDeclarationsFromRoot 只认原生 image_generation 工具、图片模型
// 与显式 tool_choice，不认被动的 image_gen namespace 声明。
func explicitImageGenerationDeclarationsFromRoot(root gjson.Result) bool {
	var modelSeen, toolsSeen, toolChoiceSeen bool
	imageIntent := false
	root.ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "model":
			if !modelSeen {
				modelSeen = true
				imageIntent = isOpenAIImageGenerationModel(strings.TrimSpace(value.String()))
			}
		case "tools":
			if !toolsSeen {
				toolsSeen = true
				imageIntent = openAIJSONToolsContainNativeImageGeneration(value)
			}
		case "tool_choice":
			if !toolChoiceSeen {
				toolChoiceSeen = true
				imageIntent = openAIJSONToolChoiceSelectsExplicitImageGeneration(value)
			}
		}
		return !imageIntent && (!modelSeen || !toolsSeen || !toolChoiceSeen)
	})
	return imageIntent
}

// IsImageGenerationIntentMap 在服务层修改请求后，使用 map 结构判断宽泛生图意图。
func IsImageGenerationIntentMap(endpoint string, requestedModel string, reqBody map[string]any) bool {
	if IsImageGenerationEndpoint(endpoint) {
		return true
	}
	if isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if reqBody == nil {
		return false
	}
	if isOpenAIImageGenerationModel(firstNonEmptyString(reqBody["model"])) {
		return true
	}
	if hasOpenAIImageGenerationTool(reqBody) {
		return true
	}
	return openAIAnyToolChoiceSelectsImageGeneration(reqBody["tool_choice"])
}

// IsExplicitImageGenerationIntentMap 在服务层修改请求后，仅判断显式生图意图。
func IsExplicitImageGenerationIntentMap(endpoint string, requestedModel string, reqBody map[string]any) bool {
	if IsImageGenerationEndpoint(endpoint) || isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if reqBody == nil {
		return false
	}
	if isOpenAIImageGenerationModel(firstNonEmptyString(reqBody["model"])) {
		return true
	}
	if openAIAnyToolsContainNativeImageGeneration(reqBody["tools"]) {
		return true
	}
	return openAIAnyToolChoiceSelectsExplicitImageGeneration(reqBody["tool_choice"])
}

// IsImageGenerationEndpoint identifies dedicated generated-image endpoints.
func IsImageGenerationEndpoint(endpoint string) bool {
	switch normalizeImageGenerationEndpoint(endpoint) {
	case "/v1/images/generations", "/v1/images/edits", "/images/generations", "/images/edits":
		return true
	default:
		return false
	}
}

func normalizeImageGenerationEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(strings.ToLower(endpoint))
	if endpoint == "" {
		return ""
	}
	endpoint = strings.TrimPrefix(endpoint, "https://api.openai.com")
	if idx := strings.IndexByte(endpoint, '?'); idx >= 0 {
		endpoint = endpoint[:idx]
	}
	return strings.TrimRight(endpoint, "/")
}

func openAIJSONToolsContainImageGeneration(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	found := false
	tools.ForEach(func(_, item gjson.Result) bool {
		if isOpenAIImageGenerationType(openAIJSONString(item.Get("type"))) {
			found = true
			return false
		}
		if isImageGenNamespaceTool(item) {
			found = true
			return false
		}
		return true
	})
	return found
}

func openAIJSONToolsContainNativeImageGeneration(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	found := false
	tools.ForEach(func(_, item gjson.Result) bool {
		found = isOpenAIImageGenerationType(openAIJSONString(item.Get("type")))
		return !found
	})
	return found
}

func openAIAnyToolsContainNativeImageGeneration(rawTools any) bool {
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if ok && isOpenAIImageGenerationType(firstNonEmptyString(tool["type"])) {
			return true
		}
	}
	return false
}

// isOpenAIImageGenerationType 判断工具类型是否为原生生图工具。
func isOpenAIImageGenerationType(value string) bool {
	return strings.TrimSpace(value) == "image_generation"
}

// isOpenAIImageGenNamespaceName 判断命名空间是否为 Codex 生图命名空间。
func isOpenAIImageGenNamespaceName(value string) bool {
	return strings.TrimSpace(value) == "image_gen"
}

// isImageGenNamespaceTool 检测 Codex 的 namespace 风格生图工具声明：
// { "type": "namespace", "name": "image_gen", ... }。Codex /image 使用
// 这种格式，而不是扁平的 { "type": "image_generation" }。
func isImageGenNamespaceTool(tool gjson.Result) bool {
	return openAIJSONString(tool.Get("type")) == "namespace" &&
		isOpenAIImageGenNamespaceName(openAIJSONString(tool.Get("name")))
}

// openAIJSONInputContainsImageGenTool 扫描 Responses input 中声明 image_gen
// namespace 的 additional_tools 项，覆盖工具嵌在 input 而非顶层 tools 的
// Responses Lite 格式。
func openAIJSONInputContainsImageGenTool(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if openAIJSONString(item.Get("type")) != "additional_tools" {
			return true
		}
		found = openAIJSONToolsContainImageGeneration(item.Get("tools"))
		return !found
	})
	return found
}

func openAIRequestBodyHasImageGenerationDeclaration(body []byte) bool {
	if !openAIBodyMayDeclareImageGeneration(body) || !gjson.ValidBytes(body) {
		return false
	}
	return openAIJSONToolsContainImageGeneration(gjson.GetBytes(body, "tools")) ||
		openAIJSONInputContainsImageGenTool(gjson.GetBytes(body, "input")) ||
		openAIJSONToolChoiceSelectsImageGeneration(gjson.GetBytes(body, "tool_choice"))
}

func openAIRequestBodyImageGenerationToolNeedsNormalization(body []byte) bool {
	if !openAIBodyMayDeclareImageGeneration(body) || !gjson.ValidBytes(body) {
		return false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	needsNormalization := false
	tools.ForEach(func(_, item gjson.Result) bool {
		if openAIJSONString(item.Get("type")) != "image_generation" {
			return true
		}
		// 只有旧字段或明确的模型不兼容字段需要修正时才进入 map 修改。
		if item.Get("format").Exists() || item.Get("compression").Exists() {
			needsNormalization = true
			return false
		}
		imageModel := strings.ToLower(strings.TrimSpace(item.Get("model").String()))
		if strings.HasPrefix(imageModel, "gpt-image-2") && item.Get("input_fidelity").Exists() {
			needsNormalization = true
			return false
		}
		return true
	})
	return needsNormalization
}

func openAIJSONToolChoiceSelectsImageGeneration(choice gjson.Result) bool {
	if !choice.Exists() {
		return false
	}
	if choice.Type == gjson.String {
		return isOpenAIImageGenerationType(choice.String())
	}
	if !choice.IsObject() {
		return false
	}
	choiceType := openAIJSONString(choice.Get("type"))
	if isOpenAIImageGenerationType(choiceType) {
		return true
	}
	if choiceType == "namespace" &&
		(isOpenAIImageGenNamespaceName(openAIJSONString(choice.Get("name"))) ||
			isOpenAIImageGenNamespaceName(openAIJSONString(choice.Get("namespace")))) {
		return true
	}
	if tool := choice.Get("tool"); tool.IsObject() && openAIJSONToolChoiceSelectsImageGeneration(tool) {
		return true
	}
	if isOpenAIImageGenerationType(openAIJSONString(choice.Get("function.name"))) {
		return true
	}
	return false
}

func openAIJSONToolChoiceSelectsExplicitImageGeneration(choice gjson.Result) bool {
	if openAIJSONToolChoiceSelectsImageGeneration(choice) {
		return true
	}
	if !choice.IsObject() {
		return false
	}
	if tool := choice.Get("tool"); tool.IsObject() && openAIJSONToolChoiceSelectsExplicitImageGeneration(tool) {
		return true
	}
	if isOpenAIImageGenFunctionReference(
		openAIJSONString(choice.Get("namespace")),
		openAIJSONString(choice.Get("name")),
	) {
		return true
	}
	if fn := choice.Get("function"); fn.IsObject() {
		return isOpenAIImageGenFunctionReference(
			openAIJSONString(fn.Get("namespace")),
			openAIJSONString(fn.Get("name")),
		)
	}
	return false
}

func isOpenAIImageGenFunctionReference(namespace string, name string) bool {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "image_gen" && name == "imagegen" {
		return true
	}
	switch name {
	case "image_gen.imagegen", "image_gen__imagegen":
		return true
	default:
		return false
	}
}

func openAIAnyToolChoiceSelectsImageGeneration(choice any) bool {
	switch v := choice.(type) {
	case string:
		return isOpenAIImageGenerationType(v)
	case map[string]any:
		choiceType := strings.TrimSpace(firstNonEmptyString(v["type"]))
		if isOpenAIImageGenerationType(choiceType) {
			return true
		}
		if choiceType == "namespace" &&
			(isOpenAIImageGenNamespaceName(firstNonEmptyString(v["name"])) ||
				isOpenAIImageGenNamespaceName(firstNonEmptyString(v["namespace"]))) {
			return true
		}
		if tool, ok := v["tool"].(map[string]any); ok && openAIAnyToolChoiceSelectsImageGeneration(tool) {
			return true
		}
		if fn, ok := v["function"].(map[string]any); ok && isOpenAIImageGenerationType(firstNonEmptyString(fn["name"])) {
			return true
		}
	}
	return false
}

func openAIAnyToolChoiceSelectsExplicitImageGeneration(choice any) bool {
	if openAIAnyToolChoiceSelectsImageGeneration(choice) {
		return true
	}
	choiceMap, ok := choice.(map[string]any)
	if !ok {
		return false
	}
	if tool, ok := choiceMap["tool"].(map[string]any); ok && openAIAnyToolChoiceSelectsExplicitImageGeneration(tool) {
		return true
	}
	if isOpenAIImageGenFunctionReference(
		firstNonEmptyString(choiceMap["namespace"]),
		firstNonEmptyString(choiceMap["name"]),
	) {
		return true
	}
	if fn, ok := choiceMap["function"].(map[string]any); ok {
		return isOpenAIImageGenFunctionReference(
			firstNonEmptyString(fn["namespace"]),
			firstNonEmptyString(fn["name"]),
		)
	}
	return false
}

func getAPIKeyFromContext(c interface{ Get(string) (any, bool) }) *APIKey {
	if c == nil {
		return nil
	}
	v, exists := c.Get("api_key")
	if !exists {
		return nil
	}
	apiKey, _ := v.(*APIKey)
	return apiKey
}

func apiKeyGroup(apiKey *APIKey) *Group {
	if apiKey == nil {
		return nil
	}
	return apiKey.Group
}

type OpenAIResponsesImageBillingConfig struct {
	Model     string
	SizeTier  string
	InputSize string
}

func resolveOpenAIResponsesImageBillingConfigDetailed(reqBody map[string]any, fallbackModel string) (OpenAIResponsesImageBillingConfig, error) {
	imageModel := ""
	imageSize := ""
	hasImageTool := false
	if reqBody != nil {
		rawTools, _ := reqBody["tools"].([]any)
		for _, rawTool := range rawTools {
			toolMap, ok := rawTool.(map[string]any)
			if !ok || strings.TrimSpace(firstNonEmptyString(toolMap["type"])) != "image_generation" {
				continue
			}
			hasImageTool = true
			imageModel = strings.TrimSpace(firstNonEmptyString(toolMap["model"]))
			imageSize = strings.TrimSpace(firstNonEmptyString(toolMap["size"]))
			break
		}
		if imageSize == "" {
			imageSize = strings.TrimSpace(firstNonEmptyString(reqBody["size"]))
		}
	}
	if imageModel == "" && reqBody != nil {
		bodyModel := strings.TrimSpace(firstNonEmptyString(reqBody["model"]))
		if isOpenAIImageBillingModelAlias(bodyModel) || !hasImageTool {
			imageModel = bodyModel
		}
	}
	if imageModel == "" && hasImageTool {
		imageModel = "gpt-image-2"
	}
	if imageModel == "" {
		imageModel = strings.TrimSpace(fallbackModel)
	}
	sizeTier := normalizeOpenAIImageSizeTier(imageSize)
	return OpenAIResponsesImageBillingConfig{
		Model:     imageModel,
		SizeTier:  sizeTier,
		InputSize: imageSize,
	}, nil
}

func resolveOpenAIResponsesImageBillingConfigFromBody(body []byte, fallbackModel string) (string, string, error) {
	cfg, err := resolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, fallbackModel)
	if err != nil {
		return "", "", err
	}
	return cfg.Model, cfg.SizeTier, nil
}

func resolveOpenAIResponsesImageBillingConfigDetailedFromBody(body []byte, fallbackModel string) (OpenAIResponsesImageBillingConfig, error) {
	imageModel := ""
	imageSize := ""
	hasImageTool := false
	if len(body) > 0 && gjson.ValidBytes(body) {
		tools := gjson.GetBytes(body, "tools")
		if tools.IsArray() {
			tools.ForEach(func(_, item gjson.Result) bool {
				if openAIJSONString(item.Get("type")) != "image_generation" {
					return true
				}
				hasImageTool = true
				imageModel = openAIJSONString(item.Get("model"))
				imageSize = openAIJSONString(item.Get("size"))
				return false
			})
		}
		if imageSize == "" {
			imageSize = openAIJSONString(gjson.GetBytes(body, "size"))
		}
		if imageModel == "" {
			bodyModel := openAIJSONString(gjson.GetBytes(body, "model"))
			if isOpenAIImageBillingModelAlias(bodyModel) || !hasImageTool {
				imageModel = bodyModel
			}
		}
	}
	if imageModel == "" && hasImageTool {
		imageModel = "gpt-image-2"
	}
	if imageModel == "" {
		imageModel = strings.TrimSpace(fallbackModel)
	}
	return OpenAIResponsesImageBillingConfig{
		Model:     imageModel,
		SizeTier:  normalizeOpenAIImageSizeTier(imageSize),
		InputSize: imageSize,
	}, nil
}

func isOpenAIImageBillingModelAlias(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	return isOpenAIImageGenerationModel(normalized) || strings.Contains(normalized, "image")
}

func openAIJSONString(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	return strings.TrimSpace(value.String())
}
