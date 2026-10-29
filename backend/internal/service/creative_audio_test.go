//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func creativeAudioTestParams() CreateCreativeRunParamsPublic {
	return CreateCreativeRunParamsPublic{GroupID: 12, Model: CreativeGrokTTSModel, Operation: CreativeOperationTTS, Prompt: " 你好🙂 ", canvasAudio: true}
}

func TestCreativeAudioValidationAndFingerprint(t *testing.T) {
	s := newCreativeAudioCatalogTestService()
	p := creativeAudioTestParams()
	v, err := s.validateCreateParams(context.Background(), 7, &p)
	require.NoError(t, err)
	require.Equal(t, "你好🙂", v.prompt)
	require.Equal(t, "eve", v.voice)
	require.Equal(t, "mp3", v.audioFormat)
	require.Equal(t, float64(1), v.speed)
	for _, mutate := range []func(*CreateCreativeRunParamsPublic){
		func(p *CreateCreativeRunParamsPublic) { p.Voice = "ara" },
		func(p *CreateCreativeRunParamsPublic) { p.AudioFormat = "wav" },
		func(p *CreateCreativeRunParamsPublic) { p.Speed = .7 },
		func(p *CreateCreativeRunParamsPublic) { p.Prompt += "世界" },
	} {
		p := creativeAudioTestParams()
		mutate(&p)
		next, err := s.validateCreateParams(context.Background(), 7, &p)
		require.NoError(t, err)
		require.NotEqual(t, v.fingerprint, next.fingerprint)
	}
	for _, mutate := range []func(*CreateCreativeRunParamsPublic){
		func(p *CreateCreativeRunParamsPublic) { p.canvasAudio = false },
		func(p *CreateCreativeRunParamsPublic) { p.Voice = "alloy" },
		func(p *CreateCreativeRunParamsPublic) { p.AudioFormat = "opus" },
		func(p *CreateCreativeRunParamsPublic) { p.Speed = .69 },
		func(p *CreateCreativeRunParamsPublic) { p.Speed = 1.51 },
		func(p *CreateCreativeRunParamsPublic) { p.Speed = math.NaN() },
		func(p *CreateCreativeRunParamsPublic) { p.Speed = math.Inf(1) },
		func(p *CreateCreativeRunParamsPublic) { p.Prompt = " " },
		func(p *CreateCreativeRunParamsPublic) { p.Prompt = strings.Repeat("好", 15001) },
		func(p *CreateCreativeRunParamsPublic) { p.SourceImages = []CreativeInputImage{{Bytes: []byte("x")}} },
		func(p *CreateCreativeRunParamsPublic) { p.ImageSize = "1K" },
		func(p *CreateCreativeRunParamsPublic) { p.OutputCount = 2 },
	} {
		p := creativeAudioTestParams()
		mutate(&p)
		_, err := s.validateCreateParams(context.Background(), 7, &p)
		require.Error(t, err)
	}
	p = creativeAudioTestParams()
	p.Prompt = strings.Repeat("好", 15000)
	p.Speed = 1.5
	_, err = s.validateCreateParams(context.Background(), 7, &p)
	require.NoError(t, err, "音频不得继承图片8000字限制")
}

func TestCreativeAudioPriceUnicodeDefaultZeroAndTier(t *testing.T) {
	s := newCreativeAudioCatalogTestService()
	g := s.GroupRepo.(*creativeFakeGroupRepo).byID[12]
	g.RateMultiplier = 2
	g.ImageRateIndependent, g.ImageRateMultiplier = true, 99
	p := creativeAudioTestParams()
	v, err := s.validateCreateParams(context.Background(), 7, &p)
	require.NoError(t, err)
	quote, err := s.resolveCreativePricing(context.Background(), 7, v)
	require.NoError(t, err)
	require.InDelta(t, 3*15.0/1e6, quote.BaseUnitPrice, 1e-12)
	require.InDelta(t, 3*15.0/1e6*2, quote.EstimatedCost, 1e-12)
	require.Equal(t, float64(2), quote.BalanceRateMultiplier)
	zero := 0.0
	g.AudioTTSPricePerMillionChars = &zero
	quote, err = s.resolveCreativePricing(context.Background(), 7, v)
	require.NoError(t, err)
	require.Zero(t, quote.BaseUnitPrice)
	price := 20.0
	g.ModelPricing = []ChannelModelPricing{{Models: []string{"tts"}, Platform: PlatformGrok, BillingMode: BillingModePerRequest, PerRequestPrice: &price}}
	s.PricingResolver = NewModelPricingResolver(nil, &BillingService{})
	quote, err = s.resolveCreativePricing(context.Background(), 7, v)
	require.NoError(t, err)
	require.InDelta(t, 3*20.0/1e6, quote.BaseUnitPrice, 1e-12)
	price = 0
	g.ModelPricing[0].PerRequestPrice = &price
	quote, err = s.resolveCreativePricing(context.Background(), 7, v)
	require.NoError(t, err)
	require.Zero(t, quote.EstimatedCost)
}

