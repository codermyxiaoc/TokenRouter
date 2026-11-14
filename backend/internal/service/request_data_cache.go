package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

// 请求数据缓存沿用错误请求的快照链路（ops_request_details）：
// 错误请求一直会写入快照；普通请求在开启缓存后以同样方式写入。两类请求各自配置开关、缓存时长与分组范围。
const (
	// RequestDataCacheKindNormal 表示成功请求的快照。
	RequestDataCacheKindNormal = "normal"
	// RequestDataCacheKindError 表示失败请求的快照（与历史数据一致）。
	RequestDataCacheKindError = "error"

	// requestDataCacheMinTTLMinutes / requestDataCacheMaxTTLMinutes 限定缓存时长范围（1 分钟 ~ 365 天）。
	requestDataCacheMinTTLMinutes = 1
	requestDataCacheMaxTTLMinutes = 365 * 24 * 60
	// requestDataCacheDefaultNormalTTLMinutes 普通请求默认缓存 1 天。
	requestDataCacheDefaultNormalTTLMinutes = 24 * 60
	// requestDataCacheDefaultErrorTTLMinutes 错误请求默认缓存 30 天，与错误日志默认保留天数一致。
	requestDataCacheDefaultErrorTTLMinutes = 30 * 24 * 60
	// requestDataCacheMaxGroupIDs 限制分组白名单长度，避免异常配置拖慢热路径判断。
	requestDataCacheMaxGroupIDs = 1000
)

// RequestDataCacheScope 是一类请求（普通/错误）的缓存配置。
type RequestDataCacheScope struct {
	// Enabled 关闭时该类请求不写入快照。
	Enabled bool `json:"enabled"`
	// TTLMinutes 缓存时长（分钟），到期后不再返回并由清理任务删除。
	TTLMinutes int `json:"ttl_minutes"`
	// AllGroups 为 true 时缓存全部分组；为 false 时只缓存 GroupIDs 中的分组。
	AllGroups bool `json:"all_groups"`
	// GroupIDs 仅在 AllGroups 为 false 时生效。
	GroupIDs []int64 `json:"group_ids"`
	// IncludeUngrouped 控制未绑定分组的请求（如认证失败前的请求）是否缓存，仅在 AllGroups 为 false 时生效。
	IncludeUngrouped bool `json:"include_ungrouped"`
}

// RequestDataCacheSettings 是请求数据缓存的完整配置。
type RequestDataCacheSettings struct {
	Normal RequestDataCacheScope `json:"normal"`
	Error  RequestDataCacheScope `json:"error"`
}

// DefaultRequestDataCacheSettings 返回默认配置：普通请求默认不缓存；
// 错误请求默认对全部分组缓存，与功能上线前"失败请求都保留快照"的行为一致。
func DefaultRequestDataCacheSettings() RequestDataCacheSettings {
	return RequestDataCacheSettings{
		Normal: RequestDataCacheScope{
			Enabled:    false,
			TTLMinutes: requestDataCacheDefaultNormalTTLMinutes,
			AllGroups:  true,
			GroupIDs:   []int64{},
		},
		Error: RequestDataCacheScope{
			Enabled:          true,
			TTLMinutes:       requestDataCacheDefaultErrorTTLMinutes,
			AllGroups:        true,
			GroupIDs:         []int64{},
			IncludeUngrouped: true,
		},
	}
}

// normalizeRequestDataCacheScope 去重排序分组并修正非法时长。
func normalizeRequestDataCacheScope(scope *RequestDataCacheScope, defaultTTL int) {
	if scope.TTLMinutes <= 0 {
		scope.TTLMinutes = defaultTTL
	}
	seen := make(map[int64]struct{}, len(scope.GroupIDs))
	ids := make([]int64, 0, len(scope.GroupIDs))
	for _, id := range scope.GroupIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	scope.GroupIDs = ids
}

