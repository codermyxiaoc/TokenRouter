package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const DailyActivitySettingKey = "daily_activity_config"
const DailyActivityBatchLimit = 100

var (
	ErrDailyActivityDisabled     = infraerrors.Forbidden("DAILY_ACTIVITY_DISABLED", "每日活动尚未开启")
	ErrDailyActivityRuleDisabled = infraerrors.Forbidden("DAILY_ACTIVITY_RULE_DISABLED", "该活动暂未开启")
	ErrDailyActivityUnavailable  = infraerrors.Conflict("DAILY_ACTIVITY_UNAVAILABLE", "暂无可领取次数")
	ErrDailyActivityIdempotency  = infraerrors.Conflict("DAILY_ACTIVITY_IDEMPOTENCY_CONFLICT", "该领取请求标识已用于其他操作")
	dailyActivityKeyPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{16,100}$`)
)

type DailyActivityRule struct {
	Enabled   bool    `json:"enabled"`
	Threshold float64 `json:"threshold"`
	RewardMax float64 `json:"reward_max"`
}

// UnmarshalJSON 为存量规则补上启用默认值，同时保留管理接口对未知字段的严格校验。
func (r *DailyActivityRule) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("daily activity rule cannot be null")
	}
	var decoded struct {
		Enabled   json.RawMessage `json:"enabled"`
		Threshold float64         `json:"threshold"`
		RewardMax float64         `json:"reward_max"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	enabled := true
	if len(decoded.Enabled) > 0 {
		if bytes.Equal(bytes.TrimSpace(decoded.Enabled), []byte("null")) {
			return fmt.Errorf("daily activity enabled must be a boolean")
		}
		if err := json.Unmarshal(decoded.Enabled, &enabled); err != nil {
			return err
		}
	}
	*r = DailyActivityRule{Enabled: enabled, Threshold: decoded.Threshold, RewardMax: decoded.RewardMax}
	return nil
}

type DailyActivityConfig struct {
	Enabled bool `json:"enabled"`
	// 两种可选充值来源默认关闭，仅记录来源开关开启后的新增到账正金额。
	IncludeRedeemBalance bool              `json:"include_redeem_balance"`
	IncludeAdminBalance  bool              `json:"include_admin_balance"`
	StartedAt            *time.Time        `json:"started_at"`
	DailyGift            DailyActivityRule `json:"daily_gift"`
	Recharge             DailyActivityRule `json:"recharge"`
	Consumption          DailyActivityRule `json:"consumption"`
}

// DefaultDailyActivityConfig 默认不开启，首次开启时间只能由仓储写入。
func DefaultDailyActivityConfig() DailyActivityConfig {
	return DailyActivityConfig{DailyGift: DailyActivityRule{Enabled: true, Threshold: 10, RewardMax: .5}, Recharge: DailyActivityRule{Enabled: true, Threshold: 50, RewardMax: .8}, Consumption: DailyActivityRule{Enabled: true, Threshold: 50, RewardMax: .8}}
}

type DailyActivityState struct {
	Enabled        bool    `json:"enabled"`
	Type           string  `json:"type"`
	Threshold      float64 `json:"threshold"`
	RewardMax      float64 `json:"reward_max"`
	EligibleAmount float64 `json:"eligible_amount"`
	ClaimedCount   int64   `json:"claimed_count"`
	AvailableCount int64   `json:"available_count"`
	Progress       float64 `json:"progress"`
}

type DailyActivityStatus struct {
	Enabled              bool                 `json:"enabled"`
	IncludeRedeemBalance bool                 `json:"include_redeem_balance"`
	IncludeAdminBalance  bool                 `json:"include_admin_balance"`
	StartedAt            *time.Time           `json:"started_at"`
	Day                  string               `json:"day"`
	Activities           []DailyActivityState `json:"activities"`
}

type DailyActivityClaimInput struct {
	ActivityType   string `json:"activity_type"`
	All            bool   `json:"all"`
	IdempotencyKey string `json:"idempotency_key"`
}

type DailyActivityRecord struct {
	ID           int64     `json:"id"`
	ActivityType string    `json:"activity_type"`
	Amount       float64   `json:"amount"`
	CreatedAt    time.Time `json:"created_at"`
}

type DailyActivityClaimResult struct {
	Items          []DailyActivityRecord `json:"items"`
	Amount         float64               `json:"amount"`
	Balance        float64               `json:"balance"`
	RemainingCount int64                 `json:"remaining_count"`
	Replayed       bool                  `json:"replayed"`
}

