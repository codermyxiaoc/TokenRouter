package middleware

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const smartRoutingResolverContextKey = "smart_routing_resolver"

// abortSmartRoutingKeyError 区分调用参数错误与临时服务不可用，保留协议原生错误结构。
func abortSmartRoutingKeyError(c *gin.Context, err error) {
	status := infraerrors.Code(err)
	kind := "invalid_request_error"
	if status >= http.StatusInternalServerError {
		kind = "api_error"
	} else if status == http.StatusForbidden {
		kind = "permission_error"
	}
	if isOpenAICompositeEndpoint(c.Request.URL.Path) || strings.HasSuffix(c.Request.URL.Path, "/videos") {
		if status >= http.StatusInternalServerError {
			kind = "server_error"
		}
		c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": kind, "code": infraerrors.Reason(err), "message": infraerrors.Message(err), "param": "model"}})
		return
	}
	c.AbortWithStatusJSON(status, gin.H{"type": "error", "error": gin.H{"type": kind, "code": infraerrors.Reason(err), "message": infraerrors.Message(err)}})
}

// WithSmartRoutingResolver 为智能 Key 提供候选检查和失败换组，普通 Key 保留原认证链。
func WithSmartRoutingResolver(resolver service.SmartRoutingGroupResolver, next gin.HandlerFunc, guards ...SmartRoutingRetryGuard) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(smartRoutingResolverContextKey, resolver)
		runSmartRoutingAuthentication(c, resolver, next, guards)
	}
}

