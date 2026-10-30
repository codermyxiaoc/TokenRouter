package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/pkg/gemini"
	"github.com/TokenFlux/TokenRouter/internal/pkg/geminicli"
	"github.com/TokenFlux/TokenRouter/internal/pkg/googleapi"
	"github.com/TokenFlux/TokenRouter/internal/util/responseheaders"
)

// Gemini 分组对外提供 AI Studio 兼容协议（GET /v1beta/models、/v1beta/models/{model}），
// 分组内的账号可以是 AI Studio 兼容上游（API Key / OAuth），也可以是 Agent Platform (Vertex)。
// 本文件把不同账号类型的模型接口统一成 AI Studio 格式的响应。

// errGeminiModelsUpstreamStatus 在 Vertex 分页读取遇到非 2xx 时中止分页，原始响应由调用方另行保存。
var errGeminiModelsUpstreamStatus = errors.New("upstream model list returned non-2xx status")

// AllGeminiAccountsDeclareModels 判断分组内每个可调度 Gemini 账号是否都有生效的 model_mapping（含 Google One 默认映射）。
// 全部声明时模型接口应与 /v1/models 同源应答，不透传某个账号的上游列表；有账号未配置映射（可接受任意模型）
// 或没有 Gemini 账号时返回 false，保持选号透传。同步上游 Sub2API de44e3a0b 的触发条件。
func (s *GeminiMessagesCompatService) AllGeminiAccountsDeclareModels(ctx context.Context, groupID *int64) (bool, error) {
	accounts, err := s.listSchedulableAccountsOnce(ctx, groupID, PlatformGemini, true)
	if err != nil {
		return false, fmt.Errorf("query accounts failed: %w", err)
	}
	seen := false
	for i := range accounts {
		if accounts[i].Platform != PlatformGemini {
			continue
		}
		if len(accounts[i].GetModelMapping()) == 0 {
			return false, nil
		}
		seen = true
	}
	return seen, nil
}

// SelectAccountForModelsEndpoints 为模型列表与模型详情接口选择账号。
//
// 优先级：
//  1. 配置了 api_key 的 API Key 账号
//  2. 不带 project_id 的 OAuth 账号（AI Studio OAuth）
//  3. 显式标记为 ai_studio 的 OAuth 账号
//  4. Code Assist OAuth 账号（令牌常缺 AI Studio 权限范围，多半退回内置列表）
//  5. 其余类型的账号
//  6. Vertex 服务账号（服务账号 JSON 认证，读取 Google 发布方模型列表）
//  7. Vertex API Key 账号（列表接口不接受 API Key，只能返回内置列表）
//
// Vertex 账号排在所有 AI Studio 兼容账号之后，混合分组始终由 AI Studio 兼容账号应答。
// 同级账号按 priority、最近使用时间挑选。
func (s *GeminiMessagesCompatService) SelectAccountForModelsEndpoints(ctx context.Context, groupID *int64) (*Account, error) {
	if group, ok := s.resolveAdvancedSchedulerGroup(ctx, groupID); ok {
		ctx = context.WithValue(ctx, ctxkey.Group, group)
	}
	accounts, err := s.listSchedulableAccountsOnce(ctx, groupID, PlatformGemini, true)
	if err != nil {
		return nil, fmt.Errorf("query accounts failed: %w", err)
	}
	if len(accounts) == 0 {
		return nil, errors.New("no available Gemini accounts")
	}

	rank := modelsEndpointAccountRank

	var selected *Account
	for i := range accounts {
		acc := &accounts[i]
		if selected == nil {
			selected = acc
			continue
		}

		r1, r2 := rank(acc), rank(selected)
		if r1 < r2 {
			selected = acc
			continue
		}
		if r1 > r2 {
			continue
		}

		if acc.Priority < selected.Priority {
			selected = acc
		} else if acc.Priority == selected.Priority {
			switch {
			case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
				selected = acc
			case acc.LastUsedAt != nil && selected.LastUsedAt == nil:
				// 保留当前候选
			case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
				if acc.Type == AccountTypeOAuth && selected.Type != AccountTypeOAuth {
					selected = acc
				}
			default:
				if acc.LastUsedAt.Before(*selected.LastUsedAt) {
					selected = acc
				}
			}
		}
	}

	if selected == nil {
		return nil, errors.New("no available Gemini accounts")
	}

	// AI Studio 端点的账号类型分级是能力约束，不能被高级调度分数跨级覆盖；
	// 仅在同一能力等级中使用通用高级评分。
	if s.groupUsesAdvancedScheduler(ctx, groupID, false) {
		bestRank := rank(selected)
		if bestRank < 999 {
			eligible := make([]*Account, 0, len(accounts))
			for i := range accounts {
				account := &accounts[i]
				if rank(account) == bestRank {
					eligible = append(eligible, account)
				}
			}
			if advanced := s.selectAdvancedGeminiAccount(
				ctx,
				groupID,
				"",
				"",
				eligible,
				s.advancedSchedulerEffectiveSettingsForRequest(ctx, groupID),
			); advanced != nil {
				selected = advanced
			}
		}
	}
	return s.hydrateSelectedAccount(ctx, selected)
}

