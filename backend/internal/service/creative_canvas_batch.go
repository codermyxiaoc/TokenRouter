package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const creativeCanvasMaxBatchOutputs = 8

// creativeCanvasOutputCounts 以原任务的固定张数为步长，不能把 MJ 的一次四张当成单张计费。
func creativeCanvasOutputCounts(perRun int) []int {
	if perRun < 1 || perRun > creativeCanvasMaxBatchOutputs {
		return []int{}
	}
	counts := make([]int, 0, creativeCanvasMaxBatchOutputs/perRun)
	for n := perRun; n <= creativeCanvasMaxBatchOutputs; n += perRun {
		counts = append(counts, n)
	}
	return counts
}

// CreateExecutionBatch 原子创建一组单任务；每个子任务复用原有幂等、预扣、捕获和退款。
// 即使某子任务失败或进程退出，已完成子任务也不会再次调用上游。
func (s *CreativeCanvasService) CreateExecutionBatch(ctx context.Context, u int64, canvasID, nodeID, kind string, params CreativeCanvasExecutionParams, count int, idempotencyKey string) (*CreativeCanvasExecutionBatch, error) {
	key := strings.TrimSpace(idempotencyKey)
	if kind != "image" || count < 1 || count > creativeCanvasMaxBatchOutputs || key == "" || len(key) > 128 || params.BatchID != "" || params.BatchIndex != 0 || params.BatchSize != 0 || params.BatchOutputCount != 0 {
		return nil, ErrCanvasInvalid
	}
	if _, err := s.Get(ctx, u, canvasID); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(struct {
		CanvasID, NodeID, Kind string
		Count                  int
		Params                 CreativeCanvasExecutionParams
	}{canvasID, nodeID, kind, count, params})
	if err != nil {
		return nil, ErrCanvasInvalid
	}
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	// 重放先恢复已受理快照，不因模型下架、参考图过期或目录变化再次生成。
	old, err := s.repo.GetExecutionBatch(ctx, u, key)
	if err != nil {
		return nil, err
	}
	if len(old) > 0 {
		for _, e := range old {
			if e.Fingerprint != fingerprint || e.Params.BatchSize != len(old) {
				return nil, ErrCanvasConflict
			}
		}
		return s.executionBatchWithAssets(ctx, key, old)
	}
	e, err := s.prepareExecution(ctx, u, canvasID, nodeID, kind, params, key)
	if err != nil {
		return nil, err
	}
	if s.creative == nil {
		return nil, ErrCanvasUnavailable
	}
	assets, err := s.loadInputs(ctx, e)
	if err != nil {
		return nil, err
	}
	imageParams, err := creativeCanvasImageParams(e, assets)
	if err != nil {
		return nil, err
	}
	validated, err := s.creative.validateCreateParams(ctx, u, &imageParams)
	if err != nil {
		return nil, err
	}
	perRun := validated.outputCount
	if perRun < 1 || count%perRun != 0 {
		return nil, ErrCanvasInvalid
	}
	size := count / perRun
	items := make([]*CreativeCanvasExecution, 0, size)
	for i := 0; i < size; i++ {
		child := *e
		// 子任务身份包含用户与批次，响应丢失或并发重放仍引用同一执行和账本。
		child.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("canvas-batch:%d:%s:%d", u, key, i))).String()
		child.IdempotencyKey = "canvas-batch:" + child.ID
		child.Fingerprint = fingerprint
		child.Params.BatchID = key
		child.Params.BatchIndex = i
		child.Params.BatchSize = size
		child.Params.BatchOutputCount = count
		items = append(items, &child)
	}
	items, err = s.repo.CreateExecutionBatch(ctx, items)
	if err != nil {
		return nil, err
	}
	return s.executionBatchWithAssets(ctx, key, items)
}

func (s *CreativeCanvasService) executionBatchWithAssets(ctx context.Context, key string, items []*CreativeCanvasExecution) (*CreativeCanvasExecutionBatch, error) {
	for _, e := range items {
		if _, err := s.withExecutionAssets(ctx, e); err != nil {
			return nil, err
		}
	}
	return &CreativeCanvasExecutionBatch{BatchID: key, Executions: items}, nil
}
