package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OAuthRefreshExecutor 各平台实现的 OAuth 刷新执行器
// TokenRefresher 接口的超集：增加了 CacheKey 方法用于分布式锁
type OAuthRefreshExecutor interface {
	TokenRefresher

	// CacheKey 返回用于分布式锁的缓存键（与 TokenProvider 使用的一致）
	CacheKey(account *Account) string
}

// GrokOAuthRefreshSuccessRepository 是 Grok 上游凭据轮换的持久化边界。
// 实现必须比较上游尝试使用的完整凭据和代理，并将成功更新与调度器失效事件原子发布。
type GrokOAuthRefreshSuccessRepository interface {
	UpdateGrokOAuthCredentialsIfUnchanged(
		ctx context.Context,
		id int64,
		expectedCredentials map[string]any,
		expectedProxyID *int64,
		credentials map[string]any,
	) (bool, error)
}

const (
	defaultRefreshLockTTL                   = 60 * time.Second
	defaultRefreshLockReleaseTimeout        = 2 * time.Second
	defaultRefreshPostPersistCleanupTimeout = 2 * time.Second
)

var (
	errOAuthRefreshAccountRereadFailed = errors.New("oauth refresh account reread failed")
	errOAuthRefreshAccountStateChanged = errors.New("oauth refresh account state changed")
	errOAuthRefreshCredentialPersist   = errors.New("oauth refresh credential persistence failed")
)

type oauthRefreshRequestPathKey struct{}

func withOAuthRefreshRequestPath(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauthRefreshRequestPathKey{}, true)
}

func isOAuthRefreshRequestPath(ctx context.Context) bool {
	requestPath, _ := ctx.Value(oauthRefreshRequestPathKey{}).(bool)
	return requestPath
}

type contextMutex struct {
	token chan struct{}
}

// 保留 #4212 引入的请求路径凭据锁接口，并与池刷新共享可感知上下文的互斥实现。
type oauthRefreshLocalLock = contextMutex

func newOAuthRefreshLocalLock() *oauthRefreshLocalLock {
	return newContextMutex()
}

type oauthRefreshStateUnavailableError struct {
	err error
}

func (e *oauthRefreshStateUnavailableError) Error() string {
	return "OAuth refresh account state is unavailable"
}

