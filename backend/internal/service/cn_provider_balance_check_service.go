package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

const (
	// CNUsageMonitorSnapshotExtraKey 是国产供应商监控唯一持久化快照键。
	CNUsageMonitorSnapshotExtraKey  = "cn_usage_monitor_snapshot"
	cnUsageMonitorSnapshotVersion   = 1
	cnUsageMonitorLeaderLockKey     = "cn:usage:monitor:leader"
	cnUsageMonitorReasonPrefix      = "cn_usage_monitor:"
	cnUsageMonitorDefaultInterval   = 10 * time.Minute
	cnUsageMonitorDefaultTimeout    = 20 * time.Second
	cnUsageMonitorDefaultRound      = 5 * time.Minute
	cnUsageMonitorDefaultConcurrent = 4
)

// CNUsageMonitorError 记录最近一次探测失败，不包含凭据或原始响应正文。
type CNUsageMonitorError struct {
	Code       string    `json:"code"`
	ObservedAt time.Time `json:"observed_at"`
}

// CNUsageMonitorSnapshot 保存最近成功数据与最近一次尝试状态。失败只更新 LastError，
// 不覆盖上一次成功的余额或窗口。
type CNUsageMonitorSnapshot struct {
	Version       int                         `json:"version"`
	Adapter       string                      `json:"adapter"`
	IdentityHash  string                      `json:"identity_hash"`
	Provider      string                      `json:"provider,omitempty"`
	Mode          string                      `json:"mode,omitempty"`
	Unit          string                      `json:"unit,omitempty"`
	Balance       *UpstreamUsageAmount        `json:"balance,omitempty"`
	Balances      []UpstreamUsageBalanceEntry `json:"balances,omitempty"`
	Available     *bool                       `json:"available,omitempty"`
	Limits        []UpstreamUsageLimit        `json:"limits,omitempty"`
	Subscription  *UpstreamUsageSubscription  `json:"subscription,omitempty"`
	ExpiresAt     *time.Time                  `json:"expires_at,omitempty"`
	ObservedAt    *time.Time                  `json:"observed_at,omitempty"`
	LastAttemptAt time.Time                   `json:"last_attempt_at"`
	LastError     *CNUsageMonitorError        `json:"last_error,omitempty"`
}

// CNProviderBalanceCheckService 协调国产供应商用量监控。名称为兼容既有注入点保留，
// 查询实现统一复用 UpstreamUsageService 的纯适配器。
type CNProviderBalanceCheckService struct {
	accountRepo  AccountRepository
	snapshotRepo CNUsageMonitorSnapshotRepository
	usageService *UpstreamUsageService
	cfg          *config.Config
	interval     time.Duration
	probeTimeout time.Duration
	roundTimeout time.Duration
	concurrency  int
	lockCache    LeaderLockCache
	db           *sql.DB
	instanceID   string
	startOnce    sync.Once
	stopOnce     sync.Once
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// NewCNProviderBalanceCheckService 构造独立监控协调器。
func NewCNProviderBalanceCheckService(
	accountRepo AccountRepository,
	usageService *UpstreamUsageService,
	cfg *config.Config,
) *CNProviderBalanceCheckService {
	service := &CNProviderBalanceCheckService{
		accountRepo:  accountRepo,
		usageService: usageService,
		cfg:          cfg,
		interval:     cnUsageMonitorDefaultInterval,
		probeTimeout: cnUsageMonitorDefaultTimeout,
		roundTimeout: cnUsageMonitorDefaultRound,
		concurrency:  cnUsageMonitorDefaultConcurrent,
		instanceID:   uuid.NewString(),
	}
	if repository, ok := accountRepo.(CNUsageMonitorSnapshotRepository); ok {
		service.snapshotRepo = repository
	}
	if cfg != nil {
		settings := cfg.Gateway.CNProviders
		if settings.IntervalMinutes > 0 {
			service.interval = time.Duration(settings.IntervalMinutes) * time.Minute
		}
		if settings.ProbeTimeoutSeconds > 0 {
			service.probeTimeout = time.Duration(settings.ProbeTimeoutSeconds) * time.Second
		}
		if settings.RoundTimeoutSeconds > 0 {
			service.roundTimeout = time.Duration(settings.RoundTimeoutSeconds) * time.Second
		}
		if settings.Concurrency > 0 {
			service.concurrency = settings.Concurrency
		}
	}
	return service
}

// SetLeaderLock 注入现有跨实例锁，Redis 不可用时会回退数据库咨询锁。
func (s *CNProviderBalanceCheckService) SetLeaderLock(lockCache LeaderLockCache, db *sql.DB) {
	if s == nil {
		return
	}
	s.lockCache = lockCache
	s.db = db
}

// Start 仅在 monitor_enabled=true 时启动；首次探测等待一个完整周期。
func (s *CNProviderBalanceCheckService) Start() {
	if s == nil || s.accountRepo == nil || s.snapshotRepo == nil || s.usageService == nil ||
		s.cfg == nil || !s.cfg.Gateway.CNProviders.MonitorEnabled || s.interval <= 0 {
		return
	}
	s.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(s.interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					s.runOnce(ctx)
				case <-ctx.Done():
					return
				}
			}
		}()
	})
}

