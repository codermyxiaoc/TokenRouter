package service

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// 只有完整读取的拒绝响应可证明本次未受理；截断或超限错误不能授权换号。
func readImageAttemptErrorBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("image upstream error response is missing")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, gatewayUpstreamErrorBodyReadLimit+1))
	if err != nil {
		return body, err
	}
	if int64(len(body)) > gatewayUpstreamErrorBodyReadLimit {
		return body, errors.New("image upstream error response exceeds limit")
	}
	return body, nil
}

// finalizeImageAttemptForward 为所有图片入口补齐安全结果，不能依赖是否存在资金预占。
func finalizeImageAttemptForward(ctx context.Context, result *OpenAIForwardResult, err error) (*OpenAIForwardResult, error) {
	if !ImageAttemptHasPolicy(ctx) {
		return result, err
	}
	if result != nil {
		if result.ImageCount > 0 {
			RecordImageAttemptDecision(ctx, ImageAttemptDecision{Outcome: ImageAttemptResultReady, Reason: "image_output_observed"})
		} else if result.Usage.InputTokens > 0 || result.Usage.OutputTokens > 0 || result.Usage.CacheReadInputTokens > 0 || result.Usage.CacheCreationInputTokens > 0 {
			RecordImageAttemptDecision(ctx, ImageAttemptDecision{Outcome: ImageAttemptAccepted, Reason: "image_usage_observed"})
		}
	}
	if err == nil {
		return result, nil
	}
	if decision, ok := ImageAttemptDecisionForError(err); ok {
		return result, RecordImageAttemptFailure(ctx, err, decision)
	}
	decision, observed := ImageAttemptLastDecision(ctx)
	// 上次安全拒绝后若当前候选尚未发送，不能沿用上个账号的可重试错误误罚本次参数校验。
	if policy := imagePolicyFromContext(ctx); policy != nil {
		policy.mu.Lock()
		waiting := policy.mayProceed
		policy.mu.Unlock()
		if waiting {
			observed = false
		}
	}
	if !observed {
		decision = ImageAttemptDecision{Outcome: ImageAttemptNotSent, Reason: "image_local_validation_failed"}
	}
	return result, RecordImageAttemptFailure(ctx, err, decision)
}

// 已获得完整 HTTP 拒绝时统一决策；普通 5xx 仍只影响后续健康路由，不授权重发本次生成。
func (s *OpenAIGatewayService) imageAttemptHTTPFailure(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, body []byte, model string) (*OpenAIForwardResult, error) {
	decision := ClassifyImageHTTPFailure(resp.StatusCode, body)
	if account.IsCustomErrorCodesEnabled() && !account.ShouldHandleErrorCode(resp.StatusCode) {
		decision.RetryDifferentAccount, decision.AttemptFault = false, false
	}
	upstreamErr := openAIImagesUpstreamErrorFromHTTP(resp.StatusCode, resp.Header, body)
	if decision.AttemptFault {
		s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, model)
	}
	if isOpenAIImagesInsufficientBalance(body) && !account.IsPoolMode() && account.ShouldHandleErrorCode(resp.StatusCode) {
		s.coolOpenAIImagesInsufficientBalance(ctx, account)
	}
	kind := "http_error"
	if ImageAttemptCanSwitch(decision) {
		kind = "failover"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"), Kind: kind, Message: upstreamErr.clientMessage()})
	if ImageAttemptCanSwitch(decision) {
		return nil, RecordImageAttemptFailure(ctx, &UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseHeaders: resp.Header.Clone(), ResponseBody: body, NextAccountAction: NextAccountRetry}, decision)
	}
	if decision.Outcome == ImageAttemptUnknown || decision.Outcome == ImageAttemptAccepted || decision.Outcome == ImageAttemptResultReady {
		// 不以普通可重试5xx消息误导调用方再次提交可能已经付费的生成。
		upstreamErr.StatusCode = http.StatusBadGateway
		upstreamErr.Code = "image_result_unconfirmed"
		upstreamErr.Message = "Image generation result is unconfirmed; do not automatically resubmit"
	}
	writeOpenAIImagesUpstreamErrorResponse(c, upstreamErr)
	return nil, RecordImageAttemptFailure(ctx, upstreamErr, decision)
}

// 无请求正文错误的兜底仍是未知结果；调用方不能靠错误字符串猜测是否已受理。
func imageAttemptUnknownFailure(ctx context.Context, err error) error {
	if err == nil {
		err = errors.New("image generation result is unconfirmed")
	}
	return RecordImageAttemptFailure(ctx, err, ImageAttemptDecision{Outcome: ImageAttemptUnknown, AttemptFault: true, Reason: "image_result_unconfirmed"})
}
