//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 测试替身只模拟持久栅栏与原账务状态，不发出真实付费请求。
type creativeGrokVideoRepoFake struct {
	state             CreativeGrokVideoExecutionState
	beginErr, saveErr error
	starts            int
}

func (r *creativeGrokVideoRepoFake) BeginGrokVideoExecution(_ context.Context, e *CreativeCanvasExecution, keyID int64) (bool, error) {
	if r.beginErr != nil {
		return false, r.beginErr
	}
	if r.state.Phase != "" {
		return false, nil
	}
	r.state = CreativeGrokVideoExecutionState{Phase: "provider_inflight", APIKeyID: keyID, GroupID: e.Params.GroupID}
	r.starts++
	return true, nil
}
func (r *creativeGrokVideoRepoFake) GetGrokVideoExecutionState(context.Context, int64, string) (*CreativeGrokVideoExecutionState, error) {
	state := r.state
	return &state, nil
}
func (r *creativeGrokVideoRepoFake) SaveGrokVideoResponse(_ context.Context, _ int64, _ string, status int, sent bool, code string) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.state.HTTPStatus, r.state.RequestSent, r.state.ErrorCode = status, sent, code
	return nil
}
func (r *creativeGrokVideoRepoFake) BindGrokVideoTask(_ context.Context, _ int64, _ string, task *LegacyVideoTask) error {
	r.state.TaskID, r.state.AccountID, r.state.Phase = task.TaskID, task.AccountID, "accepted"
	return nil
}

type creativeGrokVideoGatewayFake struct {
	repo             *creativeGrokVideoRepoFake
	task             *LegacyVideoTask
	calls, downloads int
	sent, accept     bool
	rejected         int
	status           int
	err              error
	body             []byte
	response         *http.Response
	t                *testing.T
}

func (g *creativeGrokVideoGatewayFake) Generate(ctx context.Context, key *APIKey, body []byte) (CreativeTextGatewayResponse, error) {
	g.calls++
	g.body = body
	if g.sent {
		MarkCreativeGrokVideoRequestSent(ctx)
	}
	if g.rejected > 0 {
		MarkCreativeGrokVideoRejected(ctx, g.rejected)
	}
	if g.accept {
		id := CreativeGrokVideoExecutionID(ctx, key.UserID, key.ID, *key.GroupID)
		require.NotEmpty(g.t, id)
		g.task = &LegacyVideoTask{Source: "grok_video", TaskID: "upstream-one", UserID: key.UserID, BillingUserID: key.UserID, APIKeyID: key.ID, GroupID: *key.GroupID, AccountID: 91, Status: "queued", BillingStatus: "pending"}
		require.NoError(g.t, g.repo.BindGrokVideoTask(ctx, key.UserID, id, g.task))
	}
	return CreativeTextGatewayResponse{StatusCode: g.status}, g.err
}
func (g *creativeGrokVideoGatewayFake) Task(context.Context, int64, int64, string) (*LegacyVideoTask, error) {
	return g.task, nil
}
func (g *creativeGrokVideoGatewayFake) OpenContent(ctx context.Context, key *APIKey, id string) (*http.Response, error) {
	g.downloads++
	require.True(g.t, IsCreativeGrokVideoRead(ctx, key, id))
	return g.response, nil
}
func creativeGrokVideoFixture(t *testing.T) (*creativeGrokVideoCanvasExecutor, *CreativeCanvasExecution, *creativeGrokVideoGatewayFake, *creativeGrokVideoRepoFake) {
	t.Helper()
	pub, group := newCreativeGrokVideoCatalogTestService()
	user := &User{ID: 7, Status: StatusActive}
	pub.UserRepo = &creativeFakeUserRepo{user: user}
	managed := CreativeManagedBy
	key := &APIKey{ID: 901, Key: "test-key", UserID: 7, User: user, Status: StatusAPIKeyActive, GroupID: &group.ID, Group: group, ManagedBy: &managed}
	pub.ApiKeyRepo = &creativeFakeManagedKeyRepo{key: key}
	repo := &creativeGrokVideoRepoFake{}
	gateway := &creativeGrokVideoGatewayFake{repo: repo, sent: true, accept: true, status: 200, t: t}
	e := creativeVideoTestExecution()
	e.UserID, e.Params.GroupID, e.Params.Model, e.Params.Endpoint = 7, 12, "grok-imagine-video-1.5", "grok"
	return &creativeGrokVideoCanvasExecutor{public: pub, repo: repo, gateway: gateway}, e, gateway, repo
}

