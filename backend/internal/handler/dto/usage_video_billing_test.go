package dto

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageVideoBillingDTOIncludesSafeHistoricalDetailsForBothRoles(t *testing.T) {
	resolution, duration, count, imageCost := "768p", 3, 7, .3
	log := &service.UsageLog{VideoCount: 1, VideoResolution: &resolution, VideoDurationSeconds: &duration,
		VideoBilling: &service.UsageVideoBillingDetails{Mode: "video", Unit: "second", UnitPrice: .425,
			Resolution: resolution, DurationSeconds: 2.5, ReferenceImageCount: &count, ReferenceImageCost: &imageCost}}
	for _, result := range []any{UsageLogFromService(log), UsageLogFromServiceAdmin(log)} {
		raw, err := json.Marshal(result)
		require.NoError(t, err)
		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &payload))
		require.JSONEq(t, "1", string(payload["video_count"]))
		require.JSONEq(t, `"768p"`, string(payload["video_resolution"]))
		require.JSONEq(t, "3", string(payload["video_duration_seconds"]))
		require.JSONEq(t, `{"mode":"video","unit":"second","unit_price":0.425,"resolution":"768p","has_reference_video":false,"duration_seconds":2.5,"reference_image_count":7,"reference_image_cost":0.3}`, string(payload["video_billing"]))
	}
	converted := UsageLogFromService(log)
	*converted.VideoBilling.ReferenceImageCount = 100
	*converted.VideoBilling.ReferenceImageCost = 100
	require.Equal(t, 7, *log.VideoBilling.ReferenceImageCount)
	require.Equal(t, .3, *log.VideoBilling.ReferenceImageCost)
	raw, err := json.Marshal(UsageLogFromService(&service.UsageLog{VideoCount: 1}))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "video_billing", "无任务快照的旧视频记录不伪造计价单位或参考图片数量")
}
