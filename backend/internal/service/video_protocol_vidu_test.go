//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 官方 Vidu 测试使用固定响应及纯内存请求，不触发真实生成或消耗上游额度。
func officialViduFixture(path string) VideoUpstreamTarget {
	return VideoUpstreamTarget{Version: 2, Endpoint: VideoEndpointVidu, ProtocolAdapter: VideoProtocolAdapterVidu, Model: "viduq3-pro", CreatePath: path, BaseURL: "https://api.vidu.com", AccountID: 11}
}

func TestVideoOfficialViduNativeParametersAndNoInventedDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		images           int
	}{
		{"text", "/ent/v2/text2video", `{"model":"client","prompt":"A calm lake"}`, 0},
		{"image", "/ent/v2/img2video", `{"model":"client","images":["data:image/png;base64,unchanged"]}`, 1},
		{"reference", "/ent/v2/reference2video", `{"model":"client","prompt":"References","images":["https://cdn.example/same.png","https://cdn.example/same.png"]}`, 2},
		{"start_end", "/ent/v2/start-end2video", `{"model":"client","images":["https://cdn.example/start.png","https://cdn.example/end.png"]}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, metadata, err := PrepareVideoProtocolRequest([]byte(tc.body), "", officialViduFixture(tc.path))
			require.NoError(t, err)
			require.Equal(t, "viduq3-pro", gjson.GetBytes(body, "model").String())
			require.NotNil(t, metadata.ReferenceImageCount)
			require.Equal(t, tc.images, *metadata.ReferenceImageCount)
			require.Zero(t, metadata.DurationSeconds)
			require.Empty(t, metadata.Resolution)
			require.False(t, metadata.HasReferenceVideo)
			require.Nil(t, metadata.Tokens)
			for _, key := range []string{"duration", "resolution", "aspect_ratio", "audio", "off_peak"} {
				require.False(t, gjson.GetBytes(body, key).Exists(), key)
			}
			if tc.images > 0 {
				require.JSONEq(t, gjson.Get(tc.body, "images").Raw, gjson.GetBytes(body, "images").Raw)
			}
		})
	}
}

func TestVideoOfficialViduAliasesAndOptionalFields(t *testing.T) {
	target := officialViduFixture("/ent/v2/reference2video")
	body, metadata, err := PrepareVideoProtocolRequest([]byte(`{
		"model":"client","prompt":"With audio","images":["same","same"],"image_urls":["same","same"],
		"duration":8,"resolution":"720P","ratio":"auto","audios":["data:audio/mp3;base64,audio"],
		"generate_audio":false,"off_peak":true,"seed":4294967295,"watermark":true,"wm_position":2,
		"wm_url":"https://cdn.example/logo.png","payload":"opaque","meta_data":"{\"Label\":\"test\"}",
		"callback_url":"https://callback.example/notify","extension":{"integer":9007199254740993}
	}`), "", target)
	require.NoError(t, err)
	require.Equal(t, float64(8), metadata.DurationSeconds)
	require.Equal(t, "720p", metadata.Resolution)
	require.Equal(t, 2, *metadata.ReferenceImageCount)
	require.Equal(t, "auto", gjson.GetBytes(body, "aspect_ratio").String())
	require.Equal(t, "data:audio/mp3;base64,audio", gjson.GetBytes(body, "sounds.0").String())
	require.Equal(t, gjson.False, gjson.GetBytes(body, "audio").Type)
	require.Equal(t, "9007199254740993", gjson.GetBytes(body, "extension.integer").Raw)
	require.Equal(t, "opaque", gjson.GetBytes(body, "payload").String())
	require.Equal(t, "https://callback.example/notify", gjson.GetBytes(body, "callback_url").String())
	for _, key := range []string{"image_urls", "audios", "ratio", "generate_audio"} {
		require.False(t, gjson.GetBytes(body, key).Exists(), key)
	}
}

func TestVideoOfficialViduSubjectsKeepUnknownImageCount(t *testing.T) {
	for _, tc := range []struct {
		name, fragment string
		hasVideo       bool
	}{
		{"library_subject", `"subjects":[{"name":"person","server_id":"saved-subject"}]`, false},
		{"temporary_subject", `"subjects":[{"name":"person","images":["image-a","image-b"]}]`, false},
		{"temporary_video", `"subjects":[{"name":"motion","videos":["video"]}]`, true},
		{"single_subject_example", `"subjects":{"name":"subject","images":"image","videos":"video"}`, true},
		{"automatic_library", `"auto_subjects":true`, false},
		{"images_with_subject", `"images":["image"],"subjects":[{"name":"person","server_id":"saved"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(`{"model":"m","prompt":"[@person] walking",` + tc.fragment + `}`)
			body, metadata, err := PrepareVideoProtocolRequest(input, "", officialViduFixture("/ent/v2/reference2video"))
			require.NoError(t, err)
			require.Nil(t, metadata.ReferenceImageCount, "主体库未知数量不能作零张或仅计算顶层图片")
			require.Equal(t, tc.hasVideo, metadata.HasReferenceVideo)
			if gjson.GetBytes(input, "subjects").Exists() {
				require.JSONEq(t, gjson.GetBytes(input, "subjects").Raw, gjson.GetBytes(body, "subjects").Raw)
			}
		})
	}
	// 空主体数组和关闭智能主体不会抹掉真实图片数量。
	_, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"m","prompt":"p","images":["image"],"subjects":[],"auto_subjects":false}`), "", officialViduFixture("/ent/v2/reference2video"))
	require.NoError(t, err)
	require.Equal(t, 1, *metadata.ReferenceImageCount)
}

func TestVideoOfficialViduRejectsInvalidOrConflictingRequests(t *testing.T) {
	for _, fragment := range []string{
		`"duration":"5"`, `"duration":0`, `"duration":5.5`, `"duration":null`,
		`"resolution":null`, `"resolution":""`, `"ratio":"adaptive"`,
		`"ratio":"16:9","aspect_ratio":"9:16"`, `"generate_audio":true,"audio":false`,
		`"audio":"false"`, `"off_peak":1`, `"seed":-1`, `"seed":1.5`, `"seed":"1"`,
		`"sounds":[1]`, `"sounds":null`, `"sounds":["a"],"audios":["b"]`,
		`"subjects":null`, `"subjects":[{}]`, `"subjects":[{"name":"p","images":[{}]}]`,
		`"subjects":[{"name":"p","videos":""}]`, `"auto_subjects":"true"`, `"Subjects":[]`,
		`"watermark":"true"`, `"wm_position":5`, `"wm_position":1.5`, `"payload":{}`, `"callback_url":true`,
		`"input":{}`, `"content":[]`, `"image_urls":["different"]`, `"images":["duplicate-key"]`,
	} {
		t.Run(fragment, func(t *testing.T) {
			_, _, err := PrepareVideoProtocolRequest([]byte(`{"model":"m","prompt":"p","images":["image"],`+fragment+`}`), "", officialViduFixture("/ent/v2/reference2video"))
			require.Error(t, err)
		})
	}
	for _, tc := range []struct{ path, fields string }{
		{"/ent/v2/text2video", `"images":["image"],"prompt":"p"`},
		{"/ent/v2/text2video", `"images":[]`},
		{"/ent/v2/img2video", `"images":[]`},
		{"/ent/v2/img2video", `"images":["a","b"]`},
		{"/ent/v2/start-end2video", `"images":["a"]`},
		{"/ent/v2/reference2video", `"prompt":"p"`},
		{"/ent/v2/reference2video", `"images":["a"]`},
		{"/ent/v2/img2video", `"images":["a"],"sounds":["sound"]`},
		{"/ent/v2/text2video", `"prompt":"p","subjects":[{"name":"x","server_id":"subject"}]`},
		{"/ent/v2/start-end2video", `"images":["a","b"],"auto_subjects":true`},
	} {
		_, _, err := PrepareVideoProtocolRequest([]byte(`{"model":"m",`+tc.fields+`}`), "", officialViduFixture(tc.path))
		require.Error(t, err, tc.path+tc.fields)
	}
}

func TestVideoOfficialViduResponsesPreserveIdentityAndBillingFacts(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterVidu, VideoProtocolAdapterLegacy, VideoProtocolAdapterKemei} {
		t.Run(adapter, func(t *testing.T) {
			target := officialViduFixture("/ent/v2/img2video")
			target.ProtocolAdapter = adapter
			created, err := ExtractVideoProtocolResponse(target, http.StatusOK, []byte(`{"task_id":"task-a","state":"created","duration":5,"resolution":"720p","credits":4}`), "")
			require.NoError(t, err)
			require.Equal(t, "task-a", created.TaskID)
			require.Equal(t, "queued", created.Status)
			require.Equal(t, float64(5), created.Metadata.DurationSeconds)
			require.Nil(t, created.Metadata.Tokens)
			// 查询只提供产物，不能把积分、产物 ID 或扩展 Token 当成任务身份或可信用量。
			body := []byte(`{"id":"task-a","state":"success","err_code":"","credits":4,"usage":{"output_tokens":999},"creations":[{"id":"product-b","url":"https://cdn.example/video.mp4","cover_url":"https://cdn.example/cover.png"}]}`)
			completed, err := ExtractVideoProtocolResponse(target, http.StatusOK, body, "task-a")
			require.NoError(t, err)
			require.Equal(t, "task-a", completed.TaskID)
			require.Equal(t, "completed", completed.Status)
			require.Equal(t, "https://cdn.example/video.mp4", completed.VideoURL)
			require.Nil(t, completed.Metadata.Tokens)
			require.Zero(t, completed.Metadata.DurationSeconds)
			require.Empty(t, completed.Metadata.Resolution)
			require.Equal(t, body, completed.Body)
			_, err = ExtractVideoProtocolResponse(target, http.StatusOK, body, "other-task")
			require.Error(t, err)
			for _, response := range []string{
				`{"id":"task-a","state":"success","err_code":"failure","creations":[{"url":"video"}]}`,
				`{"id":"task-a","state":"success","creations":[]}`,
				`{"id":"task-a","state":"success","creations":[{"url":"first"},{"url":"second"}]}`,
				`{"id":"task-a","state":"success","duration":-1,"creations":[{"url":"video"}]}`,
				`{"id":"task-a","state":"success","state":"failed","creations":[{"url":"video"}]}`,
			} {
				result, err := ExtractVideoProtocolResponse(target, http.StatusOK, []byte(response), "task-a")
				require.NoError(t, err)
				require.NotEmpty(t, result.MetadataError, response)
			}
		})
	}
}

func TestVideoOfficialViduDoesNotChangeKemeiDefaultsOrLegacyPayload(t *testing.T) {
	target := officialViduFixture("/ent/v2/img2video")
	input := []byte(`{"model":"viduq3-pro", "image_urls":["image"], "extension":{"integer":9007199254740993}}`)
	target.ProtocolAdapter = VideoProtocolAdapterLegacy
	legacyBody, legacyMetadata, err := PrepareVideoProtocolRequest(input, "", target)
	require.NoError(t, err)
	require.Equal(t, input, legacyBody)
	require.Zero(t, legacyMetadata.DurationSeconds)
	require.Empty(t, legacyMetadata.Resolution)
	target.ProtocolAdapter = VideoProtocolAdapterKemei
	kemeiBody, kemeiMetadata, err := PrepareVideoProtocolRequest(input, "", target)
	require.NoError(t, err)
	require.Equal(t, float64(5), kemeiMetadata.DurationSeconds)
	require.Equal(t, "540p", kemeiMetadata.Resolution)
	require.Equal(t, "16:9", gjson.GetBytes(kemeiBody, "aspect_ratio").String())
	target.ProtocolAdapter = VideoProtocolAdapterVidu
	officialBody, officialMetadata, err := PrepareVideoProtocolRequest(input, "", target)
	require.NoError(t, err)
	require.Zero(t, officialMetadata.DurationSeconds)
	require.Empty(t, officialMetadata.Resolution)
	require.False(t, gjson.GetBytes(officialBody, "aspect_ratio").Exists())
}