func TestCreativeAudioChannelPriceMatchesFormalVoiceContinuousUnits(t *testing.T) {
	for _, price := range []float64{0, 24} {
		s := newCreativeAudioCatalogTestService()
		s.PricingResolver = newResolverWithChannel(t, []ChannelModelPricing{{Platform: PlatformGrok, Models: []string{"tts"}, BillingMode: BillingModePerRequest, PerRequestPrice: &price}})
		group := &Group{ID: 100, Platform: PlatformGrok, RateMultiplier: 1, AudioTTSPricePerMillionChars: testPtrFloat64(99)}
		base, err := s.creativeAudioBasePrice(context.Background(), group, " 你好🙂 ")
		require.NoError(t, err)
		require.InDelta(t, price*3/1e6, base, 1e-12)
		formal := &OpenAIGatewayService{resolver: s.PricingResolver, billingService: &BillingService{}}
		result := &OpenAIForwardResult{AudioUsage: &AudioUsage{Mode: "tts", DurationOrUnits: 3.0 / 1e6}}
		cost, err := formal.calculateOpenAIRecordUsageCost(context.Background(), result, &APIKey{Group: group, GroupID: &group.ID}, []string{"tts"}, 1, 1, 1, 1, UsageTokens{}, "")
		require.NoError(t, err)
		require.InDelta(t, cost.TotalCost, base, 1e-12)
	}
}

func TestCreativeAudioCreateReplayPermissionAndModeration(t *testing.T) {
	s := newCreativeAudioCatalogTestService()
	s.GroupRepo.(*creativeFakeGroupRepo).byID[12].RateMultiplier = 1
	p := creativeAudioTestParams()
	ctx := context.Background()
	run, err := s.CreateRun(ctx, testCreativeScope(7), p, "audio-intent")
	require.NoError(t, err)
	storage := s.TransientStore.(*creativeFakeTransient)
	require.Equal(t, "eve", storage.payloads[run.ID].Voice)
	require.Equal(t, float64(1), storage.payloads[run.ID].Speed)
	replay, err := s.CreateRun(ctx, testCreativeScope(7), p, "audio-intent")
	require.NoError(t, err)
	require.Equal(t, run.ID, replay.ID)
	require.Equal(t, 1, s.BillingRepo.(*creativeFakeBillingRepo).reserveN)
	p.Voice = "ara"
	_, err = s.CreateRun(ctx, testCreativeScope(7), p, "audio-intent")
	require.ErrorIs(t, err, ErrCreativeRunIdempotencyConflict)
	_, err = s.Repo.GetCreativeRunByRunIDForOwner(ctx, testCreativeScope(8), run.ID)
	require.ErrorIs(t, err, ErrCreativeRunNotFound)
	_, err = s.Repo.GetCreativeRunByRunIDForOwner(ctx, CreativeRunScope{UserID: 7, WorkspaceID: "22222222-2222-4222-8222-222222222222"}, run.ID)
	require.ErrorIs(t, err, ErrCreativeRunNotFound)
	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.KeywordBlockingMode = ContentModerationKeywordModeKeywordOnly
	cfg.BlockedKeywords = []string{"你好"}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	moderationRepo := &contentModerationTestRepo{}
	s.Moderation = NewContentModerationService(&contentModerationTestSettingRepo{values: map[string]string{SettingKeyRiskControlEnabled: "true", SettingKeyContentModerationConfig: string(raw)}}, moderationRepo, &contentModerationTestHashCache{}, nil, nil, nil, nil)
	_, err = s.CreateRun(ctx, testCreativeScope(7), p, "blocked")
	require.ErrorIs(t, err, ErrCreativeContentBlocked)
	logs := requireContentModerationLogCount(t, moderationRepo, 1)
	require.Empty(t, logs[0].InputExcerpt)
	require.Empty(t, logs[0].Media)
	require.Equal(t, 1, s.BillingRepo.(*creativeFakeBillingRepo).reserveN)
	s.Moderation = nil
	s.Settings.(*creativeFakeSettingReader).models = nil
	_, err = s.CreateRun(ctx, testCreativeScope(7), p, "not-configured")
	require.ErrorIs(t, err, ErrCreativeInvalidModel)
}

