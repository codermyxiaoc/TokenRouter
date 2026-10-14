//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// 审计桩记录保存证据和捕获使用的上下文，防止成功产图被客户端断开误判为可退款。
type imageBillingContextAuditRepo struct {
	*asyncImageBillingRepoStub
	t           *testing.T
	evidenceErr error
	evidence    *service.ImageBillingSettlementEvidence
}

func (r *imageBillingContextAuditRepo) assertSettlementContext(ctx context.Context, budget time.Duration) {
	r.t.Helper()
	require.NoError(r.t, ctx.Err())
	deadline, ok := ctx.Deadline()
	require.True(r.t, ok, "结算不能使用无限期的后台上下文")
	require.Positive(r.t, time.Until(deadline))
	require.LessOrEqual(r.t, time.Until(deadline), budget)
}

func (r *imageBillingContextAuditRepo) SaveImageBillingSettlementEvidence(ctx context.Context, evidence *service.ImageBillingSettlementEvidence) error {
	// 成功证据直接使用处理器给定的十秒持久化预算。
	r.assertSettlementContext(ctx, 10*time.Second)
	r.event("evidence")
	if r.evidenceErr != nil {
		return r.evidenceErr
	}
	var err error
	r.evidence, _, err = service.NormalizeImageBillingSettlementEvidence(evidence)
	return err
}

func (*imageBillingContextAuditRepo) RecoverImageBillingSettlements(context.Context, time.Time, int) ([]service.ImageBillingSettlementRecovered, error) {
	return nil, nil
}

func (r *imageBillingContextAuditRepo) CaptureImageBilling(ctx context.Context, id string, command *service.UsageBillingCommand, base float64) (*service.UsageBillingApplyResult, error) {
	// 捕获复用公共账本 detachedBillingContext 的独立十五秒事务预算。
	r.assertSettlementContext(ctx, 15*time.Second)
	return r.asyncImageBillingRepoStub.CaptureImageBilling(ctx, id, command, base)
}

func (r *imageBillingContextAuditRepo) MarkImageBillingReconciliation(ctx context.Context, id string, keyID, userID int64) error {
	// 收尾待核对仍由处理器单独提供十秒预算，不沿用账本预算。
	r.assertSettlementContext(ctx, 10*time.Second)
	return r.asyncImageBillingRepoStub.MarkImageBillingReconciliation(ctx, id, keyID, userID)
}

// 成功产图后的证据和捕获必须脱离已取消/超时的生成请求，保存失败也不能释放或重发。
func TestAsyncImageConfirmedOutputSettlementSurvivesRequestCancellation(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformGrok} {
		for _, cancellation := range []string{"cancelled", "deadline_exceeded"} {
			for _, failure := range []string{"none", "evidence", "capture"} {
				t.Run(platform+"/"+cancellation+"/"+failure, func(t *testing.T) {
					billing := &imageBillingContextAuditRepo{asyncImageBillingRepoStub: &asyncImageBillingRepoStub{}, t: t}
					if failure == "evidence" {
						billing.evidenceErr = errors.New("本地证据存储暂时失败")
					}
					if failure == "capture" {
						billing.captureErr = errors.New("本地捕获事务暂时失败")
					}
					cfg := &config.Config{RunMode: config.RunModeStandard}
					cfg.Default.RateMultiplier = 1
					groupID, price := int64(4201), 0.12
					key := &service.APIKey{ID: 4202, UserID: 4203, Status: service.StatusActive, BillingMode: service.APIKeyBillingModeAuto,
						GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: platform, RateMultiplier: 6, AllowImageGeneration: true, ImagePrice1K: &price},
						User: &service.User{ID: 4203, Status: service.StatusActive, Balance: 100}}
					account := &service.Account{ID: 4204, Platform: platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive}
					gateway := service.NewOpenAIGatewayService(nil, &routingFocusUsageSink{}, billing, nil, nil, asyncImageBillingRateRepo{}, nil, cfg, nil, nil,
						service.NewBillingService(cfg, nil), nil, nil, nil, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
					state := &asyncImageBillingState{gateway: gateway}
					model := "gpt-image-2"
					if platform == service.PlatformGrok {
						model = "grok-imagine-image"
					}
					base := context.WithValue(service.WithAsyncImageExecutionContext(context.Background()), ctxkey.ClientRequestID, "image-context-audit")
					require.NoError(t, state.prepare(base, key, account, nil, model, 1, "1K", service.ChannelMappingResult{}))
					require.NotNil(t, state.reservation)
					ctx, cancel := context.WithCancel(base)
					cancel()
					if cancellation == "deadline_exceeded" {
						ctx, cancel = context.WithDeadline(base, time.Now().Add(-time.Second))
						defer cancel()
					}
					require.Error(t, ctx.Err())
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(ctx)
					err := state.record(c, &service.OpenAIRecordUsageInput{APIKey: key, User: key.User, Account: account,
						Result: &service.OpenAIForwardResult{Model: model, ImageCount: 1, ImageSize: "1K"}})
					state.finish(c, zap.NewNop())
					switch failure {
					case "evidence":
						require.ErrorIs(t, err, billing.evidenceErr)
						require.Equal(t, []string{"reserve", "evidence", "reconciliation"}, billing.events)
						require.Nil(t, billing.captureCommand)
					case "capture":
						require.Error(t, err)
						require.Equal(t, []string{"reserve", "evidence", "capture", "reconciliation"}, billing.events)
					default:
						require.NoError(t, err)
						require.Equal(t, []string{"reserve", "evidence", "capture"}, billing.events)
						require.True(t, state.settled)
					}
					if failure != "evidence" {
						require.NotNil(t, billing.evidence)
						require.Equal(t, state.reservation.Reservation.ID, billing.evidence.Command.RequestID)
						require.InDelta(t, 0.12, billing.evidence.Command.BaseAmountUSD, 1e-10)
						require.Equal(t, key.UserID, billing.evidence.Command.UserID)
						require.Equal(t, account.ID, billing.evidence.Command.AccountID)
					}
				})
			}
		}
	}
}
