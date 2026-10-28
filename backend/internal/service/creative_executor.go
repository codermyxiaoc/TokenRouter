package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
)

// 创作台执行器常量。
const (
	defaultCreativeExecuteTimeout = 5 * time.Minute
	defaultCreativeMaxAttempts    = 3
	// creativeMaxOutputBytes 限制单张输出图片大小（32 MiB）。
	creativeMaxOutputBytes = 32 << 20
	// creativeMidjourneyOutputCount 是 MJ 每次调用固定的四张产物，也是预占数量合同。
	creativeMidjourneyOutputCount = 4
)

// CreativeUpstreamError 是执行器向上抛出的上游调用错误，携带可重试判定。
type CreativeUpstreamError struct {
	StatusCode int
	Message    string
	Retryable  bool
	// Code 仅保存本站定义的错误类别；不影响原有 HTTP 重试策略。
	Code string
}

// CreativeProviderResultUnconfirmedError 表示上游可能已经生成，但尚未取得可交付产物。
// 此类错误既不能重发生成，也不能推断失败并退款；具体响应和素材地址不写入错误文本。
type CreativeProviderResultUnconfirmedError struct{}

func (*CreativeProviderResultUnconfirmedError) Error() string {
	return "creative provider result is unconfirmed"
}

// creativeResultUnconfirmedError 为产物解析、下载等已受理后的不确定结果生成安全错误。
func creativeResultUnconfirmedError() error {
	return &CreativeProviderResultUnconfirmedError{}
}

func isCreativeResultUnconfirmedError(err error) bool {
	var unconfirmed *CreativeProviderResultUnconfirmedError
	return errors.As(err, &unconfirmed)
}

func (e *CreativeUpstreamError) Error() string {
	if e == nil {
		return "creative upstream error"
	}
	return fmt.Sprintf("creative upstream error status=%d: %s", e.StatusCode, e.Message)
}

// creativeNonRetryableError 生成不可重试的执行错误（如平台不支持该操作）。
func creativeNonRetryableError(format string, args ...any) *CreativeUpstreamError {
	return &CreativeUpstreamError{StatusCode: 0, Message: fmt.Sprintf(format, args...), Retryable: false}
}

// creativeHTTPStatusError 把上游 HTTP 状态码映射为可重试性：网络层错误、429 与 5xx 可重试，其余 4xx 不可重试。
func creativeHTTPStatusError(statusCode int, message string) *CreativeUpstreamError {
	retryable := statusCode == 0 || statusCode == http.StatusTooManyRequests || statusCode >= 500
	// 上游 body 可能回显 prompt、内部 URL 或认证信息；对外只保留稳定的状态类消息。
	publicMessage := "creative provider request failed"
	switch {
	case statusCode == 0:
		publicMessage = "creative provider connection failed"
	case statusCode == http.StatusTooManyRequests:
		publicMessage = "creative provider rate limited"
	case statusCode >= 500:
		publicMessage = "creative provider unavailable"
	case statusCode >= 400:
		publicMessage = "creative provider rejected request"
	}
	code := creativeProviderFailureCode(statusCode, message)
	if reason := creativeProviderFailureMessage(code); reason != "" {
		publicMessage = reason
	}
	return &CreativeUpstreamError{StatusCode: statusCode, Message: publicMessage, Retryable: retryable, Code: code}
}

// IsRetryableCreativeError 判断错误是否值得 worker 有限重试。
func IsRetryableCreativeError(err error) bool {
	if decision, ok := ImageAttemptDecisionForError(err); ok {
		return ImageAttemptCanSwitch(decision)
	}
	if isCreativeResultUnconfirmedError(err) {
		return false
	}
	var upstreamErr *CreativeUpstreamError
	if errors.As(err, &upstreamErr) {
		return upstreamErr.Retryable
	}
	// 未知错误（网络层、序列化等）保守视为可重试，由最大次数兜底。
	return err != nil
}

// CreativeExecutor 是创作台的 provider 执行器：按分组平台直接构造上游 HTTP 请求，
// 绝不通过本地 HTTP 回环调用网关。
type CreativeExecutor struct {
	cfg            *config.Config
	accountRepo    CreativeAccountRepository
	groupRepo      CreativeGroupRepository
	gateway        *OpenAIGatewayService
	gatewayService *GatewayService
	geminiTokens   *GeminiTokenProvider
	settingService *SettingService
	// 产物下载与生成传输隔离，测试可注入下载客户端，生产始终使用公网安全下载器。
	imageDownloader *ImageResultUploader
}