func TestCreativeGrokVideoRequestReferencesAndParameters(t *testing.T) {
	x, e, _, _ := creativeGrokVideoFixture(t)
	models, err := x.public.creativeGrokVideoModelsForGroup(context.Background(), x.public.GroupRepo.(*creativeFakeGroupRepo).byID[12], x.public.creativeModelSettings(context.Background()))
	require.NoError(t, err)
	require.Len(t, models, 1)
	e.Params.AspectRatio, e.Params.Resolution = "adaptive", "1080p"
	e.Params.References = []CreativeCanvasReference{{AssetID: "image-one", Role: "first_frame"}}
	assets := []CreativeCanvasInputAsset{{Asset: &CreativeCanvasAsset{ID: "image-one", UserID: 7, MediaType: "image", MIMEType: "image/png"}, Role: "first_frame", Content: makeTestPNG(t, 16, 16)}}
	body, err := creativeGrokVideoCanvasRequest(e, &models[0], assets, 8000)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(gjson.GetBytes(body, "image.url").String(), "data:image/png;base64,"))
	require.Equal(t, "image_url", gjson.GetBytes(body, "image.type").String())
	require.False(t, gjson.GetBytes(body, "images").Exists())
	require.False(t, gjson.GetBytes(body, "ratio").Exists())
	require.False(t, gjson.GetBytes(body, "aspect_ratio").Exists())
	require.Equal(t, "1080p", gjson.GetBytes(body, "resolution").String())
	require.Equal(t, float64(5), gjson.GetBytes(body, "duration").Float())
	e.Params.AspectRatio = "9:16"
	body, err = creativeGrokVideoCanvasRequest(e, &models[0], assets, 8000)
	require.NoError(t, err)
	require.Equal(t, "9:16", gjson.GetBytes(body, "aspect_ratio").String())
	assets[0].Asset.UserID = 8
	_, err = creativeGrokVideoCanvasRequest(e, &models[0], assets, 8000)
	require.Error(t, err)
	assets[0].Asset.UserID = 7
	assets[0].Content = []byte("not an image")
	_, err = creativeGrokVideoCanvasRequest(e, &models[0], assets, 8000)
	require.Error(t, err)
}

func TestCreativeGrokVideoSubmitRecoversWithoutRegeneration(t *testing.T) {
	x, e, g, r := creativeGrokVideoFixture(t)
	id, err := x.Submit(context.Background(), e, nil)
	require.NoError(t, err)
	require.Equal(t, creativeGrokVideoTaskPrefix+e.ID, id)
	require.Equal(t, 1, g.calls)
	require.Equal(t, 1, r.starts)
	require.Equal(t, "upstream-one", r.state.TaskID)
	x.public.Settings.(*creativeFakeSettingReader).enabled = false
	e.Params.References = []CreativeCanvasReference{{AssetID: "expired"}}
	id2, err := x.Submit(context.Background(), e, nil)
	require.NoError(t, err)
	require.Equal(t, id, id2)
	require.Equal(t, 1, g.calls)
}