func creativeAudioTestExecution(t *testing.T) (CreativeRun, CreativeRunPayload, *Account) {
	t.Helper()
	s := newCreativeAudioCatalogTestService()
	p := creativeAudioTestParams()
	p.AudioFormat = "wav"
	v, err := s.validateCreateParams(context.Background(), 7, &p)
	require.NoError(t, err)
	run := CreativeRun{GroupID: 12, Model: CreativeGrokTTSModel, Operation: CreativeOperationTTS, RequestedOutputCount: 1, PromptHash: v.promptHash, RequestFingerprint: v.fingerprint}
	payload := CreativeRunPayload{Prompt: v.prompt, Voice: v.voice, AudioFormat: v.audioFormat, Speed: v.speed, RequestFingerprint: v.fingerprint}
	account := &Account{ID: 55, Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-only", "base_url": "https://voice.example/v1"}}
	return run, payload, account
}

func TestCreativeAudioExecutorUsesOfficialBodyAndNoGatewayBilling(t *testing.T) {
	run, payload, account := creativeAudioTestExecution(t)
	up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(minimalGrokTestWAV()))}}
	exec := &CreativeExecutor{gateway: &OpenAIGatewayService{httpUpstream: up}}
	result, err := exec.Execute(context.Background(), run, payload, &CreativeExecution{Account: account, UpstreamModel: CreativeGrokTTSModel})
	require.NoError(t, err)
	require.Len(t, result.Outputs, 1)
	require.Equal(t, "audio/wav", result.Outputs[0].Mime)
	require.Equal(t, "https://voice.example/v1/tts", up.lastReq.URL.String())
	require.True(t, HTTPUpstreamRedirectsDisabled(up.lastReq.Context()))
	var body map[string]any
	require.NoError(t, json.Unmarshal(up.lastBody, &body))
	require.Equal(t, map[string]any{"text": "你好🙂", "voice_id": "eve", "language": "auto", "speed": float64(1), "output_format": map[string]any{"codec": "wav"}}, body)
	payload.Voice = "ara"
	_, err = exec.Execute(context.Background(), run, payload, &CreativeExecution{Account: account, UpstreamModel: CreativeGrokTTSModel})
	require.Error(t, err)
	require.Len(t, up.requests, 1, "参数变更不得复用原费用合同发第二次请求")
}

func TestCreativeAudioExecutorUnconfirmedAndKnownReject(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      []byte
		transport bool
		unknown   bool
	}{
		{"timeout", 0, nil, true, true}, {"server", 502, nil, false, true}, {"read_timeout", 408, nil, false, true},
		{"redirect", 307, nil, false, true}, {"empty", 200, nil, false, true}, {"json", 200, []byte(`{"error":"bad"}`), false, true},
		{"header_only", 200, []byte("RIFFxxxxWAVE"), false, true}, {"invalid", 400, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, payload, account := creativeAudioTestExecution(t)
			up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: tc.status, Body: io.NopCloser(bytes.NewReader(tc.body))}}
			if tc.transport {
				up.err = errors.New("simulated write timeout")
			}
			exec := &CreativeExecutor{gateway: &OpenAIGatewayService{httpUpstream: up}}
			_, err := exec.Execute(context.Background(), run, payload, &CreativeExecution{Account: account, UpstreamModel: CreativeGrokTTSModel})
			require.Error(t, err)
			require.Equal(t, tc.unknown, isCreativeResultUnconfirmedError(err))
			require.False(t, IsRetryableCreativeError(err))
		})
	}
}

