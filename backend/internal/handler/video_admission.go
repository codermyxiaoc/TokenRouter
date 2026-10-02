package handler

import (
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// admitVideo 复用内容策略、用户并发和等待后的消费检查，不借用图片生成开关。
func (h *VideoHandler) admitVideo(c *gin.Context, key *service.APIKey, body []byte, pathModel string) (func(), bool) {
	metadata, err := service.ParseVideoRequestModel(body, pathModel)
	if err != nil {
		writeVideoError(c, err)
		return nil, false
	}
	setOpsRequestContext(c, metadata.Model, false)
	if h.gateway == nil {
		return nil, true
	}
	subject, exists := middleware.GetAuthSubjectFromContext(c)
	if !exists {
		writeVideoError(c, infraerrors.Unauthorized("VIDEO_UNAUTHORIZED", "User context is unavailable"))
		return nil, false
	}
	log := requestLogger(c, "handler.video")
	info := service.ParseGrokMediaRequest("application/json", body)
	texts := []string{info.Prompt, gjson.GetBytes(body, "input.prompt").String()}
	for _, path := range []string{"content", "input.content"} {
		for _, item := range gjson.GetBytes(body, path).Array() {
			if item.Get("type").String() == "text" {
				texts = append(texts, item.Get("text").String())
			}
			if item.Get("type").String() == "image_url" {
				info.InputImageURLs = append(info.InputImageURLs, item.Get("image_url.url").String())
			}
		}
	}
	info.Prompt = strings.Join(texts, "\n")
	if moderation := info.ModerationBody(); len(moderation) > 0 {
		decision := h.gateway.checkContentModeration(c, log, key, subject, service.ContentModerationProtocolOpenAIImages, metadata.Model, moderation)
		if decision != nil && decision.Blocked {
			writeVideoError(c, infraerrors.New(contentModerationStatus(decision), contentModerationErrorCode(decision), decision.Message))
			return nil, false
		}
	}
	started := false
	release, acquired := h.gateway.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, false, &started, log)
	if !acquired {
		return nil, false
	}
	subscription, _ := middleware.GetSubscriptionFromContext(c)
	if h.gateway.billingCacheService != nil {
		if err := h.gateway.billingCacheService.CheckBillingEligibility(c.Request.Context(), key.User, key, key.Group, subscription, service.QuotaPlatform(c.Request.Context(), key)); err != nil {
			if release != nil {
				release()
			}
			writeVideoError(c, err)
			return nil, false
		}
	}
	return release, true
}