func TestCreativeGrokVideoRejectsUnrepresentableBillingParametersBeforeSend(t *testing.T) {
	for _, change := range []func(*CreativeCanvasExecution){
		func(e *CreativeCanvasExecution) { e.Params.Resolution = "768p" },
		func(e *CreativeCanvasExecution) { e.Params.Duration = 60 },
		func(e *CreativeCanvasExecution) { e.Params.Duration = 0 },
	} {
		x, e, g, r := creativeGrokVideoFixture(t)
		change(e)
		_, err := x.Submit(context.Background(), e, nil)
		require.ErrorIs(t, err, ErrCreativeGrokVideoParameters)
		require.Zero(t, g.calls)
		require.Zero(t, r.starts)
	}
}

func TestCreativeGrokVideoErrorResponseWithAcceptedTaskKeepsBinding(t *testing.T) {
	x, e, g, r := creativeGrokVideoFixture(t)
	g.status = 429
	id, err := x.Submit(context.Background(), e, nil)
	require.NoError(t, err)
	e.TaskID = id
	require.Equal(t, "upstream-one", r.state.TaskID)
	result, err := x.Poll(context.Background(), e)
	require.NoError(t, err)
	require.Equal(t, "running", result.Status)
	_, err = x.Submit(context.Background(), e, nil)
	require.NoError(t, err)
	require.Equal(t, 1, g.calls)
}

func TestCreativeGrokVideoUnknownSubmissionAndLocalRejection(t *testing.T) {
	for _, tc := range []struct {
		name               string
		sent               bool
		rejected           int
		status             int
		invokeErr, saveErr error
		failed             bool
	}{
		{"transport_unknown", true, 0, 502, errors.New("transport"), nil, false},
		{"accepted_missing_id", true, 0, 200, nil, nil, false},
		{"response_storage_failed", true, 400, 400, nil, errors.New("db"), false},
		{"explicit_rejection", true, 400, 400, nil, nil, true},
		{"rejection_mapped_to_502", true, 401, 502, nil, nil, true},
		{"unknown_mapped_to_400", true, 0, 400, nil, nil, false},
		{"conflicting_task_in_429", true, 0, 429, nil, nil, false},
		{"local_no_account", false, 0, 503, nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, e, g, r := creativeGrokVideoFixture(t)
			g.accept, g.sent, g.status, g.err, r.saveErr = false, tc.sent, tc.status, tc.invokeErr, tc.saveErr
			g.rejected = tc.rejected
			id, err := x.Submit(context.Background(), e, nil)
			require.NoError(t, err)
			e.TaskID = id
			result, err := x.Poll(context.Background(), e)
			require.NoError(t, err)
			require.Equal(t, tc.failed, result.Status == "failed")
			_, err = x.Submit(context.Background(), e, nil)
			require.NoError(t, err)
			require.Equal(t, 1, g.calls)
			require.Zero(t, g.downloads)
		})
	}
	x, e, g, r := creativeGrokVideoFixture(t)
	r.beginErr = errors.New("database unavailable")
	_, err := x.Submit(context.Background(), e, nil)
	require.Error(t, err)
	require.Zero(t, g.calls)
}

func TestCreativeGrokVideoPollUsesOriginalSettlementAndOwnership(t *testing.T) {
	x, e, g, _ := creativeGrokVideoFixture(t)
	id, err := x.Submit(context.Background(), e, nil)
	require.NoError(t, err)
	e.TaskID = id
	for _, state := range []struct{ status, billing, result string }{{"queued", "pending", "running"}, {"completed", "pending", "running"}, {"completed", "reconciliation", "running"}, {"failed", "pending", "running"}, {"failed", "not_billable", "failed"}} {
		g.task.Status, g.task.BillingStatus = state.status, state.billing
		result, err := x.Poll(context.Background(), e)
		require.NoError(t, err)
		require.Equal(t, state.result, result.Status)
		require.Zero(t, g.downloads)
	}
	g.task.Status, g.task.BillingStatus = "completed", "settled"
	g.task.AccountID++
	_, err = x.Poll(context.Background(), e)
	require.ErrorIs(t, err, ErrCanvasConflict)
	require.Zero(t, g.downloads)
	g.task.AccountID--
	g.response = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"video/mp4"}}, Body: io.NopCloser(strings.NewReader("test-video")), ContentLength: 10}
	result, err := x.Poll(context.Background(), e)
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.Status)
	require.Len(t, result.Outputs, 1)
	require.Equal(t, 1, g.downloads)
	result.Outputs[0].Reader.Close()
	require.Equal(t, 1, g.calls)
}

