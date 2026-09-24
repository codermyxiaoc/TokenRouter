package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

const (
	groupAvailabilityProbeDefaultMaxWorkers = 5
	// 最长运行时间覆盖合法配置下的全部尝试，并为账号选择和结果保存预留一分钟。
	groupAvailabilityProbeMaxRunDuration     = time.Duration(maxGroupAvailabilityProbeMaxRetries+1)*time.Duration(maxGroupAvailabilityProbeTimeoutSeconds)*time.Second + time.Minute
	groupAvailabilityProbeMaintenanceTimeout = time.Minute
	groupAvailabilityProbeClaimTimeout       = time.Minute
	groupAvailabilityProbeLockDuration       = groupAvailabilityProbeMaxRunDuration + groupAvailabilityProbeClaimTimeout + time.Minute
	groupAvailabilityProbeCleanupRetention   = 90 * 24 * time.Hour
	groupAvailabilityProbeCleanupMinInterval = 12 * time.Hour
)

// GroupAvailabilityProbeRunnerService 定期执行分组主动可用性探测。
type GroupAvailabilityProbeRunnerService struct {
	repo            GroupAvailabilityProbeRepository
	accountTestSvc  *AccountTestService
	gatewaySvc      *GatewayService
	openAIGateway   *OpenAIGatewayService
	geminiCompatSvc *GeminiMessagesCompatService
	cfg             *config.Config

	instanceID    string
	cron          *cron.Cron
	lastCleanupAt time.Time
	runMu         sync.Mutex
	manualMu      sync.Mutex
	manualRunning int
	startOnce     sync.Once
	stopOnce      sync.Once
}

// RunOnce 原子保存配置后执行一次探测；无论成功失败都写入渠道可用性，手动调用不追加重试。
func (s *GroupAvailabilityProbeRunnerService) RunOnce(ctx context.Context, groupID int64, config GroupAvailabilityProbeConfig) (*GroupAvailabilityProbeResult, error) {
	if s == nil || s.repo == nil || s.accountTestSvc == nil {
		return nil, infraerrors.ServiceUnavailable("GROUP_AVAILABILITY_PROBE_UNAVAILABLE", "Availability probe service is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !config.Enabled {
		return nil, infraerrors.BadRequest(invalidGroupAvailabilityProbeConfigReason, "Enable availability probing before testing")
	}
	config, err := normalizeGroupAvailabilityProbeConfigForAdminWrite(config)
	if err != nil {
		return nil, err
	}
	// 除分组级数据库租约外，限制管理员并行探测总数，避免跨分组点击耗尽实例资源。
	s.manualMu.Lock()
	if s.manualRunning >= groupAvailabilityProbeDefaultMaxWorkers {
		s.manualMu.Unlock()
		return nil, infraerrors.Conflict("GROUP_AVAILABILITY_PROBE_BUSY", "Availability probes are busy; please try again later")
	}
	s.manualRunning++
	s.manualMu.Unlock()
	defer func() {
		s.manualMu.Lock()
		s.manualRunning--
		s.manualMu.Unlock()
	}()
	now := time.Now()
	claimCtx, cancelClaim := context.WithTimeout(ctx, 10*time.Second)
	due, err := s.repo.ClaimGroup(claimCtx, groupID, config, now, now.Add(time.Duration(maxGroupAvailabilityProbeTimeoutSeconds)*time.Second+time.Minute), s.instanceID)
	cancelClaim()
	if err != nil {
		return nil, err
	}
	if due == nil {
		return nil, infraerrors.Conflict("GROUP_AVAILABILITY_PROBE_BUSY", "This group is already being tested or is no longer enabled")
	}
	// 领取后独立完成有界测试及保存，关闭弹窗或客户端断连不能丢掉已经发生的探测。
	probeConfig, configErr := normalizeGroupAvailabilityProbeConfig(due.Config)
	var result *GroupAvailabilityProbeResult
	if configErr != nil {
		result = &GroupAvailabilityProbeResult{GroupID: groupID, ModelID: due.Config.ModelID, Protocol: due.Config.Protocol,
			Status: GroupAvailabilityProbeStatusFailed, ErrorMessage: configErr.Error(), StartedAt: now, FinishedAt: time.Now()}
	} else {
		attemptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(probeConfig.TimeoutSeconds)*time.Second)
		result = s.runProbeAttempt(attemptCtx, *due, probeConfig)
		cancel()
	}
	interval := probeConfig.IntervalMinutes
	if interval <= 0 {
		interval = defaultGroupAvailabilityProbeIntervalMinutes
	}
	saveCtx, cancelSave := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelSave()
	if err := s.repo.SaveResultAndScheduleNext(saveCtx, result, time.Now().Add(time.Duration(interval)*time.Minute)); err != nil {
		return nil, fmt.Errorf("save manual availability probe result: %w", err)
	}
	return result, nil
}

