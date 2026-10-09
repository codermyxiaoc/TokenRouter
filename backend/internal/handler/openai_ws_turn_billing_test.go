package handler

import (
	"context"
	"errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

type wsTurnAPIKeyLookupFunc func(ctx context.Context, key string) (*service.APIKey, error)

func (f wsTurnAPIKeyLookupFunc) GetByKey(ctx context.Context, key string) (*service.APIKey, error) {
	return f(ctx, key)
}

func TestRefreshOpenAIWSTurnBillingAPIKey(t *testing.T) {
	groupID := int64(77)
	conn := &service.APIKey{
		ID:      5,
		Key:     "sk-conn",
		GroupID: &groupID,
		User:    &service.User{ID: 9},
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 3},
	}
	latestWith := func(mutate func(k *service.APIKey)) *service.APIKey {
		gid := groupID
		k := &service.APIKey{
			ID:      5,
			Key:     "sk-conn",
			GroupID: &gid,
			User:    &service.User{ID: 9, Balance: 123},
			Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 0.3},
		}
		if mutate != nil {
			mutate(k)
		}
		return k
	}

	t.Run("same group adopts the latest group and nothing else", func(t *testing.T) {
		got := refreshOpenAIWSTurnBillingAPIKey(context.Background(), wsTurnAPIKeyLookupFunc(func(ctx context.Context, key string) (*service.APIKey, error) {
			require.Equal(t, "sk-conn", key)
			return latestWith(nil), nil
		}), conn)
		require.NotSame(t, conn, got)
		require.InDelta(t, 0.3, got.Group.RateMultiplier, 1e-12)
		require.Same(t, conn.User, got.User, "only the group snapshot is refreshed")
		require.Equal(t, conn.ID, got.ID)
		require.InDelta(t, 3.0, conn.Group.RateMultiplier, 1e-12, "the connection snapshot is never mutated")
	})

	keep := map[string]wsTurnAPIKeyLookupFunc{
		"lookup error": func(ctx context.Context, key string) (*service.APIKey, error) {
			return nil, errors.New("boom")
		},
		"not found": func(ctx context.Context, key string) (*service.APIKey, error) {
			return nil, service.ErrAPIKeyNotFound
		},
		"nil key": func(ctx context.Context, key string) (*service.APIKey, error) { return nil, nil },
		"different key id": func(ctx context.Context, key string) (*service.APIKey, error) {
			return latestWith(func(k *service.APIKey) { k.ID = 6 }), nil
		},
		"moved group": func(ctx context.Context, key string) (*service.APIKey, error) {
			return latestWith(func(k *service.APIKey) { other := int64(78); k.GroupID = &other; k.Group.ID = other }), nil
		},
		"no group": func(ctx context.Context, key string) (*service.APIKey, error) {
			return latestWith(func(k *service.APIKey) { k.Group = nil }), nil
		},
		"platform changed": func(ctx context.Context, key string) (*service.APIKey, error) {
			return latestWith(func(k *service.APIKey) { k.Group.Platform = service.PlatformAnthropic }), nil
		},
		"routing kind changed": func(ctx context.Context, key string) (*service.APIKey, error) {
			return latestWith(func(k *service.APIKey) { k.SmartRouting = true }), nil
		},
	}
	for name, lookup := range keep {
		t.Run(name+" keeps the connection snapshot", func(t *testing.T) {
			require.Same(t, conn, refreshOpenAIWSTurnBillingAPIKey(context.Background(), lookup, conn))
		})
	}

	t.Run("no credential or no group skips the lookup", func(t *testing.T) {
		called := false
		lookup := wsTurnAPIKeyLookupFunc(func(ctx context.Context, key string) (*service.APIKey, error) {
			called = true
			return latestWith(nil), nil
		})
		noKey := *conn
		noKey.Key = ""
		require.Same(t, &noKey, refreshOpenAIWSTurnBillingAPIKey(context.Background(), lookup, &noKey))
		noGroup := *conn
		noGroup.Group = nil
		require.Same(t, &noGroup, refreshOpenAIWSTurnBillingAPIKey(context.Background(), lookup, &noGroup))
		require.Nil(t, refreshOpenAIWSTurnBillingAPIKey(context.Background(), lookup, nil))
		require.Same(t, conn, refreshOpenAIWSTurnBillingAPIKey(context.Background(), nil, conn))
		require.False(t, called)
	})
}

