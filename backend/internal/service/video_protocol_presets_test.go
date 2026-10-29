//go:build unit

package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// videoPresetTarget 按预置模板生成冻结的版本三目标。
func videoPresetTarget(t *testing.T, key, model string) VideoUpstreamTarget {
	t.Helper()
	profile, err := videoProtocolPresetProfile(key, 0)
	require.NoError(t, err)
	snapshot, err := json.Marshal(profile.Spec)
	require.NoError(t, err)
	target := VideoUpstreamTarget{Version: videoTargetVersionProfile, Model: model, BaseURL: "https://relay.example", AccountID: 1,
		ProtocolAdapter: VideoProfileProtocolID(key, profile.Version), ProfileSnapshot: snapshot, ProfileHash: profile.SpecHash}
	target.CreatePath, err = renderVideoProfilePath(profile.Spec.Create.Path, target, "", false)
	require.NoError(t, err)
	require.NoError(t, ValidateVideoProtocolTarget(target))
	return target
}

// videoPresetLegacyTarget 生成旧版本二厂商方案目标，作为对照基准。
func videoPresetLegacyTarget(endpoint VideoEndpoint, adapter, path, model string) VideoUpstreamTarget {
	target := VideoUpstreamTarget{Version: 2, Endpoint: endpoint, ProtocolAdapter: adapter, Model: model, BaseURL: "https://relay.example", AccountID: 1}
	switch endpoint {
	case VideoEndpointCompat:
		target.CreatePath = "/v1/video/generations"
	case VideoEndpointOpenAIVideos:
		target.CreatePath = "/v1/videos"
	case VideoEndpointSeedance:
		target.CreatePath = "/api/v3/contents/generations/tasks"
	case VideoEndpointMiniMax:
		target.CreatePath = "/v2/video_generation"
	case VideoEndpointWan:
		target.CreatePath = "/api/v1/services/aigc/video-generation/video-synthesis"
	case VideoEndpointKling:
		target.CreatePath = strings.ReplaceAll(path, "{model}", model)
	case VideoEndpointVidu:
		target.CreatePath = path
	}
	return target
}

// 全部预置模板都能通过保存校验，内置定义与入库版本的哈希一致。
func TestVideoProtocolPresetsValidate(t *testing.T) {
	seen := map[string]bool{}
	for _, preset := range videoProtocolPresets() {
		require.True(t, ValidVideoProfileKey(preset.Key), preset.Key)
		require.False(t, seen[preset.Key], "duplicate preset %s", preset.Key)
		seen[preset.Key] = true
		require.NoError(t, preset.Spec.Validate(), preset.Key)
		raw, err := json.Marshal(preset.Spec)
		require.NoError(t, err)
		var decoded VideoProtocolProfileSpec
		require.NoError(t, json.Unmarshal(raw, &decoded))
		first, _ := preset.Spec.Hash()
		second, _ := decoded.Hash()
		require.Equal(t, first, second, "%s hash must survive a JSON round trip", preset.Key)
	}
}

type videoPresetRequestCase struct {
	name    string
	preset  string
	legacy  VideoUpstreamTarget
	request string
}

