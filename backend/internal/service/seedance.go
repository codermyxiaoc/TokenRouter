package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	SeedanceEndpointCreate           GrokMediaEndpoint        = "seedance_create"
	SeedanceEndpointStatus           GrokMediaEndpoint        = "seedance_status"
	SeedanceEndpointDelete           GrokMediaEndpoint        = "seedance_delete"
	OpenAIEndpointCapabilitySeedance OpenAIEndpointCapability = "seedance"
)

func (e GrokMediaEndpoint) IsSeedance() bool {
	return e == SeedanceEndpointCreate || e == SeedanceEndpointStatus || e == SeedanceEndpointDelete
}

// SeedanceTaskKey 隔离不同供应商的同名任务，避免归属和计费键碰撞。
func SeedanceTaskKey(id string) string { return "seedance:" + strings.TrimSpace(id) }

// SeedanceBillingSnapshot 冻结任务创建时的结算选择和渠道模型链，不存储上游凭据。
type SeedanceBillingSnapshot struct {
	OriginalModel           string               `json:"original_model,omitempty"`
	BillingMode             string               `json:"billing_mode"`
	PreferredSubscriptionID *int64               `json:"preferred_subscription_id,omitempty"`
	Subscription            *UserSubscription    `json:"subscription,omitempty"`
	ChannelMapping          ChannelMappingResult `json:"channel_mapping"`
	Group                   *Group               `json:"group,omitempty"`
}

const seedanceDeferredResponseKey = "seedance_deferred_response"

type seedanceDeferredResponse struct {
	status int
	header http.Header
	body   []byte
}

// DeferSeedanceResponse 让创建流程在提交成功响应前保存任务归属和结算快照。
func DeferSeedanceResponse(c *gin.Context) { c.Set(seedanceDeferredResponseKey, true) }

// CommitSeedanceResponse 仅写回已缓存的方舟原生响应，不重建任务或修改 JSON。
func (s *OpenAIGatewayService) CommitSeedanceResponse(c *gin.Context) {
	value, _ := c.Get(seedanceDeferredResponseKey)
	if response, ok := value.(*seedanceDeferredResponse); ok {
		writeGrokMediaResponse(c, &http.Response{StatusCode: response.status, Header: response.header}, response.body, s.responseHeaderFilter)
	}
}

// CheckSeedanceTaskStorage 在创建付费任务前确认任务缓存可访问，避免已知故障下继续创建。
func (s *OpenAIGatewayService) CheckSeedanceTaskStorage(ctx context.Context) error {
	if s == nil || s.cache == nil {
		return fmt.Errorf("seedance task storage unavailable")
	}
	cache, ok := s.cache.(GrokVideoBillingCache)
	if !ok {
		return fmt.Errorf("seedance task storage unavailable")
	}
	_, err := cache.GetGrokVideoPendingBilling(ctx, "seedance-storage-check")
	return err
}

func ParseSeedanceRequest(body []byte) (GrokMediaRequestInfo, error) {
	var info GrokMediaRequestInfo
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return info, fmt.Errorf("request body must be a JSON object")
	}
	model := gjson.GetBytes(body, "model")
	if model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		return info, fmt.Errorf("model is required")
	}
	content := gjson.GetBytes(body, "content")
	if !content.IsArray() || len(content.Array()) == 0 {
		return info, fmt.Errorf("content must be a non-empty array")
	}
	info.Model = strings.TrimSpace(model.String())
	var texts []string
	for _, item := range content.Array() {
		switch item.Get("type").String() {
		case "text":
			texts = append(texts, item.Get("text").String())
		case "image_url":
			info.InputImageURLs = append(info.InputImageURLs, item.Get("image_url.url").String())
		}
	}
	info.Prompt = strings.Join(texts, "\n")
	return info, nil
}

