//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 官方请求测试只使用本地 JSON，避免消耗真实上游额度。
func officialProtocolTarget(endpoint VideoEndpoint, adapter string) VideoUpstreamTarget {
	paths := map[VideoEndpoint]string{
		VideoEndpointOpenAIVideos: "/v1/videos", VideoEndpointMiniMax: "/v2/video_generation",
		VideoEndpointSeedance: "/api/v3/contents/generations/tasks", VideoEndpointWan: "/api/v1/services/aigc/video-generation/video-synthesis",
		VideoEndpointKling: "/image-to-video/model",
	}
	return VideoUpstreamTarget{Version: 2, Endpoint: endpoint, ProtocolAdapter: adapter, Model: "model", CreatePath: paths[endpoint]}
}

func TestVideoProtocolRequestOfficialOpenAI(t *testing.T) {
	target := officialProtocolTarget(VideoEndpointOpenAIVideos, VideoProtocolAdapterOpenAI)
	body, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"incoming","prompt":"test","duration":8,"resolution":"720p","ratio":"16:9","images":["data:image/png;base64,AAAA"],"seed":9007199254740993}`), "", target)
	require.NoError(t, err)
	require.Equal(t, "model", gjson.GetBytes(body, "model").String())
	require.Equal(t, "8", gjson.GetBytes(body, "seconds").String())
	require.Equal(t, "1280x720", gjson.GetBytes(body, "size").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(body, "seed").Raw)
	require.False(t, gjson.GetBytes(body, "images").Exists())
	require.Equal(t, "data:image/png;base64,AAAA", gjson.GetBytes(body, "input_reference.image_url").String())
	require.Equal(t, float64(8), metadata.DurationSeconds)
	require.Equal(t, "1280x720", metadata.Resolution)
	require.Equal(t, 1, *metadata.ReferenceImageCount)

	body, metadata, err = PrepareVideoProtocolRequest([]byte(`{"model":"model","prompt":"test"}`), "", target)
	require.NoError(t, err)
	require.Equal(t, "4", gjson.GetBytes(body, "seconds").String())
	require.Equal(t, "720x1280", gjson.GetBytes(body, "size").String())
	require.Equal(t, 0, *metadata.ReferenceImageCount)

	_, metadata, err = PrepareVideoProtocolRequest([]byte(`{"model":"model","prompt":"test","input_reference":{"file_id":"file_123"}}`), "", target)
	require.NoError(t, err)
	require.Equal(t, 1, *metadata.ReferenceImageCount)
}

func TestVideoProtocolRequestOfficialRejectsAmbiguity(t *testing.T) {
	cases := []struct {
		name          string
		endpoint      VideoEndpoint
		adapter, body string
	}{
		{"openai unknown seconds", VideoEndpointOpenAIVideos, VideoProtocolAdapterOpenAI, `{"model":"model","prompt":"p","seconds":"5"}`},
		{"openai unknown size", VideoEndpointOpenAIVideos, VideoProtocolAdapterOpenAI, `{"model":"model","prompt":"p","size":"1920x1080"}`},
		{"openai conflicting duration", VideoEndpointOpenAIVideos, VideoProtocolAdapterOpenAI, `{"model":"model","prompt":"p","seconds":"8","duration":4}`},
		{"openai conflicting size", VideoEndpointOpenAIVideos, VideoProtocolAdapterOpenAI, `{"model":"model","prompt":"p","size":"1280x720","ratio":"9:16"}`},
		{"openai conflicting image", VideoEndpointOpenAIVideos, VideoProtocolAdapterOpenAI, `{"model":"model","prompt":"p","images":["a"],"input_reference":{"file_id":"b"}}`},
		{"openai dual reference", VideoEndpointOpenAIVideos, VideoProtocolAdapterOpenAI, `{"model":"model","prompt":"p","input_reference":{"image_url":"a","file_id":"b"}}`},
		{"openai multiple images", VideoEndpointOpenAIVideos, VideoProtocolAdapterOpenAI, `{"model":"model","prompt":"p","images":["a","b"]}`},
		{"uppercase image", VideoEndpointOpenAIVideos, VideoProtocolAdapterOpenAI, `{"model":"model","prompt":"p","Images":["a"]}`},
		{"duplicate key", VideoEndpointOpenAIVideos, VideoProtocolAdapterOpenAI, `{"model":"model","prompt":"p","seconds":"4","seconds":"8"}`},
		{"minimax missing resolution", VideoEndpointMiniMax, VideoProtocolAdapterMiniMax, `{"model":"model","prompt":"p"}`},
		{"minimax content aliases", VideoEndpointMiniMax, VideoProtocolAdapterMiniMax, `{"model":"model","resolution":"768p","prompt":"p","content":[{"type":"text","text":"p"}]}`},
		{"minimax fractional seconds", VideoEndpointMiniMax, VideoProtocolAdapterMiniMax, `{"model":"model","resolution":"768p","prompt":"p","duration":1.5}`},
		{"minimax multiple watermark", VideoEndpointMiniMax, VideoProtocolAdapterMiniMax, `{"model":"model","resolution":"768p","prompt":"p","watermark":true,"aigc_watermark":false}`},
		{"wan conflicting duration", VideoEndpointWan, VideoProtocolAdapterWan, `{"model":"model","prompt":"p","duration":5,"parameters":{"duration":10}}`},
		{"wan conflicting image", VideoEndpointWan, VideoProtocolAdapterWan, `{"model":"model","prompt":"p","images":["a"],"input":{"img_url":"b"}}`},
		{"wan ignored flat media", VideoEndpointWan, VideoProtocolAdapterWan, `{"model":"model","prompt":"p","media":[{"type":"image","url":"a"}]}`},
		{"kling conflicting settings", VideoEndpointKling, VideoProtocolAdapterKling, `{"model":"model","image":"a","duration":5,"settings":{"duration":10}}`},
		{"kling missing first frame", VideoEndpointKling, VideoProtocolAdapterKling, `{"model":"model","prompt":"p","contents":[{"type":"last_frame","url":"a"}]}`},
		{"kling ignores old image list", VideoEndpointKling, VideoProtocolAdapterKling, `{"model":"model","prompt":"p","image_list":[{"image_url":"a"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := PrepareVideoProtocolRequest([]byte(tc.body), "", officialProtocolTarget(tc.endpoint, tc.adapter))
			require.Error(t, err)
		})
	}
}