func NewGroupAvailabilityProbeRunnerService(
	repo GroupAvailabilityProbeRepository,
	accountTestSvc *AccountTestService,
	gatewaySvc *GatewayService,
	openAIGateway *OpenAIGatewayService,
	geminiCompatSvc *GeminiMessagesCompatService,
	cfg *config.Config,
) *GroupAvailabilityProbeRunnerService {
	return &GroupAvailabilityProbeRunnerService{
		repo:            repo,
		accountTestSvc:  accountTestSvc,
		gatewaySvc:      gatewaySvc,
		openAIGateway:   openAIGateway,
		geminiCompatSvc: geminiCompatSvc,
		cfg:             cfg,
		instanceID:      uuid.NewString(),
	}
}

func (s *GroupAvailabilityProbeRunnerService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		if s.repo == nil || s.accountTestSvc == nil {
			logger.LegacyPrintf("service.group_availability_probe", "[GroupAvailabilityProbe] not started (missing dependencies)")
			return
		}

		loc := time.Local
		if s.cfg != nil {
			if parsed, err := time.LoadLocation(s.cfg.Timezone); err == nil && parsed != nil {
				loc = parsed
			}
		}

		c := cron.New(cron.WithParser(scheduledTestCronParser), cron.WithLocation(loc))
		if _, err := c.AddFunc("* * * * *", func() { s.runDue() }); err != nil {
			logger.LegacyPrintf("service.group_availability_probe", "[GroupAvailabilityProbe] not started (invalid schedule): %v", err)
			return
		}
		s.cron = c
		s.cron.Start()
		logger.LegacyPrintf("service.group_availability_probe", "[GroupAvailabilityProbe] started (tick=every minute)")
	})
}

func (s *GroupAvailabilityProbeRunnerService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cron == nil {
			return
		}
		ctx := s.cron.Stop()
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
			logger.LegacyPrintf("service.group_availability_probe", "[GroupAvailabilityProbe] cron stop timed out")
		}
	})
}

// @project-doc docs/interfaces/model_catalog_and_marketplace.md#group_availability_probe
func (s *GroupAvailabilityProbeRunnerService) runDue() {
	// cron 每分钟触发一次；上一轮尚未结束时跳过本轮，避免突破实例级 worker 上限。
	if !s.runMu.TryLock() {
		return
	}
	defer s.runMu.Unlock()

	// 清理与领取使用独立短上下文，不能消耗分组探测本身的重试预算。
	maintenanceCtx, maintenanceCancel := context.WithTimeout(context.Background(), groupAvailabilityProbeMaintenanceTimeout)
	s.cleanupIfNeeded(maintenanceCtx, time.Now())
	maintenanceCancel()

	// 只领取能够立即交给 worker 的分组，确保领取租约不会在排队期间过期。
	claimCtx, claimCancel := context.WithTimeout(context.Background(), groupAvailabilityProbeClaimTimeout)
	claimNow := time.Now()
	dueGroups, err := s.repo.ClaimDue(claimCtx, claimNow, claimNow.Add(groupAvailabilityProbeLockDuration), s.instanceID, groupAvailabilityProbeDefaultMaxWorkers)
	claimCancel()
	if err != nil {
		logger.LegacyPrintf("service.group_availability_probe", "[GroupAvailabilityProbe] ClaimDue error: %v", err)
		return
	}
	if len(dueGroups) == 0 {
		return
	}

	sem := make(chan struct{}, groupAvailabilityProbeDefaultMaxWorkers)
	var wg sync.WaitGroup
	for i := range dueGroups {
		group := dueGroups[i]
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// 每个分组独立计时，数据库维护和其它分组不会挤占它的合法重试窗口。
			probeCtx, probeCancel := context.WithTimeout(context.Background(), groupAvailabilityProbeMaxRunDuration)
			defer probeCancel()
			s.runOne(probeCtx, group)
		}()
	}
	wg.Wait()
}