// Stop 取消当前整轮探测并等待工作协程退出。
func (s *CNProviderBalanceCheckService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
	s.wg.Wait()
}

func (s *CNProviderBalanceCheckService) runOnce(parents ...context.Context) {
	parent := context.Background()
	if len(parents) > 0 && parents[0] != nil {
		parent = parents[0]
	}
	if s == nil {
		return
	}
	if s.snapshotRepo == nil || s.usageService == nil {
		return
	}
	roundCtx, cancel := context.WithTimeout(parent, s.roundTimeout)
	defer cancel()
	release, acquired := tryAcquireSingletonLeaderLock(
		roundCtx,
		s.lockCache,
		s.db,
		cnUsageMonitorLeaderLockKey,
		s.instanceID,
		s.roundTimeout+30*time.Second,
	)
	if !acquired {
		return
	}
	defer release()

	accounts := s.monitorCandidates(roundCtx)
	group, groupCtx := errgroup.WithContext(roundCtx)
	group.SetLimit(s.concurrency)
	for i := range accounts {
		accountID := accounts[i].ID
		group.Go(func() error {
			s.probeOne(groupCtx, accountID)
			return nil
		})
	}
	_ = group.Wait()
}

func (s *CNProviderBalanceCheckService) monitorCandidates(ctx context.Context) []Account {
	result := make([]Account, 0)
	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax} {
		accounts, err := s.accountRepo.ListByPlatform(ctx, platform)
		if err != nil {
			slog.Warn("cn_usage_monitor_list_failed", "platform", platform, "error", err)
			continue
		}
		for i := range accounts {
			account := accounts[i]
			if account.Type != AccountTypeAPIKey || account.Status != StatusActive {
				continue
			}
			queryConfig, err := EffectiveUpstreamUsageConfig(&account)
			if err != nil || !queryConfig.Enabled || cnUpstreamUsageAdapterName(&account) == "" {
				continue
			}
			result = append(result, account)
		}
	}
	return result
}

func (s *CNProviderBalanceCheckService) probeOne(parent context.Context, accountID int64) {
	account, err := s.accountRepo.GetByID(parent, accountID)
	if err != nil || account == nil {
		return
	}
	queryConfig, err := EffectiveUpstreamUsageConfig(account)
	if err != nil || !queryConfig.Enabled {
		return
	}
	queryConfig.Adapter = cnUpstreamUsageAdapterName(account)
	if queryConfig.Adapter == "" {
		return
	}
	identityHash := upstreamUsageContextFingerprint(account, queryConfig)
	if err := s.validateMonitorHost(account, queryConfig); err != nil {
		s.persistAttempt(parent, account, queryConfig, identityHash, nil, err)
		return
	}

	probeCtx, cancel := context.WithTimeout(parent, s.probeTimeout)
	result, queryErr := s.usageService.QueryAccount(probeCtx, accountID)
	cancel()
	if queryErr == nil && result != nil {
		s.applyBalanceDecision(parent, accountID, identityHash, result)
	}
	current, err := s.accountRepo.GetByID(parent, accountID)
	if err != nil || current == nil {
		return
	}
	s.persistAttempt(parent, current, queryConfig, identityHash, result, queryErr)
}