// NormalizeRequestDataCacheSettings 规范化配置（读取持久化值与保存前都会调用）。
func NormalizeRequestDataCacheSettings(cfg *RequestDataCacheSettings) {
	if cfg == nil {
		return
	}
	normalizeRequestDataCacheScope(&cfg.Normal, requestDataCacheDefaultNormalTTLMinutes)
	normalizeRequestDataCacheScope(&cfg.Error, requestDataCacheDefaultErrorTTLMinutes)
}

// validateRequestDataCacheScope 校验单类配置，name 用于错误提示。
func validateRequestDataCacheScope(name string, scope RequestDataCacheScope) error {
	if scope.TTLMinutes < requestDataCacheMinTTLMinutes || scope.TTLMinutes > requestDataCacheMaxTTLMinutes {
		return fmt.Errorf("%s.ttl_minutes must be between %d and %d", name, requestDataCacheMinTTLMinutes, requestDataCacheMaxTTLMinutes)
	}
	if len(scope.GroupIDs) > requestDataCacheMaxGroupIDs {
		return fmt.Errorf("%s.group_ids must not exceed %d entries", name, requestDataCacheMaxGroupIDs)
	}
	for _, id := range scope.GroupIDs {
		if id <= 0 {
			return fmt.Errorf("%s.group_ids must be positive", name)
		}
	}
	return nil
}

// ValidateRequestDataCacheSettings 校验整份配置。
func ValidateRequestDataCacheSettings(cfg RequestDataCacheSettings) error {
	if err := validateRequestDataCacheScope("normal", cfg.Normal); err != nil {
		return err
	}
	return validateRequestDataCacheScope("error", cfg.Error)
}

// parseRequestDataCacheSettings 解析持久化值；缺失或损坏时回退默认值，保持错误请求快照不丢失。
func parseRequestDataCacheSettings(raw string) RequestDataCacheSettings {
	cfg := DefaultRequestDataCacheSettings()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cfg
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return DefaultRequestDataCacheSettings()
	}
	NormalizeRequestDataCacheSettings(&cfg)
	return cfg
}

// requestDataCacheRuntime 是热路径读取的不可变快照：分组集合预先转成 map。
type requestDataCacheRuntime struct {
	settings     RequestDataCacheSettings
	normalGroups map[int64]struct{}
	errorGroups  map[int64]struct{}
}

func newRequestDataCacheRuntime(cfg RequestDataCacheSettings) *requestDataCacheRuntime {
	toSet := func(ids []int64) map[int64]struct{} {
		out := make(map[int64]struct{}, len(ids))
		for _, id := range ids {
			out[id] = struct{}{}
		}
		return out
	}
	return &requestDataCacheRuntime{
		settings:     cfg,
		normalGroups: toSet(cfg.Normal.GroupIDs),
		errorGroups:  toSet(cfg.Error.GroupIDs),
	}
}

// RequestDataCacheDecision 是单个请求是否写入快照及其缓存时长的判定结果。
type RequestDataCacheDecision struct {
	Cache bool
	Kind  string
	TTL   time.Duration
}

// decide 根据请求类别与分组判定是否缓存。groupID 为 nil 表示请求未绑定分组。
func (r *requestDataCacheRuntime) decide(isError bool, groupID *int64) RequestDataCacheDecision {
	scope := r.settings.Normal
	groups := r.normalGroups
	kind := RequestDataCacheKindNormal
	if isError {
		scope = r.settings.Error
		groups = r.errorGroups
		kind = RequestDataCacheKindError
	}
	decision := RequestDataCacheDecision{Kind: kind, TTL: time.Duration(scope.TTLMinutes) * time.Minute}
	if !scope.Enabled {
		return decision
	}
	if scope.AllGroups {
		decision.Cache = true
		return decision
	}
	if groupID == nil || *groupID <= 0 {
		decision.Cache = scope.IncludeUngrouped
		return decision
	}
	_, decision.Cache = groups[*groupID]
	return decision
}

// requestDataCacheRuntimeSnapshot 返回当前快照；未加载时使用默认配置。
func (s *OpsService) requestDataCacheRuntimeSnapshot() *requestDataCacheRuntime {
	if s != nil {
		if snapshot := s.requestDataCache.Load(); snapshot != nil {
			return snapshot
		}
	}
	return newRequestDataCacheRuntime(DefaultRequestDataCacheSettings())
}

