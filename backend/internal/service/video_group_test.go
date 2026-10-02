//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 视频平台仅开放可兑现的基础调度，共用表单隐藏的文本默认值不能改变准入。
func TestVideoGroupRejectsAdvancedAndClearsTextDefaults(t *testing.T) {
	ctx := context.Background()
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}
	_, err := svc.CreateGroup(ctx, &CreateGroupInput{Name: "video", Platform: PlatformVideo, RateMultiplier: 1, SchedulerType: "advanced"})
	require.Equal(t, "VIDEO_SCHEDULER_UNSUPPORTED", infraerrors.Reason(err))
	require.Nil(t, repo.created)
	trueValue := true
	fallback := int64(900)
	group, err := svc.CreateGroup(ctx, &CreateGroupInput{Name: "video", Platform: PlatformVideo, RateMultiplier: 1, MCPXMLInject: &trueValue, ClaudeCodeOnly: true, RequireOAuthOnly: true, SessionIsolationEnabled: true, FallbackGroupID: &fallback, AvailabilityProbeConfig: GroupAvailabilityProbeConfig{Enabled: true}})
	require.NoError(t, err)
	require.Equal(t, GroupSchedulerTypeBasic, group.SchedulerType)
	require.False(t, group.MCPXMLInject)
	require.False(t, group.ClaudeCodeOnly)
	require.False(t, group.RequireOAuthOnly)
	require.False(t, group.SessionIsolationEnabled)
	require.Nil(t, group.FallbackGroupID)
	require.False(t, group.AvailabilityProbeConfig.Enabled)
	repo.getByID = group
	advanced := "advanced"
	_, err = svc.UpdateGroup(ctx, 1, &UpdateGroupInput{SchedulerType: &advanced})
	require.Equal(t, "VIDEO_SCHEDULER_UNSUPPORTED", infraerrors.Reason(err))
}

func TestVideoGroupBindingsRemainIsolatedWhenMixedCheckSkipped(t *testing.T) {
	ctx := context.Background()
	repo := &groupRepoStubForAdmin{getByID: &Group{ID: 4, Platform: PlatformVideo}}
	svc := &adminServiceImpl{groupRepo: repo}
	require.NoError(t, svc.validateVideoGroupBindings(ctx, PlatformVideo, []int64{4}))
	require.Equal(t, "VIDEO_GROUP_PLATFORM_MISMATCH", infraerrors.Reason(svc.validateVideoGroupBindings(ctx, PlatformOpenAI, []int64{4})))
	repo.getByID.Platform = PlatformOpenAI
	require.Equal(t, "VIDEO_GROUP_PLATFORM_MISMATCH", infraerrors.Reason(svc.validateVideoGroupBindings(ctx, PlatformVideo, []int64{4})))
	require.NoError(t, svc.validateVideoGroupBindings(ctx, PlatformAnthropic, []int64{4}))
}
