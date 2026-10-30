//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 模板声明的素材能力决定画布可用的参考图/参考视频数量；没有读取规则时不开放，限定角色时只能发首帧。
func TestCreativeVideoProfileReferenceLimit(t *testing.T) {
	limit := func(key string) CreativeVideoReferenceLimit {
		profile, err := videoProtocolPresetProfile(key, 0)
		require.NoError(t, err)
		return creativeVideoProfileReferenceLimit(&profile.Spec)
	}
	require.Equal(t, CreativeVideoReferenceLimit{MaxImages: 9, MaxVideos: 3}, limit("kemei-minimax"))
	require.Equal(t, CreativeVideoReferenceLimit{MaxImages: 1}, limit("kemei-kling-image"), "图生只接受首尾帧角色，画布只能发一张首帧")
	require.Equal(t, CreativeVideoReferenceLimit{}, creativeVideoProfileReferenceLimit(nil))
	spec := &VideoProtocolProfileSpec{Create: VideoProfileCreate{Body: []VideoProfileRule{{From: "prompt", To: "prompt"}}}}
	require.Equal(t, CreativeVideoReferenceLimit{}, creativeVideoProfileReferenceLimit(spec), "没有规则读取素材时不开放")
	spec.Create.Body = append(spec.Create.Body, VideoProfileRule{From: "images", To: "images"}, VideoProfileRule{From: "videos", To: "videos"})
	require.Equal(t, CreativeVideoReferenceLimit{MaxImages: videoCanonicalMaxMedia, MaxVideos: videoCanonicalMaxMedia}, creativeVideoProfileReferenceLimit(spec), "未声明上限时取规范请求上限")
	// 只映射单项时按可消费的项数推导：多发的素材会被引擎以未映射拒绝，画布不能放开到规范上限。
	only := func(rules ...VideoProfileRule) CreativeVideoReferenceLimit {
		return creativeVideoProfileReferenceLimit(&VideoProtocolProfileSpec{Create: VideoProfileCreate{Body: append([]VideoProfileRule{{From: "prompt", To: "prompt"}}, rules...)}})
	}
	require.Equal(t, CreativeVideoReferenceLimit{MaxImages: 1}, only(VideoProfileRule{From: "images[0]", To: "image"}))
	require.Equal(t, CreativeVideoReferenceLimit{MaxImages: 1}, only(VideoProfileRule{From: "images[role=first_frame]", To: "first_frame"}))
	require.Equal(t, CreativeVideoReferenceLimit{MaxImages: 2}, only(VideoProfileRule{From: "images[0]", To: "a"}, VideoProfileRule{From: "images[1]", To: "b"}))
	require.Equal(t, CreativeVideoReferenceLimit{}, only(VideoProfileRule{From: "images[1]", To: "b"}), "缺少下标 0 时第一张就无法发送")
	require.Equal(t, CreativeVideoReferenceLimit{}, only(VideoProfileRule{From: "images[role=last_frame]", To: "last"}), "画布不会发送尾帧角色")
	require.Equal(t, CreativeVideoReferenceLimit{MaxVideos: 1}, only(VideoProfileRule{From: "videos[0]", To: "video"}))
	// 能力上限只会收紧推导结果，不会放大单项映射。
	capped := &VideoProtocolProfileSpec{Create: VideoProfileCreate{Body: []VideoProfileRule{{From: "images[0]", To: "image"}}}, Capabilities: &VideoProfileCapabilities{MaxImages: 4}}
	require.Equal(t, CreativeVideoReferenceLimit{MaxImages: 1}, creativeVideoProfileReferenceLimit(capped))
}