func TestOpenAIWSTurnBillingAPIKeysKeepPreviousTurnUntilItIsRecorded(t *testing.T) {
	conn := &service.APIKey{ID: 1}
	turn2 := &service.APIKey{ID: 2}
	turn3 := &service.APIKey{ID: 3}
	var keys openAIWSTurnBillingAPIKeys
	require.Same(t, conn, keys.forTurn(1, conn), "a turn without BeforeTurn bills with the connection snapshot")
	keys.set(2, turn2)
	keys.set(3, turn3) // 第三轮开始时，第二轮的用量尚未提交。
	require.Same(t, turn2, keys.forTurn(2, conn))
	require.Same(t, turn3, keys.forTurn(3, conn))
	keys.set(4, conn)
	require.Same(t, conn, keys.forTurn(2, conn), "only the current and previous turn are kept")
	require.Len(t, keys.keys, 2)
}

// 复合与智能 Key 只能刷新建连时已选分组，付款人、成员及资金选择保持原快照。
func TestRefreshOpenAIWSTurnBillingCompositeAndSmartKey(t *testing.T) {
	for _, mode := range []string{"composite", "smart"} {
		t.Run(mode, func(t *testing.T) {
			group := &service.Group{ID: 77, Platform: service.PlatformOpenAI, RateMultiplier: 3}
			subscriptionID, teamID := int64(91), int64(92)
			conn := &service.APIKey{ID: 5, Key: "test-key", GroupID: &group.ID, Group: group,
				UserID: 10, User: &service.User{ID: 9}, ActorUser: &service.User{ID: 10}, TeamID: &teamID,
				IsComposite: mode == "composite", SmartRouting: mode == "smart",
				BillingMode: service.APIKeyBillingModeSubscription, PreferredSubscriptionID: &subscriptionID}
			latest := *conn
			latest.Group, latest.GroupID = nil, nil
			latest.User, latest.ActorUser = &service.User{ID: 999}, &service.User{ID: 998}
			latest.BillingMode, latest.PreferredSubscriptionID = service.APIKeyBillingModeBalance, nil
			latest.CompositeGroups = []service.APIKeyCompositeGroup{
				{GroupID: 88, Group: &service.Group{ID: 88, Platform: service.PlatformOpenAI, RateMultiplier: 99}},
				{GroupID: 77, Group: &service.Group{ID: 77, Platform: service.PlatformOpenAI, RateMultiplier: 0.3}},
			}
			lookup := wsTurnAPIKeyLookupFunc(func(context.Context, string) (*service.APIKey, error) { return &latest, nil })
			got := refreshOpenAIWSTurnBillingAPIKey(context.Background(), lookup, conn)
			require.Equal(t, int64(77), got.Group.ID)
			require.InDelta(t, 0.3, got.Group.RateMultiplier, 1e-12)
			require.Same(t, conn.User, got.User)
			require.Same(t, conn.ActorUser, got.ActorUser)
			require.Equal(t, conn.TeamID, got.TeamID)
			require.Equal(t, conn.BillingMode, got.BillingMode)
			require.Same(t, conn.PreferredSubscriptionID, got.PreferredSubscriptionID)
			latest.CompositeGroups = latest.CompositeGroups[:1]
			require.Same(t, conn, refreshOpenAIWSTurnBillingAPIKey(context.Background(), lookup, conn), "不能漂移到另一个候选组")
			latest.CompositeGroups = []service.APIKeyCompositeGroup{{GroupID: 77, Group: nil}}
			require.Same(t, conn, refreshOpenAIWSTurnBillingAPIKey(context.Background(), lookup, conn), "缺失的分组快照不能采用")
		})
	}
}