// NewCreativeExecutor 创建创作台执行器。
func NewCreativeExecutor(
	cfg *config.Config,
	accountRepo CreativeAccountRepository,
	groupRepo CreativeGroupRepository,
	gateway *OpenAIGatewayService,
	gatewayService *GatewayService,
	geminiTokens *GeminiTokenProvider,
	settingService *SettingService,
) *CreativeExecutor {
	return &CreativeExecutor{
		cfg:            cfg,
		accountRepo:    accountRepo,
		groupRepo:      groupRepo,
		gateway:        gateway,
		gatewayService: gatewayService,
		geminiTokens:   geminiTokens,
		settingService: settingService,
	}
}

// Prepare 复用现有网关调度器选择账号并预占账号并发槽位。
func (e *CreativeExecutor) Prepare(ctx context.Context, run CreativeRun) (*CreativeExecution, error) {
	if run.Operation == CreativeOperationTTS {
		return e.prepareCreativeCandidate(ctx, run, nil)
	}
	if len(run.ImageAttemptAccountIDs) >= ImageAttemptMaxSends || run.AttemptCount >= ImageAttemptMaxSends {
		return nil, ErrCreativeImageBudgetExhausted
	}
	if run.ImageExecutionDeadlineAt != nil && !time.Now().Before(*run.ImageExecutionDeadlineAt) {
		return nil, ErrCreativeImageBudgetExhausted
	}
	excluded := make(map[int64]struct{}, len(run.ImageAttemptAccountIDs))
	for _, id := range run.ImageAttemptAccountIDs {
		excluded[id] = struct{}{}
	}
	// 冷却候选只跳过选号，不消耗发送次数；单轮选号有界，暂时无槽位由队列承担等待。
	selectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sawCooling := false
	for {
		execution, err := e.prepareCreativeCandidate(selectCtx, run, excluded)
		if err != nil {
			if selectCtx.Err() != nil && ctx.Err() == nil {
				return nil, ErrCreativeExecutionPending
			}
			if errors.Is(err, ErrNoAvailableAccounts) && len(run.ImageAttemptAccountIDs) > 0 && !sawCooling {
				return nil, ErrCreativeImageBudgetExhausted
			}
			return nil, err
		}
		id := execution.Account.ID
		if _, repeated := excluded[id]; repeated {
			if execution.ReleaseFunc != nil {
				execution.ReleaseFunc()
			}
			return nil, ErrCreativeExecutionPending
		}
		remaining := time.Duration(0)
		if execution.Account.Platform == PlatformGemini && e.gatewayService != nil {
			remaining = e.gatewayService.ImageModelCooldownRemaining(selectCtx, id, execution.UpstreamModel, run.Operation)
		} else if e.gateway != nil {
			remaining = e.gateway.ImageModelCooldownRemaining(selectCtx, id, execution.UpstreamModel, run.Operation)
		}
		if remaining <= 0 {
			return execution, nil
		}
		if execution.ReleaseFunc != nil {
			execution.ReleaseFunc()
		}
		excluded[id] = struct{}{}
		sawCooling = true
		if selectCtx.Err() != nil {
			return nil, ErrCreativeExecutionPending
		}
	}
}

