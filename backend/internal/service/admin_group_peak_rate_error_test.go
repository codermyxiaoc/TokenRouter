//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 高峰倍率在 fork 中适用于全部平台，非法值统一返回 400，不能退回上游的订阅专用规则。
func TestAdminGroupPeakRateInvalidConfigReturnsBadRequest(t *testing.T) {
	platforms := []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok, PlatformDeepseek, PlatformKimi, PlatformZhipu, PlatformMiniMax, PlatformQoder}
	for _, platform := range platforms {
		t.Run(platform, func(t *testing.T) {
			for _, invalid := range []struct {
				name, start string
				multiplier  float64
			}{
				{"time", "bad", 2}, {"multiplier", "14:00", -1},
			} {
				t.Run(invalid.name, func(t *testing.T) {
					repo := &groupRepoStubForAdmin{}
					svc := &adminServiceImpl{groupRepo: repo}
					_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
						Name: "invalid-peak", Platform: platform, RateMultiplier: 1,
						PeakRateEnabled: true, PeakStart: invalid.start, PeakEnd: "18:00", PeakRateMultiplier: &invalid.multiplier,
					})
					require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
					require.Equal(t, "INVALID_PEAK_RATE_CONFIG", infraerrors.Reason(err))
					require.Nil(t, repo.created)
					repo.getByID = &Group{ID: 1, Name: "existing", Platform: platform, Status: StatusActive}
					enabled := true
					end := "18:00"
					_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{
						PeakRateEnabled: &enabled, PeakStart: &invalid.start, PeakEnd: &end, PeakRateMultiplier: &invalid.multiplier,
					})
					require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
					require.Equal(t, "INVALID_PEAK_RATE_CONFIG", infraerrors.Reason(err))
					require.Nil(t, repo.updated)
				})
			}
		})
	}
}
