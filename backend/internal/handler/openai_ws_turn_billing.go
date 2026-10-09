package handler

import (
	"context"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

// openAIWSTurnAPIKeyLookup 使用现有认证缓存读取后续轮次快照。
type openAIWSTurnAPIKeyLookup interface {
	GetByKey(ctx context.Context, key string) (*service.APIKey, error)
}

// openAIWSTurnBillingAPIKeys 为每轮冻结分组价卡；保留当前和前一轮，避免异步记账串价。
// 只刷新分组，不改变连接绑定账号、用户、Key 结算模式或套餐。
type openAIWSTurnBillingAPIKeys struct {
	mu   sync.Mutex
	keys map[int]*service.APIKey
}

// begin 在发送后续轮次前刷新同一分组的定价快照。
func (k *openAIWSTurnBillingAPIKeys) begin(ctx context.Context, apiKeyService *service.APIKeyService, turn int, conn *service.APIKey) context.Context {
	turnKey := conn
	if turn > 1 && apiKeyService != nil {
		turnKey = refreshOpenAIWSTurnBillingAPIKey(ctx, apiKeyService, conn)
	}
	k.set(turn, turnKey)
	return ctx
}

func (k *openAIWSTurnBillingAPIKeys) set(turn int, key *service.APIKey) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys == nil {
		k.keys = make(map[int]*service.APIKey, 2)
	}
	for t := range k.keys {
		if t < turn-1 {
			delete(k.keys, t)
		}
	}
	k.keys[turn] = key
}

func (k *openAIWSTurnBillingAPIKeys) forTurn(turn int, conn *service.APIKey) *service.APIKey {
	k.mu.Lock()
	defer k.mu.Unlock()
	if key := k.keys[turn]; key != nil {
		return key
	}
	return conn
}

// refreshOpenAIWSTurnBillingAPIKey 返回本 turn 计费用的 API Key：分组取当前认证
// 快照，其余字段与建连快照共享。不满足采用条件时原样返回建连快照。
func refreshOpenAIWSTurnBillingAPIKey(ctx context.Context, lookup openAIWSTurnAPIKeyLookup, conn *service.APIKey) *service.APIKey {
	if lookup == nil || conn == nil || conn.Key == "" || conn.GroupID == nil || conn.Group == nil || conn.Group.ID != *conn.GroupID {
		return conn
	}
	latest, err := lookup.GetByKey(ctx, conn.Key)
	if err != nil || latest == nil || latest.ID != conn.ID || latest.IsComposite != conn.IsComposite || latest.SmartRouting != conn.SmartRouting {
		return conn
	}
	var group *service.Group
	if conn.IsComposite || conn.SmartRouting {
		// 只能刷新已选中的候选分组，禁止借调价跨组或回退到候选集合外。
		for _, binding := range latest.CompositeGroups {
			if binding.GroupID == *conn.GroupID {
				group = binding.Group
				break
			}
		}
	} else if latest.GroupID != nil && *latest.GroupID == *conn.GroupID {
		group = latest.Group
	}
	if group == nil || group.ID != conn.Group.ID ||
		group.Platform != conn.Group.Platform {
		return conn
	}
	turnKey := *conn
	turnKey.Group = group
	return &turnKey
}