func (s *CNProviderBalanceCheckService) persistAttempt(
	ctx context.Context,
	account *Account,
	queryConfig UpstreamUsageQueryConfig,
	identityHash string,
	result *UpstreamUsageQueryResult,
	queryErr error,
) {
	if account == nil || ctx.Err() != nil {
		return
	}
	currentConfig, err := EffectiveUpstreamUsageConfig(account)
	if err != nil {
		return
	}
	currentConfig.Adapter = cnUpstreamUsageAdapterName(account)
	if currentConfig != queryConfig || upstreamUsageContextFingerprint(account, currentConfig) != identityHash {
		return
	}
	now := time.Now().UTC()
	snapshot := cnUsageMonitorSnapshotFromExtra(account.Extra)
	if snapshot == nil || snapshot.Version != cnUsageMonitorSnapshotVersion || snapshot.IdentityHash != identityHash {
		snapshot = &CNUsageMonitorSnapshot{
			Version:      cnUsageMonitorSnapshotVersion,
			Adapter:      queryConfig.Adapter,
			IdentityHash: identityHash,
		}
	}
	snapshot.LastAttemptAt = now
	if queryErr != nil || result == nil {
		code := infraerrors.Reason(queryErr)
		if code == "" {
			code = "CN_USAGE_MONITOR_QUERY_FAILED"
		}
		snapshot.LastError = &CNUsageMonitorError{Code: code, ObservedAt: now}
	} else {
		observedAt := result.ObservedAt.UTC()
		snapshot.Adapter = result.Adapter
		snapshot.Provider = result.Provider
		snapshot.Mode = result.Mode
		snapshot.Unit = result.Unit
		snapshot.Balance = result.Balance
		snapshot.Balances = result.Balances
		snapshot.Available = result.Available
		snapshot.Limits = result.Limits
		snapshot.Subscription = result.Subscription
		snapshot.ExpiresAt = result.ExpiresAt
		snapshot.ObservedAt = &observedAt
		snapshot.LastError = nil
	}
	written, err := s.snapshotRepo.UpdateCNUsageMonitorSnapshotCAS(
		ctx,
		account.ID,
		account.UpdatedAt,
		snapshot,
		"",
	)
	if err != nil {
		slog.Warn("cn_usage_monitor_snapshot_failed", "account_id", account.ID, "error", err)
	} else if !written {
		slog.Debug("cn_usage_monitor_snapshot_stale", "account_id", account.ID)
	}
}

func (s *CNProviderBalanceCheckService) applyBalanceDecision(
	ctx context.Context,
	accountID int64,
	identityHash string,
	result *UpstreamUsageQueryResult,
) {
	if result == nil || result.Mode != "balance" || ctx.Err() != nil {
		return
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return
	}
	queryConfig, err := EffectiveUpstreamUsageConfig(account)
	if err != nil {
		return
	}
	queryConfig.Adapter = cnUpstreamUsageAdapterName(account)
	if upstreamUsageContextFingerprint(account, queryConfig) != identityHash {
		return
	}
	threshold := 0.5
	if s.cfg != nil {
		threshold = s.cfg.Gateway.CNProviders.BalanceThreshold
	}
	low, known := cnUsageBalanceBelowThreshold(result, threshold)
	if !known {
		return
	}
	reason := cnUsageMonitorReason(identityHash)
	if low {
		if !account.IsSchedulable() {
			return
		}
		until := time.Now().Add(2 * s.interval)
		if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
			slog.Warn("cn_usage_monitor_pause_failed", "account_id", account.ID, "error", err)
		}
		return
	}
	if account.TempUnschedulableUntil != nil && account.TempUnschedulableReason == reason {
		if err := s.accountRepo.ClearTempUnschedulable(ctx, account.ID); err != nil {
			slog.Warn("cn_usage_monitor_resume_failed", "account_id", account.ID, "error", err)
		}
	}
}

func cnUsageBalanceBelowThreshold(result *UpstreamUsageQueryResult, threshold float64) (bool, bool) {
	if result == nil || result.Mode != "balance" {
		return false, false
	}
	if result.Available != nil && !*result.Available {
		return true, true
	}
	if len(result.Balances) > 0 {
		for _, balance := range result.Balances {
			if balance.Remaining >= threshold {
				return false, true
			}
		}
		return true, true
	}
	if result.Balance == nil || result.Balance.Remaining == nil {
		return false, false
	}
	return *result.Balance.Remaining < threshold, true
}

