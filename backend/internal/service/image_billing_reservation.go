package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	ImageBillingReserved       = "reserved"
	ImageBillingCaptured       = "captured"
	ImageBillingReleased       = "released"
	ImageBillingReconciliation = "reconciliation"
)

var (
	ErrImageBillingReservationInvalid  = errors.New("invalid image billing reservation")
	ErrImageBillingReservationNotFound = errors.New("image billing reservation not found")
	ErrImageBillingReservationConflict = errors.New("image billing reservation conflict")
	ErrImageBillingReservationReleased = errors.New("image billing reservation has been released")
)

// ImageBillingReserveCommand 只持久化资金与定价快照，不能携带密钥、提示词或图片内容。
type ImageBillingReserveCommand struct {
	Hold      BatchImageBalanceHoldCommand
	Quote     json.RawMessage
	ExpiresAt time.Time
}

// ImageBillingReservation 独立于短期图片结果留存，保留可审计、可幂等完成的资金合同。
// Applied 仅本次新预占为真；重复请求不能凭已有 reserved 状态再次调用上游。
type ImageBillingReservation struct {
	ID        string
	State     string
	Applied   bool
	Hold      BatchImageBalanceHoldCommand
	Quote     json.RawMessage
	ExpiresAt time.Time
	Result    *UsageBillingApplyResult
}

// ImageBillingReservationRepository 是普通图片可选的持久预占能力；固定价异步图片缺少该能力必须拒绝执行。
type ImageBillingReservationRepository interface {
	ReserveImageBilling(context.Context, *ImageBillingReserveCommand) (*ImageBillingReservation, error)
	CaptureImageBilling(context.Context, string, *UsageBillingCommand, float64) (*UsageBillingApplyResult, error)
	ReleaseImageBilling(context.Context, string, int64, int64) error
	MarkImageBillingReconciliation(context.Context, string, int64, int64) error
	ReconcileExpiredImageBilling(context.Context, time.Time, int) (int64, error)
}