// prepareCreativeCandidate 保留原平台准入，只将图片任务的持久失败集合传给现有调度器。
func (e *CreativeExecutor) prepareCreativeCandidate(ctx context.Context, run CreativeRun, excluded map[int64]struct{}) (*CreativeExecution, error) {
	if e == nil {
		return nil, errors.New("creative executor is not configured")
	}
	platform, err := e.resolveGroupPlatform(ctx, run.GroupID)
	if err != nil {
		return nil, err
	}
	if run.Operation == CreativeOperationTTS && (platform != PlatformGrok || run.Model != CreativeGrokTTSModel || run.RequestedOutputCount != 1) {
		return nil, creativeNonRetryableError("unsupported creative audio group")
	}
	groupID := run.GroupID
	var selection *AccountSelectionResult
	switch platform {
	case PlatformOpenAI:
		if e.gateway == nil {
			return nil, errors.New("creative OpenAI gateway is not configured")
		}
		selection, err = e.prepareOpenAIImageSelection(ctx, groupID, run.Model, excluded)
	case PlatformGrok:
		if e.gateway == nil {
			return nil, errors.New("creative OpenAI gateway is not configured")
		}
		selectionModel, capability := run.Model, OpenAIEndpointCapabilityGrokMediaGeneration
		if run.Operation == CreativeOperationTTS {
			// Voice 不借用图片付费资格，沿用正式语音端点的文本账号准入。
			selectionModel, capability = CreativeGrokTTSSelectionModel, OpenAIEndpointCapabilityTextGeneration
		}
		selection, _, err = e.gateway.SelectAccountWithSchedulerForCapability(
			ctx,
			&groupID,
			"",
			"",
			selectionModel,
			excluded,
			OpenAIUpstreamTransportHTTPSSE,
			capability,
			false,
			false,
			PlatformGrok,
		)
	case PlatformGemini:
		if e.gatewayService == nil {
			return nil, errors.New("creative gateway service is not configured")
		}
		selection, err = e.gatewayService.SelectAccountWithLoadAwareness(ctx, &groupID, "", run.Model, excluded, "", 0)
	default:
		return nil, creativeNonRetryableError("creative executor unsupported account platform %s", platform)
	}
	if err != nil {
		return nil, err
	}
	if selection == nil || selection.Account == nil {
		return nil, creativeNonRetryableError("no compatible creative account available for group %d model %s", run.GroupID, run.Model)
	}
	if !selection.Acquired {
		// 异步队列自身承担等待职责，不能阻塞 worker 等待账号槽位。
		if selection.WaitPlan != nil {
			return nil, ErrCreativeExecutionPending
		}
		return nil, creativeNonRetryableError("creative account %d was not admitted", selection.Account.ID)
	}
	upstreamModel := CreativeGrokTTSModel
	if run.Operation != CreativeOperationTTS {
		upstreamModel = strings.TrimSpace(resolveAccountUpstreamModel(ctx, selection.Account, run.Model))
	}
	if upstreamModel == "" {
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return nil, creativeNonRetryableError("creative account %d has no upstream model for %s", selection.Account.ID, run.Model)
	}
	// 账号/渠道映射后必须再次校验最终模型能力，避免文本模型别名被错误送入图片执行器。
	if run.Operation == CreativeOperationTTS && platform == PlatformGrok && run.Model == CreativeGrokTTSModel {
		upstreamModel = CreativeGrokTTSModel
	} else if !creativePlatformImageModel(platform, upstreamModel) {
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return nil, creativeNonRetryableError("creative mapped model %s is not an image model", upstreamModel)
	}
	if IsMidjourneyImageGenerationModel(upstreamModel) && selection.Account.Type != AccountTypeAPIKey {
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return nil, creativeNonRetryableError("creative Midjourney requires an API key account")
	}
	return &CreativeExecution{
		Account:       selection.Account,
		UpstreamModel: upstreamModel,
		Selection:     selection,
		ReleaseFunc:   selection.ReleaseFunc,
	}, nil
}

// prepareOpenAIImageSelection 只把 Midjourney 交给 API Key 账号，不改变 GPT Image 的原生能力。
// 请求别名可能在账号层才映射到 MJ；若首次选中 OAuth，先释放槽位，再按 API Key 能力选一次。
func (e *CreativeExecutor) prepareOpenAIImageSelection(ctx context.Context, groupID int64, model string, exclusions ...map[int64]struct{}) (*AccountSelectionResult, error) {
	var excluded map[int64]struct{}
	if len(exclusions) > 0 {
		excluded = exclusions[0]
	}
	capability := OpenAIImagesCapabilityNative
	if IsMidjourneyImageGenerationModel(model) || IsMidjourneyImageGenerationModel(e.gateway.resolveChannelRoutingModel(ctx, &groupID, model)) {
		capability = OpenAIImagesCapabilityAPIKey
	}
	for range 2 {
		selection, _, err := e.gateway.SelectAccountWithSchedulerForImages(ctx, &groupID, "", model, excluded, capability)
		if err != nil || selection == nil || selection.Account == nil {
			return selection, err
		}
		upstreamModel := resolveAccountUpstreamModel(ctx, selection.Account, model)
		if !IsMidjourneyImageGenerationModel(upstreamModel) || selection.Account.Type == AccountTypeAPIKey {
			return selection, nil
		}
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		if capability == OpenAIImagesCapabilityAPIKey {
			return nil, creativeNonRetryableError("creative Midjourney requires an API key account")
		}
		capability = OpenAIImagesCapabilityAPIKey
	}
	return nil, creativeNonRetryableError("no compatible creative Midjourney account available")
}

