package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	grokTokenCacheSkew          = 5 * time.Minute
	grokRequestRefreshTimeout   = 8 * time.Second
	grokRefreshLockWaitTimeout  = 2 * time.Second
	grokRefreshLockPollInterval = 25 * time.Millisecond
)

var (
	errGrokOAuthRefreshNotConfigured = errors.New("grok oauth refresh is not configured")
	errGrokOAuthRefreshTokenMissing  = errors.New("grok oauth refresh token is missing")
	errGrokOAuthAccessTokenMissing   = errors.New("grok oauth access token is missing")
	errGrokOAuthAccessTokenExpired   = errors.New("grok oauth access token is expired")
	errGrokOAuthConfiguredProxyMiss  = errors.New("grok oauth configured proxy is missing")
)

type GrokTokenCache = GeminiTokenCache

type GrokTokenProvider struct {
	accountRepo      AccountRepository
	tokenCache       GrokTokenCache
	refreshAPI       *OAuthRefreshAPI
	executor         OAuthRefreshExecutor
	refreshPolicy    ProviderRefreshPolicy
	tempUnschedCache TempUnschedCache
}

func NewGrokTokenProvider(
	accountRepo AccountRepository,
	tokenCache GrokTokenCache,
) *GrokTokenProvider {
	return &GrokTokenProvider{
		accountRepo:   accountRepo,
		tokenCache:    tokenCache,
		refreshPolicy: GrokProviderRefreshPolicy(),
	}
}

func (p *GrokTokenProvider) SetRefreshAPI(api *OAuthRefreshAPI, executor OAuthRefreshExecutor) {
	p.refreshAPI = api
	p.executor = executor
}

func (p *GrokTokenProvider) SetRefreshPolicy(policy ProviderRefreshPolicy) {
	p.refreshPolicy = policy
}

func (p *GrokTokenProvider) SetTempUnschedCache(cache TempUnschedCache) {
	p.tempUnschedCache = cache
}

// @project-doc docs/interfaces/grok_upstream.md#grok_account_contract
func (p *GrokTokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformGrok || account.Type != AccountTypeOAuth {
		return "", errors.New("not a grok oauth account")
	}
	selectedProxyID := cloneGrokProxyID(account.ProxyID)
	if eligibilityErr := grokOAuthRequestAccountEligibilityError(account); eligibilityErr != nil {
		return "", withGrokCredentialFailureSnapshot(eligibilityErr, account)
	}

	expiresAt := account.GetCredentialAsTime("expires_at")
	accountAccessToken := strings.TrimSpace(account.GetGrokAccessToken())
	if accountAccessToken == "" {
		return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenMissing, account)
	}
	if strings.TrimSpace(account.GetGrokRefreshToken()) == "" {
		return "", withGrokCredentialFailureSnapshot(errGrokOAuthRefreshTokenMissing, account)
	}
	cacheKey := GrokTokenCacheKey(account)
	if p.tokenCache != nil {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil {
			cachedToken := strings.TrimSpace(token)
			if cachedToken != "" && accountAccessToken != "" && cachedToken == accountAccessToken &&
				expiresAt != nil && time.Until(*expiresAt) > grokTokenRefreshSkew {
				return cachedToken, nil
			}
		}
	}

	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= grokTokenRefreshSkew
	if needsRefresh {
		if p.refreshAPI == nil || p.executor == nil {
			return "", errGrokOAuthRefreshNotConfigured
		}
		refreshCtx, cancel := context.WithTimeout(ctx, grokRequestRefreshTimeout)
		defer cancel()
		result, err := p.refreshAPI.RefreshIfNeeded(withOAuthRefreshRequestPath(refreshCtx), account, p.executor, grokTokenRefreshSkew)
		if err != nil {
			if p.refreshPolicy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
		} else if result != nil && result.LockHeld {
			if p.refreshPolicy.OnLockHeld == ProviderLockHeldWaitForCache {
				token, waitErr := p.waitForRefreshedToken(refreshCtx, account, cacheKey)
				return token, withGrokCredentialFailureSnapshot(waitErr, account)
			}
			if expiresAt == nil || !time.Now().Before(*expiresAt) {
				return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenExpired, account)
			}
		} else if result != nil && result.Account != nil {
			if eligibilityErr := grokOAuthRequestAccountEligibilityError(result.Account); eligibilityErr != nil {
				return "", withGrokCredentialFailureSnapshot(eligibilityErr, result.Account)
			}
			if !grokCredentialProxyIDsEqual(result.Account.ProxyID, selectedProxyID) {
				return "", withGrokCredentialFailureSnapshot(errOAuthRefreshAccountStateChanged, result.Account)
			}
			account = result.Account
			expiresAt = account.GetCredentialAsTime("expires_at")
		}
	}

	accessToken := account.GetGrokAccessToken()
	if strings.TrimSpace(accessToken) == "" {
		return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenMissing, account)
	}
	if expiresAt != nil && !time.Now().Before(*expiresAt) {
		return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenExpired, account)
	}

	if p.tokenCache != nil {
		latestAccount, isStale := CheckTokenVersion(ctx, account, p.accountRepo)
		if isStale && latestAccount != nil {
			if eligibilityErr := grokOAuthRequestAccountEligibilityError(latestAccount); eligibilityErr != nil {
				return "", withGrokCredentialFailureSnapshot(eligibilityErr, latestAccount)
			}
			if !grokCredentialProxyIDsEqual(latestAccount.ProxyID, selectedProxyID) {
				return "", withGrokCredentialFailureSnapshot(errOAuthRefreshAccountStateChanged, latestAccount)
			}
			accessToken = latestAccount.GetGrokAccessToken()
			if strings.TrimSpace(accessToken) == "" {
				return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenMissing, latestAccount)
			}
			latestExpiry := latestAccount.GetCredentialAsTime("expires_at")
			if latestExpiry == nil || !time.Now().Before(*latestExpiry) {
				return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenExpired, latestAccount)
			}
		} else {
			ttl := 30 * time.Minute
			if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > grokTokenCacheSkew:
					ttl = until - grokTokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			_ = p.tokenCache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
		}
	}

	return accessToken, nil
}

