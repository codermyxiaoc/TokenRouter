//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 使用原任务查询替身验证展示投影，不发请求、不改变预扣或结算状态。
func TestCreativeVideoPollProjectsReconciliationWithoutBillingChanges(t *testing.T) {
	for _, tt := range []struct {
		status, billing, code, message string
	}{
		{"submission_unknown", "reconciliation", "CREATIVE_VIDEO_SUBMISSION_UNCONFIRMED", "上游受理结果待核对"},
		{"completed", "reconciliation", "CREATIVE_VIDEO_SETTLEMENT_PENDING", "正在核对结算"},
		{"completed", "reserved", "CREATIVE_VIDEO_SETTLEMENT_PENDING", "正在核对结算"},
		{"failed", "reconciliation", "CREATIVE_VIDEO_RELEASE_PENDING", "正在核对预扣处理结果"},
		{"cancelled", "reserved", "CREATIVE_VIDEO_RELEASE_PENDING", "正在核对预扣处理结果"},
		{"expired", "reconciliation", "CREATIVE_VIDEO_RELEASE_PENDING", "正在核对预扣处理结果"},
		{"processing", "reconciliation", "CREATIVE_VIDEO_RECONCILIATION", "任务状态与费用正在核对"},
		{"queued", "reserved", "", ""},
		{"processing", "reserved", "", ""},
	} {
		t.Run(tt.status+"/"+tt.billing, func(t *testing.T) {
			s, e, upstream, billing, _ := creativeVideoTestFixture(t)
			e.TaskID = "vid_saved"
			task := &VideoTaskRecord{ID: e.TaskID, APIKeyID: 2, UserID: e.UserID, GroupID: e.Params.GroupID, IdempotencyKey: "creative_video:" + e.ID, Status: tt.status, BillingStatus: tt.billing, NextPollAt: time.Now().Add(time.Hour)}
			task.Target = VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointCompat, Model: e.Params.Model, CreatePath: "/v1/video/generations", AccountID: 4}
			s.repo.(*videoLifecycleRepo).tasks[task.ID] = task
			content := &creativeVideoContentStub{}
			s.content = content
			for range 2 {
				result, err := s.Poll(context.Background(), e)
				require.NoError(t, err)
				require.Equal(t, "running", result.Status)
				require.Equal(t, tt.code, result.ErrorCode)
				require.Contains(t, result.ErrorMessage, tt.message)
				require.NotContains(t, result.ErrorMessage, "已释放")
				require.Empty(t, result.Outputs)
			}
			require.Equal(t, tt.status, task.Status)
			require.Equal(t, tt.billing, task.BillingStatus)
			require.Zero(t, upstream.submitted)
			require.Zero(t, billing.reserves)
			require.Zero(t, billing.captures)
			require.Zero(t, billing.releases)
			require.Zero(t, content.calls)
		})
	}
}

// 人工确认失败任务的费用后，画布应停止轮询且不再次退款或提交上游。
func TestCreativeVideoPollManuallySettledFailureIsTerminal(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "expired"} {
		t.Run(status, func(t *testing.T) {
			s, e, upstream, billing, _ := creativeVideoTestFixture(t)
			e.TaskID = "vid_manually_settled"
			task := &VideoTaskRecord{ID: e.TaskID, APIKeyID: 2, UserID: e.UserID, GroupID: e.Params.GroupID,
				IdempotencyKey: "creative_video:" + e.ID, Status: status, BillingStatus: "settled", NextPollAt: time.Now().Add(time.Hour)}
			task.Target = VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointCompat, Model: e.Params.Model, CreatePath: "/v1/video/generations", AccountID: 4}
			s.repo.(*videoLifecycleRepo).tasks[task.ID] = task
			content := &creativeVideoContentStub{}
			s.content = content
			for range 2 {
				result, err := s.Poll(context.Background(), e)
				require.NoError(t, err)
				require.Equal(t, "failed", result.Status)
				require.Equal(t, "CREATIVE_VIDEO_MANUALLY_SETTLED", result.ErrorCode)
				require.Contains(t, result.ErrorMessage, "管理员已确认费用")
				require.NotContains(t, result.ErrorMessage, "已释放")
				require.Empty(t, result.Outputs)
			}
			require.Equal(t, status, task.Status)
			require.Equal(t, "settled", task.BillingStatus)
			require.Equal(t, "vid_manually_settled", e.TaskID)
			require.Zero(t, upstream.submitted)
			require.Zero(t, billing.reserves)
			require.Zero(t, billing.captures)
			require.Zero(t, billing.releases)
			require.Zero(t, content.calls)
		})
	}
}

type canvasProgressExecutor struct {
	result  *CreativeCanvasExecutionResult
	err     error
	submits int
}

func (x *canvasProgressExecutor) Submit(context.Context, *CreativeCanvasExecution, []CreativeCanvasInputAsset) (string, error) {
	x.submits++
	return "", errors.New("已有任务不得重新提交")
}
func (x *canvasProgressExecutor) Poll(context.Context, *CreativeCanvasExecution) (*CreativeCanvasExecutionResult, error) {
	return x.result, x.err
}