// storeRequestDataCacheSettings 发布新的运行时快照。
func (s *OpsService) storeRequestDataCacheSettings(cfg RequestDataCacheSettings) {
	if s == nil {
		return
	}
	s.requestDataCache.Store(newRequestDataCacheRuntime(cfg))
}

// DecideRequestDataCache 供网关中间件在响应结束后调用，只读取内存快照，不访问数据库。
func (s *OpsService) DecideRequestDataCache(isError bool, groupID *int64) RequestDataCacheDecision {
	return s.requestDataCacheRuntimeSnapshot().decide(isError, groupID)
}

// GetRequestDataCacheSettings 返回当前生效的缓存配置。
func (s *OpsService) GetRequestDataCacheSettings(ctx context.Context) (*RequestDataCacheSettings, error) {
	if s == nil || s.settingRepo == nil {
		cfg := DefaultRequestDataCacheSettings()
		return &cfg, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyRequestDataCacheSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			cfg := DefaultRequestDataCacheSettings()
			return &cfg, nil
		}
		return nil, err
	}
	cfg := parseRequestDataCacheSettings(raw)
	return &cfg, nil
}

// UpdateRequestDataCacheSettings 校验并保存缓存配置，保存成功后立即刷新本实例快照；
// 其它实例由运行时设置刷新器在下一个周期同步。
func (s *OpsService) UpdateRequestDataCacheSettings(ctx context.Context, cfg *RequestDataCacheSettings) (*RequestDataCacheSettings, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("setting repository not initialized")
	}
	if cfg == nil {
		return nil, infraerrors.BadRequest("REQUEST_DATA_CACHE_INVALID", "invalid config")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	next := *cfg
	next.Normal.GroupIDs = append([]int64(nil), cfg.Normal.GroupIDs...)
	next.Error.GroupIDs = append([]int64(nil), cfg.Error.GroupIDs...)
	if err := ValidateRequestDataCacheSettings(next); err != nil {
		return nil, infraerrors.BadRequest("REQUEST_DATA_CACHE_INVALID", err.Error())
	}
	NormalizeRequestDataCacheSettings(&next)
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyRequestDataCacheSettings, string(raw)); err != nil {
		return nil, err
	}
	s.storeRequestDataCacheSettings(next)
	return &next, nil
}

// RequestDataCacheStats 是管理端展示的缓存条数（已过期但尚未清理的不计入）。
type RequestDataCacheStats struct {
	Normal int64 `json:"normal"`
	Error  int64 `json:"error"`
}

// GetRequestDataCacheStats 返回两类缓存的有效条数。
func (s *OpsService) GetRequestDataCacheStats(ctx context.Context) (*RequestDataCacheStats, error) {
	if s == nil || s.opsRepo == nil {
		return &RequestDataCacheStats{}, nil
	}
	return s.opsRepo.CountRequestDataCache(ctx, time.Now().UTC())
}

// ClearRequestDataCache 手动清除缓存。kind 为 normal / error / all，返回删除条数。
func (s *OpsService) ClearRequestDataCache(ctx context.Context, kind string) (int64, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case RequestDataCacheKindNormal, RequestDataCacheKindError, "all":
	default:
		return 0, infraerrors.BadRequest("REQUEST_DATA_CACHE_INVALID_KIND", "kind must be normal, error or all")
	}
	if s == nil || s.opsRepo == nil {
		return 0, nil
	}
	if kind == "all" {
		kind = ""
	}
	return s.opsRepo.DeleteRequestDataCache(ctx, kind)
}

