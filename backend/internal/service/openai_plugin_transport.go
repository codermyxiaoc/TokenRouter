package service

import (
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"net/http"
)

// SetPluginManager 在启动阶段注入插件管理器，不改变未绑定插件的账号策略。
func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) { s.pluginManager = manager }

// SetPluginManager 让管理员账号测试与正式请求共享插件能力门禁。
func (s *AccountTestService) SetPluginManager(manager *PluginManager) { s.pluginManager = manager }

// doOpenAIUpstream 只将命中显式绑定的 OpenAI OAuth 请求交给插件。
// 未命中时保留调用方选定的 TLS 指纹，响应解析、错误恢复和计费仍由原链路完成。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account, profiles ...*tlsfingerprint.Profile) (*http.Response, error) {
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if len(profiles) > 0 {
		return s.httpUpstream.DoWithTLS(request, proxyURL, account.ID, account.Concurrency, profiles[0])
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}

// doOpenAIAccountTestUpstream 保留管理端测试的 TLS 路由结果与未绑定时的传输行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(request *http.Request, proxyURL string, account *Account, profile *tlsfingerprint.Profile) (*http.Response, error) {
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	return s.httpUpstream.DoWithTLS(request, proxyURL, account.ID, account.Concurrency, profile)
}
