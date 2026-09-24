package service

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 基础调度的上一响应亲和也必须遵守智能路由分组及当前请求能力。
func TestLegacyPreviousResponseRespectsRoutingScopeAndCapabilities(t *testing.T) {
	for _, name := range []string{"moved_group", "missing_scoped_group", "excluded", "image_requires_apikey", "model_removed"} {
		t.Run(name, func(t *testing.T) {
			groupID := int64(901)
			ctx := WithSmartRoutingScope(context.Background())
			account := Account{ID: 902, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}}
			req := OpenAIAccountScheduleRequest{GroupID: &groupID, Platform: PlatformOpenAI, PreviousResponseID: "resp_scope", RequestedModel: "gpt-5.5", RoutingModel: "gpt-5.5"}
			switch name {
			case "moved_group":
				account.GroupIDs = []int64{999}
			case "missing_scoped_group":
				account.GroupIDs = nil
			case "excluded":
				req.ExcludedIDs = map[int64]struct{}{account.ID: {}}
			case "image_requires_apikey":
				account.Type = AccountTypeOAuth
				req.RequiredImageCapability = OpenAIImagesCapabilityAPIKey
			case "model_removed":
				account.Credentials = map[string]any{"model_mapping": map[string]any{"other": "other"}}
			}
			var acquired, released []int64
			svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{account}}, cache: &schedulerTestGatewayCache{}, concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &acquired, releasedIDs: &released})}
			require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, req.PreviousResponseID, account.ID, time.Hour))
			selection, err := svc.selectLegacyAccountByPreviousResponse(ctx, req)
			require.NoError(t, err)
			require.Nil(t, selection)
			require.Equal(t, len(acquired), len(released), "拒绝候选必须释放已获得的槽位")
		})
	}
}