func creativePlatformImageModel(platform, model string) bool {
	switch platform {
	case PlatformOpenAI:
		return IsGPTImageGenerationModel(model) || IsMidjourneyImageGenerationModel(model)
	case PlatformGrok:
		return isGrokImageGenerationModel(model)
	case PlatformGemini:
		return isCreativeGeminiImageModel(model)
	default:
		return false
	}
}

// Execute 使用 Prepare 返回的账号上下文调用上游。
func (e *CreativeExecutor) Execute(ctx context.Context, run CreativeRun, payload CreativeRunPayload, execution *CreativeExecution) (*CreativeExecuteResult, error) {
	if e == nil {
		if run.Operation != CreativeOperationTTS {
			return nil, creativeImageLocalFailure(errors.New("creative executor is not configured"))
		}
		return nil, errors.New("creative executor is not configured")
	}
	if execution == nil || execution.Account == nil {
		if run.Operation != CreativeOperationTTS {
			return nil, creativeImageLocalFailure(errors.New("creative execution context is not configured"))
		}
		return nil, errors.New("creative execution context is not configured")
	}
	account := execution.Account
	upstreamModel := strings.TrimSpace(execution.UpstreamModel)
	if upstreamModel == "" {
		if run.Operation != CreativeOperationTTS {
			return nil, creativeImageLocalFailure(errors.New("creative execution upstream model is not configured"))
		}
		return nil, errors.New("creative execution upstream model is not configured")
	}
	if run.Operation == CreativeOperationTTS {
		if account.Platform != PlatformGrok || run.Model != CreativeGrokTTSModel || run.RequestedOutputCount != 1 || upstreamModel != CreativeGrokTTSModel {
			return nil, creativeNonRetryableError("unsupported creative audio reservation")
		}
		execCtx, cancel := context.WithTimeout(ctx, e.executeTimeout())
		defer cancel()
		outputs, err := e.executeGrokAudio(execCtx, run, payload, account)
		e.reportScheduleResult(execution, account.ID, err == nil)
		if err != nil {
			return nil, err
		}
		return &CreativeExecuteResult{Outputs: outputs, AccountID: account.ID}, nil
	}
	if IsMidjourneyImageGenerationModel(upstreamModel) && (account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey) {
		return nil, creativeImageLocalFailure(creativeNonRetryableError("creative Midjourney requires an OpenAI API key account"))
	}
	if IsMidjourneyImageGenerationModel(upstreamModel) {
		// 排队后账号映射可能变化；只有持久预占明确覆盖四张才允许调用 MJ。
		if run.RequestedOutputCount != creativeMidjourneyOutputCount {
			return nil, creativeImageLocalFailure(creativeNonRetryableError("creative Midjourney requires a four-image reservation"))
		}
	} else {
		if run.RequestedOutputCount == creativeMidjourneyOutputCount {
			// 四张报价属于 MJ 合同，执行时映射成其它模型也不能沿用该报价生成。
			return nil, creativeImageLocalFailure(creativeNonRetryableError("creative four-image reservation requires a Midjourney model"))
		}
		// 其它模型保持原有单张输出合同，不受 MJ 固定四张影响。
		run.RequestedOutputCount = 1
	}
	deadline := time.Now().Add(e.executeTimeout())
	if run.ImageExecutionDeadlineAt != nil {
		deadline = *run.ImageExecutionDeadlineAt
	}
	execCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	execCtx, policyCancel := WithImageAttemptPolicy(execCtx, time.Until(deadline))
	defer policyCancel()

	var outputs []CreativeOutput
	var err error
	switch account.Platform {
	case PlatformOpenAI:
		outputs, err = e.executeOpenAI(execCtx, run, payload, account, upstreamModel)
	case PlatformGrok:
		outputs, err = e.executeGrok(execCtx, run, payload, account, upstreamModel)
	case PlatformGemini:
		outputs, err = e.executeGemini(execCtx, run, payload, account, upstreamModel)
	default:
		return nil, creativeImageLocalFailure(creativeNonRetryableError("creative executor unsupported account platform %s", account.Platform))
	}
	if err != nil {
		// 构造请求阶段的错误没有生成副作用；发送后的无分类错误一律按未知处理。
		if _, classified := ImageAttemptDecisionForError(err); !classified {
			decision := ImageAttemptDecision{Outcome: ImageAttemptUnknown, Reason: "image_result_unconfirmed"}
			if ImageAttemptSendCount(execCtx) == 0 {
				decision = ImageAttemptDecision{Outcome: ImageAttemptNotSent, Reason: "image_request_not_sent"}
			}
			err = WrapImageAttemptError(err, decision)
		}
		e.recordCreativeImageFailure(execCtx, run, execution, err)
		return nil, err
	}
	outputs, err = normalizeCreativeOutputsForModel(outputs, upstreamModel)
	if err != nil {
		if IsMidjourneyImageGenerationModel(upstreamModel) {
			// MJ 已返回生成结果，输出校验失败不能触发再次付费生成或无依据释放预占。
			return nil, creativeResultUnconfirmedError()
		}
		return nil, WrapImageAttemptError(err, ImageAttemptDecision{Outcome: ImageAttemptAccepted, Reason: "image_output_validation_failed"})
	}
	e.reportScheduleResult(execution, account.ID, true)
	return &CreativeExecuteResult{
		Outputs:   outputs,
		AccountID: account.ID,
	}, nil
}