// 预置模板与旧厂商方案对同一规范请求生成等价的上游请求体与计价维度。
func TestVideoProtocolPresetRequestEquivalence(t *testing.T) {
	kling := func(op string) VideoUpstreamTarget {
		return videoPresetLegacyTarget(VideoEndpointKling, VideoProtocolAdapterKemei, "/"+op+"/{model}", "kling-v3")
	}
	vidu := func(op string) VideoUpstreamTarget {
		return videoPresetLegacyTarget(VideoEndpointVidu, VideoProtocolAdapterKemei, "/ent/v2/"+op, "viduq3-pro")
	}
	cases := []videoPresetRequestCase{
		{"h3 text", "metaso-h3", videoPresetLegacyTarget(VideoEndpointOpenAIVideos, VideoProtocolAdapterMetasoH3, "", "h3"),
			`{"model":"h3","prompt":"a cat","duration":6,"resolution":"768p","aspect_ratio":"16:9"}`},
		{"h3 image portrait 2k", "metaso-h3", videoPresetLegacyTarget(VideoEndpointOpenAIVideos, VideoProtocolAdapterMetasoH3, "", "h3"),
			`{"model":"h3","prompt":"a cat","duration":10,"resolution":"2k","aspect_ratio":"9:16","images":["https://img.example/a.png"]}`},
		{"h3 explicit size", "metaso-h3", videoPresetLegacyTarget(VideoEndpointOpenAIVideos, VideoProtocolAdapterMetasoH3, "", "h3"),
			`{"model":"h3","prompt":"a cat","size":"720x1280"}`},
		{"yezonghui full", "yezonghui", videoPresetLegacyTarget(VideoEndpointOpenAIVideos, VideoProtocolAdapterYezonghui, "", "yz"),
			`{"model":"yz","prompt":"a cat","duration":8,"resolution":"1080p","aspect_ratio":"16:9","images":["https://img.example/a.png"],"generate_audio":true,"seed":7,"negative_prompt":"blur"}`},
		{"yezonghui minimal", "yezonghui", videoPresetLegacyTarget(VideoEndpointOpenAIVideos, VideoProtocolAdapterYezonghui, "", "yz"),
			`{"model":"yz","prompt":"a cat","duration":5,"resolution":"720p"}`},
		{"generic flat compat", "generic-flat", videoPresetLegacyTarget(VideoEndpointCompat, VideoProtocolAdapterLegacy, "", "m"),
			`{"model":"m","prompt":"p","negative_prompt":"n","duration":5,"resolution":"720p","aspect_ratio":"16:9","images":["https://img.example/a.png"],"generate_audio":false,"watermark":true,"seed":3}`},
		{"generic flat videos", "generic-flat-videos", videoPresetLegacyTarget(VideoEndpointOpenAIVideos, VideoProtocolAdapterLegacy, "", "m"),
			`{"model":"m","prompt":"p","duration":4,"size":"1280x720"}`},
		{"kemei compat roles", "kemei-compat", videoPresetLegacyTarget(VideoEndpointCompat, VideoProtocolAdapterKemei, "", "m"),
			`{"model":"m","prompt":"p","duration":5,"resolution":"720p","aspect_ratio":"adaptive","images":[{"url":"https://img.example/a.png","role":"first_frame"},{"url":"https://img.example/b.png","role":"last_frame"}],"generate_audio":true}`},
		{"kemei compat plain", "kemei-compat", videoPresetLegacyTarget(VideoEndpointCompat, VideoProtocolAdapterKemei, "", "m"),
			`{"model":"m","prompt":"p","images":["https://img.example/a.png"],"videos":["https://vid.example/a.mp4"]}`},
		{"kemei seedance frames", "kemei-seedance", videoPresetLegacyTarget(VideoEndpointSeedance, VideoProtocolAdapterKemei, "", "seed"),
			`{"model":"seed","prompt":"p","duration":5,"resolution":"720p","aspect_ratio":"16:9","images":["https://img.example/a.png","https://img.example/b.png"],"seed":9}`},
		{"kemei seedance references", "kemei-seedance", videoPresetLegacyTarget(VideoEndpointSeedance, VideoProtocolAdapterKemei, "", "seed"),
			`{"model":"seed","prompt":"p","images":["https://img.example/a.png","https://img.example/b.png","https://img.example/c.png"],"videos":["https://vid.example/a.mp4"],"generate_audio":true}`},
		{"kemei minimax", "kemei-minimax", videoPresetLegacyTarget(VideoEndpointMiniMax, VideoProtocolAdapterKemei, "", "MiniMax-Hailuo"),
			`{"model":"MiniMax-Hailuo","prompt":"p","duration":6,"resolution":"768p","aspect_ratio":"16:9","watermark":true}`},
		{"kemei minimax image", "kemei-minimax", videoPresetLegacyTarget(VideoEndpointMiniMax, VideoProtocolAdapterKemei, "", "MiniMax-Hailuo"),
			`{"model":"MiniMax-Hailuo","prompt":"p","duration":10,"resolution":"1080p","images":["https://img.example/a.png"]}`},
		{"kemei wan", "kemei-wan", videoPresetLegacyTarget(VideoEndpointWan, VideoProtocolAdapterKemei, "", "wan2.6"),
			`{"model":"wan2.6","prompt":"p","negative_prompt":"n","duration":5,"resolution":"720p","aspect_ratio":"16:9","images":["https://img.example/a.png"],"watermark":false,"seed":1,"generate_audio":true}`},
		{"kemei kling text", "kemei-kling-text", kling("text-to-video"),
			`{"model":"kling-v3","prompt":"p","duration":10,"resolution":"1080p","aspect_ratio":"9:16","generate_audio":true,"watermark":true}`},
		{"kemei kling text defaults", "kemei-kling-text", kling("text-to-video"), `{"model":"kling-v3","prompt":"p"}`},
		{"kemei kling image frames", "kemei-kling-image", kling("image-to-video"),
			`{"model":"kling-v3","prompt":"p","images":["https://img.example/a.png","https://img.example/b.png"]}`},
		{"kemei kling omni", "kemei-kling-omni", kling("omni-video"),
			`{"model":"kling-v3","prompt":"p","images":[{"url":"https://img.example/a.png","role":"reference_image"}],"videos":["https://vid.example/a.mp4"],"duration":5}`},
		{"kemei vidu img2video", "kemei-vidu-img2video", vidu("img2video"),
			`{"model":"viduq3-pro","prompt":"p","images":["https://img.example/a.png"],"generate_audio":true}`},
		{"kemei vidu reference", "kemei-vidu-reference2video", vidu("reference2video"),
			`{"model":"viduq3-pro","prompt":"p","images":["https://img.example/a.png","https://img.example/b.png"],"duration":8,"resolution":"1080p","aspect_ratio":"9:16","seed":4}`},
		{"kemei vidu text", "kemei-vidu-text2video", vidu("text2video"), `{"model":"viduq3-pro","prompt":"p"}`},
		{"kemei vidu start end", "kemei-vidu-start-end2video", vidu("start-end2video"),
			`{"model":"viduq3-pro","prompt":"p","images":["https://img.example/a.png","https://img.example/b.png"]}`},
	}
	// 旧可美 Vidu 方案无法消费统一入口转换出的图片对象（统一入口下带图请求会被拒绝），这里改用旧客户端直接发送的原生扁平请求对照。
	native := map[string]string{
		"kemei vidu img2video": `{"model":"viduq3-pro","prompt":"p","images":["https://img.example/a.png"],"generate_audio":true}`,
		"kemei vidu reference": `{"model":"viduq3-pro","prompt":"p","images":["https://img.example/a.png","https://img.example/b.png"],"duration":8,"resolution":"1080p","ratio":"9:16","seed":4}`,
		"kemei vidu start end": `{"model":"viduq3-pro","prompt":"p","images":["https://img.example/a.png","https://img.example/b.png"]}`,
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonical, err := ParseVideoCanonicalRequest([]byte(tc.request))
			require.NoError(t, err)
			canonical.Model = tc.legacy.Model
			legacyBody, err := videoBuiltinRequestBody(canonical, tc.legacy)
			require.NoError(t, err)
			if body, ok := native[tc.name]; ok {
				legacyBody = []byte(body)
			}
			oldBody, oldMeta, err := PrepareVideoProtocolRequest(legacyBody, "", tc.legacy)
			require.NoError(t, err)

			target := videoPresetTarget(t, tc.preset, tc.legacy.Model)
			require.Equal(t, tc.legacy.CreatePath, target.CreatePath)
			profileBody, err := videoCanonicalBodyForTarget(canonical, target)
			require.NoError(t, err)
			newBody, newMeta, err := PrepareVideoProtocolRequest(profileBody, "", target)
			require.NoError(t, err)

			require.JSONEq(t, string(oldBody), string(newBody))
			require.Equal(t, oldMeta.DurationSeconds, newMeta.DurationSeconds, "duration")
			// 计价前统一按 NormalizeVideoPriceResolution 归一，大小写差异不影响报价。
			oldTier, _ := NormalizeVideoPriceResolution(oldMeta.Resolution)
			newTier, _ := NormalizeVideoPriceResolution(newMeta.Resolution)
			require.Equal(t, oldTier, newTier, "resolution")
			require.Equal(t, oldMeta.Size, newMeta.Size, "size")
			require.Equal(t, oldMeta.HasReferenceVideo, newMeta.HasReferenceVideo, "reference video")
			if oldMeta.ReferenceImageCount != nil {
				require.NotNil(t, newMeta.ReferenceImageCount)
				require.Equal(t, *oldMeta.ReferenceImageCount, *newMeta.ReferenceImageCount, "reference images")
			}
		})
	}
}

