//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

type legacyCreationOrderedObserver struct {
	onObserve func(service.MediaTaskObservation)
}

func (o *legacyCreationOrderedObserver) ObserveMediaTask(_ context.Context, event service.MediaTaskObservation) error {
	o.onObserve(event)
	return nil
}

// 回填只扫描列表投影，因此必须先保存原资金快照，再让创建任务出现在列表中。
func TestLegacyVideoHandlerPersistsSnapshotBeforePublishingObservation(t *testing.T) {
	for _, provider := range []string{"grok", "seedance"} {
		t.Run(provider, func(t *testing.T) {
			seedance := provider == "seedance"
			var platforms []string
			if seedance {
				platforms = []string{service.PlatformOpenAI}
			}
			h, slots, _, upstream := newGrokMediaSlotHandler(t, false, false, platforms...)
			store := &legacyVideoHandlerStore{saveErrors: 1}
			service.NewLegacyVideoRecovery(store, h.gatewayService, nil, nil, nil)
			observations := 0
			h.SetMediaTaskObserver(&legacyCreationOrderedObserver{onObserve: func(event service.MediaTaskObservation) {
				observations++
				require.Equal(t, 2, store.saves, "投影前必须完成本地保存重试")
				require.NotNil(t, store.task, "不能让回填先读到尚无完整资金快照的新任务")
				require.NotNil(t, store.task.Pending)
				require.NotNil(t, store.task.Pending.SeedanceBilling)
				require.Equal(t, store.task.UserID, event.UserID)
				require.Equal(t, store.task.APIKeyID, event.APIKeyID)
			}})
			body := `{"request_id":"task","status":"pending"}`
			if seedance {
				body = `{"id":"task","status":"queued"}`
			}
			upstream.call = func(req *http.Request, _ int64) (*http.Response, error) {
				require.Equal(t, http.MethodPost, req.Method)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			}
			c, w := legacyVideoHandlerContext(context.Background(), seedance, true)
			if seedance {
				h.SeedanceTasks(c)
			} else {
				h.GrokVideoGeneration(c)
			}
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, 1, observations)
			require.Equal(t, 1, upstream.calls)
			slots.assertReleased(t)
		})
	}
}

// 原选定套餐的分组倍率必须随安全快照持久化，不能只留下套餐 ID 等恢复时重读新配置。
func TestLegacyVideoBillingSnapshotRetainsSelectedPlanRate(t *testing.T) {
	group := &service.Group{ID: 3, RateMultiplier: 7}
	key := &service.APIKey{ID: 2, UserID: 1, GroupID: &group.ID, Group: group, BillingMode: service.APIKeyBillingModeAuto}
	sub := &service.UserSubscription{ID: 55, UserID: 1, Notes: "private-notes",
		User: &service.User{ID: 1, Email: "private@example.test"},
		Plan: &service.SubscriptionPlan{ID: 77, GroupIDs: []int64{group.ID}, GroupRateMultipliers: map[int64]float64{group.ID: 2}}}
	raw, err := json.Marshal(newSeedanceBillingSnapshot(key, sub, service.ChannelMappingResult{}))
	require.NoError(t, err)
	var restored service.SeedanceBillingSnapshot
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.NotNil(t, restored.Subscription)
	require.NotNil(t, restored.Subscription.Plan)
	require.Equal(t, int64(55), restored.Subscription.ID)
	require.Equal(t, float64(2), restored.Subscription.Plan.GroupRateMultipliers[group.ID])
	require.Nil(t, restored.Subscription.User)
	require.NotContains(t, string(raw), "private-notes")
	require.NotContains(t, string(raw), "private@example.test")
}
