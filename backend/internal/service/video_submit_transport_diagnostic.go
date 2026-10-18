package service

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/servertiming"
)

// 创建请求的观测对象只保存固定统计字段，不保存凭据、素材或请求响应正文。
type videoSubmitTransportDiagnosticKey struct{}

type videoSubmitTransportDiagnostic struct {
	hostname      string
	dispatched    bool
	statusCode    int
	responseBytes int
}

func logVideoSubmitTransport(ctx context.Context, selected *VideoUpstreamSelection, diagnostic *videoSubmitTransportDiagnostic, started time.Time, err error) {
	trace, _ := servertiming.HTTPTraceSnapshotFromContext(ctx)
	endpoint := "unknown"
	switch selected.Target.Endpoint {
	case VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax, VideoEndpointVidu:
		endpoint = string(selected.Target.Endpoint)
	}
	// 缺少回调以 -1 表示未知，不能把尚未写入或尚未收到首字节记为耗时零。
	stageMS := func(at time.Time) int64 {
		if at.IsZero() || at.Before(started) {
			return -1
		}
		return at.Sub(started).Milliseconds()
	}
	slog.InfoContext(ctx, "video submission transport",
		"account_id", selected.Target.AccountID, "endpoint", endpoint,
		"hostname", diagnostic.hostname, "request_bytes", len(selected.Body),
		"elapsed_ms", time.Since(started).Milliseconds(), "http_status", diagnostic.statusCode,
		"response_bytes", diagnostic.responseBytes, "failure_kind", videoSubmitFailureKind(diagnostic, err),
		"connection_requested", trace.GetConnCount > 0, "connection_observed", trace.GotConnCount > 0,
		"connection_reused", trace.ConnectionReused, "request_write_observed", trace.WroteRequestCount > 0,
		"first_byte_observed", trace.FirstResponseByteCount > 0, "request_write_error", trace.WroteRequestErrorObserved,
		"connection_count", trace.GotConnCount, "request_write_count", trace.WroteRequestCount,
		"first_byte_count", trace.FirstResponseByteCount, "connection_ms", stageMS(trace.GotConnAt),
		"request_write_ms", stageMS(trace.WroteRequestAt), "first_byte_ms", stageMS(trace.GotFirstResponseByteAt))
}

// 错误仅转换为固定类别；不得将 URL 包装错误或任意上游原文写进普通日志。
func videoSubmitFailureKind(diagnostic *videoSubmitTransportDiagnostic, err error) string {
	if err == nil {
		if diagnostic.statusCode >= 400 {
			return "http_error"
		}
		return "none"
	}
	if !diagnostic.dispatched {
		return "pre_dispatch"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	if diagnostic.statusCode != 0 {
		return "response_error"
	}
	return "transport_error"
}