func TestVideoProtocolRequestOfficialNative(t *testing.T) {
	for _, tc := range []struct {
		endpoint                             VideoEndpoint
		adapter, resolution, inputResolution string
	}{
		{VideoEndpointMiniMax, VideoProtocolAdapterMiniMax, "768P", "768p"}, {VideoEndpointSeedance, VideoProtocolAdapterSeedance, "720p", "720P"},
	} {
		t.Run(tc.adapter, func(t *testing.T) {
			body, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"model","prompt":"p","duration":5,"resolution":"`+tc.inputResolution+`","images":["https://image.test/a.jpg"]}`), "", officialProtocolTarget(tc.endpoint, tc.adapter))
			require.NoError(t, err)
			require.Equal(t, tc.resolution, gjson.GetBytes(body, "resolution").String())
			require.Equal(t, "first_frame", gjson.GetBytes(body, "content.1.role").String())
			require.Equal(t, 1, *metadata.ReferenceImageCount)
			require.Equal(t, float64(5), metadata.DurationSeconds)
			_, metadata, err = PrepareVideoProtocolRequest([]byte(`{"model":"model","resolution":"`+tc.inputResolution+`","content":[{"type":"unknown_media","payload":"a"}]}`), "", officialProtocolTarget(tc.endpoint, tc.adapter))
			require.NoError(t, err)
			require.Nil(t, metadata.ReferenceImageCount)
		})
	}
	wan := officialProtocolTarget(VideoEndpointWan, VideoProtocolAdapterWan)
	body, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"model","prompt":"p","duration":5,"resolution":"720p","img_url":"https://image.test/a.jpg"}`), "", wan)
	require.NoError(t, err)
	require.Equal(t, "720P", gjson.GetBytes(body, "parameters.resolution").String())
	require.Equal(t, "https://image.test/a.jpg", gjson.GetBytes(body, "input.img_url").String())
	require.False(t, gjson.GetBytes(body, "img_url").Exists())
	require.Equal(t, 1, *metadata.ReferenceImageCount)

	kling := officialProtocolTarget(VideoEndpointKling, VideoProtocolAdapterKling)
	body, metadata, err = PrepareVideoProtocolRequest([]byte(`{"model":"model","prompt":"p","duration":5,"resolution":"1080p","ratio":"16:9","image":"https://image.test/a.jpg","image_tail":"https://image.test/b.jpg"}`), "", kling)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "model").Exists())
	require.False(t, gjson.GetBytes(body, "image").Exists())
	require.Equal(t, "16:9", gjson.GetBytes(body, "settings.aspect_ratio").String())
	require.Equal(t, "first_frame", gjson.GetBytes(body, "contents.1.type").String())
	require.Equal(t, "last_frame", gjson.GetBytes(body, "contents.2.type").String())
	require.Equal(t, 2, *metadata.ReferenceImageCount)
	require.Equal(t, "1080p", metadata.Resolution)
	require.Equal(t, float64(5), metadata.DurationSeconds)
}

func TestVideoProtocolRequestFrozenValidation(t *testing.T) {
	target := officialProtocolTarget(VideoEndpointKling, VideoProtocolAdapterKling)
	target.CreatePath = "/omni-video/model"
	require.Error(t, ValidateVideoProtocolTarget(target))
	target.ProtocolAdapter = VideoProtocolAdapterLegacy
	require.NoError(t, ValidateVideoProtocolTarget(target))
	target = officialProtocolTarget(VideoEndpointOpenAIVideos, VideoProtocolAdapterOpenAI)
	target.Model = "vendor/model"
	require.NoError(t, ValidateVideoProtocolTarget(target))
	target.OpenAIProfile = VideoOpenAIProfileMetasoH3
	require.Error(t, ValidateVideoProtocolTarget(target))

	// 历史目标不要求新字段，也不能把默认官方规范强加给已有用户。
	target = VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointMiniMax}
	raw := []byte(`{"model":"relay/model","prompt":"p","arbitrary":{"price":9007199254740993},"duration":5}`)
	body, _, err := PrepareVideoProtocolRequest(raw, "", target)
	require.NoError(t, err)
	require.Equal(t, raw, body)
	adapter, err := VideoTargetProtocolAdapter(target)
	require.NoError(t, err)
	require.Equal(t, VideoProtocolAdapterLegacy, adapter)
}