func (s *GroupAvailabilityProbeRunnerService) cleanupIfNeeded(ctx context.Context, now time.Time) {
	if now.Sub(s.lastCleanupAt) < groupAvailabilityProbeCleanupMinInterval {
		return
	}
	s.lastCleanupAt = now
	if err := s.repo.CleanupOldResults(ctx, now.Add(-groupAvailabilityProbeCleanupRetention)); err != nil {
		logger.LegacyPrintf("service.group_availability_probe", "[GroupAvailabilityProbe] cleanup error: %v", err)
	}
}

func (s *GroupAvailabilityProbeRunnerService) runOne(ctx context.Context, due GroupAvailabilityProbeDueGroup) {
	probeConfig, err := normalizeGroupAvailabilityProbeConfig(due.Config)
	if err != nil {
		s.saveFailure(ctx, due, probeConfig, fmt.Sprintf("invalid probe config: %v", err), nil)
		return
	}
	if !probeConfig.Enabled {
		return
	}

	maxRetries := defaultGroupAvailabilityProbeMaxRetries
	if probeConfig.MaxRetries != nil {
		maxRetries = *probeConfig.MaxRetries
	}
	probeResult := runGroupAvailabilityProbeAttempts(
		ctx,
		maxRetries,
		time.Duration(probeConfig.TimeoutSeconds)*time.Second,
		func(attemptCtx context.Context) *GroupAvailabilityProbeResult {
			return s.runProbeAttempt(attemptCtx, due, probeConfig)
		},
	)
	if probeResult == nil {
		s.saveFailure(ctx, due, probeConfig, "probe did not produce a result", nil)
		return
	}

	s.saveResult(ctx, due, probeConfig, probeResult)
}

// runGroupAvailabilityProbeAttempts 执行首次探测及后续重试，任一尝试成功即结束。
// 每次尝试使用独立超时，避免前一次超时后的已取消上下文阻断后续重试。
func runGroupAvailabilityProbeAttempts(
	ctx context.Context,
	maxRetries int,
	timeout time.Duration,
	runAttempt func(context.Context) *GroupAvailabilityProbeResult,
) *GroupAvailabilityProbeResult {
	if maxRetries < 0 {
		maxRetries = 0
	}

	var lastResult *GroupAvailabilityProbeResult
	for retryCount := 0; retryCount <= maxRetries; retryCount++ {
		attemptCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		lastResult = runAttempt(attemptCtx)
		cancel()

		if lastResult != nil && lastResult.Success {
			return lastResult
		}
		if ctx.Err() != nil {
			break
		}
	}
	return lastResult
}