// modelsEndpointAccountRank 返回账号在模型接口选号中的优先级，数值越小越优先，顺序见 SelectAccountForModelsEndpoints。
func modelsEndpointAccountRank(a *Account) int {
	if a == nil {
		return 999
	}
	switch a.Type {
	case AccountTypeAPIKey:
		if strings.TrimSpace(a.GetCredential("api_key")) != "" {
			return 0
		}
		return 9
	case AccountTypeOAuth:
		if strings.TrimSpace(a.GetCredential("project_id")) == "" {
			return 1
		}
		if strings.TrimSpace(a.GetCredential("oauth_type")) == "ai_studio" {
			return 2
		}
		return 3
	case AccountTypeServiceAccount:
		if a.IsVertexAPIKey() {
			return 12
		}
		return 11
	default:
		return 10
	}
}

// ForwardListModels 读取账号上游的模型列表，返回 AI Studio 格式的响应（GET /v1beta/models）。
// AI Studio 兼容账号原样返回上游响应；Vertex 服务账号读取 Google 发布方模型列表后转换格式，
// 上游非 2xx 时原样返回该响应；Vertex API Key 账号返回内置列表。
func (s *GeminiMessagesCompatService) ForwardListModels(ctx context.Context, account *Account) (*UpstreamHTTPResult, error) {
	if account == nil {
		return nil, errors.New("account is nil")
	}
	if !account.IsVertexServiceAccount() {
		return s.forwardGeminiModelsGET(ctx, account, "/v1beta/models")
	}
	if account.IsVertexAPIKey() {
		return geminiModelsJSONResult(http.StatusOK, gemini.FallbackModelsList())
	}

	modelIDs, failure, err := s.listVertexGeminiModels(ctx, account, account.VertexLocation(""))
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return failure, nil
	}
	models := make([]gemini.Model, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		models = append(models, gemini.FallbackModel(modelID))
	}
	return geminiModelsJSONResult(http.StatusOK, gemini.ModelsListResponse{Models: models})
}

// ForwardGetModel 读取账号上游的单个模型信息，返回 AI Studio 格式的响应（GET /v1beta/models/{model}）。
// Vertex 服务账号在该模型生成请求所用区域的模型列表中查找，找不到时返回 404；Vertex API Key 账号返回内置信息。
func (s *GeminiMessagesCompatService) ForwardGetModel(ctx context.Context, account *Account, model string) (*UpstreamHTTPResult, error) {
	if account == nil {
		return nil, errors.New("account is nil")
	}
	if !account.IsVertexServiceAccount() {
		return s.forwardGeminiModelsGET(ctx, account, "/v1beta/models/"+model)
	}
	if account.IsVertexAPIKey() {
		return geminiModelsJSONResult(http.StatusOK, gemini.FallbackModel(model))
	}

	modelIDs, failure, err := s.listVertexGeminiModels(ctx, account, account.VertexLocation(model))
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return failure, nil
	}
	if slices.Contains(modelIDs, model) {
		return geminiModelsJSONResult(http.StatusOK, gemini.FallbackModel(model))
	}
	return geminiModelsJSONResult(http.StatusNotFound, map[string]any{
		"error": map[string]any{
			"code":    http.StatusNotFound,
			"message": fmt.Sprintf("models/%s is not found", model),
			"status":  googleapi.HTTPStatusToGoogleStatus(http.StatusNotFound),
		},
	})
}