// 保存只校验取值范围：模板协议可配到规范上限，内置协议沿用代码声明的上限且不能配置参考视频。
func TestCreativeVideoReferenceSettingsRange(t *testing.T) {
	caps := func(images, videos int) *CreativeVideoCapabilities {
		return &CreativeVideoCapabilities{Durations: []float64{5}, Resolutions: []string{"720p"}, AspectRatios: []string{"16:9"}, MaxReferenceImages: images, MaxReferenceVideos: videos}
	}
	out, err := normalizeCreativeVideoCapabilities(CreativeVideoEndpointUnified, caps(9, 3))
	require.NoError(t, err)
	require.Equal(t, 9, out.MaxReferenceImages)
	require.Equal(t, 3, out.MaxReferenceVideos)
	_, err = normalizeCreativeVideoCapabilities(CreativeVideoEndpointUnified, caps(12, 5))
	require.Error(t, err, "素材合计不能超过规范请求上限")
	_, err = normalizeCreativeVideoCapabilities(VideoEndpointSeedance, caps(10, 0))
	require.NoError(t, err)
	_, err = normalizeCreativeVideoCapabilities(VideoEndpointSeedance, caps(1, 1))
	require.Error(t, err, "内置协议不经画布发送参考视频")
	_, err = normalizeCreativeVideoCapabilities(VideoEndpointOpenAIVideos, caps(2, 0))
	require.Error(t, err)
	_, err = normalizeCreativeVideoCapabilities(CreativeVideoEndpointUnified, caps(-1, 0))
	require.Error(t, err)
}

// 公开目录的生效数量是管理员配置与模板上限的较小值；候选同时返回协议上限供后台表单校验。
func TestCreativeVideoCatalogUsesTemplateReferenceLimits(t *testing.T) {
	s, e, _, _, _ := creativeVideoTestFixture(t)
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	account.GroupIDs = []int64{e.Params.GroupID}
	account.Credentials = map[string]any{"api_key": "k", "base_url": "https://relay.example",
		"model_mapping":         map[string]any{e.Params.Model: "upstream-model"},
		"video_model_protocols": map[string]any{"upstream-model": map[string]any{"protocol": "profile:kemei-minimax"}}}
	s.public.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{e.Params.GroupID: {*account}}}
	setting := &s.public.Settings.(*creativeFakeSettingReader).models[0]
	setting.VideoEndpoint = CreativeVideoEndpointUnified
	setting.VideoCapabilities = &CreativeVideoCapabilities{Durations: []float64{5}, Resolutions: []string{"720p"}, AspectRatios: []string{"16:9"}, MaxReferenceImages: 12, MaxReferenceVideos: 4}
	models, err := s.public.ListModels(context.Background(), e.UserID)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
	require.Equal(t, 9, models.Data[0].VideoCapabilities.MaxReferenceImages)
	require.Equal(t, 3, models.Data[0].VideoCapabilities.MaxReferenceVideos)
	require.Equal(t, 9, models.Data[0].MaxReferenceImages)
	group := s.public.GroupRepo.(*creativeFakeGroupRepo).byID[e.Params.GroupID]
	candidates, err := s.public.creativeVideoCandidatesForGroup(context.Background(), group)
	require.NoError(t, err)
	var candidate *CreativeModelCandidate
	for i := range candidates {
		if candidates[i].Model == e.Params.Model {
			candidate = &candidates[i]
		}
	}
	require.NotNil(t, candidate)
	require.Equal(t, CreativeVideoReferenceLimit{MaxImages: 9, MaxVideos: 3}, candidate.VideoReferenceLimits[CreativeVideoEndpointUnified])
	// 管理员配置更小时以配置为准。
	setting.VideoCapabilities.MaxReferenceImages, setting.VideoCapabilities.MaxReferenceVideos = 2, 0
	models, err = s.public.ListModels(context.Background(), e.UserID)
	require.NoError(t, err)
	require.Equal(t, 2, models.Data[0].VideoCapabilities.MaxReferenceImages)
	require.Zero(t, models.Data[0].VideoCapabilities.MaxReferenceVideos)
}