func cnUsageMonitorReason(identityHash string) string {
	return cnUsageMonitorReasonPrefix + identityHash + ": 余额低于监控阈值"
}

func cnUsageMonitorSnapshotFromExtra(extra map[string]any) *CNUsageMonitorSnapshot {
	if len(extra) == 0 || extra[CNUsageMonitorSnapshotExtraKey] == nil {
		return nil
	}
	payload, err := json.Marshal(extra[CNUsageMonitorSnapshotExtraKey])
	if err != nil {
		return nil
	}
	var snapshot CNUsageMonitorSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil
	}
	return &snapshot
}

// validCNUsageMonitorSnapshot 只返回与账号当前完整查询身份匹配的快照。
func validCNUsageMonitorSnapshot(account *Account) *CNUsageMonitorSnapshot {
	if account == nil || !account.IsCNProvider() {
		return nil
	}
	queryConfig, err := EffectiveUpstreamUsageConfig(account)
	if err != nil {
		return nil
	}
	queryConfig.Adapter = cnUpstreamUsageAdapterName(account)
	if queryConfig.Adapter == "" {
		return nil
	}
	snapshot := cnUsageMonitorSnapshotFromExtra(account.Extra)
	if snapshot == nil || snapshot.Version != cnUsageMonitorSnapshotVersion ||
		snapshot.Adapter != queryConfig.Adapter ||
		snapshot.IdentityHash != upstreamUsageContextFingerprint(account, queryConfig) {
		return nil
	}
	return snapshot
}

func cnUsageMonitorIdentityFingerprint(account *Account) string {
	if account == nil || !account.IsCNProvider() || account.Type != AccountTypeAPIKey {
		return ""
	}
	queryConfig, err := EffectiveUpstreamUsageConfig(account)
	if err != nil {
		return ""
	}
	queryConfig.Adapter = cnUpstreamUsageAdapterName(account)
	if queryConfig.Adapter == "" {
		return ""
	}
	return upstreamUsageContextFingerprint(account, queryConfig)
}

func (s *CNProviderBalanceCheckService) validateMonitorHost(
	account *Account,
	queryConfig UpstreamUsageQueryConfig,
) error {
	baseURL := strings.TrimSpace(queryConfig.BaseURL)
	if baseURL == "" {
		baseURL = upstreamUsageAccountBaseURL(account)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() == "" {
		return ErrUpstreamUsageConfigInvalid
	}
	host := strings.ToLower(parsed.Hostname())
	if cnUsageOfficialHost(account.Platform, host) {
		return nil
	}
	if s.cfg == nil || !s.cfg.Security.URLAllowlist.Enabled {
		return errors.New("CN_USAGE_MONITOR_CUSTOM_HOST_NOT_ALLOWLISTED")
	}
	_, err = urlvalidator.ValidateHTTPURL(
		baseURL,
		s.cfg.Security.URLAllowlist.AllowInsecureHTTP,
		urlvalidator.ValidationOptions{
			AllowedHosts:     s.cfg.Security.URLAllowlist.UpstreamHosts,
			RequireAllowlist: true,
			AllowPrivate:     s.cfg.Security.URLAllowlist.AllowPrivateHosts,
		},
	)
	if err != nil {
		return fmt.Errorf("CN_USAGE_MONITOR_CUSTOM_HOST_NOT_ALLOWLISTED: %w", err)
	}
	return nil
}

func cnUsageOfficialHost(platform, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	switch platform {
	case PlatformKimi:
		return host == "api.moonshot.cn" || host == "api.kimi.com"
	case PlatformZhipu:
		return host == "open.bigmodel.cn" || host == "api.z.ai"
	case PlatformDeepseek:
		return host == "api.deepseek.com"
	case PlatformMiniMax:
		// 国内新旧域名与国际站均为官方入口，仍由统一 HTTP 客户端执行 URL 安全校验。
		return host == "api.minimax.io" || host == "api.minimax.cn" || host == "api.minimaxi.com"
	default:
		return false
	}
}
