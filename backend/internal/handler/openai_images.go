package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	pkghttputil "github.com/TokenFlux/TokenRouter/internal/pkg/httputil"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ip"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Images handles OpenAI Images API requests.
// POST /v1/images/generations
// POST /v1/images/edits
func (h *OpenAIGatewayHandler) Images(c *gin.Context) {
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)

	requestStart := time.Now()
	imageCtx, imageCancel := service.WithImageAttemptPolicy(c.Request.Context(), service.DefaultPublicImageAttemptTimeout)
	defer imageCancel()
	c.Request = c.Request.WithContext(imageCtx)

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.images",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	if isMultipartImagesContentType(c.GetHeader("Content-Type")) {
		setOpsRequestContext(c, "", false)
	} else {
		setOpsRequestContext(c, "", false)
	}

	parsed, err := h.gatewayService.ParseOpenAIImagesRequestForRouting(c, body)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	requestModel := parsed.Model
	// Images 端点必须先得到渠道模型 C，再校验模型族和账号所需能力。
	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, requestModel)
	routingModel := openAIChannelMappedModel(requestModel, channelMapping)
	if err := parsed.ValidateRoutingModel(routingModel); err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	reqLog = reqLog.With(
		zap.String("model", requestModel),
		zap.Bool("stream", parsed.Stream),
		zap.Bool("multipart", parsed.Multipart),
		zap.String("capability", string(parsed.RequiredCapability)),
		zap.String("img_quality", parsed.Quality),
		zap.String("img_size", parsed.Size),
	)

	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		h.errorResponse(c, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
		return
	}
	if decision := h.checkContentModeration(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIImages, requestModel, parsed.ModerationBody()); decision != nil && decision.Blocked {
		h.errorResponse(c, contentModerationStatus(decision), contentModerationErrorCode(decision), decision.Message)
		return
	}
	setOpenAICyberWarningRequestSnapshot(c, service.ContentModerationProtocolOpenAIImages, parsed.ModerationBody())
	imageReleaseFunc, acquired := h.acquireImageGenerationSlot(c, streamStarted)
	if !acquired {
		return
	}
	if imageReleaseFunc != nil {
		defer imageReleaseFunc()
	}

	if parsed.Multipart {
		setOpsRequestContext(c, requestModel, parsed.Stream)
	} else {
		setOpsRequestContext(c, requestModel, parsed.Stream)
	}
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(parsed.Stream, false)))

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, parsed.Stream, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("openai.images.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	sessionHash := h.gatewayService.GenerateExplicitSessionHash(c, body)
	if sessionHash != "" {
		if err := h.ensureOpenAISessionIsolation(c.Request.Context(), apiKey, subject.UserID, service.SessionIsolationSourceOpenAI, sessionHash); h.handleOpenAISessionIsolationError(c, err, streamStarted) {
			return
		}
	}
	requestCtx := service.WithOpenAIImagesEndpoint(service.WithOpenAIImageGenerationIntent(c.Request.Context()))
	// 标记图片操作后，调度器和粘性会话会直接跳过冷却中的“账号 + 模型 + 操作”，避免反复命中同一故障模型。
	// 全部候选都在冷却时退回逐个排除的旧路径一次，以便返回带 Retry-After 的冷却错误。
	cooldownAwareSelectCtx := service.WithImageModelOperation(requestCtx, service.ImageAttemptOperation(parsed))
	schedulerSkipsCooling := true
	imageBilling := &asyncImageBillingState{gateway: h.gatewayService}
	defer imageBilling.finish(c, reqLog)

	maxAccountSwitches := h.maxAccountSwitches
	switchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	var shortestImageCooldown time.Duration
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	stopJSONKeepalive := func() {}
	jsonKeepaliveStarted := false
	defer func() { stopJSONKeepalive() }()
	var oauth429FailoverState service.OpenAIOAuth429FailoverState

	for {
		reqLog.Debug("openai.images.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		selectCtx := requestCtx
		if schedulerSkipsCooling {
			selectCtx = cooldownAwareSelectCtx
		}
		selection, scheduleDecision, err := h.gatewayService.SelectAccountWithSchedulerForImages(
			selectCtx,
			apiKey.GroupID,
			sessionHash,
			requestModel,
			failedAccountIDs,
			parsed.RequiredCapability,
		)
		if err != nil {
			if imageAttemptDeadlineExceeded(c, requestCtx) || failoverClientGone(c) {
				reqLog.Info("openai.images.account_select_aborted_client_disconnected", zap.Error(err))
				return
			}
			if schedulerSkipsCooling && errors.Is(err, service.ErrNoAvailableAccounts) {
				// 只发生在选号阶段，不会发送请求；第二轮逐个识别冷却账号以给出准确的重试时间。
				schedulerSkipsCooling = false
				continue
			}
			reqLog.Warn("openai.images.account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if errors.Is(err, service.ErrNoAvailableAccounts) {
				// 本组没有可用账号（多为生图模型全部冷却）且未发送，允许智能路由 Key 换到下一个分组。
				service.MarkImageAttemptGroupUnsent(requestCtx)
			}
			if shortestImageCooldown > 0 && errors.Is(err, service.ErrNoAvailableAccounts) {
				c.Header("Retry-After", strconv.Itoa(max(1, int((shortestImageCooldown+time.Second-1)/time.Second))))
				h.errorResponse(c, http.StatusServiceUnavailable, "image_models_cooling_down", "Compatible image accounts are temporarily cooling down")
				return
			}
			if len(failedAccountIDs) == 0 {
				cls := classifyNoAccountErrorFromGin(c, h.gatewayService, apiKey, requestModel, requestModel, service.PlatformOpenAI)
				if !cls.ModelNotFound {
					markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				}
				message := cls.Message
				if !cls.ModelNotFound {
					message = "No available compatible accounts"
				}
				h.handleStreamingAwareError(c, cls.Status, cls.ErrType, message, streamStarted)
				return
			}
			if lastFailoverErr != nil {
				h.handleFailoverExhausted(c, lastFailoverErr, streamStarted)
			} else {
				h.handleFailoverExhaustedSimple(c, 502, streamStarted)
			}
			return
		}
		if selection == nil || selection.Account == nil {
			cls := classifyNoAccountErrorFromGin(c, h.gatewayService, apiKey, requestModel, requestModel, service.PlatformOpenAI)
			if !cls.ModelNotFound {
				markOpsRoutingCapacityLimited(c)
			}
			message := cls.Message
			if !cls.ModelNotFound {
				message = "No available compatible accounts"
			}
			h.handleStreamingAwareError(c, cls.Status, cls.ErrType, message, streamStarted)
			return
		}

		reqLog.Debug("openai.images.account_schedule_decision",
			zap.String("layer", scheduleDecision.Layer),
			zap.Bool("sticky_session_hit", scheduleDecision.StickySessionHit),
			zap.Int("candidate_count", scheduleDecision.CandidateCount),
			zap.Int("top_k", scheduleDecision.TopK),
			zap.Int64("latency_ms", scheduleDecision.LatencyMs),
			zap.Float64("load_skew", scheduleDecision.LoadSkew),
		)

		account := selection.Account
		finalImageModel := service.ImageAttemptAccountModel(requestCtx, account, routingModel)
		imageOperation := service.ImageAttemptOperation(parsed)
		if cooldown := h.gatewayService.ImageModelCooldownRemaining(requestCtx, account.ID, finalImageModel, imageOperation); cooldown > 0 {
			if shortestImageCooldown == 0 || cooldown < shortestImageCooldown {
				shortestImageCooldown = cooldown
			}
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			failedAccountIDs[account.ID] = struct{}{}
			continue
		}
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		reqLog.Debug("openai.images.account_selected", zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
		setOpsSelectedAccount(c, account.ID, account.Platform)

		accountReleaseFunc, acquired := h.acquireResponsesAccountSlot(c, apiKey.GroupID, sessionHash, selection, parsed.Stream, &streamStarted, reqLog)
		if !acquired {
			return
		}

		// 最终账号与渠道模型确定后冻结报价；后续同组账号重试沿用本次快照。
		// 账号映射也可能切到 MJ，必须在冻结资金与发送请求前核验 n 和已冻结的张数上限。
		expectedOutputCount, validationErr := parsed.ExpectedOutputCountForAccount(account, routingModel)
		if validationErr != nil || (imageBilling.reservation != nil && expectedOutputCount > imageBilling.reservation.Quote.RequestedCount) {
			if accountReleaseFunc != nil {
				accountReleaseFunc()
			}
			status, errorType := http.StatusServiceUnavailable, "service_unavailable"
			message := "Selected account output count exceeds the reserved image budget"
			if validationErr != nil {
				status, errorType = http.StatusBadRequest, "invalid_request_error"
				message = validationErr.Error()
			}
			h.handleStreamingAwareError(c, status, errorType, message, streamStarted)
			return
		}
		if err := imageBilling.prepare(requestCtx, apiKey, account, subscription, requestModel, parsed.N, parsed.SizeTier, channelMapping); err != nil {
			if accountReleaseFunc != nil {
				accountReleaseFunc()
			}
			status, code, message, retryAfter := billingErrorDetails(err)
			if retryAfter > 0 {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
			}
			h.handleStreamingAwareError(c, status, code, message, streamStarted)
			return
		}

		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		if !parsed.Stream && !jsonKeepaliveStarted {
			stopJSONKeepalive = service.StartOpenAIImagesJSONKeepalive(c, h.openAIImagesJSONKeepaliveInterval())
			jsonKeepaliveStarted = true
		}
		forwardStart := time.Now()
		writerSizeBeforeForward := service.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)
		result, err := func() (*service.OpenAIForwardResult, error) {
			defer func() {
				if accountReleaseFunc != nil {
					accountReleaseFunc()
				}
			}()
			tlsRouterMatch := h.gatewayService.MatchOpenAITLSFingerprintRouterForRequest(c, account)
			if err := h.gatewayService.EnforceOpenAIClientPolicyForRequest(requestCtx, c, account, body, tlsRouterMatch); err != nil {
				// 本地策略拒绝发生在当前账号发送前，不能沿用前一账号的故障决策处罚当前账号。
				return nil, service.WrapImageAttemptError(err, service.ImageAttemptDecision{Outcome: service.ImageAttemptNotSent, Reason: "image_local_policy_rejected"})
			}
			imageBilling.startForward(c)
			return h.gatewayService.ForwardImages(requestCtx, c, account, body, parsed, routingModel, tlsRouterMatch)
		}()
		forwardDurationMs := time.Since(forwardStart).Milliseconds()
		upstreamLatencyMs, _ := getContextInt64(c, service.OpsUpstreamLatencyMsKey)
		responseLatencyMs := forwardDurationMs
		if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
			responseLatencyMs = forwardDurationMs - upstreamLatencyMs
		}
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, responseLatencyMs)
		if result != nil && result.FirstTokenMs != nil {
			service.SetOpsLatencyMs(c, service.OpsTimeToFirstTokenMsKey, int64(*result.FirstTokenMs))
		}
		if err != nil {
			imageBilling.forwardFailed(c, err)
			if result != nil && result.ImageCount > 0 {
				reqLog.Warn("openai.images.forward_partial_error_with_image_result",
					zap.Int64("account_id", account.ID),
					zap.Int("image_count", result.ImageCount),
					zap.Error(err),
				)
			} else {
				if service.ImageAttemptHasPolicy(requestCtx) {
					if h.retryImageOnDifferentAccount(c, requestCtx, account, finalImageModel, imageOperation, err, writerSizeBeforeForward) {
						failedAccountIDs[account.ID] = struct{}{}
						h.gatewayService.RecordOpenAIAccountSwitchForSelection(selection)
						if !errors.As(err, &lastFailoverErr) {
							lastFailoverErr = &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}
						}
						continue
					}
					return
				}
				if imageBilling.reservation != nil && imageBilling.uncertain {
					// 已发出的图片请求结果未知时停止自动重放，避免一次预占触发重复生成。
					h.ensureOpenAIForwardErrorResponse(c, streamStarted, err)
					reqLog.Warn("openai.images.billing_reconciliation_required", zap.Error(err))
					return
				}
				var imageUpstreamErr *service.OpenAIImagesUpstreamError
				if errors.As(err, &imageUpstreamErr) {
					retryableServerError := service.IsOpenAIImagesRetryableUpstreamError(imageUpstreamErr)
					if retryableServerError {
						h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), false, nil, err)
					} else {
						h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), true, nil)
					}
					logEvent := "openai.images.upstream_user_error"
					if retryableServerError {
						logEvent = "openai.images.upstream_server_error_after_flush"
					}
					reqLog.Warn(logEvent,
						zap.Int64("account_id", account.ID),
						zap.Int("status_code", imageUpstreamErr.StatusCode),
						zap.String("error_type", imageUpstreamErr.ErrorType),
						zap.String("error_code", imageUpstreamErr.Code),
						zap.Error(err),
					)
					return
				}
				var failoverErr *service.UpstreamFailoverError
				if errors.As(err, &failoverErr) {
					h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), false, nil, err)
					if service.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) != writerSizeBeforeForward {
						reqLog.Warn("openai.images.upstream_failover_skipped_after_flush",
							zap.Int64("account_id", account.ID),
							zap.Int("upstream_status", failoverErr.StatusCode),
						)
						h.handleFailoverExhausted(c, failoverErr, true)
						return
					}
					if imageAttemptDeadlineExceeded(c, requestCtx) || failoverClientGone(c) {
						reqLog.Info("openai.images.failover_aborted_client_disconnected",
							zap.Int64("account_id", account.ID),
							zap.Int("upstream_status", failoverErr.StatusCode),
						)
						return
					}
					if failoverErr.RetryableOnSameAccount {
						retryLimit := effectiveSameAccountRetryLimit(failoverErr, account)
						if sameAccountRetryAllowed(failoverErr, sameAccountRetryCount[account.ID], retryLimit) {
							sameAccountRetryCount[account.ID]++
							retryDelay := sameAccountRetryDelayFor(failoverErr, sameAccountRetryCount[account.ID])
							reqLog.Warn("openai.images.pool_mode_same_account_retry",
								zap.Int64("account_id", account.ID),
								zap.Int("upstream_status", failoverErr.StatusCode),
								zap.Int("retry_limit", retryLimit),
								zap.Int("retry_count", sameAccountRetryCount[account.ID]),
								zap.Duration("retry_delay", retryDelay),
							)
							select {
							case <-requestCtx.Done():
								return
							case <-time.After(retryDelay):
							}
							continue
						}
					}
					h.gatewayService.RecordOpenAIAccountSwitchForSelection(selection)
					failedAccountIDs[account.ID] = struct{}{}
					lastFailoverErr = failoverErr
					if switchCount >= maxAccountSwitches {
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					switchCount++
					if h.gatewayService.ShouldStopOpenAIOAuth429Failover(account, failoverErr.StatusCode, switchCount, &oauth429FailoverState) {
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					reqLog.Warn("openai.images.upstream_failover_switching",
						zap.Int64("account_id", account.ID),
						zap.Int("upstream_status", failoverErr.StatusCode),
						zap.Int("switch_count", switchCount),
						zap.Int("max_switches", maxAccountSwitches),
					)
					continue
				}
				statusCode := 0
				if v, ok := getContextInt64(c, service.OpsUpstreamStatusCodeKey); ok {
					statusCode = int(v)
				}
				recordedWarning := h.recordOpenAIForwardErrorCyberWarning(c, reqLog, apiKey, account, requestModel, statusCode, err)
				if !recordedWarning {
					h.recordOpenAICyberWarning(c, reqLog, apiKey, account, requestModel, statusCode, nil, err.Error())
				}
				h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), false, nil, err)
				upstreamErrorAlreadyCommunicated := openAIForwardErrorAlreadyCommunicated(c, writerSizeBeforeForward, err)
				wroteFallback := false
				if !upstreamErrorAlreadyCommunicated && (!recordedWarning || c.Writer.Size() == writerSizeBeforeForward) {
					wroteFallback = h.ensureOpenAIForwardErrorResponse(c, streamStarted, err)
				}
				fields := []zap.Field{
					zap.Int64("account_id", account.ID),
					zap.Bool("fallback_error_response_written", wroteFallback),
					zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
					zap.Error(err),
				}
				if shouldLogOpenAIForwardFailureAsWarn(c, wroteFallback) {
					reqLog.Warn("openai.images.forward_failed", fields...)
					return
				}
				reqLog.Error("openai.images.forward_failed", fields...)
				return
			}
		}
		if result != nil {
			if result.ImageCount > 0 {
				// 确认产出图片后清零该模型的连续失败计数，下次故障重新从第一档冷却。
				h.gatewayService.RecordImageModelSuccess(requestCtx, account.ID, finalImageModel, imageOperation)
			}
			// 排除 spark 影子:其 codex_* 仅由 QueryUsage(/wham/usage bengalfox)更新(外审第7轮 P1)。
			if account.Type == service.AccountTypeOAuth && !account.IsShadow() {
				h.gatewayService.UpdateCodexUsageSnapshotFromHeaders(c.Request.Context(), account.ID, result.ResponseHeaders)
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), true, result.FirstTokenMs)
		} else {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), true, nil)
		}

		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		requestPayloadHash := service.HashUsageRequestPayload(body)
		if parsed.Multipart {
			requestPayloadHash = service.HashUsageRequestPayload([]byte(parsed.StickySessionSeed()))
		}
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
		quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
		clientSessionID := service.ExtractClientSessionID(c)

		upstreamModel := ""
		if result != nil {
			upstreamModel = result.UpstreamModel
		}
		recordUsage := func(ctx context.Context) {
			input := &service.OpenAIRecordUsageInput{
				Result:             result,
				APIKey:             apiKey,
				User:               apiKey.User,
				Account:            account,
				Subscription:       subscription,
				InboundEndpoint:    inboundEndpoint,
				UpstreamEndpoint:   upstreamEndpoint,
				UserAgent:          userAgent,
				IPAddress:          clientIP,
				RequestPayloadHash: requestPayloadHash,
				RequestBody:        body,
				APIKeyService:      h.apiKeyService,
				QuotaPlatform:      quotaPlatform,
				ClientSessionID:    clientSessionID,
				ChannelUsageFields: channelMapping.ToUsageFields(requestModel, upstreamModel),
			}
			var recordErr error
			if imageBilling.reservation != nil {
				recordErr = imageBilling.record(c, input)
			} else {
				recordErr = h.gatewayService.RecordUsage(ctx, input)
			}
			if recordErr != nil {
				logger.L().With(
					zap.String("component", "handler.openai_gateway.images"),
					zap.Int64("user_id", subject.UserID),
					zap.Int64("api_key_id", apiKey.ID),
					zap.Any("group_id", apiKey.GroupID),
					zap.String("model", requestModel),
					zap.Int64("account_id", account.ID),
				).Error("openai.images.record_usage_failed", zap.Error(recordErr))
			}
		}
		if imageBilling.reservation != nil {
			// 预占必须在任务完成前捕获，不能交给可能晚于当前请求执行的 usage worker。
			recordUsage(requestCtx)
		} else if deferred := service.AsyncImageDeferredCaptureFromContext(requestCtx); deferred != nil &&
			deferred.Defer(func(context.Context) error {
				// 记账沿用原请求上下文的值（请求 ID、载荷指纹等），只替换截止时间。
				ctx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), 10*time.Second)
				defer cancel()
				recordUsage(ctx)
				return nil
			}, nil) {
			// 无预占的异步后结算（如按 Token 计价）同样等图片交付后再记账，未交付不计费。
		} else {
			h.submitMandatoryUsageRecordTask(c, recordUsage)
		}

		reqLog.Debug("openai.images.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return
	}
}

func (h *OpenAIGatewayHandler) openAIImagesJSONKeepaliveInterval() time.Duration {
	if h.cfg == nil || h.cfg.Gateway.ImageNonstreamKeepaliveInterval <= 0 {
		return 0
	}
	return time.Duration(h.cfg.Gateway.ImageNonstreamKeepaliveInterval) * time.Second
}

func isMultipartImagesContentType(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/form-data")
}