// 参考视频只以内部占位进入正文，不读取内容；超过生效上限或素材类型不符时拒绝。
func TestCreativeVideoCanvasRequestReferenceVideos(t *testing.T) {
	videoID, imageID := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	e := &CreativeCanvasExecution{ID: "exec-1", UserID: 7, Params: CreativeCanvasExecutionParams{Model: "m", Operation: CreativeOperationGenerate, Prompt: "p", Duration: 5, Resolution: "720p", AspectRatio: "16:9",
		References: []CreativeCanvasReference{{AssetID: imageID, Role: VideoMediaRoleReferenceImage}, {AssetID: videoID, Role: VideoMediaRoleReferenceVideo}}}}
	model := &CreativeModelPublic{VideoCapabilities: &CreativeVideoCapabilities{Durations: []float64{5}, Resolutions: []string{"720p"}, AspectRatios: []string{"16:9"}, MaxReferenceImages: 1, MaxReferenceVideos: 1}}
	image := CreativeCanvasInputAsset{Asset: &CreativeCanvasAsset{ID: imageID, UserID: 7, MediaType: "image", MIMEType: "image/png"}, Role: VideoMediaRoleReferenceImage, Content: makeTestPNG(t, 16, 16)}
	video := CreativeCanvasInputAsset{Asset: &CreativeCanvasAsset{ID: videoID, UserID: 7, MediaType: "video", MIMEType: "video/mp4", ByteSize: 1024}, Role: VideoMediaRoleReferenceVideo}
	body, canonical, _, err := creativeVideoCanvasRequest(e, model, []CreativeCanvasInputAsset{image, video}, 1000)
	require.NoError(t, err)
	// 占位绑定执行与素材、不暴露素材 ID，同一执行重试时保持不变。
	placeholder := gjson.GetBytes(body, "videos.0.url").String()
	require.Equal(t, creativeVideoAssetPlaceholder("exec-1", videoID), placeholder)
	require.NotContains(t, placeholder, videoID)
	require.NotEqual(t, placeholder, creativeVideoAssetPlaceholder("exec-2", videoID))
	require.Equal(t, VideoMediaRoleReferenceVideo, gjson.GetBytes(body, "videos.0.role").String())
	require.Equal(t, VideoMediaRoleReferenceImage, canonical.Images[0].Role)
	require.Len(t, canonical.Videos, 1)
	// 生效上限为 0 时拒绝参考视频。
	model.VideoCapabilities.MaxReferenceVideos = 0
	_, _, _, err = creativeVideoCanvasRequest(e, model, []CreativeCanvasInputAsset{image, video}, 1000)
	require.ErrorIs(t, err, ErrCanvasInvalid)
	// 图片冒充参考视频、视频冒充参考图都拒绝。
	model.VideoCapabilities.MaxReferenceVideos = 1
	wrong := video
	wrong.Asset = &CreativeCanvasAsset{ID: videoID, UserID: 7, MediaType: "image", MIMEType: "image/png", ByteSize: 1024}
	_, _, _, err = creativeVideoCanvasRequest(e, model, []CreativeCanvasInputAsset{image, wrong}, 1000)
	require.ErrorIs(t, err, ErrCanvasInvalid)
	e.Params.References[1].Role = VideoMediaRoleReferenceImage
	video.Role = VideoMediaRoleReferenceImage
	model.VideoCapabilities.MaxReferenceImages = 2
	_, _, _, err = creativeVideoCanvasRequest(e, model, []CreativeCanvasInputAsset{image, video}, 1000)
	require.ErrorIs(t, err, ErrCanvasInvalid)
}

