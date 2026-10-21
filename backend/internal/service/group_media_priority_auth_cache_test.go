package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 复合 Key 走独立的分组快照转换，不能遗漏普通 Key 已支持的开关。
func TestCompositeAuthGroupSnapshotPreservesMediaSubscriptionPriority(t *testing.T) {
	original := &Group{ID: 9, ImageSubscriptionRatePriority: true, VideoSubscriptionRatePriority: true}
	encoded, err := json.Marshal(authGroupSnapshotFromGroup(original))
	require.NoError(t, err)
	var snapshot APIKeyAuthGroupSnapshot
	require.NoError(t, json.Unmarshal(encoded, &snapshot))
	roundTrip := groupFromAuthSnapshot(&snapshot)
	require.True(t, roundTrip.ImageSubscriptionRatePriority)
	require.True(t, roundTrip.VideoSubscriptionRatePriority)
}

// 旧快照必须重建，否则同一分组可能在缓存过期之前继续采用旧的计费选择。
func TestAPIKeyServiceRejectsV39SnapshotWithoutMediaSubscriptionPriority(t *testing.T) {
	svc := &APIKeyService{}
	key, ok, err := svc.applyAuthCacheEntry("legacy-media-priority", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 39},
	})
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, key)
}