// resolveSmartRoutingAPIKeyRequest 每轮在协议分派和计费前选组，跳过已尝试及冷却中的候选。
// @project-doc docs/domains/smart_routing_api_keys.md#request_selection
func resolveSmartRoutingAPIKeyRequest(c *gin.Context, apiKeyService *service.APIKeyService, apiKey *service.APIKey) (*service.APIKey, error) {
	if apiKey == nil || !apiKey.SmartRouting {
		return apiKey, nil
	}
	if isCompositeKeyUnsupportedEndpoint(c.Request.Method, c.Request.URL.Path, c.FullPath()) {
		return nil, infraerrors.BadRequest("SMART_ROUTING_ENDPOINT_UNSUPPORTED", "Smart routing does not support this endpoint")
	}
	if isCompositeKeyNoModelEndpoint(c.Request.Method, c.Request.URL.Path) {
		// 身份查询与模型聚合不需要默认组，也不能进入未分组账号池。
		c.Set(compositeKeyNoGroupContextKey, true)
		return apiKey, nil
	}
	if !smartRoutingModelEndpoint(c) {
		return nil, infraerrors.BadRequest("SMART_ROUTING_ENDPOINT_UNSUPPORTED", "Smart routing requires a supported model request")
	}
	model, err := smartRoutingRequestModel(c)
	if err != nil {
		return nil, err
	}
	// 这里只计算一跳目标用于目录匹配；原始请求留给统一重定向流程改写并恢复响应别名。
	if target, matched := apiKey.ResolveModelMapping(model); matched {
		model = target
	}
	execution := prepareSmartRoutingExecution(c, apiKey)
	requestCtx := c.Request.Context()
	if strings.Contains(c.Request.URL.Path, "/responses") {
		body, bodyErr := readAndRestoreRequestBody(c.Request)
		if bodyErr != nil {
			return nil, smartRoutingBodyError(bodyErr, infraerrors.BadRequest("SMART_ROUTING_INVALID_REQUEST", "Invalid request body"))
		}
		// 生图工具的硬资格不能只从顶层文本模型推断，预选组也须检查图片限流与能力。
		if service.IsExplicitImageGenerationIntent(c.Request.URL.Path, model, body) {
			requestCtx = service.WithOpenAIImageGenerationIntent(requestCtx)
		}
	}
	value, _ := c.Get(smartRoutingResolverContextKey)
	resolver, ok := value.(service.SmartRoutingGroupResolver)
	if !ok || resolver == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "SMART_ROUTING_UNAVAILABLE", "Smart routing is temporarily unavailable")
	}
	forcedPlatform, _ := GetForcePlatformFromContext(c)
	hasModel := false
	matchingGroups, coolingGroups := 0, 0
	var retryAfter time.Duration
	for index := range apiKey.CompositeGroups {
		binding := &apiKey.CompositeGroups[index]
		group := binding.Group
		if group == nil || !group.IsActive() || apiKey.User == nil || !apiKey.User.CanBindGroup(group.ID, group.IsExclusive) {
			continue
		}
		if forcedPlatform != "" && group.Platform != forcedPlatform {
			continue
		}
		if isSeedanceCreateRequest(c.Request.Method, c.Request.URL.Path) && group.Platform != service.PlatformOpenAI {
			continue
		}
		if execution != nil && execution.visited[group.ID] {
			continue
		}
		availability, checkErr := resolver.EvaluateSmartRoutingGroup(requestCtx, group, model, c.Request.URL.Path)
		if checkErr != nil {
			// 基础设施故障不能冒充无账号，否则会意外切换计费分组。
			return nil, infraerrors.New(http.StatusServiceUnavailable, "SMART_ROUTING_UNAVAILABLE", "Unable to check routing candidates")
		}
		if !availability.HasModel {
			continue
		}
		hasModel = true
		matchingGroups++
		if execution != nil && execution.cooldown > 0 {
			if cache, ok := resolver.(smartRoutingCooldownResolver); ok {
				ttl, cacheErr := cache.GetSmartRoutingCooldown(requestCtx, apiKey.ID, group.ID)
				if cacheErr != nil {
					slog.Warn("smart routing cooldown read failed", "key_id", apiKey.ID, "group_id", group.ID, "error", cacheErr)
				} else if ttl > 0 {
					// 远端续接与压缩上下文不能因候选冷却而被发送到另一供应商。
					if execution.continuationRequiresSameCandidate() {
						c.Set("ops_routing_capacity_limited", true)
						c.Header("Retry-After", strconv.Itoa(int(math.Ceil(ttl.Seconds()))))
						return nil, infraerrors.New(http.StatusServiceUnavailable, "SMART_ROUTING_CONTINUATION_GROUP_COOLING", "Service temporarily unavailable: the continuation routing candidate is cooling down")
					}
					coolingGroups++
					if retryAfter == 0 || ttl < retryAfter {
						retryAfter = ttl
					}
					continue
				}
			}
		}
		if !availability.Schedulable {
			continue
		}
		selected, selectErr := apiKeyService.SelectCompositeGroupForRequest(c.Request.Context(), apiKey, binding)
		if selectErr != nil {
			return nil, selectErr
		}
		c.Request = c.Request.WithContext(service.WithSmartRoutingScope(requestCtx))
		if execution != nil {
			execution.groupID = group.ID
		}
		return selected, nil
	}
	if execution != nil {
		execution.selectionExhausted = true
	}
	if hasModel {
		c.Set("ops_routing_capacity_limited", true)
		if matchingGroups == coolingGroups {
			c.Header("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
			return nil, infraerrors.New(http.StatusServiceUnavailable, "SMART_ROUTING_ALL_GROUPS_COOLING", "Service temporarily unavailable: routing candidates are cooling down")
		}
		return nil, infraerrors.New(http.StatusServiceUnavailable, "SMART_ROUTING_NO_AVAILABLE_ACCOUNTS", "No schedulable accounts in the routing candidates")
	}
	return nil, infraerrors.New(http.StatusNotFound, "SMART_ROUTING_MODEL_NOT_FOUND", "The requested model is not available in the routing candidates")
}

