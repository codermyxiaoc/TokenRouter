//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 批次替身只保存执行元数据；任何测试都不发送真实模型请求。
type canvasBatchRepoStub struct {
	CreativeCanvasRepository
	mu     sync.Mutex
	canvas *CreativeCanvas
	items  map[string][]*CreativeCanvasExecution
	saved  *CreativeCanvasExecution
}

func (r *canvasBatchRepoStub) Get(_ context.Context, u int64, id string) (*CreativeCanvas, error) {
	if r.canvas == nil || r.canvas.UserID != u || r.canvas.ID != id {
		return nil, ErrCanvasNotFound
	}
	return r.canvas, nil
}
func (r *canvasBatchRepoStub) GetExecutionBatch(_ context.Context, u int64, key string) ([]*CreativeCanvasExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := r.items[key]
	if len(items) > 0 && items[0].UserID != u {
		return nil, nil
	}
	return cloneCanvasBatch(items), nil
}

// 数据库读取会返回独立对象，替身也复制对象以覆盖真正的并发请求而非制造共享指针竞态。
func cloneCanvasBatch(items []*CreativeCanvasExecution) []*CreativeCanvasExecution {
	out := make([]*CreativeCanvasExecution, 0, len(items))
	for _, e := range items {
		copy := *e
		out = append(out, &copy)
	}
	return out
}
func (r *canvasBatchRepoStub) CreateExecutionBatch(_ context.Context, items []*CreativeCanvasExecution) ([]*CreativeCanvasExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := items[0].Params.BatchID
	if old := r.items[key]; len(old) > 0 {
		if old[0].Fingerprint != items[0].Fingerprint || len(old) != len(items) {
			return nil, ErrCanvasConflict
		}
		return cloneCanvasBatch(old), nil
	}
	r.items[key] = items
	return cloneCanvasBatch(items), nil
}
func (r *canvasBatchRepoStub) ListAssets(context.Context, int64, string, string, int, int) ([]*CreativeCanvasAsset, int64, error) {
	return []*CreativeCanvasAsset{}, 0, nil
}
func (r *canvasBatchRepoStub) SaveExecution(_ context.Context, e *CreativeCanvasExecution, _ time.Time) error {
	copy := *e
	r.saved = &copy
	return nil
}

type canvasUnusedStore struct{ CreativeCanvasAssetStore }

func canvasBatchFixture(t *testing.T) (*CreativeCanvasService, *canvasBatchRepoStub, CreativeCanvasExecutionParams) {
	t.Helper()
	pub := newCreativeTestService()
	configureOpenAICreativeTestService(pub)
	groups := pub.GroupRepo.(*creativeFakeGroupRepo)
	groups.active = []Group{*groups.byID[12]}
	repo := &canvasBatchRepoStub{canvas: &CreativeCanvas{ID: testCreativeWorkspaceID, UserID: 7, Document: json.RawMessage(`{"nodes":[{"id":"image-1","type":"image"}],"connections":[],"viewport":{"x":0,"y":0,"k":1}}`)}, items: map[string][]*CreativeCanvasExecution{}}
	return NewCreativeCanvasService(repo, &canvasUnusedStore{}, pub), repo, CreativeCanvasExecutionParams{GroupID: 12, Model: "gpt-image-2", Operation: "generate", Prompt: "小球", ImageSize: "1K"}
}

func TestCreativeCanvasBatchConcurrentReplayAndConflicts(t *testing.T) {
	s, repo, p := canvasBatchFixture(t)
	var wg sync.WaitGroup
	results := make(chan *CreativeCanvasExecutionBatch, 32)
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "image-1", "image", p, 3, "batch-one")
			results <- v
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var ids []string
	for batch := range results {
		require.Len(t, batch.Executions, 3)
		got := []string{}
		for i, e := range batch.Executions {
			got = append(got, e.ID)
			require.Equal(t, "image-1", e.NodeID)
			require.Equal(t, i, e.Params.BatchIndex)
			require.Equal(t, 3, e.Params.BatchOutputCount)
		}
		if ids == nil {
			ids = got
		} else {
			require.Equal(t, ids, got)
		}
	}
	require.Len(t, repo.items, 1)
	require.Empty(t, s.creative.Repo.(*creativeFakeRunRepo).runs, "准入只保存执行，不调用模型或预扣")
	for _, count := range []int{1, 2, 4} {
		_, err := s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "image-1", "image", p, count, "batch-one")
		require.ErrorIs(t, err, ErrCanvasConflict)
	}
	p.Prompt = "参数已变化"
	_, err := s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "image-1", "image", p, 3, "batch-one")
	require.ErrorIs(t, err, ErrCanvasConflict)
}

