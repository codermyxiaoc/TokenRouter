//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 创作台 token 计费：建行保存计费模式；执行用量只写一次，重复写入保留原金额，读回字段完整。
func TestCreativeRunTokenBillingPersistence(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	repo := &creativeRunRepository{client: integrationEntClient}
	id, err := service.NewCreativeRunID()
	require.NoError(t, err)
	_, err = repo.CreateCreativeRun(ctx, service.CreateCreativeRunParams{
		RunID: id, UserID: f.user, GroupID: f.group, APIKeyID: f.key, Model: "gpt-image-2.5-sunburst", RequestedModel: "gpt-image-2.5-sunburst",
		Operation: service.CreativeOperationGenerate, RequestedOutputCount: 1, ImageSize: "1K", ResponseMIMEType: "image/png",
		PromptHash: "test", RequestFingerprint: id, QuotaPlatform: service.PlatformOpenAI, BaseUnitPrice: 0.3,
		BillingMode: domain.CreativeBillingModeToken,
	})
	require.NoError(t, err)
	run, err := repo.GetCreativeRunByRunID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, domain.CreativeBillingModeToken, run.BillingMode)
	require.Nil(t, run.ProviderUsage)

	first := &domain.CreativeProviderUsage{Reported: true, InputTokens: 3461, ImageInputTokens: 1024, OutputTokens: 110, ImageOutputTokens: 110,
		TokenBaseAmountUSD: 0.020605, ChargeBaseAmountUSD: 0.020605}
	recorder := repo
	require.NoError(t, recorder.SetCreativeRunProviderUsage(ctx, id, first))
	require.NoError(t, recorder.SetCreativeRunProviderUsage(ctx, id, &domain.CreativeProviderUsage{Reported: true, InputTokens: 1, ChargeBaseAmountUSD: 9}))
	run, err = repo.GetCreativeRunByRunID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, first, run.ProviderUsage, "已有用量保留原值，重复写入不改变扣费金额")

	// 历史任务（未设置计费模式）读回为按张。
	legacy, err := service.NewCreativeRunID()
	require.NoError(t, err)
	_, err = repo.CreateCreativeRun(ctx, service.CreateCreativeRunParams{
		RunID: legacy, UserID: f.user, GroupID: f.group, APIKeyID: f.key, Model: "gpt-image-1", RequestedModel: "gpt-image-1",
		Operation: service.CreativeOperationGenerate, RequestedOutputCount: 1, ImageSize: "1K", ResponseMIMEType: "image/png",
		PromptHash: "test", RequestFingerprint: legacy, QuotaPlatform: service.PlatformOpenAI,
	})
	require.NoError(t, err)
	run, err = repo.GetCreativeRunByRunID(ctx, legacy)
	require.NoError(t, err)
	require.Empty(t, run.BillingMode)
	require.Nil(t, run.ProviderUsage)
}
