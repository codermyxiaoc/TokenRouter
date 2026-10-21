//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 创建默认关闭；局部更新省略时保留，显式 false 必须能关闭已开启的策略。
func TestAdminGroupMediaSubscriptionPriorityCreateAndUpdate(t *testing.T) {
	ctx := context.Background()
	for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformGemini, PlatformAntigravity, PlatformVideo} {
		t.Run(platform, func(t *testing.T) {
			repo := &groupRepoStubForAdmin{}
			svc := &adminServiceImpl{groupRepo: repo}
			created, err := svc.CreateGroup(ctx, &CreateGroupInput{Name: "priority", Platform: platform, RateMultiplier: 1})
			require.NoError(t, err)
			require.False(t, created.ImageSubscriptionRatePriority)
			require.False(t, created.VideoSubscriptionRatePriority)

			created, err = svc.CreateGroup(ctx, &CreateGroupInput{
				Name: "priority-enabled", Platform: platform, RateMultiplier: 1,
				ImageRateIndependent: true, VideoRateIndependent: true,
				ImageSubscriptionRatePriority: true, VideoSubscriptionRatePriority: true,
			})
			require.NoError(t, err)
			require.True(t, repo.created.ImageSubscriptionRatePriority)
			require.True(t, repo.created.VideoSubscriptionRatePriority)
			repo.getByID = created
			description := "unrelated edit"
			updated, err := svc.UpdateGroup(ctx, created.ID, &UpdateGroupInput{Description: &description})
			require.NoError(t, err)
			require.True(t, updated.ImageSubscriptionRatePriority)
			require.True(t, updated.VideoSubscriptionRatePriority)
			disabled := false
			updated, err = svc.UpdateGroup(ctx, created.ID, &UpdateGroupInput{
				ImageSubscriptionRatePriority: &disabled, VideoSubscriptionRatePriority: &disabled,
			})
			require.NoError(t, err)
			require.False(t, updated.ImageSubscriptionRatePriority)
			require.False(t, updated.VideoSubscriptionRatePriority)
			require.False(t, repo.updated.ImageSubscriptionRatePriority)
			require.False(t, repo.updated.VideoSubscriptionRatePriority)
		})
	}
}
