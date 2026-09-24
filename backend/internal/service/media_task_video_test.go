//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 状态投影必须保留供应商成功条件，且不能把原始任务数据写入列表。
func TestVideoTaskObservation(t *testing.T) {
	tests := []struct {
		name, body, status, source, upstream string
		endpoint                             GrokMediaEndpoint
	}{
		{"grok create", `{"request_id":"task"}`, "queued", "grok_video", "", GrokMediaEndpointVideosGenerations},
		{"grok edit", `{"request_id":"task","status":"pending"}`, "processing", "grok_video", "pending", GrokMediaEndpointVideosEdits},
		{"grok extension", `{"request_id":"task"}`, "queued", "grok_video", "", GrokMediaEndpointVideosExtensions},
		{"grok pending", `{"status":"pending"}`, "processing", "grok_video", "pending", GrokMediaEndpointVideoStatus},
		{"grok done", `{"status":"done","video":{"url":"https://secret.example/video?token=secret"}}`, "completed", "grok_video", "done", GrokMediaEndpointVideoStatus},
		{"grok done without result", `{"status":"done"}`, "processing", "grok_video", "done", GrokMediaEndpointVideoStatus},
		{"grok failed", `{"status":"failed","error":{"message":"secret prompt sk-secret"}}`, "failed", "grok_video", "failed", GrokMediaEndpointVideoStatus},
		{"grok expired", `{"status":"expired"}`, "expired", "grok_video", "expired", GrokMediaEndpointVideoStatus},
		{"seedance create", `{"id":"task"}`, "queued", "seedance_video", "", SeedanceEndpointCreate},
		{"seedance queued", `{"status":"queued"}`, "queued", "seedance_video", "queued", SeedanceEndpointStatus},
		{"seedance running", `{"status":"running"}`, "processing", "seedance_video", "running", SeedanceEndpointStatus},
		{"seedance completed without usage", `{"status":"succeeded","content":{"video_url":"https://secret.example"}}`, "completed", "seedance_video", "succeeded", SeedanceEndpointStatus},
		{"seedance failed", `{"status":"failed","error":{"message":"secret"}}`, "failed", "seedance_video", "failed", SeedanceEndpointStatus},
		{"seedance cancelled", `{"status":"cancelled"}`, "cancelled", "seedance_video", "cancelled", SeedanceEndpointStatus},
		{"seedance expired", `{"status":"expired"}`, "expired", "seedance_video", "expired", SeedanceEndpointStatus},
		{"seedance delete", ``, "cancelled", "seedance_video", "deleted", SeedanceEndpointDelete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := videoTaskObservation(test.endpoint, []byte(test.body), http.StatusOK)
			require.NotNil(t, observation)
			require.Equal(t, test.status, observation.Status)
			require.Equal(t, test.source, observation.Source)
			require.Equal(t, test.upstream, observation.UpstreamStatus)
			require.Equal(t, "video", observation.MediaType)
			require.Equal(t, http.StatusOK, observation.HTTPStatus)
			terminal := test.status == "completed" || test.status == "failed" || test.status == "cancelled" || test.status == "expired"
			require.Equal(t, terminal, observation.CompletedAt != nil)
			if test.status == "failed" {
				require.Equal(t, "Upstream video task failed", observation.ErrorMessage)
			}
			serialized, err := json.Marshal(observation)
			require.NoError(t, err)
			require.NotContains(t, string(serialized), "secret")
		})
	}
	for _, endpoint := range []GrokMediaEndpoint{GrokMediaEndpointVideoStatus, SeedanceEndpointStatus, GrokMediaEndpointImagesGenerations} {
		require.Nil(t, videoTaskObservation(endpoint, []byte(`{"status":"unknown secret"}`), http.StatusOK))
	}
}