// forwardGeminiModelsGET 向 AI Studio 兼容上游转发模型接口的 GET 请求。
// path 会被直接拼到上游 base URL 后面，因此按路径护栏逐片段校验，见 upstream_path_guard.go。
func (s *GeminiMessagesCompatService) forwardGeminiModelsGET(ctx context.Context, account *Account, path string) (*UpstreamHTTPResult, error) {
	sanitizedPath, ok := sanitizedUpstreamPathSuffix(path)
	if !ok || sanitizedPath == "" {
		return nil, errors.New("invalid path")
	}

	baseURL := account.GetGeminiBaseURL(geminicli.AIStudioBaseURL)
	normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	fullURL := strings.TrimRight(normalizedBaseURL, "/") + sanitizedPath

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}

	switch account.Type {
	case AccountTypeAPIKey:
		apiKey := strings.TrimSpace(account.GetCredential("api_key"))
		if apiKey == "" {
			return nil, errors.New("gemini api_key not configured")
		}
		req.Header.Set("x-goog-api-key", apiKey)
	case AccountTypeOAuth:
		if s.tokenProvider == nil {
			return nil, errors.New("gemini token provider not configured")
		}
		accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
	default:
		return nil, fmt.Errorf("unsupported account type: %s", account.Type)
	}

	return s.doGeminiModelsGET(req, account)
}

// listVertexGeminiModels 读取 Vertex 服务账号在指定区域可用的 Gemini 模型。
// 上游返回非 2xx 时 failure 为该页的原始响应，modelIDs 与 err 均为空。
func (s *GeminiMessagesCompatService) listVertexGeminiModels(ctx context.Context, account *Account, location string) (modelIDs []string, failure *UpstreamHTTPResult, err error) {
	if s.tokenProvider == nil {
		return nil, nil, errors.New("gemini token provider not configured")
	}
	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, nil, err
	}

	modelIDs, err = listVertexGeminiPublisherModels(location, func(_ int, listURL string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)

		res, err := s.doGeminiModelsGET(req, account)
		if err != nil {
			return nil, err
		}
		if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
			failure = res
			return nil, errGeminiModelsUpstreamStatus
		}
		return res.Body, nil
	})
	if failure != nil {
		return nil, failure, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return modelIDs, nil, nil
}

// doGeminiModelsGET 发送模型接口请求并读取响应。响应体上限取 gateway.models_list_read_max_bytes，超出时报错；响应头按配置过滤。
func (s *GeminiMessagesCompatService) doGeminiModelsGET(req *http.Request, account *Account) (*UpstreamHTTPResult, error) {
	var proxyURL string
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	bodyLimit := resolveModelsListReadLimit(s.cfg)
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if readErr != nil {
		return nil, fmt.Errorf("read upstream model list: %w", readErr)
	}
	if int64(len(body)) > bodyLimit {
		return nil, fmt.Errorf("upstream model list response exceeds %d bytes", bodyLimit)
	}
	wwwAuthenticate := resp.Header.Get("Www-Authenticate")
	filteredHeaders := responseheaders.FilterHeaders(resp.Header, s.responseHeaderFilter)
	if wwwAuthenticate != "" {
		filteredHeaders.Set("Www-Authenticate", wwwAuthenticate)
	}
	return &UpstreamHTTPResult{
		StatusCode: resp.StatusCode,
		Headers:    filteredHeaders,
		Body:       body,
	}, nil
}

// geminiModelsJSONResult 把网关生成的模型接口响应包装成 UpstreamHTTPResult，交给 handler 统一写出。
func geminiModelsJSONResult(status int, payload any) (*UpstreamHTTPResult, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &UpstreamHTTPResult{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}, nil
}
