//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 测试只使用本地任务账本及协议替身，不向真实上游发送请求。
type creativeVideoKeyStub struct{ key *APIKey }

func (s *creativeVideoKeyStub) GetByID(context.Context, int64) (*APIKey, error) { return s.key, nil }

type creativeVideoAdmissionStub struct {
	slots, releases, checks, subscriptions int
	busy                                   bool
	err                                    error
	sub                                    *UserSubscription
	seenSub                                *UserSubscription
}

func (s *creativeVideoAdmissionStub) AcquireUserSlot(context.Context, int64, int) (*AcquireResult, error) {
	s.slots++
	return &AcquireResult{Acquired: !s.busy, ReleaseFunc: func() { s.releases++ }}, nil
}
func (s *creativeVideoAdmissionStub) CheckBillingEligibility(_ context.Context, _ *User, _ *APIKey, _ *Group, sub *UserSubscription, platform string) error {
	s.checks++
	s.seenSub = sub
	if platform != PlatformVideo {
		return errors.New("wrong platform")
	}
	return s.err
}
func (s *creativeVideoAdmissionStub) GetUsableSubscription(context.Context, int64, ...int64) (*UserSubscription, bool, error) {
	s.subscriptions++
	return s.sub, false, nil
}

type creativeVideoModerationStub struct {
	blocked bool
	calls   int
	input   ContentModerationCheckInput
}

func (s *creativeVideoModerationStub) Check(_ context.Context, input ContentModerationCheckInput) (*ContentModerationDecision, error) {
	s.calls++
	s.input = input
	return &ContentModerationDecision{Blocked: s.blocked, Allowed: !s.blocked}, nil
}

type creativeVideoContentStub struct {
	calls    int
	response *http.Response
}

func (s *creativeVideoContentStub) OpenContent(context.Context, *APIKey, string, string, string) (*http.Response, error) {
	s.calls++
	return s.response, nil
}

func creativeVideoTestExecution() *CreativeCanvasExecution {
	return &CreativeCanvasExecution{ID: "exec-one", UserID: 1, CanvasID: "canvas-one", NodeID: "node-one", Kind: "video", Params: CreativeCanvasExecutionParams{GroupID: 3, Model: "video-model", Operation: CreativeOperationGenerate, Endpoint: "compat", Prompt: "小球缓缓移动", Duration: 5, Resolution: "720p", AspectRatio: "16:9"}}
}

func creativeVideoTestFixture(t *testing.T) (*CreativeVideoCanvasExecutor, *CreativeCanvasExecution, *videoLifecycleUpstream, *videoLifecycleBilling, *creativeVideoAdmissionStub) {
	t.Helper()
	tasks, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideo)
	key.User.Status = StatusActive
	key.Status = StatusAPIKeyActive
	key.Group.Status = StatusActive
	managed := CreativeManagedBy
	key.ManagedBy = &managed
	pub := newCreativeTestService()
	pub.UserRepo = &creativeFakeUserRepo{user: key.User}
	pub.GroupRepo = &creativeFakeGroupRepo{byID: map[int64]*Group{3: key.Group}, active: []Group{*key.Group}}
	pub.ApiKeyRepo = &creativeFakeManagedKeyRepo{key: key}
	account := videoFixtureAccount(VideoEndpointCompat)
	account.GroupIDs = []int64{3}
	account.Credentials["model_mapping"] = map[string]any{"video-model": "video-model"}
	pub.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{3: {*account}}}
	pub.Settings = &creativeFakeSettingReader{enabled: true, models: []CreativeModelSetting{{GroupID: 3, Model: "video-model", MediaType: "video", VideoEndpoint: VideoEndpointCompat, Operations: []string{CreativeOperationGenerate}}}}
	key.Group.Platform = PlatformVideo
	admission := &creativeVideoAdmissionStub{sub: &UserSubscription{ID: 7}}
	executor := &CreativeVideoCanvasExecutor{public: pub, tasks: tasks, repo: tasks.repo, content: tasks, keys: &creativeVideoKeyStub{key: key}, concurrency: admission, billing: admission, subscriptions: admission}
	return executor, creativeVideoTestExecution(), upstream, billing, admission
}