func TestCreativeCanvasBatchOwnershipLimitsAndServerFields(t *testing.T) {
	s, repo, p := canvasBatchFixture(t)
	for _, count := range []int{-1, 0, 9, 100} {
		_, err := s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "image-1", "image", p, count, "invalid")
		require.ErrorIs(t, err, ErrCanvasInvalid)
	}
	_, err := s.CreateExecutionBatch(context.Background(), 8, testCreativeWorkspaceID, "image-1", "image", p, 2, "wrong-user")
	require.ErrorIs(t, err, ErrCanvasNotFound)
	_, err = s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "missing", "image", p, 2, "missing")
	require.ErrorIs(t, err, ErrCanvasInvalid)
	_, err = s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "image-1", "video", p, 2, "video")
	require.ErrorIs(t, err, ErrCanvasInvalid)
	p.BatchID = "forged"
	_, err = s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "image-1", "image", p, 2, "forged")
	require.ErrorIs(t, err, ErrCanvasInvalid)
	require.Empty(t, repo.items)
}

func TestCreativeCanvasBatchReplaySurvivesCatalogChange(t *testing.T) {
	s, _, p := canvasBatchFixture(t)
	first, err := s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "image-1", "image", p, 2, "stable")
	require.NoError(t, err)
	s.creative.Settings.(*creativeFakeSettingReader).models = nil
	second, err := s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "image-1", "image", p, 2, "stable")
	require.NoError(t, err)
	require.Equal(t, first.Executions[0].ID, second.Executions[0].ID)
	_, err = s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "image-1", "image", p, 2, "new")
	require.ErrorIs(t, err, ErrCreativeInvalidModel)
}

func TestCreativeCanvasBatchCountContract(t *testing.T) {
	require.Equal(t, []int{1, 2, 3, 4, 5, 6, 7, 8}, creativeCanvasOutputCounts(1))
	require.Equal(t, []int{4, 8}, creativeCanvasOutputCounts(4))
	require.Empty(t, creativeCanvasOutputCounts(0))
	s, _, _ := canvasBatchFixture(t)
	models, err := s.creative.ListModels(context.Background(), 7)
	require.NoError(t, err)
	require.NotEmpty(t, models.Data)
	require.Equal(t, 1, models.Data[0].OutputCount)
	require.Equal(t, 1, models.Data[0].MaxOutputCount)
	require.Equal(t, []int{1, 2, 3, 4, 5, 6, 7, 8}, models.Data[0].OutputCounts)
}

// 多张仍是独立旧任务；重放、恢复不会再次预扣，MJ 八张拆成两个四张合同。
func TestCreativeCanvasBatchUsesExistingImageBillingAndRecovery(t *testing.T) {
	for _, mj := range []bool{false, true} {
		t.Run(map[bool]string{false: "普通三张", true: "MJ八张"}[mj], func(t *testing.T) {
			s, _, p := canvasBatchFixture(t)
			count, children, perRun := 3, 3, 1
			if mj {
				s.creative = newCreativeMidjourneyTestService()
				p.Model = "studio-image"
				count, children, perRun = 8, 2, 4
			}
			x := &creativeCanvasImageExecutor{creative: s.creative}
			batch, err := s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "image-1", "image", p, count, "billing")
			require.NoError(t, err)
			require.Len(t, batch.Executions, children)
			for _, e := range batch.Executions {
				id, err := x.Submit(context.Background(), e, nil)
				require.NoError(t, err)
				replayed, err := x.Submit(context.Background(), e, nil)
				require.NoError(t, err)
				require.Equal(t, id, replayed)
				recovered, err := x.Recover(context.Background(), e)
				require.NoError(t, err)
				require.Equal(t, id, recovered)
				run := s.creative.Repo.(*creativeFakeRunRepo).runs[id]
				require.Equal(t, perRun, run.RequestedOutputCount)
				require.InDelta(t, run.BaseUnitPrice*float64(perRun)*run.BalanceRateMultiplier, run.EstimatedCost, 1e-8)
			}
			billing := s.creative.BillingRepo.(*creativeFakeBillingRepo)
			require.Equal(t, children, billing.reserveN)
			require.Len(t, s.creative.Queue.(*creativeFakeQueue).enqueued, children)
			require.Zero(t, billing.captureN)
			require.Zero(t, billing.releaseN)
			if mj {
				for _, bad := range []int{1, 2, 3, 5, 6, 7} {
					_, err := s.CreateExecutionBatch(context.Background(), 7, testCreativeWorkspaceID, "image-1", "image", p, bad, "bad-count")
					require.ErrorIs(t, err, ErrCanvasInvalid)
				}
			}
		})
	}
}

func TestCreativeCanvasBatchCannotExpandOutputCountAfterMappingChange(t *testing.T) {
	pub := newCreativeMidjourneyTestService()
	p := validCreateParams()
	p.Model = "studio-image"
	p.OutputCount = 1
	p.ExpectedOutputCount = 1
	_, err := pub.validateCreateParams(context.Background(), 7, &p)
	require.ErrorIs(t, err, ErrCreativeInvalidParams, "原单张批次不能因模型改映射MJ扩大为四张预扣")
	p.OutputCount = 4
	p.ExpectedOutputCount = 4
	v, err := pub.validateCreateParams(context.Background(), 7, &p)
	require.NoError(t, err)
	require.Equal(t, 4, v.outputCount)
}