// 语音结果无法确认时不重发，也不让用户为未拿到的成品付费：一次执行后直接失败并释放。
func TestCreativeAudioWorkerDoesNotResubmitUnconfirmed(t *testing.T) {
	f := newCreativeWorkerFixture()
	seedCreativeRun(f, "audio-unknown", true)
	f.repo.runs["audio-unknown"].Operation = CreativeOperationTTS
	f.exec.err = creativeResultUnconfirmedError()
	result, err := f.worker.process(context.Background(), "audio-unknown")
	require.NoError(t, err)
	require.True(t, result.Terminal)
	for range 2 {
		result, err = f.worker.process(context.Background(), "audio-unknown")
		require.NoError(t, err)
		require.True(t, result.Terminal)
	}
	require.Equal(t, 1, f.exec.calls)
	require.Zero(t, f.billing.captureN)
	require.Equal(t, 1, f.billing.releaseN)
	require.Equal(t, CreativeRunStatusFailed, f.repo.runs["audio-unknown"].Status)
	require.Equal(t, CreativeResultUnavailableCode, *f.repo.runs["audio-unknown"].ErrorCode)
}

type creativeAudioFailSink struct{ calls int }

func (s *creativeAudioFailSink) PersistCreativeOutput(context.Context, string, int, []byte, string) error {
	s.calls++
	return errors.New("storage unavailable")
}