// RequestDataCacheEntry 是面向终端用户的请求端点信息（白名单）：
// 只包含端点、方法、平台、模型、状态等元数据，严禁返回请求头、请求体与响应正文。
type RequestDataCacheEntry struct {
	RequestID        string     `json:"request_id"`
	Kind             string     `json:"kind"`
	Method           string     `json:"method"`
	Path             string     `json:"path"`
	InboundEndpoint  string     `json:"inbound_endpoint"`
	UpstreamEndpoint string     `json:"upstream_endpoint"`
	Platform         string     `json:"platform"`
	Model            string     `json:"model"`
	StatusCode       int        `json:"status_code"`
	Stream           bool       `json:"stream"`
	CreatedAt        time.Time  `json:"created_at"`
	CompletedAt      time.Time  `json:"completed_at"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

// toRequestDataCacheEntry 把内部快照裁剪为用户安全视图。
func toRequestDataCacheEntry(detail *OpsRequestPayloadDetail) *RequestDataCacheEntry {
	if detail == nil {
		return nil
	}
	kind := detail.Kind
	if kind == "" {
		kind = RequestDataCacheKindError
	}
	return &RequestDataCacheEntry{
		RequestID:        detail.RequestID,
		Kind:             kind,
		Method:           detail.Method,
		Path:             detail.Path,
		InboundEndpoint:  detail.InboundEndpoint,
		UpstreamEndpoint: detail.UpstreamEndpoint,
		Platform:         detail.Platform,
		Model:            detail.Model,
		StatusCode:       detail.StatusCode,
		Stream:           detail.Stream,
		CreatedAt:        detail.CreatedAt,
		CompletedAt:      detail.CompletedAt,
		ExpiresAt:        detail.ExpiresAt,
	}
}

// requestDataCacheVisible 判断快照是否属于该用户且仍在缓存期内。
// 历史快照没有归属字段，无法证明归属，一律不对用户开放。
func requestDataCacheVisible(detail *OpsRequestPayloadDetail, userID int64, now time.Time) bool {
	if detail == nil || detail.UserID == nil || *detail.UserID != userID {
		return false
	}
	if detail.ExpiresAt != nil && !detail.ExpiresAt.After(now) {
		return false
	}
	return true
}

// errRequestDataCacheNotFound 对无缓存、已过期或非本人请求统一返回不存在，不泄露请求是否存在。
var errRequestDataCacheNotFound = infraerrors.NotFound("REQUEST_DATA_CACHE_NOT_FOUND", "request data cache not found")

// GetUserRequestDataCache 按请求 ID 返回当前用户自己的请求端点缓存。
func (s *OpsService) GetUserRequestDataCache(ctx context.Context, userID int64, requestID string) (*RequestDataCacheEntry, error) {
	requestID = strings.TrimSpace(requestID)
	if s == nil || s.opsRepo == nil || userID <= 0 || requestID == "" || len(requestID) > 200 {
		return nil, errRequestDataCacheNotFound
	}
	detail, err := s.opsRepo.GetRequestPayloadDetail(ctx, requestID)
	if err != nil {
		return nil, infraerrors.InternalServer("REQUEST_DATA_CACHE_LOAD_FAILED", "Failed to load request data cache").WithCause(err)
	}
	if !requestDataCacheVisible(detail, userID, time.Now().UTC()) {
		return nil, errRequestDataCacheNotFound
	}
	return toRequestDataCacheEntry(detail), nil
}

// GetUserErrorRequestDataCache 按错误记录 ID 返回当前用户的请求端点缓存；归属先由错误记录校验。
func (s *OpsService) GetUserErrorRequestDataCache(ctx context.Context, userID, errorID int64) (*RequestDataCacheEntry, error) {
	if s == nil || s.opsRepo == nil || userID <= 0 || errorID <= 0 {
		return nil, errRequestDataCacheNotFound
	}
	errLog, err := s.opsRepo.GetErrorLogByID(ctx, errorID)
	if err != nil || errLog == nil || errLog.UserID == nil || *errLog.UserID != userID {
		return nil, errRequestDataCacheNotFound
	}
	for _, requestID := range []string{errLog.RequestID, errLog.ClientRequestID} {
		if strings.TrimSpace(requestID) == "" {
			continue
		}
		entry, err := s.GetUserRequestDataCache(ctx, userID, requestID)
		if err == nil {
			return entry, nil
		}
	}
	return nil, errRequestDataCacheNotFound
}
