//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 默认配置保持上线前行为：错误请求全部缓存，普通请求不缓存。
func TestRequestDataCacheDefaultsPreserveErrorSnapshots(t *testing.T) {
	var svc *OpsService
	require.True(t, svc.DecideRequestDataCache(true, nil).Cache)
	require.True(t, svc.DecideRequestDataCache(true, int64Ptr(3)).Cache)
	require.False(t, svc.DecideRequestDataCache(false, int64Ptr(3)).Cache)

	decision := svc.DecideRequestDataCache(true, nil)
	require.Equal(t, RequestDataCacheKindError, decision.Kind)
	require.Equal(t, 30*24*time.Hour, decision.TTL)
}

// 普通请求与错误请求的开关、时长、分组范围互不影响。
func TestRequestDataCacheScopesAreIndependent(t *testing.T) {
	svc := &OpsService{}
	svc.storeRequestDataCacheSettings(RequestDataCacheSettings{
		Normal: RequestDataCacheScope{Enabled: true, TTLMinutes: 60, GroupIDs: []int64{5}},
		Error:  RequestDataCacheScope{Enabled: true, TTLMinutes: 120, GroupIDs: []int64{9}, IncludeUngrouped: false},
	})

	normal := svc.DecideRequestDataCache(false, int64Ptr(5))
	require.True(t, normal.Cache)
	require.Equal(t, RequestDataCacheKindNormal, normal.Kind)
	require.Equal(t, time.Hour, normal.TTL)
	require.False(t, svc.DecideRequestDataCache(false, int64Ptr(9)).Cache)
	require.False(t, svc.DecideRequestDataCache(false, nil).Cache)

	errDecision := svc.DecideRequestDataCache(true, int64Ptr(9))
	require.True(t, errDecision.Cache)
	require.Equal(t, 2*time.Hour, errDecision.TTL)
	require.False(t, svc.DecideRequestDataCache(true, int64Ptr(5)).Cache)
	require.False(t, svc.DecideRequestDataCache(true, nil).Cache)

	// 关闭错误请求缓存后不再写入任何错误快照。
	svc.storeRequestDataCacheSettings(RequestDataCacheSettings{
		Normal: RequestDataCacheScope{Enabled: true, TTLMinutes: 60, AllGroups: true},
		Error:  RequestDataCacheScope{Enabled: false, TTLMinutes: 60, AllGroups: true},
	})
	require.False(t, svc.DecideRequestDataCache(true, int64Ptr(9)).Cache)
	require.True(t, svc.DecideRequestDataCache(false, nil).Cache)
}

// 损坏的持久化值回退默认值；分组去重排序、时长越界被拒绝。
func TestRequestDataCacheParseAndValidate(t *testing.T) {
	require.Equal(t, DefaultRequestDataCacheSettings(), parseRequestDataCacheSettings("{bad"))
	require.Equal(t, DefaultRequestDataCacheSettings(), parseRequestDataCacheSettings(""))

	cfg := parseRequestDataCacheSettings(`{"normal":{"enabled":true,"ttl_minutes":0,"group_ids":[3,1,3,-2]}}`)
	require.Equal(t, []int64{1, 3}, cfg.Normal.GroupIDs)
	require.Equal(t, requestDataCacheDefaultNormalTTLMinutes, cfg.Normal.TTLMinutes)
	// 缺失的错误请求配置保持默认开启。
	require.True(t, cfg.Error.Enabled)

	bad := DefaultRequestDataCacheSettings()
	bad.Error.TTLMinutes = requestDataCacheMaxTTLMinutes + 1
	require.Error(t, ValidateRequestDataCacheSettings(bad))
	bad = DefaultRequestDataCacheSettings()
	bad.Normal.GroupIDs = []int64{0}
	require.Error(t, ValidateRequestDataCacheSettings(bad))
	require.NoError(t, ValidateRequestDataCacheSettings(DefaultRequestDataCacheSettings()))
}

// 用户只能看到本人且未过期的缓存；历史快照没有归属，不对用户开放。
func TestRequestDataCacheVisibility(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	past := now.Add(-time.Second)
	owned := &OpsRequestPayloadDetail{UserID: int64Ptr(7), ExpiresAt: &future}
	require.True(t, requestDataCacheVisible(owned, 7, now))
	require.False(t, requestDataCacheVisible(owned, 8, now))
	require.False(t, requestDataCacheVisible(&OpsRequestPayloadDetail{UserID: int64Ptr(7), ExpiresAt: &past}, 7, now))
	require.False(t, requestDataCacheVisible(&OpsRequestPayloadDetail{}, 7, now))
	require.True(t, requestDataCacheVisible(&OpsRequestPayloadDetail{UserID: int64Ptr(7)}, 7, now))
}

// 用户视图只暴露端点元数据，不含请求头与正文。
func TestRequestDataCacheEntryOmitsPayload(t *testing.T) {
	entry := toRequestDataCacheEntry(&OpsRequestPayloadDetail{
		RequestID: "r1", InboundEndpoint: "/v1/chat/completions", UpstreamEndpoint: "/v1/responses",
		RequestHeaders: `{"authorization":"x"}`, RequestBody: "secret", ResponseBody: "secret",
	})
	require.Equal(t, RequestDataCacheKindError, entry.Kind)
	require.Equal(t, "/v1/responses", entry.UpstreamEndpoint)
}

// 手动清除只接受合法类别。
func TestClearRequestDataCacheRejectsUnknownKind(t *testing.T) {
	svc := &OpsService{opsRepo: &opsRepoMock{}}
	_, err := svc.ClearRequestDataCache(nil, "bogus")
	require.Error(t, err)
	for _, kind := range []string{"normal", "error", "all"} {
		_, err := svc.ClearRequestDataCache(nil, kind)
		require.NoError(t, err)
	}
}