// smartRoutingRequestModel 严格要求非空字符串模型；探针和缺省模型不能借用首组。
func smartRoutingRequestModel(c *gin.Context) (string, error) {
	missing := infraerrors.BadRequest("SMART_ROUTING_MODEL_REQUIRED", "Smart routing requires a non-empty model")
	invalid := infraerrors.BadRequest("SMART_ROUTING_INVALID_REQUEST", "Smart routing requires exactly one model field")
	if isGeminiNativeModelEndpoint(c.Request.URL.Path) {
		model, err := compositeGeminiModelFromParams(c)
		if err != nil || strings.TrimSpace(model) == "" {
			return "", missing
		}
		return model, nil
	}
	mediaType, params, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if strings.HasPrefix(mediaType, "multipart/") {
		body, err := readAndRestoreRequestBody(c.Request)
		if err != nil {
			return "", smartRoutingBodyError(err, missing)
		}
		if mediaType != "multipart/form-data" || params["boundary"] == "" {
			return "", invalid
		}
		// 媒体处理器使用最后一个表单模型，选组不能只读取第一个字段。
		// 完整遍历并拒绝重复、文件和非规范名称，保证后续解析得到同一个模型。
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		model, found := "", false
		for {
			part, partErr := reader.NextPart()
			if errors.Is(partErr, io.EOF) {
				break
			}
			if partErr != nil {
				return "", invalid
			}
			name := strings.TrimSpace(part.FormName())
			if !strings.EqualFold(name, "model") {
				_ = part.Close()
				continue
			}
			if found || part.FileName() != "" || part.FormName() != "model" {
				_ = part.Close()
				return "", invalid
			}
			value, readErr := io.ReadAll(part)
			_ = part.Close()
			if readErr != nil {
				return "", smartRoutingBodyError(readErr, missing)
			}
			found = true
			model = strings.TrimSpace(string(value))
		}
		if !found || model == "" {
			return "", missing
		}
		return model, nil
	}
	body, err := readAndRestoreRequestBody(c.Request)
	if err != nil {
		return "", smartRoutingBodyError(err, missing)
	}
	if !gjson.ValidBytes(body) {
		return "", infraerrors.BadRequest("SMART_ROUTING_INVALID_REQUEST", "Invalid JSON request")
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return "", invalid
	}
	// gjson 取首个同名键，标准 JSON 绑定可能取末个且不区分大小写，因此拒绝歧义输入。
	var model gjson.Result
	modelFields := 0
	canonicalName := true
	root.ForEach(func(key, value gjson.Result) bool {
		if strings.EqualFold(key.String(), "model") {
			modelFields++
			canonicalName = canonicalName && key.String() == "model"
			model = value
		}
		return modelFields <= 1
	})
	if modelFields > 1 || !canonicalName {
		return "", invalid
	}
	if model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		return "", missing
	}
	return strings.TrimSpace(model.String()), nil
}

// smartRoutingBodyError 保留请求体超限语义，不把传输错误当成目录未命中。
func smartRoutingBodyError(err, missing error) error {
	var oversized *http.MaxBytesError
	if errors.As(err, &oversized) {
		return infraerrors.New(http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body is too large")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return infraerrors.BadRequest("SMART_ROUTING_REQUEST_CANCELED", "Request was canceled")
	}
	return missing
}

// smartRoutingModelEndpoint 显式列出能在认证阶段确定模型的入口。
func smartRoutingModelEndpoint(c *gin.Context) bool {
	path := strings.TrimSuffix(c.Request.URL.Path, "/")
	if isSeedanceCreateRequest(c.Request.Method, path) {
		return true
	}
	if isGeminiNativeModelEndpoint(path) {
		if c.Request.Method == http.MethodGet {
			return true
		}
		return c.Request.Method == http.MethodPost && (strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent") || strings.HasSuffix(path, ":countTokens"))
	}
	if c.Request.Method != http.MethodPost {
		return false
	}
	for _, prefix := range []string{"/antigravity", "/backend-api/codex", "/v1"} {
		path = strings.TrimPrefix(path, prefix)
	}
	switch path {
	case "/messages", "/messages/count_tokens", "/chat/completions", "/responses", "/responses/compact", "/responses/input_tokens",
		"/embeddings", "/images/generations", "/images/edits", "/images/generations/async", "/images/edits/async", "/images/batches", "/videos", "/videos/generations", "/videos/edits", "/videos/extensions", "/alpha/search":
		return true
	default:
		return false
	}
}