func TestCreativeCanvasReconciliationProjectionRecoversOnOriginalExecution(t *testing.T) {
	s, repo, params := canvasBatchFixture(t)
	x := &canvasProgressExecutor{result: &CreativeCanvasExecutionResult{Status: "running", ErrorCode: "CREATIVE_VIDEO_SUBMISSION_UNCONFIRMED", ErrorMessage: "上游受理结果待核对，请勿重复生成；预扣尚未结算"}}
	s.SetExecutor("video", x)
	e := &CreativeCanvasExecution{ID: "execution-one", Kind: "video", Params: params, Status: "running", TaskID: "vid_saved", IdempotencyKey: "same-intent"}
	s.reconcileOne(context.Background(), e)
	require.Equal(t, "running", repo.saved.Status)
	require.Equal(t, x.result.ErrorCode, repo.saved.ErrorCode)
	require.Equal(t, x.result.ErrorMessage, repo.saved.ErrorMessage)
	// 临时查询故障不能擦除已经确认的核对阶段，也不能使前端允许再生成。
	x.err = errors.New("temporary query failure")
	s.reconcileOne(context.Background(), e)
	require.Equal(t, x.result.ErrorMessage, repo.saved.ErrorMessage)
	x.err = nil
	x.result = &CreativeCanvasExecutionResult{Status: "running"}
	s.reconcileOne(context.Background(), e)
	require.Empty(t, repo.saved.ErrorCode)
	require.Empty(t, repo.saved.ErrorMessage)
	// 只有原视频任务确认 released 后投影为失败，既有身份始终不变。
	x.result = &CreativeCanvasExecutionResult{Status: "failed", ErrorCode: "CREATIVE_VIDEO_FAILED", ErrorMessage: "视频任务未完成，预扣已释放"}
	s.reconcileOne(context.Background(), e)
	require.Equal(t, "failed", repo.saved.Status)
	require.Equal(t, x.result.ErrorMessage, repo.saved.ErrorMessage)
	require.Equal(t, "vid_saved", repo.saved.TaskID)
	require.Equal(t, "same-intent", repo.saved.IdempotencyKey)
	require.Zero(t, x.submits)
}

func TestCreativeImageProviderReconciliationDueAfterExecutionTimeout(t *testing.T) {
	started := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	run := &CreativeRun{Status: CreativeRunStatusRunning, Operation: CreativeOperationGenerate, ProvisioningPhase: CreativeProvisioningPhaseProviderInflight, StartedAt: &started}
	require.False(t, creativeImageProviderReconciliationDue(run, started.Add(4*time.Minute+59*time.Second), 5*time.Minute))
	require.True(t, creativeImageProviderReconciliationDue(run, started.Add(5*time.Minute), 5*time.Minute))
	code := "PROVIDER_RESULT_UNCONFIRMED"
	run.ErrorCode = &code
	require.True(t, creativeImageProviderReconciliationDue(run, started, 5*time.Minute))
	run.Operation = CreativeOperationTTS
	require.False(t, creativeImageProviderReconciliationDue(run, started.Add(time.Hour), 5*time.Minute))
}

