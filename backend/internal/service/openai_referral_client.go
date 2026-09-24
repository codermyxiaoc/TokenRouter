package service

import (
	"context"
	"net/http"
)

// OpenAIReferralCall 向传输适配器传递账号刷新后的认证和代理配置。
// 认证请求头不得记录日志或持久化。
type OpenAIReferralCall struct {
	// Do 复用账号配置的代理、TLS 指纹和认证上下文，每次只提交一次请求。
	Do        func(*http.Request) (*http.Response, error)
	ProxyURL  string
	Headers   map[string]string
	ProgramID string
}

// OpenAIReferralClient 分离邀请传输与资格策略。查询成功时必须返回非空结果；
// 发送不得自动重试，仅在上游确认一条邀请后返回成功。
type OpenAIReferralClient interface {
	QueryEligibility(context.Context, OpenAIReferralCall) (*OpenAIReferralEligibility, error)
	SendInvite(context.Context, OpenAIReferralCall, string) error
}
