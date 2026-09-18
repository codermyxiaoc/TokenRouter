package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 恢复标记必须固定目标值并保留失败归属，不能污染此前失败流持有的事件指针。
func TestMarkOpsRecoveredGroupSnapshotsAndPersistsWithoutChangingFailure(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	event := &OpsUpstreamErrorEvent{GroupID: 3, GroupName: "failed", Platform: PlatformOpenAI, UpstreamStatusCode: 503}
	c.Set(OpsUpstreamErrorsKey, []*OpsUpstreamErrorEvent{nil, event})
	group := &Group{ID: 7, Name: "recovery group", Platform: PlatformAnthropic}
	MarkOpsRecoveredGroup(c, group)
	group.Name = "renamed after request"
	value, _ := c.Get(OpsUpstreamErrorsKey)
	events := value.([]*OpsUpstreamErrorEvent)
	require.Nil(t, events[0])
	require.NotSame(t, event, events[1])
	require.Zero(t, event.RecoveredGroupID)
	require.Equal(t, int64(3), events[1].GroupID)
	require.Equal(t, "failed", events[1].GroupName)
	require.Equal(t, PlatformOpenAI, events[1].Platform)
	entry := &OpsInsertErrorLogInput{UpstreamErrors: events}
	require.NoError(t, SanitizeOpsUpstreamErrorsForQueue(entry))
	require.Nil(t, entry.UpstreamErrors)
	stored, err := ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, int64(7), stored[0].RecoveredGroupID)
	require.Equal(t, "recovery group", stored[0].RecoveredGroupName)
	require.Equal(t, PlatformAnthropic, stored[0].RecoveredPlatform)
	// 无事件或未知分组不会伪造一条失败记录。
	empty, _ := gin.CreateTestContext(httptest.NewRecorder())
	MarkOpsRecoveredGroup(empty, group)
	_, exists := empty.Get(OpsUpstreamErrorsKey)
	require.False(t, exists)
	MarkOpsRecoveredGroup(c, &Group{})
	MarkOpsRecoveredGroup(nil, group)
}

// 历史缺失快照、普通 HTTP 200 和最终失败都不能从当前归属猜测恢复目标。
func TestApplyUpstreamAttemptSnapshotRequiresConfirmedRecovery(t *testing.T) {
	groupID := int64(3)
	event := &OpsUpstreamErrorEvent{GroupID: 3, GroupName: "failed snapshot", RecoveredGroupID: 7, RecoveredGroupName: "recovered snapshot", RecoveredPlatform: PlatformAnthropic}
	for _, test := range []struct {
		name, message string
		status        int
		event         *OpsUpstreamErrorEvent
		wantID        int64
	}{
		{"confirmed", "Recovered upstream error 503: failed", 200, event, 7},
		{"legacy", "Recovered upstream error 503: failed", 200, &OpsUpstreamErrorEvent{GroupID: 3}, 0},
		{"no_snapshot", "Recovered upstream error 503: failed", 200, nil, 0},
		{"failed_request", "Recovered upstream error 503: failed", 503, event, 0},
		{"only_headers_succeeded", "stream failed", 200, event, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := &OpsErrorLog{Phase: "upstream", Message: test.message, GroupID: &groupID, GroupName: "current name"}
			row.SetClientStatus(test.status)
			row.ApplyUpstreamAttemptSnapshot(test.event)
			if test.wantID == 0 {
				require.Nil(t, row.RecoveredGroupID)
				require.Empty(t, row.RecoveredGroupName)
			} else {
				require.Equal(t, test.wantID, *row.RecoveredGroupID)
				require.Equal(t, "recovered snapshot", row.RecoveredGroupName)
				require.Equal(t, "failed snapshot", row.GroupName)
			}
		})
	}
}

// 恢复成功的原始上游信息不能经新增用户可见性泄露；本人归属在恢复记录上同样生效。
func TestUserRecoveredErrorRequestsKeepScopeAndSafeSummary(t *testing.T) {
	uid, foreignUID, recoveryID := int64(42), int64(99), int64(7)
	repo := &stubOpsRepoForUserErr{}
	svc := &OpsService{opsRepo: repo}
	for _, include := range []bool{false, true} {
		filter := &OpsErrorLogFilter{UserID: &foreignUID, IncludeRecoveredUpstream: include, Phase: "upstream"}
		_, err := svc.ListUserErrorRequests(context.Background(), uid, filter)
		require.NoError(t, err)
		require.Equal(t, uid, *repo.gotFilter.UserID)
		require.Equal(t, include, repo.gotFilter.IncludeRecoveredUpstream)
		require.True(t, repo.gotFilter.RequireConfirmedRecovery)
		require.Empty(t, repo.gotFilter.Phase)
		require.Equal(t, foreignUID, *filter.UserID)
	}
	repo.detailToReturn = &OpsErrorLogDetail{
		OpsErrorLog: OpsErrorLog{ID: 1, UserID: &uid, Phase: "upstream", StatusCode: 503,
			Message: "Recovered upstream error 503: private account and key material", GroupName: "failed",
			RecoveredGroupID: &recoveryID, RecoveredGroupName: "recovered", RecoveredPlatform: PlatformAnthropic,
			AccountName: "private account", AccountID: &foreignUID, UpstreamEndpoint: "/private-endpoint"},
		ErrorBody: "private upstream body", UpstreamErrors: `[{"account_name":"private account"}]`, APIKeyPrefix: "private-key",
	}
	repo.detailToReturn.SetClientStatus(http.StatusOK)
	out, err := svc.GetUserErrorRequestDetail(context.Background(), uid, 1)
	require.NoError(t, err)
	require.True(t, out.RecoveredUpstream)
	require.Equal(t, 200, out.ClientStatusCode)
	require.Equal(t, 503, out.StatusCode)
	require.Equal(t, recoveryID, *out.RecoveredGroupID)
	require.Equal(t, "recovered", out.RecoveredGroupName)
	require.Equal(t, "Request recovered after an upstream error", out.Message)
	require.Empty(t, out.ErrorBody)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	for _, forbidden := range []string{"private", "account_id", "account_name", "upstream_errors", "api_key_prefix", "upstream_endpoint"} {
		require.False(t, strings.Contains(string(raw), forbidden), "unsafe user field: %s", forbidden)
	}
	foreign, err := svc.GetUserErrorRequestDetail(context.Background(), foreignUID, 1)
	require.Nil(t, foreign)
	require.True(t, infraerrors.IsNotFound(err))
}