func buildSeedanceURL(base string, endpoint GrokMediaEndpoint, taskID string) (string, error) {
	base = strings.TrimRight(base, "/")
	// 支持站点根地址、反向代理前缀和完整的方舟 API 基地址。
	if !strings.HasSuffix(base, "/api/v3") && !strings.HasSuffix(base, "/v3") {
		base += "/api/v3"
	}
	base += "/contents/generations/tasks"
	if endpoint != SeedanceEndpointCreate {
		if err := validateUpstreamPathSegment("Seedance task ID", taskID); err != nil || strings.TrimSpace(taskID) == "" {
			return "", fmt.Errorf("invalid Seedance task ID")
		}
		base += "/" + taskID
	}
	return base, nil
}

// ForwardSeedance 保留方舟多模态和扩展字段，仅按账号映射改写 model。
func (s *OpenAIGatewayService) ForwardSeedance(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, taskID string, body []byte) (*OpenAIForwardResult, error) {
	if !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance) || !endpoint.IsSeedance() {
		return nil, fmt.Errorf("seedance requires an OpenAI API key account with a custom base URL")
	}
	base, err := s.validateUpstreamBaseURL(account.GetCredential("base_url"))
	if err != nil {
		return nil, err
	}
	target, err := buildSeedanceURL(base, endpoint, strings.TrimPrefix(taskID, "seedance:"))
	if err != nil {
		return nil, err
	}
	model, upstreamModel := "", ""
	method := http.MethodGet
	switch endpoint {
	case SeedanceEndpointCreate:
		info, parseErr := ParseSeedanceRequest(body)
		if parseErr != nil {
			return nil, parseErr
		}
		model = info.Model
		upstreamModel = account.GetMappedModel(model)
		body, err = sjson.SetBytes(body, "model", upstreamModel)
		if err != nil {
			return nil, err
		}
		method = http.MethodPost
	case SeedanceEndpointDelete:
		method = http.MethodDelete
	}
	token := strings.TrimSpace(account.GetCredential("api_key"))
	if token == "" {
		return nil, fmt.Errorf("seedance account missing api_key")
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	// 异步创建可能已经受理付费任务，任何含糊错误均不得自动重复创建。
	// 错误保持原生状态和正文，同时写入现有运维观测链路。
	if resp.StatusCode >= 300 {
		message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(responseBody)))
		if message == "" {
			message = fmt.Sprintf("Seedance upstream returned status %d", resp.StatusCode)
		}
		setOpsUpstreamError(c, resp.StatusCode, message, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID, AccountName: account.Name, UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("X-Request-Id"), Kind: "http_error", Message: message})
		MarkResponseCommitted(c)
		writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter)
		return nil, fmt.Errorf("seedance upstream status %d", resp.StatusCode)
	}
	result := &OpenAIForwardResult{Model: model, BillingModel: model, UpstreamModel: upstreamModel, Duration: time.Since(started), ResponseHeaders: resp.Header.Clone(), UpstreamHeaders: resp.Header.Clone(), UpstreamEndpoint: "/api/v3/contents/generations/tasks"}
	result.MediaTaskObservation = videoTaskObservation(endpoint, responseBody, resp.StatusCode)
	if endpoint == SeedanceEndpointCreate {
		idValue := gjson.GetBytes(responseBody, "id")
		id := strings.TrimSpace(idValue.String())
		if idValue.Type != gjson.String || id == "" || validateUpstreamPathSegment("Seedance task ID", id) != nil {
			return nil, fmt.Errorf("seedance create response missing task ID")
		}
		result.ResponseID = SeedanceTaskKey(id)
	}
	if endpoint == SeedanceEndpointStatus {
		result.ResponseID = taskID
		result.UpstreamModel = gjson.GetBytes(responseBody, "model").String()
		if gjson.GetBytes(responseBody, "status").String() == "succeeded" {
			result.Usage.OutputTokens = max(0, int(gjson.GetBytes(responseBody, "usage.completion_tokens").Int()))
		}
	}
	if endpoint == SeedanceEndpointCreate && c.GetBool(seedanceDeferredResponseKey) {
		c.Set(seedanceDeferredResponseKey, &seedanceDeferredResponse{status: resp.StatusCode, header: resp.Header.Clone(), body: responseBody})
		return result, nil
	}
	writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter)
	return result, nil
}