// 参考视频占位对任何目标都换成短期链接：先用样例链接校验协议，再签发一次并在重选候选时复用；只替换声明的占位，提示词原样保留。
func TestCreativeVideoReferencePreparerPublishesVideos(t *testing.T) {
	videoID := "33333333-3333-4333-8333-333333333333"
	placeholder := creativeVideoAssetPlaceholder("exec-1", videoID)
	assets := []VideoReferenceAsset{{AssetID: videoID, MIMEType: "video/mp4", Size: 2048, Placeholder: placeholder}}
	target := videoPresetTarget(t, "kemei-minimax", "m")
	// 提示词里出现 creative-asset:// 文字（即使形如素材 ID 的旧占位）必须原样保留，只替换执行器声明的占位。
	prompt := "参考 creative-asset://" + videoID + " 的运镜"
	body := []byte(`{"model":"m","prompt":"` + prompt + `","duration":6,"resolution":"768p","aspect_ratio":"16:9","videos":[{"url":"` + placeholder + `","role":"reference_video"}]}`)
	pub := &internalVideoReferenceStub{}
	prepare := newCreativeVideoReferencePreparer(pub, 5, assets...)
	for i := 0; i < 2; i++ {
		out, err := prepare(context.Background(), target, body)
		require.NoError(t, err)
		require.NotContains(t, string(out), placeholder)
		require.Equal(t, prompt, gjson.GetBytes(out, "prompt").String())
		require.Contains(t, string(out), "https://canvas.example.com/api/v1/video-references/video-"+videoID)
	}
	require.Equal(t, 1, pub.assetCalls, "同一次提交只签发一次")
	require.Equal(t, int64(5), pub.userID)
	require.Equal(t, assets, pub.assets)
	require.Zero(t, pub.calls, "不要求公网素材的模板不发布参考图")
	// 协议校验失败时不消耗签发额度。
	invalid := &internalVideoReferenceStub{}
	_, err := newCreativeVideoReferencePreparer(invalid, 5, assets...)(context.Background(), target, bytes.Replace(body, []byte(`"duration":6`), []byte(`"duration":60`), 1))
	require.Error(t, err)
	require.Zero(t, invalid.assetCalls)
	// 没有声明参考视频时正文原样通过，提示词里的 creative-asset:// 文字不触发拒绝，也不会签发任何链接。
	plain := &internalVideoReferenceStub{}
	textOnly := []byte(`{"model":"m","prompt":"` + prompt + `","duration":6,"resolution":"768p","aspect_ratio":"16:9"}`)
	out, err := newCreativeVideoReferencePreparer(plain, 5)(context.Background(), target, textOnly)
	require.NoError(t, err)
	require.Equal(t, prompt, gjson.GetBytes(out, "prompt").String())
	require.Zero(t, plain.assetCalls)
	// 执行器声明的视频没有出现在正文中、或占位格式不对，都在发送前拒绝。
	_, err = newCreativeVideoReferencePreparer(&internalVideoReferenceStub{}, 5, VideoReferenceAsset{AssetID: "44444444-4444-4444-8444-444444444444", MIMEType: "video/mp4", Size: 1, Placeholder: creativeVideoAssetPlaceholder("exec-1", "44444444-4444-4444-8444-444444444444")})(context.Background(), target, body)
	require.ErrorIs(t, err, ErrVideoReferenceInvalid)
	_, err = newCreativeVideoReferencePreparer(&internalVideoReferenceStub{}, 5, VideoReferenceAsset{AssetID: videoID, MIMEType: "video/mp4", Size: 2048})(context.Background(), target, body)
	require.ErrorIs(t, err, ErrVideoReferenceInvalid)
	// 签发失败直接返回，不留下部分替换的正文。
	failing := &internalVideoReferenceStub{assetErr: ErrVideoReferenceQuota}
	_, err = newCreativeVideoReferencePreparer(failing, 5, assets...)(context.Background(), target, body)
	require.ErrorIs(t, err, ErrVideoReferenceQuota)
}

type videoReferenceAssetFakeStore struct {
	batch  VideoReferenceAssetBatch
	grants map[string]VideoReferenceAssetGrant
}

func (s *videoReferenceAssetFakeStore) Ready(context.Context) error { return nil }
func (s *videoReferenceAssetFakeStore) IssueBatch(context.Context, VideoReferenceBatch) error {
	return nil
}
func (s *videoReferenceAssetFakeStore) Open(context.Context, string, string) (*VideoReferenceContent, error) {
	return nil, ErrVideoReferenceNotFound
}
func (s *videoReferenceAssetFakeStore) IssueAssetBatch(_ context.Context, batch VideoReferenceAssetBatch) error {
	s.batch = batch
	for _, grant := range batch.Assets {
		s.grants[grant.Digest] = grant
	}
	return nil
}
func (s *videoReferenceAssetFakeStore) OpenAsset(_ context.Context, digest, _ string) (*VideoReferenceAssetGrant, error) {
	grant, ok := s.grants[digest]
	if !ok {
		return nil, ErrVideoReferenceNotFound
	}
	return &grant, nil
}

