package service

import (
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
)

// configuredMinOutputTokens 兼容未装配进程配置的测试与旧调用方。
func configuredMinOutputTokens(cfg *config.Config) int {
	if cfg != nil && cfg.Gateway.MinOutputTokens >= 16 {
		return cfg.Gateway.MinOutputTokens
	}
	return apicompat.DefaultMinMaxOutputTokens
}

// chatCompletionsToResponses 统一各平台兼容入口的可配置输出预算。
func chatCompletionsToResponses(cfg *config.Config, req *apicompat.ChatCompletionsRequest) (*apicompat.ResponsesRequest, error) {
	return apicompat.ChatCompletionsToResponsesWithMinOutputTokens(req, configuredMinOutputTokens(cfg))
}

// anthropicToResponses 只调整输出下限，复用当前 fork 的完整请求转换。
func anthropicToResponses(cfg *config.Config, req *apicompat.AnthropicRequest) (*apicompat.ResponsesRequest, error) {
	return apicompat.AnthropicToResponsesWithMinOutputTokens(req, configuredMinOutputTokens(cfg))
}

// settingServiceConfig 允许未注入设置服务的兼容路径沿用旧默认值。
func settingServiceConfig(settingService *SettingService) *config.Config {
	if settingService == nil {
		return nil
	}
	return settingService.cfg
}