// 图片等待结束后的阶段投影不能改写账务或以旧栅栏掩盖已确认结果。
func TestCreativeImagePollReconciliationStagesAndOwnership(t *testing.T) {
	for _, tt := range []struct {
		name, status, code, message string
		unconfirmed                 bool
	}{
		{"等待超时", CreativeRunStatusRunning, "CREATIVE_IMAGE_SUBMISSION_UNCONFIRMED", "请求等待已超时", false},
		{"响应已中断", CreativeRunStatusRunning, "CREATIVE_IMAGE_SUBMISSION_UNCONFIRMED", "请求已中断或返回结果无法确认", true},
		{"成品待结算", CreativeRunStatusProviderSucceeded, "CREATIVE_IMAGE_SETTLEMENT_PENDING", "正在确认结算", true},
		{"结算重试", CreativeRunStatusSettlementPending, "CREATIVE_IMAGE_SETTLEMENT_PENDING", "正在确认结算", true},
		{"预扣释放", CreativeRunStatusReleasePending, "CREATIVE_IMAGE_RELEASE_PENDING", "正在释放预扣", true},
		{"仍在排队", CreativeRunStatusQueued, "", "", true},
		{"已完成", CreativeRunStatusSucceeded, "", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newCreativeWorkerFixture()
			runID := "crun_image_reconciliation"
			seedCreativeRun(f, runID, true)
			run := f.repo.runs[runID]
			run.Status = tt.status
			run.ProvisioningPhase = CreativeProvisioningPhaseProviderInflight
			started := time.Now().Add(-6 * time.Minute)
			run.StartedAt = &started
			if tt.unconfirmed {
				run.ErrorCode = creativeStringPtr("PROVIDER_RESULT_UNCONFIRMED")
				run.ErrorMessage = creativeStringPtr("https://private-provider.example/secret?api_key=do-not-display")
				if tt.status == CreativeRunStatusRunning {
					started = time.Now()
				}
			}
			workspace := testCreativeWorkspaceID
			run.WorkspaceID = &workspace
			x := &creativeCanvasImageExecutor{creative: f.service}
			execution := &CreativeCanvasExecution{UserID: run.UserID, CanvasID: workspace, TaskID: runID, Kind: "image"}
			for range 2 {
				result, err := x.Poll(context.Background(), execution)
				require.NoError(t, err)
				require.Equal(t, tt.code, result.ErrorCode)
				require.Contains(t, result.ErrorMessage, tt.message)
				require.NotContains(t, result.ErrorMessage, "private-provider")
				require.NotContains(t, result.ErrorMessage, "do-not-display")
				if tt.code == "CREATIVE_IMAGE_SUBMISSION_UNCONFIRMED" {
					require.Contains(t, result.ErrorMessage, "请联系管理员")
					require.Contains(t, result.ErrorMessage, runID)
					require.Contains(t, result.ErrorMessage, "预扣尚未结算")
				}
				if tt.status == CreativeRunStatusSucceeded {
					require.Equal(t, "succeeded", result.Status)
				} else {
					require.Equal(t, "running", result.Status)
				}
			}
			execution.UserID++
			_, err := x.Poll(context.Background(), execution)
			require.ErrorIs(t, err, ErrCreativeRunNotFound)
			require.Equal(t, tt.status, run.Status)
			require.Zero(t, f.exec.calls)
			require.Zero(t, f.billing.captureN)
			require.Zero(t, f.billing.releaseN)
		})
	}
}

func TestCreativeCanvasArchivePendingClearsOldReconciliationProjection(t *testing.T) {
	s, repo, params := canvasBatchFixture(t)
	x := &canvasProgressExecutor{result: &CreativeCanvasExecutionResult{Status: "succeeded"}}
	s.SetExecutor("video", x)
	e := &CreativeCanvasExecution{ID: "execution-one", Kind: "video", Params: params, Status: "running", TaskID: "vid_saved", ErrorCode: "CREATIVE_VIDEO_SETTLEMENT_PENDING", ErrorMessage: "正在核对结算"}
	s.reconcileOne(context.Background(), e)
	require.Equal(t, "running", repo.saved.Status)
	require.Empty(t, repo.saved.ErrorCode)
	require.Equal(t, "成品尚未可归档", repo.saved.ErrorMessage)
	require.Equal(t, "vid_saved", repo.saved.TaskID)
	require.Zero(t, x.submits)
}

type canvasArchiveFailureRepo struct{ *canvasBatchRepoStub }

func (r *canvasArchiveFailureRepo) GetAsset(context.Context, int64, string) (*CreativeCanvasAsset, error) {
	return nil, ErrCanvasNotFound
}
func (r *canvasArchiveFailureRepo) ReserveAsset(_ context.Context, asset *CreativeCanvasAsset, _ int64) (*CreativeCanvasAsset, error) {
	return asset, nil
}

type canvasArchiveFailureStore struct {
	CreativeCanvasAssetStore
	writes int
}

func (s *canvasArchiveFailureStore) Put(context.Context, string, io.Reader, int64) (int64, string, error) {
	s.writes++
	return 0, "", errors.New("模拟存储不可用")
}

func TestCreativeCanvasArchiveFailureOnlyRetriesStorageOnOriginalExecution(t *testing.T) {
	s, repo, params := canvasBatchFixture(t)
	s.repo = &canvasArchiveFailureRepo{repo}
	store := &canvasArchiveFailureStore{}
	s.store = store
	x := &canvasProgressExecutor{}
	s.SetExecutor("video", x)
	e := &CreativeCanvasExecution{ID: "execution-one", Kind: "video", Params: params, Status: "running", TaskID: "vid_saved", IdempotencyKey: "same-intent", ErrorCode: "CREATIVE_VIDEO_SETTLEMENT_PENDING", ErrorMessage: "正在核对结算"}
	for range 2 {
		x.result = &CreativeCanvasExecutionResult{Status: "succeeded", Outputs: []CreativeCanvasOutput{{Index: 0, MIMEType: "video/mp4", Size: 16, Reader: io.NopCloser(strings.NewReader("xxxxftypisomrest!"))}}}
		s.reconcileOne(context.Background(), e)
		require.Equal(t, "running", repo.saved.Status)
		require.Empty(t, repo.saved.ErrorCode)
		require.Equal(t, "成品正在安全保存，请勿重新生成", repo.saved.ErrorMessage)
		require.Equal(t, "vid_saved", repo.saved.TaskID)
		require.Equal(t, "same-intent", repo.saved.IdempotencyKey)
	}
	require.Equal(t, 2, store.writes)
	require.Zero(t, x.submits)
}