func (e *oauthRefreshStateUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newContextMutex() *contextMutex {
	return &contextMutex{token: make(chan struct{}, 1)}
}

func (m *contextMutex) Lock(ctx context.Context) error {
	select {
	case m.token <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *contextMutex) Unlock() {
	<-m.token
}

// OAuthRefreshResult 统一刷新结果
type OAuthRefreshResult struct {
	Refreshed      bool           // 实际执行了刷新
	NewCredentials map[string]any // 刷新后的 credentials（nil 表示未刷新）
	Account        *Account       // 成功时为最新 account；刷新错误时为实际尝试的凭据快照
	LockHeld       bool           // 锁被其他 worker 持有（未执行刷新）
}

func snapshotOAuthRefreshAccount(account *Account) *Account {
	if account == nil {
		return nil
	}
	snapshot := *account
	snapshot.Credentials = shallowCopyMap(account.Credentials)
	if account.ProxyID != nil {
		proxyID := *account.ProxyID
		snapshot.ProxyID = &proxyID
	}
	return &snapshot
}

// OAuthRefreshAPI 统一的 OAuth Token 刷新入口
// 封装分布式锁、进程内互斥锁、DB 重读、已刷新检查、竞争恢复等通用逻辑
type OAuthRefreshAPI struct {
	accountRepo AccountRepository
	tokenCache  GeminiTokenCache // 可选，nil = 无分布式锁
	lockTTL     time.Duration
	localLocks  sync.Map // key: cacheKey string -> value: *contextMutex
}

// NewOAuthRefreshAPI 创建统一刷新 API
// 可选传入 lockTTL 覆盖默认的 60s 分布式锁 TTL
func NewOAuthRefreshAPI(accountRepo AccountRepository, tokenCache GeminiTokenCache, lockTTL ...time.Duration) *OAuthRefreshAPI {
	ttl := defaultRefreshLockTTL
	if len(lockTTL) > 0 && lockTTL[0] > 0 {
		ttl = lockTTL[0]
	}
	return &OAuthRefreshAPI{
		accountRepo: accountRepo,
		tokenCache:  tokenCache,
		lockTTL:     ttl,
	}
}

// getLocalLock 返回指定 cacheKey 的进程内互斥锁
func (api *OAuthRefreshAPI) getLocalLock(cacheKey string) *contextMutex {
	actual, _ := api.localLocks.LoadOrStore(cacheKey, newContextMutex())
	mu, ok := actual.(*contextMutex)
	if !ok {
		mu = newContextMutex()
		api.localLocks.Store(cacheKey, mu)
	}
	return mu
}

// RefreshIfNeeded 在分布式锁保护下按需刷新 OAuth token
//
// 流程:
//  1. 获取分布式锁
//  2. 从 DB 重读最新 account（防止使用过时的 refresh_token）
//  3. 二次检查是否仍需刷新
//  4. 调用 executor.Refresh() 执行平台特定刷新逻辑
//  5. 设置 _token_version + 更新 DB
//  6. 释放锁
func (api *OAuthRefreshAPI) RefreshIfNeeded(
	ctx context.Context,
	account *Account,
	executor OAuthRefreshExecutor,
	refreshWindow time.Duration,
) (*OAuthRefreshResult, error) {
	if api == nil || api.accountRepo == nil {
		return nil, errors.New("oauth refresh account repository is not configured")
	}
	if account == nil {
		return nil, errors.New("oauth refresh account is nil")
	}
	if executor == nil {
		return nil, errors.New("oauth refresh executor is nil")
	}
	requestPath := isOAuthRefreshRequestPath(ctx)
	cacheKey := executor.CacheKey(account)

	// 0. 获取进程内互斥锁（防止同一进程内的并发刷新竞争）
	localMu := api.getLocalLock(cacheKey)
	if err := localMu.Lock(ctx); err != nil {
		return nil, fmt.Errorf("oauth refresh local lock: %w", err)
	}
	defer localMu.Unlock()

	// 1. 获取分布式锁
	if api.tokenCache != nil {
		acquired, lockErr := api.tokenCache.AcquireRefreshLock(ctx, cacheKey, api.lockTTL)
		if lockErr != nil {
			// Redis 错误，降级为无锁刷新（进程内互斥锁仍生效）
			slog.Warn("oauth_refresh_lock_failed_degraded",
				"account_id", account.ID,
				"cache_key", cacheKey,
				"error", lockErr,
			)
		} else if !acquired {
			// 锁被其他 worker 持有
			return &OAuthRefreshResult{LockHeld: true}, nil
		} else {
			defer api.releaseRefreshLock(ctx, cacheKey)
		}
	}

	// 2. 从 DB 重读最新 account（锁保护下，确保使用最新的 refresh_token）
	freshAccount, err := api.accountRepo.GetByID(ctx, account.ID)
	if err != nil {
		if requestPath {
			return nil, fmt.Errorf("%w: %v", errOAuthRefreshAccountRereadFailed, err)
		}
		return nil, &oauthRefreshStateUnavailableError{err: err}
	}
	if freshAccount == nil {
		if requestPath {
			return nil, fmt.Errorf("%w: account not found", errOAuthRefreshAccountStateChanged)
		}
		return nil, &oauthRefreshStateUnavailableError{err: fmt.Errorf("account not found")}
	}
	if freshAccount.ID != account.ID {
		return nil, fmt.Errorf("%w: account identity mismatch", errOAuthRefreshAccountRereadFailed)
	}
	// 请求路径对状态变化安全失败；后台路径跳过已停用账号，显式管理端刷新使用独立入口。
	if !freshAccount.IsActive() {
		if requestPath {
			return nil, fmt.Errorf("%w: account is not active", errOAuthRefreshAccountStateChanged)
		}
		return &OAuthRefreshResult{Account: freshAccount}, nil
	}
	if requestPath && freshAccount.Platform == PlatformGrok {
		if eligibilityErr := grokOAuthRequestAccountEligibilityError(freshAccount); eligibilityErr != nil {
			return nil, withGrokCredentialFailureSnapshot(eligibilityErr, freshAccount)
		}
	}
	if !executor.CanRefresh(freshAccount) {
		if requestPath && freshAccount.IsGrokOAuth() && strings.TrimSpace(freshAccount.GetGrokRefreshToken()) == "" {
			return nil, withGrokCredentialFailureSnapshot(errGrokOAuthRefreshTokenMissing, freshAccount)
		}
		if requestPath {
			return nil, fmt.Errorf("%w: account is no longer refreshable", errOAuthRefreshAccountStateChanged)
		}
		return &OAuthRefreshResult{Account: freshAccount}, nil
	}

	// 3. 二次检查是否仍需刷新（另一条路径可能已刷新）
	if !executor.NeedsRefresh(freshAccount, refreshWindow) {
		return &OAuthRefreshResult{
			Account: freshAccount,
		}, nil
	}

	// 4. 执行平台特定刷新逻辑
	attemptedAccount := snapshotOAuthRefreshAccount(freshAccount)
	newCredentials, refreshErr := executor.Refresh(ctx, freshAccount)
	if ctxErr := ctx.Err(); ctxErr != nil {
		// 上游实现可能忽略取消并延迟返回凭据，超过尝试或周期边界后不得持久化。
		return nil, ctxErr
	}
	if refreshErr != nil {
		// 竞争恢复：refresh token 被拒绝可能是另一个 worker 已消费了旧 refresh_token
		// 重新读取 DB，如果 refresh_token 已更新则说明是竞争，返回成功
		if isInvalidGrantError(refreshErr) {
			if recoveredAccount, recovered := api.tryRecoverFromRefreshRace(ctx, freshAccount); recovered {
				if requestPath && recoveredAccount.Platform == PlatformGrok {
					if eligibilityErr := grokOAuthRequestAccountEligibilityError(recoveredAccount); eligibilityErr != nil {
						return nil, withGrokCredentialFailureSnapshot(eligibilityErr, recoveredAccount)
					}
				}
				slog.Info("oauth_refresh_race_recovered",
					"account_id", freshAccount.ID,
					"platform", freshAccount.Platform,
				)
				return &OAuthRefreshResult{
					Account: recoveredAccount,
				}, nil
			}
		}
		// 保留失败上游调用使用的精确账号快照，使调用方只条件更新该凭据版本，避免隔离并发重新授权的账号。
		result := &OAuthRefreshResult{Account: attemptedAccount}
		if requestPath && attemptedAccount.Platform == PlatformGrok {
			return result, withGrokCredentialFailureSnapshot(refreshErr, attemptedAccount)
		}
		return result, refreshErr
	}

	// 5. 设置版本号 + 更新 DB
	if newCredentials != nil {
		// 克隆 map 避免修改 executor.Refresh() 返回的共享 map
		cloned := make(map[string]any, len(newCredentials)+1)
		for k, v := range newCredentials {
			cloned[k] = v
		}
		cloned["_token_version"] = time.Now().UnixMilli()
		newCredentials = cloned

		if freshAccount.IsGrokOAuth() {
			conditionalRepo, ok := api.accountRepo.(GrokOAuthRefreshSuccessRepository)
			if !ok {
				return nil, &providerConfigurationRefreshError{
					err: fmt.Errorf("grok OAuth refresh success CAS repository is not configured"),
				}
			}
			applied, updateErr := conditionalRepo.UpdateGrokOAuthCredentialsIfUnchanged(
				ctx,
				freshAccount.ID,
				attemptedAccount.Credentials,
				attemptedAccount.ProxyID,
				newCredentials,
			)
			if updateErr != nil {
				slog.Error("oauth_refresh_update_failed",
					"account_id", freshAccount.ID,
					"platform", freshAccount.Platform,
					"error", updateErr,
				)
				// 上游可能已轮换并消费 refresh token，本地持久化结果不明时重试会让健康账号变成 invalid_grant。
				return nil, &providerCycleContainmentRefreshError{
					err: fmt.Errorf("OAuth refresh succeeded but credential persistence failed: %w", updateErr),
				}
			}
			if !applied {
				currentAccount, readErr := api.accountRepo.GetByID(ctx, freshAccount.ID)
				if readErr != nil || currentAccount == nil {
					if readErr == nil {
						readErr = fmt.Errorf("account not found after Grok OAuth success CAS miss")
					}
					return nil, &providerCycleContainmentRefreshError{
						err: fmt.Errorf("grok OAuth success CAS lost and current state is unavailable: %w", readErr),
					}
				}
				slog.Info("oauth_refresh_success_cas_skipped_stale_credentials",
					"account_id", freshAccount.ID,
					"platform", freshAccount.Platform,
				)
				return &OAuthRefreshResult{Account: currentAccount}, nil
			}
			durableAccount, readErr := api.loadGrokDurableAccountAfterPersist(ctx, cacheKey, freshAccount.ID)
			if readErr != nil || durableAccount == nil {
				if readErr == nil {
					readErr = fmt.Errorf("account not found after Grok OAuth success CAS")
				}
				return nil, &providerCycleContainmentRefreshError{
					err: fmt.Errorf("grok OAuth success persisted but durable account state is unavailable: %w", readErr),
				}
			}
			// CAS 只修改凭据；返回持久化后的最新行，避免并发管理或调度变更被旧快照覆盖。
			freshAccount = durableAccount
		} else if updateErr := persistAccountCredentials(ctx, api.accountRepo, freshAccount, newCredentials); updateErr != nil {
			slog.Error("oauth_refresh_update_failed",
				"account_id", freshAccount.ID,
				"error", updateErr,
			)
			return nil, fmt.Errorf("%w: %v", errOAuthRefreshCredentialPersist, updateErr)
		}
	}

	if requestPath && freshAccount.Platform == PlatformGrok {
		if eligibilityErr := grokOAuthRequestAccountEligibilityError(freshAccount); eligibilityErr != nil {
			return nil, withGrokCredentialFailureSnapshot(eligibilityErr, freshAccount)
		}
	}

	return &OAuthRefreshResult{
		Refreshed:      true,
		NewCredentials: newCredentials,
		Account:        freshAccount,
	}, nil
}

func (api *OAuthRefreshAPI) releaseRefreshLock(parent context.Context, cacheKey string) {
	cleanupParent := context.Background()
	if parent != nil {
		cleanupParent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(cleanupParent, defaultRefreshLockReleaseTimeout)
	defer cancel()
	if err := api.tokenCache.ReleaseRefreshLock(ctx, cacheKey); err != nil {
		slog.Warn("oauth_refresh_lock_release_failed", "cache_key", cacheKey, "error", err)
	}
}

func (api *OAuthRefreshAPI) loadGrokDurableAccountAfterPersist(parent context.Context, cacheKey string, accountID int64) (*Account, error) {
	cleanupParent := context.Background()
	if parent != nil {
		cleanupParent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(cleanupParent, defaultRefreshPostPersistCleanupTimeout)
	defer cancel()

	// 成功轮换可能撤销旧凭据对应的缓存 access token，即使父上下文刚被取消也要在提交边界删除。
	if api.tokenCache != nil {
		if err := api.tokenCache.DeleteAccessToken(ctx, cacheKey); err != nil {
			slog.Warn("oauth_refresh_post_persist_cache_delete_failed",
				"account_id", accountID,
				"cache_key", cacheKey,
				"error", err,
			)
		}
	}

	return api.accountRepo.GetByID(ctx, accountID)
}

// isInvalidGrantError 检查错误是否表示 refresh token 已失效或已被消费。
func isInvalidGrantError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "refresh_token_reused") ||
		strings.Contains(msg, "refresh token has already been used")
}

// tryRecoverFromRefreshRace 在 refresh token 被拒绝后尝试竞争恢复
// 重新读取 DB，如果 refresh_token 已改变（说明另一个 worker 成功刷新），则返回更新后的 account
func (api *OAuthRefreshAPI) tryRecoverFromRefreshRace(ctx context.Context, usedAccount *Account) (*Account, bool) {
	if api.accountRepo == nil {
		return nil, false
	}
	reReadAccount, err := api.accountRepo.GetByID(ctx, usedAccount.ID)
	if err != nil || reReadAccount == nil {
		return nil, false
	}
	usedRT := usedAccount.GetCredential("refresh_token")
	currentRT := reReadAccount.GetCredential("refresh_token")
	if usedRT == "" || currentRT == "" {
		return nil, false
	}
	// refresh_token 不同 → 另一个 worker 已成功刷新
	if usedRT != currentRT {
		return reReadAccount, true
	}
	return nil, false
}

// MergeCredentials 将旧 credentials 中不存在于新 map 的字段保留到新 map 中
func MergeCredentials(oldCreds, newCreds map[string]any) map[string]any {
	if newCreds == nil {
		newCreds = make(map[string]any)
	}
	for k, v := range oldCreds {
		if _, exists := newCreds[k]; !exists {
			newCreds[k] = v
		}
	}
	return newCreds
}

// BuildClaudeAccountCredentials 为 Claude 平台构建 OAuth credentials map
// 消除 Claude 平台没有 BuildAccountCredentials 方法的问题
func BuildClaudeAccountCredentials(tokenInfo *TokenInfo) map[string]any {
	creds := map[string]any{
		"access_token": tokenInfo.AccessToken,
		"token_type":   tokenInfo.TokenType,
		"expires_in":   strconv.FormatInt(tokenInfo.ExpiresIn, 10),
		"expires_at":   strconv.FormatInt(tokenInfo.ExpiresAt, 10),
	}
	if tokenInfo.RefreshToken != "" {
		creds["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.Scope != "" {
		creds["scope"] = tokenInfo.Scope
	}
	return creds
}
