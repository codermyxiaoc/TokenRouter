package service

import (
	"context"
	"net/http"
	"strings"
)

// ListPluginAccounts 仅向声明对应能力的插件列举启用中的 OpenAI OAuth 正式账号。
// 平台、账号类型和影子账号边界均由宿主强制执行。
func (s *OpenAIGatewayService) ListPluginAccounts(ctx context.Context, platform, accountType string) ([]int64, error) {
	if s == nil || s.accountRepo == nil {
		return nil, nil
	}
	if p := strings.TrimSpace(platform); p != "" && p != PlatformOpenAI {
		return nil, nil
	}
	if at := strings.TrimSpace(accountType); at != "" && at != AccountTypeOAuth {
		// 当前能力仅覆盖 OAuth，Setup Token 等其他账号类型不属于该范围。
		return nil, nil
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(accounts))
	for i := range accounts {
		account := accounts[i]
		if account.Status == StatusActive && account.IsOpenAIOAuthLike() && !account.IsShadow() && account.Type == AccountTypeOAuth {
			ids = append(ids, account.ID)
		}
	}
	return ids, nil
}

// ResolvePluginOutboundIdentity 仅解析相同账号范围的令牌、出站身份头与代理。
// 停用、其他平台、其他类型及影子账号均不能取得凭据。
func (s *OpenAIGatewayService) ResolvePluginOutboundIdentity(ctx context.Context, accountID int64) (*PluginOutboundIdentity, error) {
	if s == nil || s.accountRepo == nil || accountID <= 0 {
		return nil, nil
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil || account.Status != StatusActive || account.Type != AccountTypeOAuth || !account.IsOpenAIOAuthLike() || account.IsShadow() {
		return nil, nil
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, nil
	}
	headers := http.Header{}
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, headers, account); err != nil {
		return nil, err
	}
	ensureCodexIdentityHeaders(headers)
	enforceCodexIdentityHeaders(headers)
	return &PluginOutboundIdentity{
		AccountID:   account.ID,
		Platform:    account.Platform,
		AccountType: account.Type,
		ProxyURL:    resolveAccountProxyURL(account),
		Token:       token,
		Headers:     headers,
	}, nil
}