type DailyActivityHistory struct {
	Items    []DailyActivityRecord `json:"items"`
	Total    int64                 `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
}

type DailyActivityRepository interface {
	GetConfig(context.Context) (*DailyActivityConfig, error)
	SaveConfig(context.Context, DailyActivityConfig) (*DailyActivityConfig, error)
	Status(context.Context, int64) (*DailyActivityStatus, error)
	Claim(context.Context, int64, DailyActivityClaimInput) (*DailyActivityClaimResult, error)
	History(context.Context, int64, string, int, int) (*DailyActivityHistory, error)
}

type DailyActivityService struct {
	repo     DailyActivityRepository
	apiKeys  *APIKeyService
	billing  *BillingCacheService
	onUpdate func()
}

func NewDailyActivityService(repo DailyActivityRepository, apiKeys *APIKeyService, billing *BillingCacheService) *DailyActivityService {
	return &DailyActivityService{repo: repo, apiKeys: apiKeys, billing: billing}
}

func (s *DailyActivityService) GetConfig(ctx context.Context) (*DailyActivityConfig, error) {
	return s.repo.GetConfig(ctx)
}
func (s *DailyActivityService) SaveConfig(ctx context.Context, c DailyActivityConfig) (*DailyActivityConfig, error) {
	if err := ValidateDailyActivityConfig(c); err != nil {
		return nil, err
	}
	out, err := s.repo.SaveConfig(ctx, c)
	if err == nil && s.onUpdate != nil {
		s.onUpdate()
	}
	return out, err
}
func (s *DailyActivityService) Status(ctx context.Context, uid int64) (*DailyActivityStatus, error) {
	return s.repo.Status(ctx, uid)
}

func (s *DailyActivityService) Claim(ctx context.Context, uid int64, in DailyActivityClaimInput) (*DailyActivityClaimResult, error) {
	if err := ValidateDailyActivityClaim(in); err != nil {
		return nil, err
	}
	out, err := s.repo.Claim(ctx, uid, in)
	if err != nil {
		return nil, err
	}
	// 数据库已提交后即使调用方断开也清理缓存；幂等回放也用于补偿提交后的断连。
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if s.apiKeys != nil {
		s.apiKeys.InvalidateAuthCacheByUserID(cacheCtx, uid)
	}
	if s.billing != nil {
		_ = s.billing.InvalidateUserBalance(cacheCtx, uid)
	}
	return out, nil
}

func (s *DailyActivityService) History(ctx context.Context, uid int64, kind string, page, size int) (*DailyActivityHistory, error) {
	if kind != "" && !ValidDailyActivityType(kind) {
		return nil, infraerrors.BadRequest("DAILY_ACTIVITY_INVALID_TYPE", "未知活动类型")
	}
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	if page > 1000000 {
		return nil, infraerrors.BadRequest("DAILY_ACTIVITY_INVALID_PAGE", "页码过大")
	}
	return s.repo.History(ctx, uid, kind, page, size)
}

func ValidDailyActivityType(kind string) bool {
	return kind == "daily_gift" || kind == "recharge" || kind == "consumption"
}

func ValidateDailyActivityConfig(c DailyActivityConfig) error {
	for _, rule := range []DailyActivityRule{c.DailyGift, c.Recharge, c.Consumption} {
		for _, v := range []float64{rule.Threshold, rule.RewardMax} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > 1000000 || !decimal.NewFromFloat(v).Equal(decimal.NewFromFloat(v).Round(2)) {
				return infraerrors.BadRequest("DAILY_ACTIVITY_INVALID_CONFIG", "活动金额须为正数、最多两位小数且不超过1000000")
			}
		}
		if rule.RewardMax < .10 {
			return infraerrors.BadRequest("DAILY_ACTIVITY_INVALID_CONFIG", "随机奖励上限不能小于0.10")
		}
	}
	return nil
}

func ValidateDailyActivityClaim(in DailyActivityClaimInput) error {
	if !ValidDailyActivityType(in.ActivityType) {
		return infraerrors.BadRequest("DAILY_ACTIVITY_INVALID_TYPE", "未知活动类型")
	}
	if !dailyActivityKeyPattern.MatchString(in.IdempotencyKey) {
		return infraerrors.BadRequest("DAILY_ACTIVITY_INVALID_KEY", "领取请求标识须为16至100位字母、数字、下划线或连字符")
	}
	return nil
}

// DailyActivityRewardCents 使用密码学随机整数分，避免浮点随机端点偏差。
func DailyActivityRewardCents(max float64) (int64, error) {
	if math.IsNaN(max) || math.IsInf(max, 0) || max < .10 || max > 1000000 {
		return 0, infraerrors.BadRequest("DAILY_ACTIVITY_INVALID_CONFIG", "随机奖励范围无效")
	}
	maxCents := decimal.NewFromFloat(max).Mul(decimal.NewFromInt(100)).IntPart()
	n, err := rand.Int(rand.Reader, big.NewInt(maxCents-10+1))
	if err != nil {
		return 0, err
	}
	return n.Int64() + 10, nil
}

// DailyActivityDay 仅每日礼包按北京时间零点换日，累计活动不依赖日期。
func DailyActivityDay(now time.Time) (string, time.Time, time.Time) {
	local := now.In(time.FixedZone("Asia/Shanghai", 8*60*60))
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	return start.Format("2006-01-02"), start, start.AddDate(0, 0, 1)
}