// IsRetryable 实现 CreativeRunExecutor 接口。
func (e *CreativeExecutor) IsRetryable(err error) bool {
	return IsRetryableCreativeError(err)
}

// reportScheduleResult 将创作台执行结果反馈给实际使用的调度器。
func (e *CreativeExecutor) reportScheduleResult(execution *CreativeExecution, accountID int64, success bool) {
	if e == nil || execution == nil || execution.Selection == nil || accountID <= 0 {
		return
	}
	switch execution.Account.Platform {
	case PlatformOpenAI, PlatformGrok:
		if e.gateway != nil {
			e.gateway.ReportOpenAIAccountScheduleResultForSelection(execution.Selection, accountID, execution.UpstreamModel, success, nil)
		}
	case PlatformGemini:
		if e.gatewayService != nil {
			e.gatewayService.ReportAdvancedAccountScheduleResult(execution.Selection, accountID, success, nil)
		}
	}
}

// resolveGroupPlatform 读取分组平台；平台决定执行协议分派。
func (e *CreativeExecutor) resolveGroupPlatform(ctx context.Context, groupID int64) (string, error) {
	if e.groupRepo == nil {
		return "", errors.New("creative group repository is not configured")
	}
	group, err := e.groupRepo.GetByIDLite(ctx, groupID)
	if err != nil || group == nil {
		return "", creativeNonRetryableError("creative group %d is unavailable", groupID)
	}
	switch group.Platform {
	case PlatformOpenAI, PlatformGrok, PlatformGemini:
		return group.Platform, nil
	default:
		return "", creativeNonRetryableError("creative group platform %s is not supported", group.Platform)
	}
}

func (e *CreativeExecutor) executeTimeout() time.Duration {
	if e != nil && e.cfg != nil && e.cfg.Creative.ExecuteTimeoutSeconds > 0 {
		return time.Duration(e.cfg.Creative.ExecuteTimeoutSeconds) * time.Second
	}
	return defaultCreativeExecuteTimeout
}

// accountProxyURL 返回账号绑定的代理地址（无代理时为空串）。
func accountProxyURL(account *Account) string {
	if account == nil || account.ProxyID == nil || account.Proxy == nil {
		return ""
	}
	return account.Proxy.URL()
}

// readCreativeUpstreamBody 读取上游响应体，限制最大读取量，避免异常响应撑爆内存。
func readCreativeUpstreamBody(body io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = 64 << 20
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if int64(len(data)) > limit {
		// 不能把恰好截断后的 JSON 或 SSE 当作完整成功响应。
		return nil, creativeResultUnconfirmedError()
	}
	return data, err
}

// normalizeCreativeOutputs 保持旧执行器单张输出行为。
func normalizeCreativeOutputs(outputs []CreativeOutput) ([]CreativeOutput, error) {
	return normalizeCreativeOutputsForModel(outputs, "")
}