type videoReferenceAssetFakeSource struct{ asset *CreativeCanvasAsset }

type videoReferenceNopSeeker struct{ *bytes.Reader }

func (videoReferenceNopSeeker) Close() error { return nil }

func (s *videoReferenceAssetFakeSource) OpenVideoReferenceAsset(_ context.Context, userID int64, assetID string) (io.ReadSeekCloser, *CreativeCanvasAsset, error) {
	if s.asset == nil || s.asset.UserID != userID || s.asset.ID != assetID {
		return nil, nil, ErrCanvasNotFound
	}
	return videoReferenceNopSeeker{bytes.NewReader([]byte("video-bytes"))}, s.asset, nil
}

// 视频票据只保存摘要到素材的授权，读取时按签发用户重新校验素材；内容变化、类型不符或未注入素材源都按不存在处理。
func TestVideoReferenceServiceAssetTickets(t *testing.T) {
	assetID := "55555555-5555-4555-8555-555555555555"
	store := &videoReferenceAssetFakeStore{grants: map[string]VideoReferenceAssetGrant{}}
	source := &videoReferenceAssetFakeSource{asset: &CreativeCanvasAsset{ID: assetID, UserID: 8, MediaType: "video", MIMEType: "video/mp4", ByteSize: 11}}
	svc := &VideoReferenceService{store: store, baseURL: func(context.Context) string { return "https://canvas.example.com/app" }}
	_, err := svc.PrepareAssetURLs(context.Background(), 8, []VideoReferenceAsset{{AssetID: assetID, MIMEType: "video/mp4", Size: 11}})
	require.ErrorIs(t, err, ErrVideoReferenceUnavailable, "未注入素材源时不签发")
	svc.SetAssetSource(source)
	for _, bad := range [][]VideoReferenceAsset{
		{{AssetID: "not-a-uuid", MIMEType: "video/mp4", Size: 11}},
		{{AssetID: assetID, MIMEType: "image/png", Size: 11}},
		{{AssetID: assetID, MIMEType: "video/mp4", Size: VideoReferenceAssetMaxBytes + 1}},
		{{AssetID: assetID, MIMEType: "video/mp4", Size: 11}, {AssetID: assetID, MIMEType: "video/mp4", Size: 11}},
	} {
		_, err = svc.PrepareAssetURLs(context.Background(), 8, bad)
		require.ErrorIs(t, err, ErrVideoReferenceInvalid)
	}
	urls, err := svc.PrepareAssetURLs(context.Background(), 8, []VideoReferenceAsset{{AssetID: assetID, MIMEType: "video/mp4", Size: 11}})
	require.NoError(t, err)
	require.Len(t, urls, 1)
	require.Regexp(t, `^https://canvas\.example\.com/api/v1/video-references/[0-9a-f]{64}$`, urls[0])
	require.Equal(t, videoReferenceAssetMaxReads, store.batch.MaxReads)
	require.Equal(t, VideoReferenceTTL, store.batch.ExpiresAt.Sub(store.batch.IssuedAt))
	require.WithinDuration(t, time.Now(), store.batch.IssuedAt, time.Minute)
	token := urls[0][len(urls[0])-64:]
	content, err := svc.OpenAsset(context.Background(), token, http.MethodGet)
	require.NoError(t, err)
	body, _ := io.ReadAll(content.Reader)
	require.Equal(t, "video-bytes", string(body))
	require.Equal(t, "video/mp4", content.MIMEType)
	// 素材被替换或删除后，旧票据不能读到新内容。
	source.asset.ByteSize = 99
	_, err = svc.OpenAsset(context.Background(), token, http.MethodGet)
	require.ErrorIs(t, err, ErrVideoReferenceNotFound)
	source.asset = nil
	_, err = svc.OpenAsset(context.Background(), token, http.MethodHead)
	require.ErrorIs(t, err, ErrVideoReferenceNotFound)
	_, err = svc.OpenAsset(context.Background(), "ABC", http.MethodGet)
	require.ErrorIs(t, err, ErrVideoReferenceNotFound)
}
