package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// 图片报价测试只替换持久账本，沿用真实的定价与 RecordUsage 实现。
type imageQuoteBillingRepo struct {
	UsageBillingRepository
	ImageBillingReservationRepository
	reserve    *ImageBillingReserveCommand
	capture    *UsageBillingCommand
	actualBase float64
	duplicate  bool
	reserveErr error
	released   bool
	reconciled bool
}

func (r *imageQuoteBillingRepo) ReserveImageBilling(_ context.Context, cmd *ImageBillingReserveCommand) (*ImageBillingReservation, error) {
	if r.reserveErr != nil {
		return nil, r.reserveErr
	}
	r.reserve = cmd
	return &ImageBillingReservation{ID: cmd.Hold.RequestID, Applied: !r.duplicate, State: ImageBillingReserved, Hold: cmd.Hold, Quote: cmd.Quote}, nil
}

func (r *imageQuoteBillingRepo) CaptureImageBilling(_ context.Context, id string, cmd *UsageBillingCommand, base float64) (*UsageBillingApplyResult, error) {
	if id != r.reserve.Hold.RequestID {
		return nil, errors.New("wrong reservation id")
	}
	r.capture, r.actualBase = cmd, base
	return &UsageBillingApplyResult{Applied: true, BalanceAmountUSD: base * r.reserve.Hold.BalanceRateMultiplier}, nil
}

func (r *imageQuoteBillingRepo) ReleaseImageBilling(context.Context, string, int64, int64) error {
	r.released = true
	return nil
}
func (r *imageQuoteBillingRepo) MarkImageBillingReconciliation(context.Context, string, int64, int64) error {
	r.reconciled = true
	return nil
}

func newImageQuoteTestService(t *testing.T) (*OpenAIGatewayService, *imageQuoteBillingRepo, *APIKey, *Account, context.Context) {
	t.Helper()
	groupID := int64(91)
	price := 0.12
	key := &APIKey{ID: 2, User: &User{ID: 2759}, GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1, ImagePrice1K: &price, ImagePrice2K: &price, ImagePrice4K: &price}}
	account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	svc := newOpenAIRecordUsageServiceForTest(&openAIRecordUsageLogRepoStub{inserted: true}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.resolver = newOpenAIImageChannelPricingResolverForTest(t, groupID, "gpt-image-2", 0.9)
	repo := &imageQuoteBillingRepo{}
	svc.usageBillingRepo = repo
	ctx := WithAsyncImageExecutionContext(context.WithValue(context.Background(), ctxkey.ClientRequestID, "image-quote"))
	return svc, repo, key, account, ctx
}

func TestAsyncImageQuoteFreezesPriceAndBalanceRate(t *testing.T) {
	svc, repo, key, account, ctx := newImageQuoteTestService(t)
	handle, err := svc.PrepareAsyncImageBilling(ctx, key, account, nil, "gpt-image-2", 2, "4K", ChannelMappingResult{})
	require.NoError(t, err)
	require.InDelta(t, 0.24, repo.reserve.Hold.BaseAmountUSD, 1e-10)
	require.Equal(t, 1.0, repo.reserve.Hold.BalanceRateMultiplier)
	require.Equal(t, "client:image-quote", repo.reserve.Hold.RequestID)
	// 模拟生成期间改价、改倍率；完成一张仍按原价捕获，另一张由账本退回差额。
	price := 5.0
	key.Group.ImagePrice4K, key.Group.RateMultiplier = &price, 9
	err = svc.RecordUsage(ctx, &OpenAIRecordUsageInput{APIKey: key, User: key.User, Account: account,
		Result:     &OpenAIForwardResult{Model: "gpt-image-2", RequestID: "other-upstream-id", ImageSize: "4K", ImageCount: 1},
		ImageQuote: handle.Quote, ImageReservation: handle.Reservation})
	require.NoError(t, err)
	require.InDelta(t, 0.12, repo.actualBase, 1e-10)
	require.Equal(t, 1.0, repo.capture.BalanceRateMultiplier)
	require.Equal(t, "client:image-quote", repo.capture.RequestID)
}

func TestAsyncImageQuoteBypassAndDuplicateProtection(t *testing.T) {
	for _, name := range []string{"sync", "simple", "token", "missing_repository", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			svc, repo, key, account, ctx := newImageQuoteTestService(t)
			switch name {
			case "sync":
				ctx = context.Background()
			case "simple":
				svc.cfg = &config.Config{RunMode: config.RunModeSimple}
			case "token":
				svc.resolver = newOpenAITokenImageChannelPricingResolverForTest(t, key.Group.ID, "gpt-image-2")
			case "missing_repository":
				svc.usageBillingRepo = &openAIRecordUsageBillingRepoStub{}
			case "duplicate":
				repo.duplicate = true
			}
			handle, err := svc.PrepareAsyncImageBilling(ctx, key, account, nil, "gpt-image-2", 1, "4K", ChannelMappingResult{})
			require.Nil(t, handle)
			if name == "missing_repository" || name == "duplicate" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Nil(t, repo.reserve)
			}
		})
	}
}