// 非画布任务的归档钩子失败不阻断结算：Redis 副本可取回即按交付计费，且绝不重新请求供应商。
func TestCreativeAudioArchiveFailureRetainsBytesAndSettlesWithoutResubmit(t *testing.T) {
	f := newCreativeWorkerFixture()
	seedCreativeRun(f, "audio-archive", true)
	run := f.repo.runs["audio-archive"]
	run.Operation = CreativeOperationTTS
	run.BaseUnitPrice = .02
	sink := &creativeAudioFailSink{}
	f.service.CloudOutputSink = sink
	f.exec.result = &CreativeExecuteResult{AccountID: 55, Outputs: []CreativeOutput{{Index: 0, Mime: "audio/wav", Bytes: minimalGrokTestWAV()}}}
	result, err := f.worker.process(context.Background(), run.RunID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	data, err := f.store.LoadOutput(context.Background(), run.RunID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	result, err = f.worker.process(context.Background(), run.RunID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 1, f.exec.calls)
	require.Equal(t, 1, f.billing.captureN)
	require.Zero(t, f.billing.releaseN)
	// 画布可在原任务完成后继续从已有暂存字节归档，不能再请求供应商。
	workspace := testCreativeWorkspaceID
	run.WorkspaceID = &workspace
	x := &creativeCanvasImageExecutor{creative: f.service}
	poll, err := x.Poll(context.Background(), &CreativeCanvasExecution{UserID: 7, CanvasID: workspace, TaskID: run.RunID, Kind: "audio"})
	require.NoError(t, err)
	require.Equal(t, "succeeded", poll.Status)
	require.Len(t, poll.Outputs, 1)
	poll.Outputs[0].Reader.Close()
}

func TestCreativeAudioPollUnconfirmedAndOwnerIsolation(t *testing.T) {
	f := newCreativeWorkerFixture()
	seedCreativeRun(f, "audio-poll", true)
	run := f.repo.runs["audio-poll"]
	run.Operation = CreativeOperationTTS
	workspace := testCreativeWorkspaceID
	run.WorkspaceID = &workspace
	x := &creativeCanvasImageExecutor{creative: f.service}
	e := &CreativeCanvasExecution{UserID: 7, CanvasID: workspace, TaskID: run.RunID, Kind: "audio"}
	for _, status := range []string{CreativeRunStatusSettlementPending, CreativeRunStatusReleasePending} {
		run.Status = status
		result, err := x.Poll(context.Background(), e)
		require.NoError(t, err)
		require.Equal(t, "running", result.Status)
		require.True(t, strings.HasPrefix(result.ErrorCode, "CREATIVE_AUDIO_"))
	}
	run.Status = CreativeRunStatusRunning
	run.ProvisioningPhase = CreativeProvisioningPhaseProviderInflight
	run.ErrorCode = creativeStringPtr("PROVIDER_RESULT_UNCONFIRMED")
	result, err := x.Poll(context.Background(), e)
	require.NoError(t, err)
	require.Equal(t, "CREATIVE_AUDIO_SUBMISSION_UNCONFIRMED", result.ErrorCode)
	e.UserID = 8
	_, err = x.Poll(context.Background(), e)
	require.ErrorIs(t, err, ErrCreativeRunNotFound)
	require.Zero(t, f.billing.captureN)
	require.Zero(t, f.billing.releaseN)
}

func TestCreativeAudioCanvasParametersAndMediaSignature(t *testing.T) {
	params := CreativeCanvasExecutionParams{GroupID: 12, Model: CreativeGrokTTSModel, Prompt: "你好", Operation: "generate", Voice: "eve", AudioFormat: "wav", Speed: 1}
	e := &CreativeCanvasExecution{Kind: "audio", Params: params}
	p, err := creativeCanvasImageParams(e, nil)
	require.NoError(t, err)
	require.True(t, p.canvasAudio)
	for _, mutate := range []func(*CreativeCanvasExecutionParams){
		func(p *CreativeCanvasExecutionParams) { p.Duration = 5 }, func(p *CreativeCanvasExecutionParams) { p.Resolution = "720p" }, func(p *CreativeCanvasExecutionParams) { p.Endpoint = "openai_videos" },
		func(p *CreativeCanvasExecutionParams) { p.GenerateAudio = new(bool) }, func(p *CreativeCanvasExecutionParams) { p.Watermark = new(bool) }, func(p *CreativeCanvasExecutionParams) { p.BatchIndex = 1 },
		func(p *CreativeCanvasExecutionParams) { p.References = []CreativeCanvasReference{{AssetID: "other"}} },
	} {
		copy := params
		mutate(&copy)
		_, err := creativeCanvasImageParams(&CreativeCanvasExecution{Kind: "audio", Params: copy}, nil)
		require.Error(t, err)
	}
	kind, mime, err := CreativeCanvasMediaType(minimalGrokTestWAV())
	require.NoError(t, err)
	require.Equal(t, "audio", kind)
	require.Contains(t, mime, "wav")
	_, valid := creativeAudioOutputMIME(minimalGrokTestWAV()[:44], "wav")
	require.False(t, valid)
	_, valid = creativeAudioOutputMIME(minimalGrokTestWAV(), "wav")
	require.True(t, valid)
}

func TestCreativeAudioMP3FrameAndID3Container(t *testing.T) {
	// MPEG-1 Layer III，128 kbps/44.1 kHz，完整帧长度为 417 字节。
	frame := make([]byte, 417)
	copy(frame, []byte{0xff, 0xfb, 0x90, 0x64})
	for _, data := range [][]byte{frame, append([]byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 0}, frame...)} {
		mime, valid := creativeAudioOutputMIME(data, "mp3")
		require.True(t, valid)
		require.Equal(t, "audio/mpeg", mime)
		kind, mime, err := CreativeCanvasMediaType(data)
		require.NoError(t, err)
		require.Equal(t, "audio", kind)
		require.Equal(t, "audio/mpeg", mime)
	}
	for _, data := range [][]byte{frame[:4], frame[:416], []byte("ID3\x04\x00\x00\x7f\x7f\x7f\x7f"), []byte("<html>audio</html>")} {
		_, valid := creativeAudioOutputMIME(data, "mp3")
		require.False(t, valid)
	}
}

func TestCreativeAudioWorkerKnownRejectionReleasesOnlyOnce(t *testing.T) {
	f := newCreativeWorkerFixture()
	seedCreativeRun(f, "audio-rejected", true)
	f.repo.runs["audio-rejected"].Operation = CreativeOperationTTS
	f.exec.err = creativeHTTPStatusError(http.StatusBadRequest, "rejected")
	for range 2 {
		result, err := f.worker.process(context.Background(), "audio-rejected")
		require.NoError(t, err)
		require.True(t, result.Terminal)
	}
	require.Equal(t, 1, f.exec.calls)
	require.Equal(t, 1, f.billing.releaseN)
	require.Zero(t, f.billing.captureN)
	require.Equal(t, CreativeRunStatusFailed, f.repo.runs["audio-rejected"].Status)
}

// 第一次捕获失败仅重放原资金命令，供应商生成不能跟随结算重试。
type creativeAudioCaptureRetry struct {
	*creativeFakeBillingRepo
	attempts int
}

func (r *creativeAudioCaptureRetry) CaptureBatchImageBalance(ctx context.Context, cmd *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	r.attempts++
	if r.attempts == 1 {
		return nil, errors.New("temporary database outage")
	}
	return r.creativeFakeBillingRepo.CaptureBatchImageBalance(ctx, cmd)
}
func TestCreativeAudioWorkerSettlementRetryDoesNotRegenerate(t *testing.T) {
	f := newCreativeWorkerFixture()
	seedCreativeRun(f, "audio-capture", true)
	run := f.repo.runs["audio-capture"]
	run.Operation = CreativeOperationTTS
	run.BaseUnitPrice = .02
	billing := &creativeAudioCaptureRetry{creativeFakeBillingRepo: f.billing}
	f.service.BillingRepo = billing
	f.exec.result = &CreativeExecuteResult{AccountID: 55, Outputs: []CreativeOutput{{Index: 0, Mime: "audio/wav", Bytes: minimalGrokTestWAV()}}}
	first, err := f.worker.process(context.Background(), run.RunID)
	require.NoError(t, err)
	require.False(t, first.Terminal)
	second, err := f.worker.process(context.Background(), run.RunID)
	require.NoError(t, err)
	require.True(t, second.Terminal)
	require.Equal(t, 1, f.exec.calls)
	require.Equal(t, 2, billing.attempts)
	require.Equal(t, 1, f.billing.captureN)
	require.Zero(t, f.billing.releaseN)
}

func TestCreativeAudioPrepareKeepsVoiceAccountModelContract(t *testing.T) {
	account := &Account{ID: 55, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"grok-4.5": "grok-4.5"}, "model_whitelist": []string{"grok-4.5"}}}
	scheduler := &creativeMJSafetyScheduler{accounts: []*Account{account}, released: map[int64]int{}}
	exec := &CreativeExecutor{gateway: &OpenAIGatewayService{openaiScheduler: scheduler}, groupRepo: &creativeFakeGroupRepo{byID: map[int64]*Group{12: {ID: 12, Platform: PlatformGrok, Status: StatusActive}}}}
	selection, err := exec.Prepare(withAdvancedSchedulerTestGroup(context.Background(), 12), CreativeRun{GroupID: 12, Model: CreativeGrokTTSModel, Operation: CreativeOperationTTS, RequestedOutputCount: 1})
	require.NoError(t, err)
	require.Equal(t, CreativeGrokTTSModel, selection.UpstreamModel)
	require.Len(t, scheduler.requests, 1)
	require.Equal(t, CreativeGrokTTSSelectionModel, scheduler.requests[0].RequestedModel)
	require.Equal(t, OpenAIEndpointCapabilityTextGeneration, scheduler.requests[0].RequiredCapability)
	selection.ReleaseFunc()
	require.Equal(t, 1, scheduler.released[55])
}

func TestCreativeAudioUsageLogKeepsFreeRateAndAudioIdentity(t *testing.T) {
	s := newCreativeAudioCatalogTestService()
	logs := &creativeFakeUsageLogRepo{}
	s.UsageLogRepo = logs
	accountID := int64(55)
	run := &CreativeRun{RunID: "audio-free-log", UserID: 7, GroupID: 12, APIKeyID: 900, AccountID: &accountID, Model: CreativeGrokTTSModel, RequestedModel: CreativeGrokTTSModel, Operation: CreativeOperationTTS, BaseUnitPrice: .01, BalanceRateMultiplier: 0}
	require.NoError(t, s.recordCreativeUsageLog(context.Background(), run, 0, 1, nil, run.CreatedAt))
	require.Len(t, logs.logs, 1)
	log := logs.logs[0]
	require.Zero(t, log.RateMultiplier)
	require.Zero(t, log.ActualCost)
	require.Zero(t, log.ImageCount)
	require.Zero(t, log.ImageOutputCost)
	require.Nil(t, log.ImageSize)
	require.Equal(t, "/v1/tts", *log.UpstreamEndpoint)
	require.Equal(t, string(BillingModePerRequest), *log.BillingMode)
	require.Equal(t, .01, log.TotalCost)
}