// GetAccessTokenForManualTest 为管理员发起的“测试连接”返回访问令牌。它与
// GetAccessToken 不同，不应用请求路径的调度资格门（手动调度开关、限流、过载或
// 临时冷却），因为手动测试正是用来检查这些状态中的账号，与 Codex/OpenAI
// 测试不受调度状态影响的行为一致（#4598）。
//
// 凭据完整性检查仍然生效，包括配置代理缺失、共享刷新锁协议和刷新 API 的账号重读。
// 非 active（禁用或错误）账号的凭据轮换仍由 RefreshIfNeeded 拦截，其尚未过期的
// 令牌只用于原样探测。
func (p *GrokTokenProvider) GetAccessTokenForManualTest(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformGrok || account.Type != AccountTypeOAuth {
		return "", errors.New("not a grok oauth account")
	}
	if account.ProxyID != nil && account.Proxy == nil {
		return "", errGrokOAuthConfiguredProxyMiss
	}
	if strings.TrimSpace(account.GetGrokRefreshToken()) == "" {
		return "", errGrokOAuthRefreshTokenMissing
	}

	accessToken := strings.TrimSpace(account.GetGrokAccessToken())
	expiresAt := account.GetCredentialAsTime("expires_at")
	tokenValid := accessToken != "" && expiresAt != nil && time.Now().Before(*expiresAt)
	if accessToken != "" && expiresAt != nil && time.Until(*expiresAt) > grokTokenRefreshSkew {
		return accessToken, nil
	}

	if p.refreshAPI == nil || p.executor == nil {
		if tokenValid {
			return accessToken, nil
		}
		return "", errGrokOAuthRefreshNotConfigured
	}

	// 刻意不标记为请求路径刷新：该路径会在 RefreshIfNeeded 内再次应用调度资格，
	// 而这正是手动测试需要绕过的门禁。
	refreshCtx, cancel := context.WithTimeout(ctx, grokRequestRefreshTimeout)
	defer cancel()
	result, err := p.refreshAPI.RefreshIfNeeded(refreshCtx, account, p.executor, grokTokenRefreshSkew)
	if err != nil {
		if tokenValid {
			return accessToken, nil
		}
		return "", err
	}
	if result != nil && result.LockHeld {
		if tokenValid {
			return accessToken, nil
		}
		return "", errors.New("token refresh is already in progress on another worker; retry in a few seconds")
	}
	if result != nil && result.Account != nil {
		account = result.Account
	}

	accessToken = strings.TrimSpace(account.GetGrokAccessToken())
	if accessToken == "" {
		return "", errGrokOAuthAccessTokenMissing
	}
	if latestExpiry := account.GetCredentialAsTime("expires_at"); latestExpiry != nil && !time.Now().Before(*latestExpiry) {
		return "", errGrokOAuthAccessTokenExpired
	}
	return accessToken, nil
}