func TestCreativeCanvasImageReferenceConversion(t *testing.T) {
	s, _, p := canvasBatchFixture(t)
	p.Operation = "edit"
	png := makeTestPNG(t, 2, 2)
	e := &CreativeCanvasExecution{Params: p}
	params, err := creativeCanvasImageParams(e, []CreativeCanvasInputAsset{{Asset: &CreativeCanvasAsset{MediaType: "image", MIMEType: "image/png"}, Content: png}})
	require.NoError(t, err)
	require.Len(t, params.SourceImages, 1)
	require.Equal(t, png, params.SourceImages[0].Bytes)
	_, err = s.creative.validateCreateParams(context.Background(), 7, &params)
	require.NoError(t, err)
	_, err = creativeCanvasImageParams(e, []CreativeCanvasInputAsset{{Asset: &CreativeCanvasAsset{MediaType: "video", MIMEType: "video/mp4"}, Content: []byte("not-image")}})
	require.ErrorIs(t, err, ErrCreativeInvalidParams)
}

func TestCreativeCanvasImageInsufficientBalanceDoesNotEnqueue(t *testing.T) {
	s, _, p := canvasBatchFixture(t)
	p.Operation = "edit"
	billing := &creativeMJBillingRecorder{UsageBillingRepository: s.creative.BillingRepo, reserveErr: ErrBatchImageInsufficientBalance}
	s.creative.BillingRepo = billing
	x := &creativeCanvasImageExecutor{creative: s.creative}
	e := &CreativeCanvasExecution{ID: "no-balance", UserID: 7, CanvasID: testCreativeWorkspaceID, Params: p}
	_, err := x.Submit(context.Background(), e, []CreativeCanvasInputAsset{{Asset: &CreativeCanvasAsset{MediaType: "image", MIMEType: "image/png"}, Content: makeTestPNG(t, 2, 2)}})
	require.ErrorIs(t, err, ErrCreativeInsufficientBalance)
	require.Empty(t, s.creative.Queue.(*creativeFakeQueue).enqueued, "余额不足必须在排队调用模型前停止")
	require.Empty(t, billing.captured)
	require.Len(t, billing.reserved, 1)
	// 模拟画布尚未保存失败状态就重启：先恢复旧 run，释放补偿完成后仍显示真正的余额原因。
	id, err := x.Recover(context.Background(), e)
	require.NoError(t, err)
	require.NotEmpty(t, id)
	run := s.creative.Repo.(*creativeFakeRunRepo).runs[id]
	require.Equal(t, "CREATIVE_INSUFFICIENT_BALANCE", creativeDerefString(run.ErrorCode))
	run.Status = CreativeRunStatusFailed
	e.TaskID = id
	result, err := x.Poll(context.Background(), e)
	require.NoError(t, err)
	require.Equal(t, "failed", result.Status)
	require.Contains(t, result.ErrorMessage, "余额")
}

type canvasRejectedExecutor struct {
	err     error
	submits int
}

func (x *canvasRejectedExecutor) Submit(context.Context, *CreativeCanvasExecution, []CreativeCanvasInputAsset) (string, error) {
	x.submits++
	return "", x.err
}
func (x *canvasRejectedExecutor) Poll(context.Context, *CreativeCanvasExecution) (*CreativeCanvasExecutionResult, error) {
	return nil, errors.New("不应轮询未提交任务")
}

func TestCreativeCanvasReportsActualFailureWithoutSensitiveData(t *testing.T) {
	s, repo, p := canvasBatchFixture(t)
	x := &canvasRejectedExecutor{err: ErrCreativeInsufficientBalance.WithCause(errors.New("secret-token-and-source-url"))}
	s.SetExecutor("image", x)
	e := &CreativeCanvasExecution{ID: "failed", Kind: "image", Params: p, Status: "queued"}
	s.reconcileOne(context.Background(), e)
	require.Equal(t, 1, x.submits)
	require.Equal(t, "failed", repo.saved.Status)
	require.Equal(t, "CREATIVE_INSUFFICIENT_BALANCE", repo.saved.ErrorCode)
	require.Contains(t, repo.saved.ErrorMessage, "余额")
	require.NotContains(t, repo.saved.ErrorMessage, "素材")
	require.NotContains(t, repo.saved.ErrorMessage, "secret")
	old := &CreativeCanvasExecution{Status: "failed", ErrorCode: "CREATIVE_INSUFFICIENT_BALANCE", ErrorMessage: "生成参数或权限校验失败"}
	projected, err := s.withExecutionAssets(context.Background(), old)
	require.NoError(t, err)
	require.Contains(t, projected.ErrorMessage, "余额")
	require.Empty(t, creativeCanvasFailureMessage("SECRET_URL"))
}