func TestCreativeVideoSettingsPreserveImageAndValidate(t *testing.T) {
	image := CreativeModelSetting{GroupID: 1, Model: "image-model", Operations: []string{"generate"}}
	video := CreativeModelSetting{GroupID: 2, Model: "video-model", MediaType: "video", VideoEndpoint: VideoEndpointMiniMax, Operations: []string{"generate"}}
	items, err := NormalizeCreativeModelSettings([]CreativeModelSetting{image, video})
	require.NoError(t, err)
	require.Equal(t, image, items[0])
	require.Equal(t, []float64{6}, items[1].VideoCapabilities.Durations)
	require.Equal(t, []string{"768p"}, items[1].VideoCapabilities.Resolutions)
	raw, _, err := marshalCreativeModelSettings(items)
	require.NoError(t, err)
	require.Equal(t, items, parseCreativeModelSettings(raw))
	require.Len(t, creativeModelSettingsIndex(items), 1)
	for _, change := range []struct {
		name   string
		mutate func(*CreativeModelSetting)
	}{
		{"unknown_type", func(v *CreativeModelSetting) { v.MediaType = "audio" }},
		{"missing_endpoint", func(v *CreativeModelSetting) { v.VideoEndpoint = "" }},
		{"unknown_endpoint", func(v *CreativeModelSetting) { v.VideoEndpoint = "https://evil.example" }},
		{"edit", func(v *CreativeModelSetting) { v.Operations = []string{"edit"} }},
		{"duration_nan", func(v *CreativeModelSetting) {
			v.VideoCapabilities = &CreativeVideoCapabilities{Durations: []float64{math.NaN()}, Resolutions: []string{"720p"}, AspectRatios: []string{"16:9"}}
		}},
		{"duplicate_resolution", func(v *CreativeModelSetting) {
			v.VideoCapabilities = &CreativeVideoCapabilities{Durations: []float64{5}, Resolutions: []string{"720P", "720p"}, AspectRatios: []string{"16:9"}}
		}},
		{"unbounded_references", func(v *CreativeModelSetting) {
			v.VideoCapabilities = &CreativeVideoCapabilities{Durations: []float64{5}, Resolutions: []string{"720p"}, AspectRatios: []string{"16:9"}, MaxReferenceImages: 100}
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			v := video
			change.mutate(&v)
			_, err := NormalizeCreativeModelSettings([]CreativeModelSetting{v})
			require.Error(t, err)
		})
	}
}

func TestCreativeVideoCatalogRequiresBothWhitelistAndAccountEndpoint(t *testing.T) {
	s, e, _, _, _ := creativeVideoTestFixture(t)
	models, err := s.public.ListModels(context.Background(), e.UserID)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
	require.Equal(t, "video", models.Data[0].MediaType)
	require.Equal(t, VideoEndpointCompat, models.Data[0].VideoEndpoint)
	candidates, err := s.public.ListCreativeModelCandidates(context.Background())
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, []VideoEndpoint{VideoEndpointCompat}, candidates[0].VideoEndpoints)
	s.public.Settings.(*creativeFakeSettingReader).models[0].VideoEndpoint = VideoEndpointMiniMax
	models, err = s.public.ListModels(context.Background(), e.UserID)
	require.NoError(t, err)
	require.Empty(t, models.Data)
	s.public.Settings.(*creativeFakeSettingReader).models[0].VideoEndpoint = VideoEndpointCompat
	s.public.GroupRepo.(*creativeFakeGroupRepo).active[0].IsExclusive = true
	models, err = s.public.ListModels(context.Background(), e.UserID)
	require.NoError(t, err)
	require.Empty(t, models.Data)
}

// 仅配置 model_whitelist 的视频账号也必须进入创作台目录，不能要求管理员重复填写 mapping。
func TestCreativeVideoCatalogIncludesWhitelistOnlyAccount(t *testing.T) {
	s, e, _, _, _ := creativeVideoTestFixture(t)
	accounts := s.public.AccountRepo.(*creativeFakeAccountRepo).byGroup[3]
	require.Len(t, accounts, 1)
	delete(accounts[0].Credentials, "model_mapping")
	accounts[0].Credentials["model_whitelist"] = []string{"video-model"}

	models, err := s.public.ListModels(context.Background(), e.UserID)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
	require.Equal(t, "video-model", models.Data[0].Model)
	require.Equal(t, VideoEndpointCompat, models.Data[0].VideoEndpoint)

	candidates, err := s.public.ListCreativeModelCandidates(context.Background())
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, "video-model", candidates[0].Model)
	require.Equal(t, []VideoEndpoint{VideoEndpointCompat}, candidates[0].VideoEndpoints)
}