// normalizeCreativeOutputsForModel 校验输出大小、去重并保留模型允许的产物数量。
// MJ 最多交付四张并按有效去重数量计费，其它模型继续固定交付一张。
func normalizeCreativeOutputsForModel(outputs []CreativeOutput, model string) ([]CreativeOutput, error) {
	if len(outputs) == 0 {
		return nil, creativeNonRetryableError("provider returned no image output")
	}
	maxOutputs := 1
	if IsMidjourneyImageGenerationModel(model) {
		maxOutputs = creativeMidjourneyOutputCount
		if len(outputs) > maxOutputs {
			return nil, creativeResultUnconfirmedError()
		}
	}
	seen := make(map[string]struct{}, len(outputs))
	out := make([]CreativeOutput, 0, len(outputs))
	for _, output := range outputs {
		if len(output.Bytes) == 0 {
			continue
		}
		if len(output.Bytes) > creativeMaxOutputBytes {
			return nil, creativeNonRetryableError("creative output %d exceeds size limit %d bytes", output.Index, creativeMaxOutputBytes)
		}
		sum := sha256Hex(output.Bytes)
		if _, ok := seen[sum]; ok {
			continue
		}
		seen[sum] = struct{}{}
		out = append(out, output)
	}
	if len(out) == 0 {
		return nil, creativeNonRetryableError("provider returned no usable image output")
	}
	if len(out) > maxOutputs {
		out = out[:maxOutputs]
	}
	for index := range out {
		out[index].Index = index
	}
	return out, nil
}

// creativeOpenAIImageSize 把创作台尺寸档位映射为 OpenAI images 协议支持的像素尺寸。
// 4K 档位遵守 GPT Image 2 的 3840 最大边长和约 8.3MP 总像素上限。
func creativeOpenAIImageSize(imageSize, aspectRatio string) string {
	tier := NormalizeImageBillingTierOrDefault(imageSize)
	ratio := strings.TrimSpace(aspectRatio)
	if tier == ImageBillingSize4K {
		switch ratio {
		case "16:9":
			return "3840x2160"
		case "9:16":
			return "2160x3840"
		case "4:3":
			return "3264x2448"
		case "3:4":
			return "2448x3264"
		default:
			return "2880x2880"
		}
	}
	base := 1024
	if tier != ImageBillingSize1K {
		base = 1536
	}
	switch ratio {
	case "16:9", "3:2", "4:3", "2:1", "19.5:9", "20:9":
		return fmt.Sprintf("%dx%d", base+base/2, base)
	case "9:16", "3:4", "2:3", "1:2", "9:19.5", "9:20":
		return fmt.Sprintf("%dx%d", base, base+base/2)
	default:
		return fmt.Sprintf("%dx%d", base, base)
	}
}

// creativeGrokImageResolution 把尺寸档位映射为 grok imagine 的 resolution（1k/2k）。
func creativeGrokImageResolution(imageSize string) string {
	if NormalizeImageBillingTierOrDefault(imageSize) == ImageBillingSize1K {
		return "1k"
	}
	return "2k"
}

// creativeGrokAspectRatio 校验并返回 grok imagine 支持的 aspect_ratio，不支持时返回空串（上游取默认）。
func creativeGrokAspectRatio(aspectRatio string) string {
	aspectRatio = strings.TrimSpace(aspectRatio)
	if aspectRatio == "" {
		return ""
	}
	if aspectRatio == "auto" {
		return aspectRatio
	}
	for _, candidate := range grokImagineAspectRatioValues {
		if candidate.label == aspectRatio {
			return aspectRatio
		}
	}
	return ""
}

// decodedCreativeImage 是 base64 解码后的图片字节与嗅探出的 MIME。
type decodedCreativeImage struct {
	Bytes []byte
	Mime  string
}

// decodeBase64Image 解码上游返回的 base64 图片并按魔数嗅探 MIME。
func decodeBase64Image(raw string) (decodedCreativeImage, error) {
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// 部分上游会省略 padding，按 RawStdEncoding 再试一次。
		data, err = base64.RawStdEncoding.DecodeString(raw)
		if err != nil {
			return decodedCreativeImage{}, err
		}
	}
	mime := sniffCreativeImageMime(data)
	if mime == "" {
		mime = "image/png"
	}
	return decodedCreativeImage{Bytes: data, Mime: mime}, nil
}

// creativeFileExtension 返回 MIME 对应的文件扩展名（用于 multipart 文件名）。
func creativeFileExtension(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	default:
		return "png"
	}
}