func TestAsyncImageQuoteSizesFreeAndInvalidResults(t *testing.T) {
	quote := &OpenAIImageBillingQuote{BillingModel: "image", BillingMode: "image", RequestedCount: 2,
		UnitPrices: map[string]float64{"1K": 0, "2K": 0.12, "4K": 0.24}}
	for _, tc := range []struct {
		size           string
		count          int
		rate, expected float64
		invalid        bool
	}{
		{"1K", 2, 6.6, 0, false}, {"2K", 1, 1, 0.12, false}, {"4K", 2, 6.6, 3.168, false},
		{"4K", 1, 0, 0, false}, {"2K", 0, 1, 0, true}, {"2K", 3, 1, 0, true}, {"2K", 1, math.NaN(), 0, true},
	} {
		cost, err := quote.cost(tc.size, tc.count, tc.rate)
		if tc.invalid {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		require.InDelta(t, tc.expected, cost.ActualCost, 1e-8)
	}
}

func TestAsyncImageQuoteUsesDeadlineAndSafeFinalizers(t *testing.T) {
	svc, repo, key, account, ctx := newImageQuoteTestService(t)
	deadline := time.Now().Add(20 * time.Minute)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	handle, err := svc.PrepareAsyncImageBilling(ctx, key, account, nil, "gpt-image-2", 1, "2K", ChannelMappingResult{})
	require.NoError(t, err)
	require.Equal(t, deadline.Add(3*time.Minute), repo.reserve.ExpiresAt)
	require.NoError(t, svc.MarkAsyncImageBillingReconciliation(ctx, handle))
	require.True(t, repo.reconciled)
	require.False(t, repo.released)
	require.NoError(t, svc.ReleaseAsyncImageBilling(ctx, handle))
	require.True(t, repo.released)
}

// 冻结价卡必须使用实际转发链相同的请求、渠道和账号模型语义。
func TestAsyncImageQuoteMatchesForwardBillingModel(t *testing.T) {
	for _, tc := range []struct{ name, platform, accountType, source, want string }{
		{"openai_default", PlatformOpenAI, AccountTypeAPIKey, "", "channel-image"},
		{"openai_upstream", PlatformOpenAI, AccountTypeAPIKey, BillingModelSourceUpstream, "account-image"},
		{"oauth_upstream", PlatformOpenAI, AccountTypeOAuth, BillingModelSourceUpstream, "account-image"},
		{"grok_default", PlatformGrok, AccountTypeAPIKey, "", "account-image"},
		{"grok_oauth", PlatformGrok, AccountTypeOAuth, "", "account-image"},
		{"grok_requested", PlatformGrok, AccountTypeAPIKey, BillingModelSourceRequested, "client-image"},
		{"grok_channel", PlatformGrok, AccountTypeAPIKey, BillingModelSourceChannelMapped, "channel-image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, key, account, ctx := newImageQuoteTestService(t)
			account.Platform, account.Type = tc.platform, tc.accountType
			account.Credentials = map[string]any{"model_mapping": map[string]any{"channel-image": "account-image"}}
			handle, err := svc.PrepareAsyncImageBilling(ctx, key, account, nil, "client-image", 1, "2K",
				ChannelMappingResult{Mapped: true, MappedModel: "channel-image", BillingModelSource: tc.source})
			require.NoError(t, err)
			require.Equal(t, tc.want, handle.Quote.BillingModel)
		})
	}
}

func TestAsyncImageQuoteSubscriptionChargeWithFreeBalance(t *testing.T) {
	q := &OpenAIImageBillingQuote{BillingMode: "image", RequestedCount: 1, UnitPrices: map[string]float64{"2K": 0.12}}
	hold := &ImageBillingReservation{Hold: BatchImageBalanceHoldCommand{BaseAmountUSD: 0.12, BalanceRateMultiplier: 0,
		SubscriptionHoldAllocations: []domain.BillingAllocation{{BaseAmountUSD: 0.12, AmountUSD: 0.792, RateMultiplier: 6.6}}}}
	cost, err := q.settledCost("2K", 1, hold)
	require.NoError(t, err)
	require.InDelta(t, 0.792, cost.ActualCost, 1e-10)
	q.UnitPrices["2K"] = 0.13
	_, err = q.settledCost("2K", 1, hold)
	require.ErrorIs(t, err, ErrBatchImageSettlementCostExceedsHold)
}

func TestAsyncImageQuoteReserveFailuresAreSafe(t *testing.T) {
	for _, tc := range []struct{ failure, want error }{
		{ErrBatchImageInsufficientBalance, ErrInsufficientBalance},
		{ErrPreferredSubscriptionInsufficient, ErrPreferredSubscriptionInsufficient},
		{errors.New("internal database detail"), ErrBillingServiceUnavailable},
	} {
		svc, repo, key, account, ctx := newImageQuoteTestService(t)
		repo.reserveErr = tc.failure
		handle, err := svc.PrepareAsyncImageBilling(ctx, key, account, nil, "gpt-image-2", 1, "2K", ChannelMappingResult{})
		require.Nil(t, handle)
		require.ErrorIs(t, err, tc.want)
	}
}