func TestCreativeVideoAdmissionFailureNeverSubmitsOrReserves(t *testing.T) {
	for _, name := range []string{"wrong_user", "inactive_user", "wrong_group", "exclusive_group", "disabled_feature", "wrong_model", "wrong_endpoint", "wrong_duration", "wrong_resolution", "wrong_ratio", "image_parameter", "hidden_key_owner", "hidden_key_group", "team_key", "composite_key", "moderation", "busy", "billing"} {
		t.Run(name, func(t *testing.T) {
			s, e, upstream, billing, admission := creativeVideoTestFixture(t)
			key := s.public.ApiKeyRepo.(*creativeFakeManagedKeyRepo).key
			switch name {
			case "wrong_user":
				e.UserID++
			case "inactive_user":
				key.User.Status = "disabled"
			case "wrong_group":
				key.Group.Platform = PlatformOpenAI
			case "exclusive_group":
				key.Group.IsExclusive = true
			case "disabled_feature":
				s.public.Settings.(*creativeFakeSettingReader).enabled = false
			case "wrong_model":
				e.Params.Model = "unknown"
			case "wrong_endpoint":
				e.Params.Endpoint = "minimax"
			case "wrong_duration":
				e.Params.Duration = 61
			case "wrong_resolution":
				e.Params.Resolution = "4k"
			case "wrong_ratio":
				e.Params.AspectRatio = "0:1"
			case "image_parameter":
				e.Params.ImageSize = "4K"
			case "hidden_key_owner":
				key.UserID++
			case "hidden_key_group":
				other := int64(100)
				key.GroupID = &other
			case "team_key":
				id := int64(3)
				key.TeamID = &id
			case "composite_key":
				key.IsComposite = true
			case "moderation":
				s.moderation = &creativeVideoModerationStub{blocked: true}
			case "busy":
				admission.busy = true
			case "billing":
				admission.err = errors.New("insufficient credit")
			}
			_, err := s.Submit(context.Background(), e, nil)
			require.Error(t, err)
			require.Zero(t, upstream.submitted)
			require.Zero(t, billing.reserves)
			if name == "billing" {
				require.Equal(t, 1, admission.releases)
			}
		})
	}
}

func TestCreativeVideoSubmissionRecoveryReservesAndChargesOnlyOnce(t *testing.T) {
	s, e, upstream, billing, admission := creativeVideoTestFixture(t)
	moderation := &creativeVideoModerationStub{}
	s.moderation = moderation
	id, err := s.Submit(context.Background(), e, nil)
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.Equal(t, 1, upstream.submitted)
	require.Equal(t, 1, billing.reserves)
	require.Equal(t, 1, admission.releases)
	require.Same(t, admission.sub, admission.seenSub)
	require.True(t, moderation.input.NoMediaRetention)
	// 模拟创建成功而画布绑定还未保存时重启；即使管理员已停用目录，恢复也不再生成。
	s.public.Settings.(*creativeFakeSettingReader).enabled = false
	recovered, err := s.Submit(context.Background(), e, nil)
	require.NoError(t, err)
	require.Equal(t, id, recovered)
	require.Equal(t, 1, upstream.submitted)
	require.Equal(t, 1, billing.reserves)
	task, err := s.repo.Get(context.Background(), id)
	require.NoError(t, err)
	upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{DurationSeconds: 8, Resolution: "720p"}, VideoURL: "https://cdn.example/video.mp4", Body: []byte(`{"status":"completed"}`)}
	s.tasks.(*VideoTaskService).advance(context.Background(), task)
	task, err = s.repo.Get(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "settled", task.BillingStatus)
	s.tasks.(*VideoTaskService).advance(context.Background(), task)
	require.Equal(t, 1, billing.captures)
	require.Equal(t, 16.0, task.BillingResult.ActualAmountUSD)
	e.TaskID = id
	s.content = &creativeVideoContentStub{response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"video/mp4"}}, ContentLength: 3, Body: io.NopCloser(strings.NewReader("mp4"))}}
	result, err := s.Poll(context.Background(), e)
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.Status)
	require.Len(t, result.Outputs, 1)
	_ = result.Outputs[0].Reader.Close()
	require.Equal(t, 1, billing.captures)
	require.Equal(t, 1, upstream.submitted)
}