// runProbeAttempt 完成一次账号选择和真实请求，并转换为统一的分组探测结果。
func (s *GroupAvailabilityProbeRunnerService) runProbeAttempt(ctx context.Context, due GroupAvailabilityProbeDueGroup, probeConfig GroupAvailabilityProbeConfig) *GroupAvailabilityProbeResult {
	startedAt := time.Now()
	account, err := s.selectProbeAccount(ctx, due, probeConfig.ModelID)
	if err != nil {
		finishedAt := time.Now()
		return &GroupAvailabilityProbeResult{
			GroupID:      due.GroupID,
			ModelID:      probeConfig.ModelID,
			Protocol:     probeConfig.Protocol,
			Status:       GroupAvailabilityProbeStatusFailed,
			Success:      false,
			LatencyMs:    finishedAt.Sub(startedAt).Milliseconds(),
			ErrorMessage: err.Error(),
			StartedAt:    startedAt,
			FinishedAt:   finishedAt,
		}
	}

	result, err := s.accountTestSvc.RunTestBackgroundWithPromptAndUserAgentAndProtocol(ctx, account.ID, probeConfig.ModelID, probeConfig.Prompt, probeConfig.UserAgent, probeConfig.Protocol)
	finishedAt := time.Now()
	probeResult := &GroupAvailabilityProbeResult{
		GroupID:    due.GroupID,
		AccountID:  &account.ID,
		ModelID:    probeConfig.ModelID,
		Protocol:   probeConfig.Protocol,
		Status:     GroupAvailabilityProbeStatusSuccess,
		Success:    true,
		LatencyMs:  finishedAt.Sub(startedAt).Milliseconds(),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	if result != nil {
		probeResult.Status = result.Status
		probeResult.Success = result.Status == GroupAvailabilityProbeStatusSuccess
		probeResult.LatencyMs = result.LatencyMs
		probeResult.ErrorMessage = result.ErrorMessage
		probeResult.StartedAt = result.StartedAt
		probeResult.FinishedAt = result.FinishedAt
	}
	if err != nil {
		probeResult.Status = GroupAvailabilityProbeStatusFailed
		probeResult.Success = false
		if probeResult.ErrorMessage == "" {
			probeResult.ErrorMessage = err.Error()
		}
	}
	if ctx.Err() != nil && probeResult.Success {
		probeResult.Status = GroupAvailabilityProbeStatusFailed
		probeResult.Success = false
		probeResult.ErrorMessage = ctx.Err().Error()
	}

	return probeResult
}

func (s *GroupAvailabilityProbeRunnerService) saveFailure(ctx context.Context, due GroupAvailabilityProbeDueGroup, cfg GroupAvailabilityProbeConfig, message string, accountID *int64) {
	now := time.Now()
	modelID := strings.TrimSpace(cfg.ModelID)
	if modelID == "" {
		modelID = strings.TrimSpace(due.Config.ModelID)
	}
	s.saveResult(ctx, due, cfg, &GroupAvailabilityProbeResult{
		GroupID:      due.GroupID,
		AccountID:    accountID,
		ModelID:      modelID,
		Status:       GroupAvailabilityProbeStatusFailed,
		Success:      false,
		ErrorMessage: message,
		StartedAt:    now,
		FinishedAt:   now,
	})
}

func (s *GroupAvailabilityProbeRunnerService) saveResult(ctx context.Context, due GroupAvailabilityProbeDueGroup, cfg GroupAvailabilityProbeConfig, result *GroupAvailabilityProbeResult) {
	interval := time.Duration(cfg.IntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = time.Duration(defaultGroupAvailabilityProbeIntervalMinutes) * time.Minute
	}
	nextRunAt := time.Now().Add(interval)
	if err := s.repo.SaveResultAndScheduleNext(ctx, result, nextRunAt); err != nil {
		logger.LegacyPrintf("service.group_availability_probe", "[GroupAvailabilityProbe] group=%d save result error: %v", due.GroupID, err)
	}
}

func (s *GroupAvailabilityProbeRunnerService) selectProbeAccount(ctx context.Context, due GroupAvailabilityProbeDueGroup, modelID string) (*Account, error) {
	groupID := due.GroupID
	switch due.Platform {
	case PlatformOpenAI:
		if s.openAIGateway == nil {
			return nil, fmt.Errorf("openai gateway service not configured")
		}
		return s.openAIGateway.SelectAccountForModel(ctx, &groupID, "", modelID)
	case PlatformGemini:
		if s.geminiCompatSvc != nil {
			return s.geminiCompatSvc.SelectAccountForModel(ctx, &groupID, "", modelID)
		}
		if s.gatewaySvc != nil {
			return s.gatewaySvc.SelectAccountForModel(ctx, &groupID, "", modelID)
		}
		return nil, fmt.Errorf("gemini gateway service not configured")
	default:
		if s.gatewaySvc == nil {
			return nil, fmt.Errorf("gateway service not configured")
		}
		return s.gatewaySvc.SelectAccountForModel(ctx, &groupID, "", modelID)
	}
}