// 旧方案拒绝的规范请求，预置模板在选号与预扣前同样拒绝。
func TestVideoProtocolPresetRequestRejections(t *testing.T) {
	for _, tc := range []struct{ preset, model, request string }{
		{"metaso-h3", "h3", `{"model":"h3","prompt":"p","duration":5.5,"resolution":"768p","aspect_ratio":"16:9"}`},
		{"metaso-h3", "h3", `{"model":"h3","prompt":"p","resolution":"768p"}`},
		{"metaso-h3", "h3", `{"model":"h3","prompt":"p","resolution":"1080p","aspect_ratio":"16:9"}`},
		{"metaso-h3", "h3", `{"model":"h3","prompt":"p","images":["https://a.example/1.png","https://a.example/2.png"]}`},
		{"metaso-h3", "h3", `{"model":"h3","prompt":"p","videos":["https://a.example/1.mp4"]}`},
		{"yezonghui", "yz", `{"model":"yz","duration":5,"resolution":"720p"}`},
		{"yezonghui", "yz", `{"model":"yz","prompt":"p","resolution":"720p"}`},
		{"yezonghui", "yz", `{"model":"yz","prompt":"p","duration":5,"resolution":"1280x720"}`},
		{"yezonghui", "yz", `{"model":"yz","prompt":"p","duration":5,"resolution":"720p","images":["data:image/png;base64,AAAA"]}`},
		{"yezonghui", "yz", `{"model":"yz","prompt":"p","duration":5,"resolution":"720p","aspect_ratio":"adaptive"}`},
		{"yezonghui", "yz", `{"model":"yz","prompt":"p","duration":5,"resolution":"720p","watermark":true}`},
		{"kemei-minimax", "mm", `{"model":"mm","prompt":"p","duration":3,"resolution":"768p","aspect_ratio":"16:9"}`},
		{"kemei-minimax", "mm", `{"model":"mm","prompt":"p","duration":6,"resolution":"768p"}`},
		{"kemei-minimax", "mm", `{"model":"mm","prompt":"p","duration":6,"aspect_ratio":"16:9"}`},
		{"kemei-minimax", "mm", `{"model":"mm","prompt":"p","duration":6,"resolution":"768p","images":[{"url":"https://a.example/1.png","role":"first_frame"},{"url":"https://a.example/2.png","role":"reference_image"}]}`},
		{"kemei-wan", "wan", `{"model":"wan","prompt":"p","images":[{"url":"https://a.example/1.png","role":"last_frame"}]}`},
		{"kemei-kling-text", "kling-v3", `{"model":"kling-v3","prompt":"p","images":["https://a.example/1.png"]}`},
		{"kemei-kling-image", "kling-v3", `{"model":"kling-v3","prompt":"p"}`},
		{"kemei-kling-omni", "kling-v3", `{"model":"kling-v3","prompt":"p","videos":["https://a.example/1.mp4","https://a.example/2.mp4"]}`},
		{"kemei-vidu-text2video", "viduq4", `{"model":"viduq4","prompt":"p"}`},
		{"kemei-vidu-start-end2video", "viduq4-turbo", `{"model":"viduq4-turbo","prompt":"p","images":["https://a.example/1.png","https://a.example/2.png"]}`},
		{"kemei-vidu-img2video", "viduq3-pro", `{"model":"viduq3-pro","prompt":"p"}`},
		{"kemei-vidu-text2video", "viduq3-pro", `{"model":"viduq3-pro","prompt":"p","images":["https://a.example/1.png"]}`},
	} {
		target := videoPresetTarget(t, tc.preset, tc.model)
		_, _, err := PrepareVideoProtocolRequest([]byte(tc.request), "", target)
		require.Error(t, err, "%s %s", tc.preset, tc.request)
	}
}

