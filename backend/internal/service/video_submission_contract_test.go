package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 换号只可改变上游账号及映射模型，不能改变原报价所解释的时长、像素或参考素材数量。
func TestVideoSubmissionRetryKeepsParameterContract(t *testing.T) {
	for name, mutate := range map[string]func(*VideoUpstreamSelection){
		"profile":          func(s *VideoUpstreamSelection) { s.Target.OpenAIProfile = VideoOpenAIProfileMetasoH3 },
		"duration":         func(s *VideoUpstreamSelection) { s.Metadata.DurationSeconds++ },
		"resolution":       func(s *VideoUpstreamSelection) { s.Metadata.Resolution = "768p" },
		"size":             func(s *VideoUpstreamSelection) { s.Metadata.Size = "1280x720" },
		"reference video":  func(s *VideoUpstreamSelection) { s.Metadata.HasReferenceVideo = !s.Metadata.HasReferenceVideo },
		"reference images": func(s *VideoUpstreamSelection) { count := 7; s.Metadata.ReferenceImageCount = &count },
	} {
		t.Run(name, func(t *testing.T) {
			s, key, u, b, _, repo := newVideoRetryFixture()
			if name == "profile" {
				// H3 profile 只属于 OpenAI Videos，不以其他端点的无关字段制造协议差异。
				for _, selection := range u.selections {
					selection.Target.Endpoint = VideoEndpointOpenAIVideos
				}
			}
			mutate(u.selections[1])
			u.responses = []*VideoUpstreamResponse{retryVideoResponse(429, `{"error":{"code":"QuotaExceeded"}}`), retryVideoResponse(200, `{"id":"accepted","status":"queued"}`)}
			result, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			require.Equal(t, []int64{4, 6}, u.attempted, "不兼容的候选必须跳过且不能向其发送生成")
			task, err := repo.Get(context.Background(), result.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, u.selections[0].Metadata, task.Metadata)
			require.Equal(t, 1, b.reserves)
			require.Zero(t, b.releases)
		})
	}
}

func TestVideoSubmissionRetryEmptyAndPassthroughProfilesEquivalent(t *testing.T) {
	for _, profile := range []string{"", VideoOpenAIProfilePassthrough} {
		task := &VideoTaskRecord{Target: VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointOpenAIVideos, OpenAIProfile: profile}, Metadata: VideoRequestMetadata{Model: "old", Resolution: "1280x720", Size: "1280x720", DurationSeconds: 4}}
		next := task.Target
		next.OpenAIProfile = VideoOpenAIProfilePassthrough
		metadata := task.Metadata
		metadata.Model = "mapped"
		require.True(t, VideoTaskRetryTargetCompatible(task, next, metadata))
	}
}
