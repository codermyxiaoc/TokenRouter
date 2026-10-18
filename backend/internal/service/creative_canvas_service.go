package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/google/uuid"
)

// CreativeCanvasService 管理云文档、私有素材和执行绑定，不拥有任何资金算法。
type CreativeCanvasService struct {
	repo      CreativeCanvasRepository
	store     CreativeCanvasAssetStore
	creative  *CreativePublicService
	mu        sync.RWMutex
	executors map[string]CreativeCanvasExecutor
	start     sync.Once
	stop      sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
	now       func() time.Time
}

func NewCreativeCanvasService(repo CreativeCanvasRepository, store CreativeCanvasAssetStore, creative *CreativePublicService) *CreativeCanvasService {
	s := &CreativeCanvasService{repo: repo, store: store, creative: creative, executors: make(map[string]CreativeCanvasExecutor), done: make(chan struct{}), now: time.Now}
	if creative != nil {
		s.executors["image"] = &creativeCanvasImageExecutor{creative: creative, repo: repo}
		s.executors["audio"] = &creativeCanvasImageExecutor{creative: creative, repo: repo}
		textRepo, _ := repo.(CreativeTextRepository)
		s.executors["text"] = &creativeTextCanvasExecutor{public: creative, repo: textRepo}
	}
	return s
}
func (s *CreativeCanvasService) SetExecutor(kind string, executor CreativeCanvasExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executors[kind] = executor
}
func (s *CreativeCanvasService) executor(kind string) CreativeCanvasExecutor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.executors[kind]
}
func (s *CreativeCanvasService) enabled(ctx context.Context) error {
	if s == nil || s.repo == nil || s.store == nil {
		return ErrCanvasUnavailable
	}
	if s.creative != nil && !s.creative.enabled(ctx) {
		return ErrCreativeDisabled
	}
	return nil
}

func (s *CreativeCanvasService) Create(ctx context.Context, userID int64, title string, document json.RawMessage) (*CreativeCanvas, error) {
	if err := s.enabled(ctx); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrCanvasNotFound
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "无限画布"
	}
	if utf8.RuneCountInString(title) > 160 {
		return nil, ErrCanvasInvalid
	}
	clean, assetIDs, err := ValidateCreativeCanvasDocument(document)
	if err != nil {
		return nil, err
	}
	if err = s.validateOwnedAssets(ctx, userID, assetIDs, false); err != nil {
		return nil, err
	}
	v := &CreativeCanvas{ID: uuid.NewString(), UserID: userID, Title: title, Document: clean, Revision: 1}
	if err = s.repo.Create(ctx, v); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, userID, v.ID)
}
func (s *CreativeCanvasService) Get(ctx context.Context, u int64, id string) (*CreativeCanvas, error) {
	if err := s.enabled(ctx); err != nil {
		return nil, err
	}
	if !validCanvasID(id) {
		return nil, ErrCanvasNotFound
	}
	return s.repo.Get(ctx, u, id)
}
func (s *CreativeCanvasService) List(ctx context.Context, u int64, limit, offset int) ([]*CreativeCanvas, int64, error) {
	if err := s.enabled(ctx); err != nil {
		return nil, 0, err
	}
	limit, offset = canvasPage(limit, offset)
	return s.repo.List(ctx, u, limit, offset)
}
func (s *CreativeCanvasService) Update(ctx context.Context, u int64, id, title string, document json.RawMessage, revision int64) (*CreativeCanvas, error) {
	old, err := s.Get(ctx, u, id)
	if err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if revision < 1 || title == "" || utf8.RuneCountInString(title) > 160 {
		return nil, ErrCanvasInvalid
	}
	clean, assetIDs, err := ValidateCreativeCanvasDocument(document)
	if err != nil {
		return nil, err
	}
	if err = s.validateOwnedAssets(ctx, u, assetIDs, false); err != nil {
		return nil, err
	}
	old.Title, old.Document = title, clean
	if err = s.repo.Update(ctx, old, revision); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, u, id)
}
func (s *CreativeCanvasService) Delete(ctx context.Context, u int64, id string, revision int64) error {
	if _, err := s.Get(ctx, u, id); err != nil {
		return err
	}
	if revision < 1 {
		return ErrCanvasInvalid
	}
	return s.repo.Delete(ctx, u, id, revision)
}
func canvasPage(limit, offset int) (int, int) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func (s *CreativeCanvasService) validateOwnedAssets(ctx context.Context, u int64, ids []string, requireReady bool) error {
	for _, id := range ids {
		a, err := s.repo.GetAsset(ctx, u, id)
		if err != nil {
			return err
		}
		if requireReady && (a.Status != "ready" || !s.now().Before(a.ExpiresAt)) {
			return ErrCanvasExpired
		}
	}
	return nil
}

