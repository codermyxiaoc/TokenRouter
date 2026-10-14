package service

import (
	"context"
	"time"
)

const (
	legacyVideoCreateTimeout = 2 * time.Minute
	legacyVideoStatusTimeout = 30 * time.Second
)

func isGrokAsyncVideoCreateEndpoint(endpoint GrokMediaEndpoint) bool {
	return endpoint == GrokMediaEndpointVideosGenerations || endpoint == GrokMediaEndpointVideosEdits || endpoint == GrokMediaEndpointVideosExtensions
}

// legacyVideoUpstreamContext 只限制旧异步视频的单次受理/查询，不限制生成任务的寿命。
// 总预算覆盖响应体读取；图片和内容下载继续原有上下文，避免误截断同步媒体。
func legacyVideoUpstreamContext(ctx context.Context, endpoint GrokMediaEndpoint) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch {
	case isGrokAsyncVideoCreateEndpoint(endpoint):
		// 保留 Grok 已发起受理不随客户端断开取消的约定，但不再无限占用并发槽。
		ctx = WithHTTPUpstreamProfile(context.WithoutCancel(ctx), HTTPUpstreamProfileGrok)
		return context.WithTimeout(WithHTTPUpstreamRedirectsDisabled(ctx), legacyVideoCreateTimeout)
	case endpoint == GrokMediaEndpointVideoStatus:
		// 查询无须脱离调用方取消；更短的调用方截止时间仍然优先。
		return context.WithTimeout(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileGrok), legacyVideoStatusTimeout)
	case endpoint == SeedanceEndpointCreate:
		return context.WithTimeout(WithHTTPUpstreamRedirectsDisabled(ctx), legacyVideoCreateTimeout)
	case endpoint == SeedanceEndpointStatus:
		return context.WithTimeout(ctx, legacyVideoStatusTimeout)
	case endpoint == SeedanceEndpointDelete:
		return ctx, func() {}
	default:
		return detachUpstreamContext(ctx)
	}
}