func TestCreativeGrokVideoTrustedContextCannotBeForged(t *testing.T) {
	x, e, _, _ := creativeGrokVideoFixture(t)
	key := x.public.ApiKeyRepo.(*creativeFakeManagedKeyRepo).key
	require.False(t, IsCreativeGrokVideoRead(context.Background(), key, "task"))
	ctx, _ := creativeGrokVideoContextFor(context.Background(), e, key, "task")
	require.True(t, IsCreativeGrokVideoRead(ctx, key, "task"))
	require.False(t, IsCreativeGrokVideoRead(ctx, key, "other"))
	copyKey := *key
	copyKey.ID++
	require.False(t, IsCreativeGrokVideoRead(ctx, &copyKey, "task"))
	require.Empty(t, CreativeGrokVideoExecutionID(ctx, key.UserID, key.ID, *key.GroupID))
	ctx, _ = creativeGrokVideoContextFor(context.Background(), e, key, "")
	require.Equal(t, e.ID, CreativeGrokVideoExecutionID(ctx, key.UserID, key.ID, *key.GroupID))
	require.Empty(t, CreativeGrokVideoExecutionID(ctx, key.UserID+1, key.ID, *key.GroupID))
	require.False(t, IsCreativeGrokVideoRead(ctx, key, "task"))
	MarkCreativeGrokVideoRequestSent(ctx)
	MarkCreativeGrokVideoRejected(ctx, 401)
	sink := ctx.Value(creativeGrokVideoContextKey{}).(*creativeGrokVideoContext)
	require.Equal(t, int32(401), sink.rejectedStatus.Load())
	MarkCreativeGrokVideoRequestSent(ctx)
	require.Zero(t, sink.rejectedStatus.Load())
}

func TestCreativeGrokVideoForwardCapturesOriginalRejectionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		rejected bool
	}{
		{"credentials", 401, `{"error":{"message":"invalid API key"}}`, true},
		{"validation", 400, `{"error":{"message":"invalid duration"}}`, true},
		{"moderation", 403, `{"error":{"code":"content_policy_violation","message":"Request blocked by content safety policy"}}`, true},
		{"ambiguous_safety_message", 403, `{"error":{"message":"content safety rejection"}}`, false},
		{"unknown_server", 500, `{"error":{"message":"internal error"}}`, false},
		{"conflicting_ids", 429, `{"id":"task-a","request_id":"task-b"}`, false},
		{"accepted_with_error", 400, `{"id":"task-a","error":{"message":"invalid duration"}}`, false},
		{"positive_usage_with_validation", 400, `{"usage":{"output_seconds":5},"error":{"type":"invalid_request_error"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, e, _, _ := creativeGrokVideoFixture(t)
			key := x.public.ApiKeyRepo.(*creativeFakeManagedKeyRepo).key
			ctx, sink := creativeGrokVideoContextFor(context.Background(), e, key, "")
			upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(tc.body)}
			upstream.response.StatusCode = tc.status
			svc := &OpenAIGatewayService{httpUpstream: upstream}
			c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/videos/generations", nil)
			_, _ = svc.ForwardGrokMedia(ctx, c, grokMediaContentTestAccount(), GrokMediaEndpointVideosGenerations, "", []byte(`{"model":"grok-imagine-video","prompt":"waves"}`), "application/json")
			require.True(t, sink.sent.Load())
			require.Equal(t, tc.rejected, sink.rejectedStatus.Load() > 0)
			require.Len(t, upstream.requests, 1)
		})
	}
}
