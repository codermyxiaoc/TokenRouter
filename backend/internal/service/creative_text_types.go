package service

import "context"

const CreativeTextOutputLimit = 4 << 20

// CreativeTextGateway 由 handler 层接回现有网关，service 不反向依赖 Gin 处理器。
type CreativeTextGateway interface {
	Generate(context.Context, *APIKey, []byte) (CreativeTextGatewayResponse, error)
}
type CreativeTextGatewayResponse struct {
	StatusCode int
	Body       []byte
}

// 文本证据只保存冻结的账务字段和去除实体关联的 usage，不保存密钥和请求正文。
type CreativeTextBillingEvidence struct {
	Version int                 `json:"version"`
	Command UsageBillingCommand `json:"command"`
	Usage   UsageLog            `json:"usage"`
}

type CreativeTextExecutionState struct {
	Phase      string
	HTTPStatus int
	OutputText string
	Evidence   *CreativeTextBillingEvidence
	Receipt    *UsageBillingApplyResult
}

// 文本扩展与原素材仓储分开，旧图片/视频/音频无需实现额外执行语义。
type CreativeTextRepository interface {
	BeginTextExecution(context.Context, *CreativeCanvasExecution, string) (bool, error)
	GetTextExecutionState(context.Context, int64, string) (*CreativeTextExecutionState, error)
	SaveTextResponse(context.Context, int64, string, int, string) error
	SaveTextBillingEvidence(context.Context, int64, string, *CreativeTextBillingEvidence, int, string) error
}

type CreativeTextCapabilities struct {
	MaxInputChars          int `json:"max_input_chars"`
	MaxOutputTokens        int `json:"max_output_tokens"`
	DefaultMaxOutputTokens int `json:"default_max_output_tokens"`
	MaxReferenceImages     int `json:"max_reference_images"`
}