func TestCreativeVideoPollCannotExposeUnpaidOrForeignTask(t *testing.T) {
	for _, name := range []string{"wrong_user", "wrong_execution", "wrong_key", "reserved", "reconciliation", "released_failure"} {
		t.Run(name, func(t *testing.T) {
			s, e, _, _, _ := creativeVideoTestFixture(t)
			e.TaskID = "vid_saved"
			task := &VideoTaskRecord{ID: e.TaskID, APIKeyID: 2, UserID: e.UserID, GroupID: e.Params.GroupID, IdempotencyKey: "creative_video:" + e.ID, Status: "completed", BillingStatus: "settled", EffectsDone: true, NextPollAt: time.Now().Add(time.Hour)}
			task.Target = VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointCompat, Model: e.Params.Model, CreatePath: "/v1/video/generations", AccountID: 4}
			content := &creativeVideoContentStub{}
			s.content = content
			switch name {
			case "wrong_user":
				task.UserID++
			case "wrong_execution":
				task.IdempotencyKey = "creative_video:other"
			case "wrong_key":
				s.keys.(*creativeVideoKeyStub).key.ID++
			case "reserved", "reconciliation":
				task.BillingStatus = name
			case "released_failure":
				task.Status = "failed"
				task.BillingStatus = "released"
			}
			s.repo.(*videoLifecycleRepo).tasks[task.ID] = task
			result, err := s.Poll(context.Background(), e)
			if strings.HasPrefix(name, "wrong") {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotEqual(t, "completed", result.Status)
				if name == "released_failure" {
					require.Equal(t, "failed", result.Status)
					require.Equal(t, "CREATIVE_VIDEO_FAILED", result.ErrorCode)
					require.Contains(t, result.ErrorMessage, "预扣已释放")
				}
			}
			require.Zero(t, content.calls)
		})
	}
}

func TestCreativeVideoReferencesAndProtocolMetadata(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointMiniMax, VideoEndpointWan, VideoEndpointKling, VideoEndpointVidu} {
		for _, withImage := range []bool{false, true} {
			t.Run(string(endpoint)+map[bool]string{false: "/text", true: "/image"}[withImage], func(t *testing.T) {
				e := creativeVideoTestExecution()
				e.Params.Endpoint = string(endpoint)
				caps, err := normalizeCreativeVideoCapabilities(endpoint, nil)
				require.NoError(t, err)
				e.Params.Duration = caps.Durations[0]
				e.Params.Resolution = caps.Resolutions[0]
				model := &CreativeModelPublic{Model: e.Params.Model, VideoEndpoint: endpoint, VideoCapabilities: caps}
				var assets []CreativeCanvasInputAsset
				if withImage {
					e.Params.References = []CreativeCanvasReference{{AssetID: "asset-one"}}
					assets = []CreativeCanvasInputAsset{{Asset: &CreativeCanvasAsset{ID: "asset-one", UserID: e.UserID, CanvasID: e.CanvasID, MediaType: "image", MIMEType: "image/png"}, Content: makeTestPNG(t, 16, 16)}}
				}
				_, canonical, _, err := creativeVideoCanvasRequest(e, model, assets, 8000)
				require.NoError(t, err)
				target := VideoUpstreamTarget{Version: 2, Endpoint: endpoint, Model: e.Params.Model, BaseURL: "https://unused.example"}
				switch endpoint {
				case VideoEndpointCompat:
					target.Version = 1
					target.CreatePath = "/v1/video/generations"
				case VideoEndpointOpenAIVideos:
					target.ProtocolAdapter = VideoProtocolAdapterOpenAI
					target.CreatePath = "/v1/videos"
				case VideoEndpointSeedance:
					target.ProtocolAdapter = VideoProtocolAdapterSeedance
					target.CreatePath = "/api/v3/contents/generations/tasks"
				case VideoEndpointMiniMax:
					target.ProtocolAdapter = VideoProtocolAdapterMiniMax
					target.CreatePath = "/v2/video_generation"
				case VideoEndpointWan:
					target.ProtocolAdapter = VideoProtocolAdapterWan
					target.CreatePath = "/api/v1/services/aigc/video-generation/video-synthesis"
				case VideoEndpointKling:
					target.ProtocolAdapter = VideoProtocolAdapterKling
					target.CreatePath = "/text-to-video/" + e.Params.Model
					if withImage {
						target.CreatePath = "/image-to-video/" + e.Params.Model
					}
				case VideoEndpointVidu:
					target.ProtocolAdapter = VideoProtocolAdapterVidu
					target.CreatePath = "/ent/v2/text2video"
					if withImage {
						target.CreatePath = "/ent/v2/img2video"
					}
				}
				// 画布只提交规范请求，按冻结协议转换后再走原协议校验与计价维度提取。
				body, err := videoCanonicalBodyForTarget(canonical, target)
				require.NoError(t, err)
				_, metadata, err := PrepareVideoProtocolRequest(body, "", target)
				require.NoError(t, err)
				require.Equal(t, e.Params.Duration, metadata.DurationSeconds)
				require.NotNil(t, metadata.ReferenceImageCount)
				require.Equal(t, len(assets), *metadata.ReferenceImageCount)
				if withImage {
					require.Contains(t, string(body), "data:image/png;base64,")
				}
			})
		}
	}
}

