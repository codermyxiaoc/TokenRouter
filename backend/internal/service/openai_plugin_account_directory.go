package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	pluginv1 "github.com/TokenFlux/TokenRouter/pkg/pluginapi/v1"
)

// ListPluginAccounts 仅向声明对应能力的插件列举启用中的 OpenAI OAuth 正式账号。
// 平台、账号类型和影子账号边界均由宿主强制执行。
func (s *OpenAIGatewayService) ListPluginAccounts(ctx context.Context, platform, accountType string) ([]int64, error) {
	accounts, err := s.ListPluginAccountMetadata(ctx, platform, accountType)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.Id)
	}
	return ids, nil
}

// ListPluginAccountMetadata 在同一授权范围内返回运行状态，暂停但未停用的账号仍可被插件识别。
func (s *OpenAIGatewayService) ListPluginAccountMetadata(ctx context.Context, platform, accountType string) ([]*pluginv1.AccountInfo, error) {
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
	infos := make([]*pluginv1.AccountInfo, 0, len(accounts))
	for i := range accounts {
		account := accounts[i]
		if account.Status == StatusActive && account.IsOpenAIOAuthLike() && !account.IsShadow() && account.Type == AccountTypeOAuth {
			infos = append(infos, &pluginv1.AccountInfo{
				Id: account.ID, Platform: account.Platform, AccountType: account.Type,
				Name: account.Name, Status: account.Status, Schedulable: account.IsSchedulable(),
				IsShadow: account.IsShadow(), MetadataJson: accountReadableSnapshotJSON(&account),
			})
		}
	}
	return infos, nil
}

// accountReadableSnapshotJSON 排除凭据、任意扩展数据、代理密码和循环关联；出站凭据仅通过原身份接口提供。
func accountReadableSnapshotJSON(account *Account) []byte {
	if account == nil {
		return nil
	}
	clone := *account
	clone.Credentials = nil
	clone.Extra = nil
	clone.Proxy = nil
	clone.Groups = nil
	clone.AccountGroups = nil
	data, err := json.Marshal(&clone)
	if err != nil {
		return nil
	}
	return data
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
