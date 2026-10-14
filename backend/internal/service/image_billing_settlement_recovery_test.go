//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func imageSettlementEvidenceForTest() *ImageBillingSettlementEvidence {
	return &ImageBillingSettlementEvidence{Version: 1,
		Command: UsageBillingCommand{RequestID: "client:image", UserID: 1, ActorUserID: 1, APIKeyID: 2, AccountID: 3, ImageCount: 1, BaseAmountUSD: 0.12},
		Usage:   UsageLog{RequestID: "client:image", UserID: 1, BillingUserID: 1, APIKeyID: 2, AccountID: 3, ImageCount: 1, TotalCost: 0.12}}
}

// 证据保存不能把完整账号/API Key 对象或客户端自由文本一并落库。
func TestImageBillingSettlementEvidencePrivacyAndValidation(t *testing.T) {
	evidence := imageSettlementEvidenceForTest()
	evidence.Usage.APIKey = &APIKey{Key: "secret-api-key"}
	evidence.Usage.Account = &Account{Credentials: map[string]any{"api_key": "secret-upstream-key"}}
	ua := "secret-user-agent"
	evidence.Usage.UserAgent = &ua
	safe, encoded, err := NormalizeImageBillingSettlementEvidence(evidence)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-")
	require.Nil(t, safe.Usage.APIKey)
	require.NotNil(t, evidence.Usage.APIKey, "不能修改原正常计费上下文")
	for _, mutate := range []func(*ImageBillingSettlementEvidence){
		func(e *ImageBillingSettlementEvidence) { e.Usage.APIKeyID++ },
		func(e *ImageBillingSettlementEvidence) { e.Usage.BillingUserID++ },
		func(e *ImageBillingSettlementEvidence) { e.Command.ImageCount = 0 },
		func(e *ImageBillingSettlementEvidence) { e.Usage.ImageCount++ },
		func(e *ImageBillingSettlementEvidence) { e.Usage.Model = strings.Repeat("x", 65*1024) },
	} {
		invalid := imageSettlementEvidenceForTest()
		mutate(invalid)
		_, _, err := NormalizeImageBillingSettlementEvidence(invalid)
		require.ErrorIs(t, err, ErrImageBillingReservationInvalid)
	}
}

func TestImageBillingSettlementResultRestoresZeroUsage(t *testing.T) {
	log := UsageLog{TotalCost: 0.12}
	ApplyImageBillingSettlementResult(&log, &UsageBillingApplyResult{SubscriptionAmountUSD: 0.36, BalanceAmountUSD: 0.06})
	require.InDelta(t, 0.42, log.ActualCost, 1e-10)
	require.InDelta(t, 3.5, log.RateMultiplier, 1e-10)
	require.Equal(t, BillingTypeSubscription, log.BillingType)
}

type imageSettlementRecoveryFake struct {
	UsageBillingRepository
	ImageBillingSettlementRepository
}

func (*imageSettlementRecoveryFake) RecoverImageBillingSettlements(context.Context, time.Time, int) ([]ImageBillingSettlementRecovered, error) {
	return []ImageBillingSettlementRecovered{{UserID: 1, ActorUserID: 2, APIKeyID: 3, AccountID: 4}}, nil
}

// 团队恢复必须同时清理付款用户和实际 Key 所有者的鉴权快照。
func TestImageBillingSettlementRecoveryInvalidatesPayerAndActor(t *testing.T) {
	svc := &OpenAIGatewayService{usageBillingRepo: &imageSettlementRecoveryFake{}}
	var ids []int64
	require.NoError(t, svc.RecoverAsyncImageBilling(context.Background(), func(_ context.Context, id int64) { ids = append(ids, id) }))
	require.Equal(t, []int64{1, 2}, ids)
}

type imageEvidenceCaptureRepo struct {
	*imageQuoteBillingRepo
	evidence *ImageBillingSettlementEvidence
	err      error
}

func (r *imageEvidenceCaptureRepo) SaveImageBillingSettlementEvidence(_ context.Context, e *ImageBillingSettlementEvidence) error {
	if r.err != nil {
		return r.err
	}
	var err error
	r.evidence, _, err = NormalizeImageBillingSettlementEvidence(e)
	return err
}
func (r *imageEvidenceCaptureRepo) RecoverImageBillingSettlements(context.Context, time.Time, int) ([]ImageBillingSettlementRecovered, error) {
	return nil, nil
}

// 成功证据必须先于捕获：持久化失败时不先扣费，也不能把客户端失败当作重新生成的许可。
func TestAsyncImageCaptureRequiresSavedRecoveryEvidence(t *testing.T) {
	for _, fail := range []bool{false, true} {
		svc, base, key, account, ctx := newImageQuoteTestService(t)
		repo := &imageEvidenceCaptureRepo{imageQuoteBillingRepo: base}
		if fail {
			repo.err = errors.New("database unavailable")
		}
		svc.usageBillingRepo = repo
		handle, err := svc.PrepareAsyncImageBilling(ctx, key, account, nil, "gpt-image-2", 1, "4K", ChannelMappingResult{})
		require.NoError(t, err)
		err = svc.RecordUsage(ctx, &OpenAIRecordUsageInput{APIKey: key, User: key.User, Account: account,
			Result:     &OpenAIForwardResult{Model: "gpt-image-2", ImageSize: "4K", ImageCount: 1},
			ImageQuote: handle.Quote, ImageReservation: handle.Reservation})
		if fail {
			require.Error(t, err)
			require.Nil(t, base.capture)
		} else {
			require.NoError(t, err)
			require.NotNil(t, repo.evidence)
			require.Equal(t, base.capture.RequestID, repo.evidence.Command.RequestID)
			require.InDelta(t, 0.12, repo.evidence.Command.BaseAmountUSD, 1e-10)
		}
	}
}
