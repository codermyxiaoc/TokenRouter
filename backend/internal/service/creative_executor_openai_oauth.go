package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// 创作台保持独立的预占/结算生命周期，OAuth 只复用网关的图片协议、认证和 TLS 构建。
// 不调用网关 Handler，不重复调度、扣费或写入包含素材的运维请求体。
func (e *CreativeExecutor) executeOpenAIOAuth(ctx context.Context, run CreativeRun, account *Account, model, endpoint, contentType string, body []byte) ([]CreativeOutput, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, creativeNonRetryableError("invalid creative image request")
	}
	request.Header.Set("Content-Type", contentType)
	c := &gin.Context{Request: request}
	// 沿用任务所属 Key 的上游会话隔离，禁止不同用户共享匿名设备身份。
	c.Set("api_key", &APIKey{ID: run.APIKeyID})
	parsed, err := e.gateway.ParseOpenAIImagesRequest(c, body)
	if err != nil {
		return nil, creativeNonRetryableError("invalid creative image payload")
	}
	token, _, err := e.gateway.GetAccessToken(ctx, account)
	if err != nil {
		return nil, creativeHTTPStatusError(0, err.Error())
	}
	tlsMatch := e.gateway.MatchOpenAITLSFingerprintRouterForRequest(c, account)
	direct := usesCodexDirectImages(model)
	for attempt := 0; attempt < 2; attempt++ {
		var payload []byte
		var target string
		if direct {
			payload, target, err = buildOpenAIImagesOAuthPayload(parsed, model)
		} else {
			payload, err = buildOpenAIImagesResponsesRequest(parsed, model)
			target = chatgptCodexURL
		}
		if err != nil {
			return nil, creativeNonRetryableError("invalid creative image payload")
		}
		upstream, err := e.gateway.buildUpstreamRequest(withOpenAIImagesSelfBuiltRequest(ctx), c, account, payload, token, true, fmt.Sprintf("creative:%d", run.ID), false, tlsMatch)
		if err != nil {
			return nil, creativeNonRetryableError("failed to prepare creative image request")
		}
		upstream.URL, err = url.Parse(target)
		if err != nil {
			return nil, creativeNonRetryableError("invalid creative image endpoint")
		}
		upstream.Header.Set("Content-Type", "application/json")
		upstream.Header.Set("Accept", "text/event-stream")
		if direct {
			upstream.Header.Del("OpenAI-Beta")
			upstream.Header.Set("Accept", "application/json")
		} else {
			upstream.Header.Set("OpenAI-Beta", "responses=experimental")
		}
		resp, err := e.gateway.httpUpstream.DoWithTLS(upstream, accountProxyURL(account), account.ID, account.Concurrency, e.gateway.resolveOpenAITLSProfile(account, tlsMatch))
		if err != nil {
			// 已提交生成后，网络错误无法证明上游没有完成，禁止 worker 再次提交。
			return nil, creativeNonRetryableError("creative image generation result is unknown after a connection failure")
		}
		responseBody, readErr := readCreativeUpstreamBody(resp.Body, 64<<20)
		_ = resp.Body.Close()
		// Responses 是 SSE：读取中断前可能已有完整图片，先保留这些已读事件供下方恢复。
		// 原生 JSON 无法从截断正文确认完整产出，HTTP 拒绝也不按成功图片处理。
		if readErr != nil && (direct || resp.StatusCode >= 400) {
			return nil, creativeNonRetryableError("creative image generation response was interrupted")
		}
		if direct && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed) {
			direct = false
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, creativeHTTPStatusError(resp.StatusCode, "image upstream rejected request")
		}
		var images []openAIResponsesImageResult
		var upstreamErr *OpenAIImagesUpstreamError
		if direct {
			images, err = parseCodexDirectImagesResponse(responseBody)
		} else {
			images, _, _, _, _, err = collectOpenAIImagesFromResponsesBody(responseBody)
			upstreamErr = extractOpenAIImagesUpstreamError(responseBody)
		}
		// 完整图片先于后续终态错误：交回既有单张输出/结算链路，禁止 worker 再生成一遍。
		for _, item := range images {
			decoded, decodeErr := decodeBase64Image(item.Result)
			if decodeErr == nil && len(decoded.Bytes) > 0 {
				if upstreamErr != nil || readErr != nil {
					status, errorClass := http.StatusBadGateway, "upstream_stream_interrupted"
					if upstreamErr != nil {
						status, errorClass = upstreamErr.StatusCode, "upstream_terminal_error"
					}
					// 恢复成功仍记录后台诊断，只记固定分类和任务身份，禁止保留提示词/图片/上游原正文。
					logger.L().Warn("creative.openai_oauth_error_recovered",
						zap.String("run_id", run.RunID), zap.Int64("account_id", account.ID),
						zap.String("upstream_endpoint", upstream.URL.Path), zap.Int("status_code", status),
						zap.String("error_class", errorClass), zap.Bool("stream_interrupted", readErr != nil),
						zap.Int("image_count", 1))
				}
				return []CreativeOutput{{Index: 0, Bytes: decoded.Bytes, Mime: decoded.Mime}}, nil
			}
		}
		if readErr != nil {
			return nil, creativeNonRetryableError("creative image generation response was interrupted")
		}
		if len(images) == 0 && upstreamErr != nil {
			return nil, creativeHTTPStatusError(upstreamErr.StatusCode, "image upstream failed")
		}
		if err != nil || len(images) == 0 {
			return nil, creativeNonRetryableError("creative image generation returned no complete image")
		}
		return nil, creativeNonRetryableError("creative image generation returned no decodable image")
	}
	return nil, creativeNonRetryableError("creative image endpoint unavailable")
}
