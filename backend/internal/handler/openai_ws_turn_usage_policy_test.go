package handler

import (
	"errors"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 保护 WS AfterTurn 的真实收尾策略，避免流读取错误丢账或重复结算风控记录。
func TestOpenAIWSTurnUsagePolicy(t *testing.T) {
	streamErr := errors.New("upstream stream disconnected")
	for _, tc := range []struct {
		name       string
		result     *service.OpenAIForwardResult
		err        error
		cyber      bool
		wantRecord bool
		wantOK     bool
	}{
		{name: "token usage with error", result: &service.OpenAIForwardResult{Usage: service.OpenAIUsage{InputTokens: 3, OutputTokens: 2}}, err: streamErr, wantRecord: true},
		{name: "cache only usage with error", result: &service.OpenAIForwardResult{Usage: service.OpenAIUsage{CacheReadInputTokens: 3}}, err: streamErr, wantRecord: true},
		{name: "image tokens with error", result: &service.OpenAIForwardResult{Usage: service.OpenAIUsage{ImageOutputTokens: 2}}, err: streamErr, wantRecord: true},
		{name: "partial image with error", result: &service.OpenAIForwardResult{ImageCount: 1}, err: streamErr, wantRecord: true},
		{name: "error without usage", result: &service.OpenAIForwardResult{}, err: streamErr},
		{name: "error without result", err: streamErr},
		{name: "cyber already settled token usage", result: &service.OpenAIForwardResult{Usage: service.OpenAIUsage{InputTokens: 3}}, err: streamErr, cyber: true},
		{name: "cyber already settled partial image", result: &service.OpenAIForwardResult{ImageCount: 1}, err: streamErr, cyber: true},
		{name: "success still records zero usage", result: &service.OpenAIForwardResult{}, wantRecord: true, wantOK: true},
		{name: "failed terminal is not scheduling success", result: &service.OpenAIForwardResult{Usage: service.OpenAIUsage{InputTokens: 3}, UpstreamTerminalEvent: "response.failed"}, wantRecord: true},
		{name: "completed terminal plus read error is not scheduling success", result: &service.OpenAIForwardResult{Usage: service.OpenAIUsage{InputTokens: 3}, UpstreamTerminalEvent: "response.completed"}, err: streamErr, wantRecord: true},
		{name: "completed terminal", result: &service.OpenAIForwardResult{Usage: service.OpenAIUsage{InputTokens: 3}, UpstreamTerminalEvent: "response.completed"}, wantRecord: true, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.result != nil {
				tc.result.OpenAIWSMode = true
			}
			record, succeeded := openAIWSTurnUsagePolicy(tc.result, tc.err, tc.cyber)
			require.Equal(t, tc.wantRecord, record)
			require.Equal(t, tc.wantOK, succeeded)
		})
	}
}