// UploadAsset 先占用存储预算再流式写入；只有文件落盘且数据库确认后才向客户端公开。
func (s *CreativeCanvasService) UploadAsset(ctx context.Context, u int64, canvasID string, reader io.Reader, limit int64) (*CreativeCanvasAsset, error) {
	if _, err := s.Get(ctx, u, canvasID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > CreativeCanvasUploadLimit {
		limit = CreativeCanvasUploadLimit
	}
	prefix := make([]byte, 512)
	n, err := io.ReadFull(reader, prefix)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	prefix = prefix[:n]
	kind, mime, err := CreativeCanvasMediaType(prefix)
	if err != nil {
		return nil, err
	}
	if kind == "image" && limit > 32<<20 {
		limit = 32 << 20
	}
	a := &CreativeCanvasAsset{ID: uuid.NewString(), UserID: u, CanvasID: canvasID, MediaType: kind, MIMEType: mime, ByteSize: limit, ExpiresAt: s.now().Add(CreativeCanvasAssetTTL)}
	a, err = s.repo.ReserveAsset(ctx, a, CreativeCanvasUserQuota)
	if err != nil {
		return nil, err
	}
	return s.writeAsset(ctx, a, io.MultiReader(bytes.NewReader(prefix), reader), limit)
}
func (s *CreativeCanvasService) writeAsset(ctx context.Context, a *CreativeCanvasAsset, reader io.Reader, limit int64) (*CreativeCanvasAsset, error) {
	n, hash, err := s.store.Put(ctx, a.ID, reader, limit)
	if err != nil {
		return nil, err
	}
	if err = s.repo.ReadyAsset(ctx, a.UserID, a.ID, n, hash); err != nil {
		return nil, err
	}
	return s.repo.GetAsset(ctx, a.UserID, a.ID)
}
func (s *CreativeCanvasService) GetAsset(ctx context.Context, u int64, id string) (*CreativeCanvasAsset, error) {
	if err := s.enabled(ctx); err != nil {
		return nil, err
	}
	if !validCanvasID(id) {
		return nil, ErrCanvasNotFound
	}
	a, err := s.repo.GetAsset(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if !s.now().Before(a.ExpiresAt) {
		a.Status = "expired"
	}
	return a, nil
}
func (s *CreativeCanvasService) OpenAsset(ctx context.Context, u int64, id string) (*CreativeCanvasAsset, io.ReadSeekCloser, error) {
	a, err := s.GetAsset(ctx, u, id)
	if err != nil {
		return nil, nil, err
	}
	if !s.now().Before(a.ExpiresAt) || a.Status == "deleted" || a.Status == "purged" {
		return nil, nil, ErrCanvasExpired
	}
	if a.Status != "ready" {
		return nil, nil, ErrCanvasNotFound
	}
	reader, err := s.store.Open(ctx, a.ID)
	if err != nil {
		return nil, nil, ErrCanvasUnavailable
	}
	return a, reader, nil
}
func (s *CreativeCanvasService) ListAssets(ctx context.Context, u int64, canvasID string, limit, offset int) ([]*CreativeCanvasAsset, int64, error) {
	if _, err := s.Get(ctx, u, canvasID); err != nil {
		return nil, 0, err
	}
	limit, offset = canvasPage(limit, offset)
	items, total, err := s.repo.ListAssets(ctx, u, canvasID, "", limit, offset)
	for _, a := range items {
		if !s.now().Before(a.ExpiresAt) {
			a.Status = "expired"
		}
	}
	return items, total, err
}
func (s *CreativeCanvasService) DeleteAsset(ctx context.Context, u int64, id string) error {
	if _, err := s.GetAsset(ctx, u, id); err != nil {
		return err
	}
	return s.repo.DeleteAsset(ctx, u, id)
}

func (s *CreativeCanvasService) CreateExecution(ctx context.Context, u int64, canvasID, nodeID, kind string, params CreativeCanvasExecutionParams, idempotencyKey string) (*CreativeCanvasExecution, error) {
	e, err := s.prepareExecution(ctx, u, canvasID, nodeID, kind, params, idempotencyKey)
	if err != nil {
		return nil, err
	}
	e, err = s.repo.CreateExecution(ctx, e)
	if err != nil {
		return nil, err
	}
	return s.withExecutionAssets(ctx, e)
}

// prepareExecution 共用单任务和批次的所有权、节点与素材校验，准备过程不触发生成。
func (s *CreativeCanvasService) prepareExecution(ctx context.Context, u int64, canvasID, nodeID, kind string, params CreativeCanvasExecutionParams, idempotencyKey string) (*CreativeCanvasExecution, error) {
	if params.BatchID != "" || params.BatchIndex != 0 || params.BatchSize != 0 || params.BatchOutputCount != 0 {
		return nil, ErrCanvasInvalid
	}
	canvas, err := s.Get(ctx, u, canvasID)
	if err != nil {
		return nil, err
	}
	if kind != "image" && kind != "video" && kind != "audio" && kind != "text" || s.executor(kind) == nil {
		return nil, ErrCanvasInvalid
	}
	if kind != "audio" && (params.Voice != "" || params.AudioFormat != "" || params.Speed != 0) {
		return nil, ErrCanvasInvalid
	}
	if kind == "text" {
		if err := normalizeCreativeTextParams(&params); err != nil {
			return nil, err
		}
	} else if params.MaxOutputTokens != 0 {
		return nil, ErrCanvasInvalid
	}
	if len(nodeID) == 0 || len(nodeID) > 128 || params.GroupID <= 0 || strings.TrimSpace(params.Model) == "" || len(params.Model) > 256 || len(params.Prompt) > 64000 || len(params.References) > 16 {
		return nil, ErrCanvasInvalid
	}
	var doc struct {
		Nodes []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"nodes"`
	}
	if json.Unmarshal(canvas.Document, &doc) != nil {
		return nil, ErrCanvasInvalid
	}
	found := false
	for _, n := range doc.Nodes {
		if n.ID == nodeID && (n.Type == kind || n.Type == "config") {
			found = true
		}
	}
	if !found {
		return nil, ErrCanvasInvalid
	}
	ids := make([]string, 0, len(params.References)+1)
	for _, ref := range params.References {
		if !validCanvasID(ref.AssetID) || len(ref.Role) > 40 {
			return nil, ErrCanvasInvalid
		}
		ids = append(ids, ref.AssetID)
	}
	if params.MaskAssetID != "" {
		if !validCanvasID(params.MaskAssetID) {
			return nil, ErrCanvasInvalid
		}
		ids = append(ids, params.MaskAssetID)
	}
	if err = s.validateOwnedAssets(ctx, u, ids, true); err != nil {
		return nil, err
	}
	if kind == "audio" {
		// 纯参数错误在登记执行前拒绝，不创建永远不能发送的排队任务。
		audioParams, err := creativeCanvasImageParams(&CreativeCanvasExecution{Kind: kind, Params: params}, nil)
		if err != nil {
			return nil, err
		}
		if _, _, _, _, err := normalizeCreativeAudioParams(audioParams.Prompt, audioParams.Voice, audioParams.AudioFormat, audioParams.Speed); err != nil {
			return nil, err
		}
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return nil, ErrCanvasInvalid
	}
	fingerprintInput := struct {
		CanvasID, NodeID, Kind string
		Params                 CreativeCanvasExecutionParams
	}{canvasID, nodeID, kind, params}
	raw, err := json.Marshal(fingerprintInput)
	if err != nil {
		return nil, ErrCanvasInvalid
	}
	hash := sha256.Sum256(raw)
	e := &CreativeCanvasExecution{ID: uuid.NewString(), UserID: u, CanvasID: canvasID, NodeID: nodeID, Kind: kind, Params: params, IdempotencyKey: idempotencyKey, Fingerprint: hex.EncodeToString(hash[:]), Status: "queued"}
	return e, nil
}
func (s *CreativeCanvasService) withExecutionAssets(ctx context.Context, e *CreativeCanvasExecution) (*CreativeCanvasExecution, error) {
	assets, _, err := s.repo.ListAssets(ctx, e.UserID, e.CanvasID, e.ID, 100, 0)
	if err != nil {
		return nil, err
	}
	e.Assets = assets
	// 历史失败记录也按稳定错误码投影，避免余额不足被旧的通用文案误报为素材错误。
	if e.Status == "failed" {
		if message := creativeCanvasExecutionFailureMessage(e.Kind, e.ErrorCode); message != "" {
			e.ErrorMessage = message
		}
	}
	return e, nil
}

// creativeCanvasFailureMessage 只映射服务端已知错误码，不暴露上游响应、提示词或素材地址。
func creativeCanvasExecutionFailureMessage(kind, code string) string {
	if kind == "audio" {
		switch code {
		case "UPSTREAM_STATUS_404":
			return "语音服务端点不存在，请检查账号服务地址"
		case "CREATIVE_INVALID_MODEL":
			return "当前语音模型不可用，请重新选择管理员已开放的模型"
		case "CREATIVE_INVALID_PARAMS":
			return "语音参数不符合当前模型能力，请检查文字、声音、格式和语速"
		case "CREATIVE_OPERATION_UNSUPPORTED":
			return "当前语音模型未开放此生成方式"
		case "CREATIVE_GROUP_FORBIDDEN":
			return "当前分组没有使用权限，请更换可用分组"
		}
	}
	return creativeCanvasFailureMessage(code)
}

// creativeCanvasFailureMessage 保留既有图片和视频的安全错误文案。
func creativeCanvasFailureMessage(code string) string {
	switch code {
	case "PROVISIONING_INCOMPLETE":
		return "生成任务准备失败，请重新生成"
	case "CREATIVE_INSUFFICIENT_BALANCE", "INSUFFICIENT_BALANCE", "VIDEO_INSUFFICIENT_BALANCE":
		return "可用余额或订阅额度不足，无法预扣本次生成费用，请充值或更换可用分组后重试"
	case "CREATIVE_INVALID_MODEL":
		return "当前图片模型不可用，请重新选择管理员已开放的模型"
	case "CREATIVE_OPERATION_UNSUPPORTED":
		return "当前模型未开放此生成方式；使用参考图需要模型支持图生图"
	case "CREATIVE_GROUP_FORBIDDEN", "CREATIVE_GROUP_IMAGE_DISABLED":
		return "当前分组未开放图片生成或没有使用权限，请更换可用分组"
	case "CREATIVE_INVALID_PARAMS":
		return "生成参数不符合当前模型能力，请检查尺寸、比例、质量和参考图数量"
	case "VIDEO_INVALID_OPENAI_PARAMETERS", "VIDEO_PROTOCOL_PARAMETERS_INVALID", "VIDEO_METADATA_CONFLICT":
		return "视频端点不支持当前参数，请检查分辨率、比例、时长和参考图要求"
	case "VIDEO_PRICING_UNAVAILABLE":
		// 缺价由原任务服务在预扣前拒绝，应提示管理员补价而不是让用户反复调整参数。
		return "当前模型或分辨率未配置可用的视频价格，请联系管理员配置定价；本次未提交生成或预扣费用"
	case "CREATIVE_INVALID_MIME":
		return "参考图片格式不支持，请上传 PNG、JPEG 或 WebP 图片"
	case "CREATIVE_VIDEO_REFERENCE_TOO_LARGE":
		return "参考图超过本站 H3 单张 2 MiB 发送上限，请手动更换不超过 2 MiB 的图片；原图已保留，本次未提交生成或预扣费用"
	case "CREATIVE_ASSET_TOO_LARGE", "CREATIVE_INPUT_TOO_LARGE", "CANVAS_STORAGE_LIMIT":
		return "参考素材超过大小或存储限制，请缩小素材后重试"
	case "CANVAS_EXECUTION_LIMIT":
		return ErrCanvasExecutionLimit.Message
	case "CREATIVE_PROMPT_TOO_LONG":
		return "提示词过长，请缩短后重试"
	case "CREATIVE_MASK_REQUIRED", "CREATIVE_MASK_SIZE_MISMATCH":
		return "局部重绘需要与原图尺寸一致的 PNG 蒙版"
	case "CANVAS_ASSET_EXPIRED":
		return "参考素材已超过七天保留期限，请重新上传"
	case "CANVAS_NOT_FOUND":
		return "参考素材不存在或没有访问权限，请重新选择素材"
	case "CREATIVE_CONTENT_BLOCKED":
		return "请求未通过内容审核，请调整提示词或参考素材"
	case "CREATIVE_DISABLED":
		return "创作台已停用，请联系管理员"
	default:
		return ""
	}
}
func (s *CreativeCanvasService) GetExecution(ctx context.Context, u int64, id string) (*CreativeCanvasExecution, error) {
	if err := s.enabled(ctx); err != nil {
		return nil, err
	}
	if !validCanvasID(id) {
		return nil, ErrCanvasNotFound
	}
	e, err := s.repo.GetExecution(ctx, u, id)
	if err != nil {
		return nil, err
	}
	return s.withExecutionAssets(ctx, e)
}
func (s *CreativeCanvasService) ListExecutions(ctx context.Context, u int64, canvasID string, limit, offset int) ([]*CreativeCanvasExecution, int64, error) {
	if _, err := s.Get(ctx, u, canvasID); err != nil {
		return nil, 0, err
	}
	limit, offset = canvasPage(limit, offset)
	items, total, err := s.repo.ListExecutions(ctx, u, canvasID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	for _, e := range items {
		if _, err = s.withExecutionAssets(ctx, e); err != nil {
			return nil, 0, err
		}
	}
	return items, total, nil
}

func (s *CreativeCanvasService) Start() {
	s.start.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.mu.Lock()
		s.cancel = cancel
		s.mu.Unlock()
		go s.run(ctx)
	})
}
func (s *CreativeCanvasService) Stop() {
	s.stop.Do(func() {
		s.mu.RLock()
		cancel := s.cancel
		s.mu.RUnlock()
		if cancel != nil {
			cancel()
			<-s.done
		}
	})
}
func (s *CreativeCanvasService) run(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	lastCleanup := time.Time{}
	for {
		if s.repo != nil && s.store != nil {
			if s.now().Sub(lastCleanup) >= 7*24*time.Hour {
				if err := s.CleanupExpired(ctx); err == nil {
					lastCleanup = s.now()
				}
			}
			s.Reconcile(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// 到期立即不可读；启动补偿和每周批处理只删除本命名空间已登记的过期素材。
func (s *CreativeCanvasService) CleanupExpired(ctx context.Context) error {
	for {
		assets, err := s.repo.ExpiredAssets(ctx, s.now(), 100)
		if err != nil {
			return err
		}
		if len(assets) == 0 {
			return nil
		}
		for _, a := range assets {
			if err = s.store.Delete(ctx, a.ID); err != nil {
				return err
			}
			if err = s.repo.MarkAssetPurged(ctx, a.ID); err != nil {
				return err
			}
		}
	}
}
func (s *CreativeCanvasService) Reconcile(ctx context.Context) {
	jobs, err := s.repo.ClaimExecutions(ctx, 4, 7*time.Minute)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("canvas claim failed", "error", err)
		}
		return
	}
	var wg sync.WaitGroup
	for _, e := range jobs {
		wg.Add(1)
		go func(e *CreativeCanvasExecution) {
			defer wg.Done()
			work, cancel := context.WithTimeout(ctx, 6*time.Minute)
			defer cancel()
			s.reconcileOne(work, e)
		}(e)
	}
	wg.Wait()
}
func (s *CreativeCanvasService) loadInputs(ctx context.Context, e *CreativeCanvasExecution) ([]CreativeCanvasInputAsset, error) {
	refs := append([]CreativeCanvasReference(nil), e.Params.References...)
	if e.Params.MaskAssetID != "" {
		refs = append(refs, CreativeCanvasReference{AssetID: e.Params.MaskAssetID, Role: "mask"})
	}
	out := make([]CreativeCanvasInputAsset, 0, len(refs))
	var total int64
	for _, ref := range refs {
		a, err := s.repo.GetAsset(ctx, e.UserID, ref.AssetID)
		if err != nil {
			return nil, err
		}
		if a.Status != "ready" || !s.now().Before(a.ExpiresAt) {
			return nil, ErrCanvasExpired
		}
		if a.ByteSize > CreativeCanvasUploadLimit-total {
			return nil, ErrCanvasQuota
		}
		r, err := s.store.Open(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(r, a.ByteSize+1))
		r.Close()
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(data)) != a.ByteSize {
			return nil, ErrCanvasInvalid
		}
		total += a.ByteSize
		out = append(out, CreativeCanvasInputAsset{Asset: a, Role: ref.Role, Content: data})
	}
	return out, nil
}
func (s *CreativeCanvasService) saveExecution(ctx context.Context, e *CreativeCanvasExecution, delay time.Duration) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.repo.SaveExecution(cleanup, e, s.now().Add(delay)); err != nil {
		slog.Warn("canvas execution save pending", "execution_id", e.ID, "error", err)
	}
}
func (s *CreativeCanvasService) reconcileOne(ctx context.Context, e *CreativeCanvasExecution) {
	executor := s.executor(e.Kind)
	if executor == nil {
		s.saveExecution(ctx, e, 30*time.Second)
		return
	}
	if e.TaskID == "" {
		if recoverer, ok := executor.(CreativeCanvasExecutorRecoverer); ok {
			id, err := recoverer.Recover(ctx, e)
			if err != nil {
				s.saveExecution(ctx, e, 10*time.Second)
				return
			}
			if id != "" {
				e.TaskID, e.Status = id, "running"
				s.saveExecution(ctx, e, time.Second)
				return
			}
		}
		assets, err := s.loadInputs(ctx, e)
		if err == nil {
			e.TaskID, err = executor.Submit(ctx, e, assets)
		}
		if err != nil {
			status := infraerrors.Code(err)
			if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
				e.Status = "failed"
				e.ErrorCode = infraerrors.Reason(err)
				e.ErrorMessage = creativeCanvasExecutionFailureMessage(e.Kind, e.ErrorCode)
				if e.ErrorMessage == "" {
					e.ErrorMessage = "生成请求未通过校验，请检查模型配置或联系管理员"
				}
			} else {
				e.ErrorMessage = "任务正在恢复，请勿重复生成"
			}
			s.saveExecution(ctx, e, 10*time.Second)
			return
		}
		if e.TaskID == "" {
			s.saveExecution(ctx, e, 10*time.Second)
			return
		}
		e.Status = "running"
		s.saveExecution(ctx, e, time.Second)
		return
	}
	result, err := executor.Poll(ctx, e)
	if err != nil {
		// 已知核对提示应跨临时查询故障保留，避免重新显示成普通生成中。
		if (!strings.HasPrefix(e.ErrorCode, "CREATIVE_VIDEO_") && !strings.HasPrefix(e.ErrorCode, "CREATIVE_AUDIO_") && !strings.HasPrefix(e.ErrorCode, "CREATIVE_TEXT_")) || e.ErrorMessage == "" {
			e.ErrorMessage = "任务状态或素材正在恢复"
		}
		s.saveExecution(ctx, e, 10*time.Second)
		return
	}
	if result == nil {
		s.saveExecution(ctx, e, 5*time.Second)
		return
	}
	defer func() {
		for _, o := range result.Outputs {
			if o.Reader != nil {
				o.Reader.Close()
			}
		}
	}()
	if result.Status != "succeeded" {
		if result.Status == "failed" || result.Status == "result_lost" {
			e.Status = result.Status
			e.ErrorCode = result.ErrorCode
			e.ErrorMessage = result.ErrorMessage
		} else {
			e.Status = "running"
			// 非终态的安全提示同样持久化；下一次正常进度会清掉旧核对提示。
			e.ErrorCode = result.ErrorCode
			e.ErrorMessage = result.ErrorMessage
		}
		s.saveExecution(ctx, e, 5*time.Second)
		return
	}
	// 进入成品归档后不再显示受理/结算核对，存储失败只继续原归档重试。
	e.ErrorCode = ""
	e.ErrorMessage = ""
	if e.Kind == "text" {
		if strings.TrimSpace(result.OutputText) == "" {
			s.saveExecution(ctx, e, 10*time.Second)
			return
		}
		e.Status = "succeeded"
		e.OutputText = result.OutputText
		s.saveExecution(ctx, e, 0)
		return
	}
	if len(result.Outputs) == 0 || len(result.Outputs) > 4 {
		e.ErrorMessage = "成品尚未可归档"
		s.saveExecution(ctx, e, 10*time.Second)
		return
	}
	for _, output := range result.Outputs {
		if err = s.archiveOutput(ctx, e, output); err != nil {
			e.ErrorMessage = "成品正在安全保存，请勿重新生成"
			s.saveExecution(ctx, e, 5*time.Second)
			return
		}
		if ack, ok := executor.(CreativeCanvasExecutorAcknowledger); ok {
			if err = ack.Ack(ctx, e, output.Index); err != nil {
				s.saveExecution(ctx, e, 5*time.Second)
				return
			}
		}
	}
	e.Status = "succeeded"
	e.ErrorCode = ""
	e.ErrorMessage = ""
	s.saveExecution(ctx, e, 0)
}
func (s *CreativeCanvasService) archiveOutput(ctx context.Context, e *CreativeCanvasExecution, o CreativeCanvasOutput) error {
	id := creativeCanvasAssetID(e.ID, o.Index)
	old, err := s.repo.GetAsset(ctx, e.UserID, id)
	if err == nil && old.Status == "ready" {
		return nil
	}
	if err != nil && !errors.Is(err, ErrCanvasNotFound) {
		return err
	}
	if o.Reader == nil {
		return ErrCanvasExpired
	}
	prefix := make([]byte, 512)
	n, err := io.ReadFull(o.Reader, prefix)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	prefix = prefix[:n]
	kind, mime, err := CreativeCanvasMediaType(prefix)
	if err != nil || kind != e.Kind {
		return ErrCanvasInvalid
	}
	limit := CreativeCanvasOutputLimit
	if e.Kind == "image" {
		limit = 32 << 20
	}
	if o.Size > limit {
		return ErrCanvasQuota
	}
	reserved := o.Size
	if reserved <= 0 {
		reserved = limit
	}
	a := &CreativeCanvasAsset{ID: id, UserID: e.UserID, CanvasID: e.CanvasID, ExecutionID: e.ID, OutputIndex: o.Index, MediaType: kind, MIMEType: mime, ByteSize: reserved, ExpiresAt: s.now().Add(CreativeCanvasAssetTTL)}
	a, err = s.repo.ReserveAsset(ctx, a, CreativeCanvasUserQuota)
	if err != nil {
		return err
	}
	_, err = s.writeAsset(ctx, a, io.MultiReader(bytes.NewReader(prefix), o.Reader), reserved)
	return err
}

// PersistCreativeOutput 是 worker 结果钩子：先把已确认产物写入云资产，再允许 Redis 临时结果过期。
// 只有能按 run ID 找到画布 execution 才处理，普通创作台任务保持原无留存路径。
func (s *CreativeCanvasService) PersistCreativeOutput(ctx context.Context, runID string, index int, data []byte, mime string) error {
	if len(data) == 0 || s.repo == nil || s.store == nil {
		return nil
	}
	// 仓储通过 execution 的稳定关联查找，未关联的普通任务不改变原行为。
	e, err := s.repo.FindExecutionByTaskID(ctx, runID)
	if errors.Is(err, ErrCanvasNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if e == nil || e.Kind == "" {
		return nil
	}
	result := CreativeCanvasOutput{Index: index, MIMEType: mime, Size: int64(len(data)), Reader: io.NopCloser(bytes.NewReader(data))}
	return s.archiveOutput(ctx, e, result)
}