func (p *GrokTokenProvider) waitForRefreshedToken(ctx context.Context, account *Account, cacheKey string) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, grokRefreshLockWaitTimeout)
	defer cancel()

	initialToken := strings.TrimSpace(account.GetGrokAccessToken())
	initialVersion := account.GetCredentialAsInt64("_token_version")
	selectedProxyID := cloneGrokProxyID(account.ProxyID)
	sawAuthoritativeState := false
	var lastAccountReadErr error
	ticker := time.NewTicker(grokRefreshLockPollInterval)
	defer ticker.Stop()

	for {
		cachedToken := ""
		if p.tokenCache != nil {
			if token, err := p.tokenCache.GetAccessToken(waitCtx, cacheKey); err == nil {
				cachedToken = strings.TrimSpace(token)
			}
		}

		if p.accountRepo != nil {
			latest, err := p.accountRepo.GetByID(waitCtx, account.ID)
			if err != nil {
				lastAccountReadErr = err
			} else if latest == nil {
				return "", errOAuthRefreshAccountStateChanged
			} else {
				sawAuthoritativeState = true
				if eligibilityErr := grokOAuthRequestAccountEligibilityError(latest); eligibilityErr != nil {
					return "", withGrokCredentialFailureSnapshot(eligibilityErr, latest)
				}
				if !grokCredentialProxyIDsEqual(latest.ProxyID, selectedProxyID) {
					return "", withGrokCredentialFailureSnapshot(errOAuthRefreshAccountStateChanged, latest)
				}
				token := strings.TrimSpace(latest.GetGrokAccessToken())
				version := latest.GetCredentialAsInt64("_token_version")
				expiresAt := latest.GetCredentialAsTime("expires_at")
				changed := token != initialToken || (version > 0 && version > initialVersion)
				valid := expiresAt != nil && time.Now().Before(*expiresAt)
				if token != "" && changed && valid {
					// 带版本的数据库凭据是权威状态；旧缓存不得让请求继续使用过期令牌，需尽力修复。
					if cachedToken != "" && cachedToken != token {
						ttl := time.Until(*expiresAt)
						if ttl > grokTokenCacheSkew {
							ttl -= grokTokenCacheSkew
						}
						_ = p.tokenCache.SetAccessToken(waitCtx, cacheKey, token, ttl)
					}
					return token, nil
				}
			}
		}

		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if !sawAuthoritativeState {
				if lastAccountReadErr == nil {
					lastAccountReadErr = waitCtx.Err()
				}
				return "", fmt.Errorf("%w: %v", errOAuthRefreshAccountRereadFailed, lastAccountReadErr)
			}
			// 另一个 worker 仍持有刷新权且权威账号行未变，不隔离旧凭据；
			// 对方可能在本次有界等待结束后立即提交刷新结果。
			return "", errOAuthRefreshAccountStateChanged
		case <-ticker.C:
		}
	}
}

func grokOAuthRequestAccountEligibilityError(account *Account) error {
	if account == nil || !account.IsGrokOAuth() || !account.IsSchedulable() {
		return errOAuthRefreshAccountStateChanged
	}
	if account.ProxyID != nil && account.Proxy == nil {
		return errGrokOAuthConfiguredProxyMiss
	}
	return nil
}

func cloneGrokProxyID(proxyID *int64) *int64 {
	if proxyID == nil {
		return nil
	}
	value := *proxyID
	return &value
}

func (p *GrokTokenProvider) InvalidateToken(ctx context.Context, account *Account) error {
	if p == nil || p.tokenCache == nil || account == nil {
		return nil
	}
	return p.tokenCache.DeleteAccessToken(ctx, GrokTokenCacheKey(account))
}

func GrokTokenCacheKey(account *Account) string {
	if account == nil {
		return "grok:account:0"
	}
	return "grok:account:" + strconv.FormatInt(account.ID, 10)
}