type videoPresetResponseCase struct {
	name   string
	preset string
	legacy VideoUpstreamTarget
	taskID string
	body   string
}

// 预置模板与旧厂商方案对同一上游响应解析出一致的状态、成品与计价观测；旧方案判为不可信的响应，模板同样不可信。
func TestVideoProtocolPresetResponseEquivalence(t *testing.T) {
	h3 := videoPresetLegacyTarget(VideoEndpointOpenAIVideos, VideoProtocolAdapterMetasoH3, "", "h3")
	yz := videoPresetLegacyTarget(VideoEndpointOpenAIVideos, VideoProtocolAdapterYezonghui, "", "yz")
	compat := videoPresetLegacyTarget(VideoEndpointCompat, VideoProtocolAdapterKemei, "", "m")
	seedance := videoPresetLegacyTarget(VideoEndpointSeedance, VideoProtocolAdapterKemei, "", "seed")
	minimax := videoPresetLegacyTarget(VideoEndpointMiniMax, VideoProtocolAdapterKemei, "", "mm")
	wan := videoPresetLegacyTarget(VideoEndpointWan, VideoProtocolAdapterKemei, "", "wan")
	vidu := videoPresetLegacyTarget(VideoEndpointVidu, VideoProtocolAdapterKemei, "/ent/v2/img2video", "viduq3-pro")
	flat := videoPresetLegacyTarget(VideoEndpointCompat, VideoProtocolAdapterLegacy, "", "m")
	klingOmni := videoPresetLegacyTarget(VideoEndpointKling, VideoProtocolAdapterKemei, "/omni-video/{model}", "kling-v3")
	cases := []videoPresetResponseCase{
		{"h3 created", "metaso-h3", h3, "", `{"id":"t1","status":"queued"}`},
		{"h3 completed", "metaso-h3", h3, "t1", `{"id":"t1","status":"completed","seconds":"6","size":"1280x720","video":{"url":"https://cdn.example/v.mp4"}}`},
		{"h3 conflicting seconds", "metaso-h3", h3, "t1", `{"id":"t1","status":"completed","seconds":"4","duration":5}`},
		{"h3 case ambiguous", "metaso-h3", h3, "t1", `{"id":"t1","status":"completed","seconds":"4","Seconds":"8"}`},
		{"h3 failed", "metaso-h3", h3, "t1", `{"id":"t1","status":"failed"}`},
		{"yz created", "yezonghui", yz, "", `{"id":"t1","object":"video","status":"queued"}`},
		{"yz in progress", "yezonghui", yz, "t1", `{"id":"t1","status":"in_progress"}`},
		{"yz completed", "yezonghui", yz, "t1", `{"id":"t1","object":"video","status":"completed","seconds":"8","resolution":"1080p","url":"https://cdn.example/v.mp4","usage":{"output_tokens":120}}`},
		{"yz zero tokens", "yezonghui", yz, "t1", `{"id":"t1","status":"completed","seconds":"8","url":"https://cdn.example/v.mp4","usage":{"output_tokens":0}}`},
		{"yz error envelope", "yezonghui", yz, "t1", `{"id":"t1","status":"completed","error":{"message":"x"},"url":"https://cdn.example/v.mp4"}`},
		{"yz private url", "yezonghui", yz, "t1", `{"id":"t1","status":"completed","url":"https://127.0.0.1/v.mp4"}`},
		{"yz duplicate", "yezonghui", yz, "t1", `{"id":"t1","status":"completed","seconds":"4","seconds":"8"}`},
		{"kemei compat completed", "kemei-compat", compat, "t1", `{"id":"t1","status":"succeeded","data":[{"url":"https://cdn.example/v.mp4"}],"usage":{"completion_tokens":300,"output_seconds":5}}`},
		{"kemei compat zero tokens", "kemei-compat", compat, "t1", `{"id":"t1","status":"succeeded","data":[{"url":"https://cdn.example/v.mp4"}],"usage":{"completion_tokens":0}}`},
		{"kemei compat multiple outputs", "kemei-compat", compat, "t1", `{"id":"t1","status":"succeeded","data":[{"url":"https://cdn.example/a.mp4"},{"url":"https://cdn.example/b.mp4"}]}`},
		{"kemei compat invalid tokens", "kemei-compat", compat, "t1", `{"id":"t1","status":"succeeded","usage":{"completion_tokens":-1}}`},
		{"kemei seedance completed", "kemei-seedance", seedance, "t1", `{"id":"t1","status":"succeeded","content":{"video_url":"https://cdn.example/v.mp4"},"duration":5,"resolution":"720p","usage":{"completion_tokens":1000}}`},
		{"kemei seedance running", "kemei-seedance", seedance, "t1", `{"id":"t1","status":"running"}`},
		{"kemei minimax created", "kemei-minimax", minimax, "", `{"task_id":"t1","base_resp":{"status_code":0}}`},
		{"kemei minimax completed", "kemei-minimax", minimax, "t1", `{"task":{"id":"t1","status":"succeeded","content":{"url":"https://cdn.example/v.mp4"},"usage":{"output_seconds":6,"completion_tokens":50}}}`},
		{"kemei wan completed", "kemei-wan", wan, "t1", `{"output":{"task_id":"t1","task_status":"SUCCEEDED","video_url":"https://cdn.example/v.mp4"},"usage":{"output_video_duration":5,"SR":720}}`},
		{"kemei wan pending", "kemei-wan", wan, "t1", `{"output":{"task_id":"t1","task_status":"PENDING"}}`},
		{"kemei vidu created", "kemei-vidu-img2video", vidu, "", `{"task_id":"t1","state":"created"}`},
		{"kemei vidu completed", "kemei-vidu-img2video", vidu, "t1", `{"id":"t1","state":"success","duration":5,"resolution":"720p","creations":[{"url":"https://cdn.example/v.mp4"}]}`},
		{"kemei vidu error code", "kemei-vidu-img2video", vidu, "t1", `{"id":"t1","state":"success","err_code":"failed","creations":[{"url":"https://cdn.example/v.mp4"}]}`},
		{"kemei vidu many creations", "kemei-vidu-img2video", vidu, "t1", `{"id":"t1","state":"success","creations":[{"url":"https://cdn.example/a"},{"url":"https://cdn.example/b"}]}`},
		{"kemei vidu failed", "kemei-vidu-img2video", vidu, "t1", `{"id":"t1","state":"failed","err_code":"invalid_image"}`},
		{"kemei kling created", "kemei-kling-omni", klingOmni, "", `{"code":0,"data":{"task_id":"t1","task_status":"submitted"}}`},
		{"kemei kling rejected", "kemei-kling-omni", klingOmni, "", `{"code":1201,"message":"bad"}`},
		{"kemei kling processing", "kemei-kling-omni", klingOmni, "t1", `{"code":0,"data":[{"id":"t1","status":"processing"}]}`},
		{"kemei kling completed", "kemei-kling-omni", klingOmni, "t1", `{"code":0,"data":[{"id":"t1","status":"succeed","settings":{"resolution":"1080p"},"outputs":[{"type":"cover","url":"https://cdn.example/c.png"},{"type":"video","url":"https://cdn.example/v.mp4","duration":"5"}]}]}`},
		{"kemei kling no duration", "kemei-kling-omni", klingOmni, "t1", `{"code":0,"data":[{"id":"t1","status":"succeed","outputs":[{"type":"video","url":"https://cdn.example/v.mp4"}]}]}`},
		{"kemei kling two videos", "kemei-kling-omni", klingOmni, "t1", `{"code":0,"data":[{"id":"t1","status":"succeed","outputs":[{"type":"video","url":"https://cdn.example/a.mp4","duration":5},{"type":"video","url":"https://cdn.example/b.mp4","duration":5}]}]}`},
		{"kemei kling compat shape", "kemei-kling-omni", klingOmni, "t1", `{"id":"t1","status":"completed","data":[{"url":"https://cdn.example/v.mp4"}]}`},
		{"flat created", "generic-flat", flat, "", `{"task_id":"t1","status":"submitted"}`},
		{"flat completed", "generic-flat", flat, "t1", `{"task_id":"t1","status":"completed","video":{"url":"https://cdn.example/v.mp4"},"duration":5,"usage":{"completion_tokens":0}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old, err := ExtractVideoProtocolResponse(tc.legacy, 200, []byte(tc.body), tc.taskID)
			require.NoError(t, err)
			target := videoPresetTarget(t, tc.preset, tc.legacy.Model)
			require.True(t, VideoProtocolResponseMatchesTask(target, []byte(tc.body), tc.taskID))
			current, err := ExtractVideoProtocolResponse(target, 200, []byte(tc.body), tc.taskID)
			require.NoError(t, err)
			require.Equal(t, old.MetadataError != "", current.MetadataError != "", "metadata error old=%q new=%q", old.MetadataError, current.MetadataError)
			require.Equal(t, old.TaskID, current.TaskID, "task id")
			if old.MetadataError != "" {
				// 不可信观测只要求两边都不能交付或计价。
				require.Empty(t, current.VideoURL)
				return
			}
			require.Equal(t, old.Status, current.Status, "status")
			require.Equal(t, old.VideoURL, current.VideoURL, "video url")
			require.Equal(t, old.Metadata.DurationSeconds, current.Metadata.DurationSeconds, "seconds")
			require.Equal(t, old.Metadata.Resolution, current.Metadata.Resolution, "resolution")
			require.Equal(t, old.Metadata.Tokens, current.Metadata.Tokens, "tokens")
		})
	}
}

// 旧厂商方案保存时自动改绑到预置模板或官方协议；没有等价替代的旧路径保持原样并在保存时拒绝。
func TestVideoDeprecatedProtocolRebinding(t *testing.T) {
	for protocol, want := range map[string]string{
		"openai_videos.metaso_h3_v1": "profile:metaso-h3",
		"openai_videos.yezonghui_v1": "profile:yezonghui",
		"openai_videos.legacy_v1":    "profile:generic-flat-videos",
		"compat.legacy_v1":           "profile:generic-flat",
		"compat.kemei_v1":            "profile:kemei-compat",
		"seedance.kemei_v1":          "profile:kemei-seedance",
		"minimax.kemei_v1":           "profile:kemei-minimax",
		"wan.kemei_v1":               "profile:kemei-wan",
		"seedance.legacy_v1":         "seedance.seedance_v1",
		"minimax.legacy_v1":          "minimax.minimax_v2",
		"wan.legacy_v1":              "wan.wan_v1",
		"seedance.seedance_v1":       "seedance.seedance_v1",
		"openai_videos.openai_v1":    "openai_videos.openai_v1",
	} {
		protocols := map[string]VideoModelProtocol{"*": {Protocol: protocol, BaseURL: "https://b.example"}}
		rebindDeprecatedVideoProtocols(protocols)
		require.Equal(t, want, protocols["*"].Protocol, protocol)
		require.Equal(t, "https://b.example", protocols["*"].BaseURL)
	}
	for path, want := range map[string]string{"/text-to-video/{model}": "profile:kemei-kling-text", "/image-to-video/{model}": "profile:kemei-kling-image", "/omni-video/{model}": "profile:kemei-kling-omni"} {
		protocols := map[string]VideoModelProtocol{"m": {Protocol: "kling.kemei_v1", Path: path}}
		rebindDeprecatedVideoProtocols(protocols)
		require.Equal(t, want, protocols["m"].Protocol)
		require.Empty(t, protocols["m"].Path)
	}
	for _, op := range []string{"img2video", "reference2video", "text2video", "start-end2video"} {
		protocols := map[string]VideoModelProtocol{"m": {Protocol: "vidu.kemei_v1", Path: "/ent/v2/" + op}}
		rebindDeprecatedVideoProtocols(protocols)
		require.Equal(t, "profile:kemei-vidu-"+op, protocols["m"].Protocol)
	}
	// 原样协议的旧版 Kling 路径没有等价模板，保持原样，保存时提示手动改绑。
	protocols := map[string]VideoModelProtocol{"m": {Protocol: "kling.legacy_v1", Path: "/v1/videos/text2video"}}
	require.False(t, rebindDeprecatedVideoProtocols(protocols))
	account := &Account{Platform: PlatformVideo, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "k", "base_url": "https://relay.example",
		"video_model_protocols": map[string]any{"m": map[string]any{"protocol": "kling.legacy_v1", "path": "/v1/videos/text2video"}},
	}}
	require.ErrorContains(t, normalizeVideoCredentials(account), "replaced by preset templates")
}

// 数据库中没有预置模板时使用内置定义；键名已入库后停用或草稿状态照常生效。
func TestVideoProtocolPresetResolveFallback(t *testing.T) {
	var nilService *VideoProtocolProfileService
	profile, err := nilService.Resolve(t.Context(), "metaso-h3", 0)
	require.NoError(t, err)
	require.Equal(t, 1, profile.Version)
	_, err = nilService.Resolve(t.Context(), "unknown-profile", 0)
	require.ErrorIs(t, err, ErrVideoProtocolProfileNotFound)

	repo := &videoProfileMemoryRepo{}
	svc := NewVideoProtocolProfileService(repo)
	profile, err = svc.Resolve(t.Context(), "kemei-wan", 0)
	require.NoError(t, err)
	require.Equal(t, "kemei-wan", profile.ProfileKey)
	created, err := EnsureVideoProtocolPresets(t.Context(), svc)
	require.NoError(t, err)
	require.Equal(t, len(videoProtocolPresets()), created)
	created, err = EnsureVideoProtocolPresets(t.Context(), svc)
	require.NoError(t, err)
	require.Zero(t, created, "已存在的预置模板不能被覆盖或重复创建")
	require.NoError(t, svc.SetStatus(t.Context(), "metaso-h3", 1, VideoProtocolProfileDisabled))
	_, err = svc.Resolve(t.Context(), "metaso-h3", 0)
	require.Error(t, err, "管理员停用的预置模板不能回退到内置定义")
}