func TestCreativeVideoRejectsForeignOrCorruptReference(t *testing.T) {
	for _, name := range []string{"owner", "foreign_canvas_allowed", "id", "role", "mime", "corrupt", "dimensions", "missing", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			e := creativeVideoTestExecution()
			e.Params.References = []CreativeCanvasReference{{AssetID: "asset-one"}}
			caps, _ := normalizeCreativeVideoCapabilities(VideoEndpointCompat, nil)
			model := &CreativeModelPublic{VideoEndpoint: VideoEndpointCompat, VideoCapabilities: caps}
			asset := CreativeCanvasInputAsset{Asset: &CreativeCanvasAsset{ID: "asset-one", UserID: e.UserID, CanvasID: e.CanvasID, MediaType: "image", MIMEType: "image/png"}, Content: makeTestPNG(t, 16, 16)}
			switch name {
			case "owner":
				asset.Asset.UserID++
			case "foreign_canvas_allowed":
				asset.Asset.CanvasID = "other"
			// 资产可以由同一用户的其它画布复制而来，用户边界仍由 UserID 校验。
			case "id":
				asset.Asset.ID = "other"
			case "role":
				asset.Role = "last_frame"
			case "mime":
				asset.Asset.MIMEType = "image/jpeg"
			case "corrupt":
				asset.Content = []byte("not image")
			case "dimensions":
				asset.Content = makeTestPNG(t, 1, 1)
			case "missing":
				e.Params.References = nil
			case "duplicate":
				e.Params.References = append(e.Params.References, e.Params.References[0])
			}
			_, _, _, err := creativeVideoCanvasRequest(e, model, []CreativeCanvasInputAsset{asset}, 8000)
			if name == "foreign_canvas_allowed" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// Kling 的操作路径来自账号对该模型的协议绑定，统一入口不能再由客户端路径改写。
func TestCreativeVideoKlingConfiguredPathAndExplicitHTTPPath(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointKling)
	s := NewVideoUpstreamService(&OpenAIGatewayService{})
	for _, path := range []string{"/text-to-video/{model}", "/image-to-video/{model}"} {
		account.Credentials = map[string]any{"api_key": "fixture-secret", "base_url": "https://relay.example/prefix",
			"video_model_protocols": map[string]any{"model-a": map[string]any{"protocol": "kling.kling_v1", "path": path}}}
		target, err := s.resolveTarget(context.Background(), account, "model-a")
		require.NoError(t, err)
		require.Equal(t, strings.ReplaceAll(path, "{model}", "model-a"), target.CreatePath)
	}
	account.Credentials["video_model_protocols"] = map[string]any{"model-a": map[string]any{"protocol": "kling.kling_v1"}}
	_, err := s.resolveTarget(context.Background(), account, "model-a")
	require.Error(t, err)
}

func TestCreativeVideoSettingsJSONContract(t *testing.T) {
	setting := CreativeModelSetting{GroupID: 5, Model: "video", MediaType: "video", VideoEndpoint: VideoEndpointSeedance, Operations: []string{"generate"}, VideoCapabilities: &CreativeVideoCapabilities{Durations: []float64{5, 10}, Resolutions: []string{"720p", "1080p"}, AspectRatios: []string{"16:9"}, MaxReferenceImages: 2}}
	normalized, err := NormalizeCreativeModelSettings([]CreativeModelSetting{setting})
	require.NoError(t, err)
	body, err := json.Marshal(normalized)
	require.NoError(t, err)
	require.Equal(t, "video", gjson.GetBytes(body, "0.media_type").String())
	require.Equal(t, int64(2), gjson.GetBytes(body, "0.video_capabilities.max_reference_images").Int())
	require.Equal(t, normalized, parseCreativeModelSettings(string(body)))
}
