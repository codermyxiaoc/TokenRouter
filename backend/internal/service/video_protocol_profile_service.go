package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/tidwall/gjson"
)

// ErrVideoProtocolProfileNotFound 表示模板不存在或没有启用版本。
var ErrVideoProtocolProfileNotFound = infraerrors.NotFound("VIDEO_PROFILE_NOT_FOUND", "Video protocol profile not found")

// VideoProtocolProfileRepository 持久化模板版本；version 为 0 表示读取当前启用版本。
type VideoProtocolProfileRepository interface {
	CreateVersion(ctx context.Context, profile *VideoProtocolProfile) (*VideoProtocolProfile, error)
	Get(ctx context.Context, key string, version int) (*VideoProtocolProfile, error)
	List(ctx context.Context) ([]VideoProtocolProfile, error)
	// SetStatus 启用某版本时在同一事务内把该键名的其它启用版本降为草稿。
	SetStatus(ctx context.Context, key string, version int, status string) error
	// Delete 删除指定版本；version 为 0 时删除该键名的全部版本。
	Delete(ctx context.Context, key string, version int) error
}

// VideoProtocolProfileService 负责模板的保存校验、版本启停与调试预览。
type VideoProtocolProfileService struct {
	repo VideoProtocolProfileRepository
	// accounts 用于删除前检查仍在使用模板的视频账号；settings 记录被删除的预置模板，避免启动时重新补齐。
	accounts AccountRepository
	settings SettingRepository

	mu    sync.Mutex
	cache map[string]videoProfileCacheEntry
}

// SettingKeyVideoProtocolPresetsDeleted 记录管理员已删除的预置模板键名（JSON 数组）。
const SettingKeyVideoProtocolPresetsDeleted = "video_protocol_presets_deleted"

// ProvideVideoProtocolProfileService 创建带删除检查依赖的模板服务。
func ProvideVideoProtocolProfileService(repo VideoProtocolProfileRepository, settings SettingRepository, accounts AccountRepository) *VideoProtocolProfileService {
	s := NewVideoProtocolProfileService(repo)
	s.settings, s.accounts = settings, accounts
	return s
}

// Delete 删除模板版本或整个模板：启用中的版本须先停用或启用其它版本；仍被视频账号绑定的模板（或锁定的版本）拒绝删除。
// 已受理任务冻结了整份模板快照，删除不影响其查询、内容与结算。
func (s *VideoProtocolProfileService) Delete(ctx context.Context, key string, version int) error {
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("VIDEO_PROFILE_UNAVAILABLE", "Video protocol profiles are unavailable")
	}
	if !ValidVideoProfileKey(key) || version < 0 {
		return videoProfileInvalid("invalid profile key or version")
	}
	all, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	versions := []VideoProtocolProfile{}
	for _, profile := range all {
		if profile.ProfileKey == key {
			versions = append(versions, profile)
		}
	}
	if len(versions) == 0 {
		return ErrVideoProtocolProfileNotFound
	}
	remaining := 0
	if version > 0 {
		found := false
		for _, profile := range versions {
			if profile.Version != version {
				remaining++
				continue
			}
			found = true
			if profile.Status == VideoProtocolProfileActive {
				return infraerrors.New(http.StatusConflict, "VIDEO_PROFILE_ACTIVE", "Disable this version or activate another version before deleting it")
			}
		}
		if !found {
			return ErrVideoProtocolProfileNotFound
		}
	}
	if users := s.profileUsers(ctx, key, version, remaining == 0); len(users) > 0 {
		return infraerrors.New(http.StatusConflict, "VIDEO_PROFILE_IN_USE", "Video accounts still bind this template: "+strings.Join(users, ", "))
	}
	// 删除预置模板的全部版本前先持久化“已删除”标记：标记写入失败则不删除，
	// 避免删除成功而标记丢失、重启后预置模板被重新补齐。标记先写而删除失败时无害（行仍存在就不会补齐）。
	if remaining == 0 {
		if _, presetErr := videoProtocolPresetProfile(key, 0); presetErr == nil {
			if err := s.markPresetDeleted(ctx, key); err != nil {
				return infraerrors.ServiceUnavailable("VIDEO_PROFILE_DELETE_MARK_FAILED", "Could not record the preset template deletion; nothing was deleted").WithCause(err)
			}
		}
	}
	if err := s.repo.Delete(ctx, key, version); err != nil {
		return err
	}
	s.invalidate(key)
	return nil
}

// profileUsers 返回仍在使用模板的视频账号名称：删除整个模板时检查全部绑定，删除单个版本时只检查锁定该版本的绑定。
func (s *VideoProtocolProfileService) profileUsers(ctx context.Context, key string, version int, whole bool) []string {
	if s.accounts == nil {
		return nil
	}
	accounts, err := s.accounts.ListByPlatform(ctx, PlatformVideo)
	if err != nil {
		return []string{"unknown (account list unavailable)"}
	}
	users := []string{}
	for i := range accounts {
		cfg, cfgErr := accounts[i].VideoConfiguration()
		if cfgErr != nil {
			continue
		}
		for _, binding := range cfg.ModelProtocols {
			resolved, parseErr := parseVideoModelProtocol(binding)
			if parseErr != nil || !resolved.IsProfile() || resolved.ProfileKey != key {
				continue
			}
			if whole || version == 0 || resolved.ProfileVersion == version {
				users = append(users, accounts[i].Name)
				break
			}
		}
		if len(users) >= 5 {
			break
		}
	}
	return users
}

// keyExists 判断该键名在数据库中是否还有任何版本。
func (s *VideoProtocolProfileService) keyExists(ctx context.Context, key string) bool {
	all, err := s.repo.List(ctx)
	if err != nil {
		// 无法确认时按存在处理，不回退到内置定义，避免覆盖管理员的停用决定。
		return true
	}
	for _, profile := range all {
		if profile.ProfileKey == key {
			return true
		}
	}
	return false
}

// deletedPresets 读取已删除的预置模板键名。
func (s *VideoProtocolProfileService) deletedPresets(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if s == nil || s.settings == nil {
		return out
	}
	raw, err := s.settings.GetValue(ctx, SettingKeyVideoProtocolPresetsDeleted)
	if err != nil || strings.TrimSpace(raw) == "" {
		return out
	}
	var keys []string
	if json.Unmarshal([]byte(raw), &keys) == nil {
		for _, key := range keys {
			out[key] = true
		}
	}
	return out
}

func (s *VideoProtocolProfileService) presetDeleted(ctx context.Context, key string) bool {
	return s.deletedPresets(ctx)[key]
}

// markPresetDeleted 记录被删除的预置模板，使其不会在重启时被重新补齐。
// 支持原子更新的仓储在同一事务内加锁读改写，并发删除不同预置模板不会互相覆盖；写入失败返回错误。
func (s *VideoProtocolProfileService) markPresetDeleted(ctx context.Context, key string) error {
	if s.settings == nil {
		return nil
	}
	merge := func(raw string) string {
		deleted := map[string]bool{key: true}
		var keys []string
		if strings.TrimSpace(raw) != "" && json.Unmarshal([]byte(raw), &keys) == nil {
			for _, name := range keys {
				deleted[name] = true
			}
		}
		keys = keys[:0]
		for name := range deleted {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		encoded, _ := json.Marshal(keys)
		return string(encoded)
	}
	if atomic, ok := s.settings.(SettingAtomicUpdater); ok {
		return atomic.UpdateMultiple(ctx, []string{SettingKeyVideoProtocolPresetsDeleted}, func(current map[string]string) (map[string]string, error) {
			return map[string]string{SettingKeyVideoProtocolPresetsDeleted: merge(current[SettingKeyVideoProtocolPresetsDeleted])}, nil
		})
	}
	raw, err := s.settings.GetValue(ctx, SettingKeyVideoProtocolPresetsDeleted)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return err
	}
	return s.settings.Set(ctx, SettingKeyVideoProtocolPresetsDeleted, merge(raw))
}

// invalidate 清除该键名的解析缓存。
func (s *VideoProtocolProfileService) invalidate(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for cacheKey := range s.cache {
		if strings.HasPrefix(cacheKey, key+"@") {
			delete(s.cache, cacheKey)
		}
	}
}

type videoProfileCacheEntry struct {
	profile *VideoProtocolProfile
	expires time.Time
}

// 启用版本的解析结果只短暂缓存，管理员切换版本后最多十秒生效；已受理任务不受影响。
const videoProfileCacheTTL = 10 * time.Second

func NewVideoProtocolProfileService(repo VideoProtocolProfileRepository) *VideoProtocolProfileService {
	return &VideoProtocolProfileService{repo: repo, cache: map[string]videoProfileCacheEntry{}}
}

// VideoProtocolProfileInput 是管理端保存模板的请求。
type VideoProtocolProfileInput struct {
	ProfileKey string                   `json:"profile_key"`
	Name       string                   `json:"name"`
	Spec       VideoProtocolProfileSpec `json:"spec"`
	Activate   bool                     `json:"activate"`
}

// Save 校验后保存为新版本；可选同时启用，旧版本保留供在途任务与回退使用。
func (s *VideoProtocolProfileService) Save(ctx context.Context, input VideoProtocolProfileInput, createdBy int64) (*VideoProtocolProfile, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("VIDEO_PROFILE_UNAVAILABLE", "Video protocol profiles are unavailable")
	}
	key := strings.TrimSpace(input.ProfileKey)
	name := strings.TrimSpace(input.Name)
	if !ValidVideoProfileKey(key) {
		return nil, videoProfileInvalid("profile_key must use lowercase letters, digits, _ or - (max 64)")
	}
	if name == "" || len([]rune(name)) > 100 {
		return nil, videoProfileInvalid("name is required (max 100 characters)")
	}
	if err := input.Spec.Validate(); err != nil {
		return nil, err
	}
	hash, err := input.Spec.Hash()
	if err != nil {
		return nil, err
	}
	profile := &VideoProtocolProfile{ProfileKey: key, Name: name, Spec: input.Spec, SpecHash: hash, Status: VideoProtocolProfileDraft}
	if createdBy > 0 {
		profile.CreatedBy = &createdBy
	}
	saved, err := s.repo.CreateVersion(ctx, profile)
	if err != nil {
		return nil, err
	}
	if input.Activate {
		if err := s.SetStatus(ctx, key, saved.Version, VideoProtocolProfileActive); err != nil {
			return nil, err
		}
		saved.Status = VideoProtocolProfileActive
	}
	return saved, nil
}

// SetStatus 启用或停用指定版本；启用时原启用版本降为草稿，停用只影响之后的新任务。
func (s *VideoProtocolProfileService) SetStatus(ctx context.Context, key string, version int, status string) error {
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("VIDEO_PROFILE_UNAVAILABLE", "Video protocol profiles are unavailable")
	}
	if !ValidVideoProfileKey(key) || version <= 0 || (status != VideoProtocolProfileActive && status != VideoProtocolProfileDisabled && status != VideoProtocolProfileDraft) {
		return videoProfileInvalid("invalid profile version or status")
	}
	if err := s.repo.SetStatus(ctx, key, version, status); err != nil {
		return err
	}
	s.mu.Lock()
	for cacheKey := range s.cache {
		if strings.HasPrefix(cacheKey, key+"@") {
			delete(s.cache, cacheKey)
		}
	}
	s.mu.Unlock()
	return nil
}

// List 返回全部版本，供管理端展示。
func (s *VideoProtocolProfileService) List(ctx context.Context) ([]VideoProtocolProfile, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	return s.repo.List(ctx)
}

// Resolve 读取可用于新任务的模板：指定版本必须未停用，未指定版本时取当前启用版本。
func (s *VideoProtocolProfileService) Resolve(ctx context.Context, key string, version int) (*VideoProtocolProfile, error) {
	if s == nil || s.repo == nil {
		return videoProtocolPresetProfile(key, version)
	}
	cacheKey := key + "@" + strconv.Itoa(version)
	now := time.Now()
	s.mu.Lock()
	if entry, ok := s.cache[cacheKey]; ok && now.Before(entry.expires) {
		s.mu.Unlock()
		return entry.profile, nil
	}
	s.mu.Unlock()
	profile, err := s.repo.Get(ctx, key, version)
	if errors.Is(err, ErrVideoProtocolProfileNotFound) {
		// 预置模板尚未写入数据库（例如启动补齐失败）时使用内置定义，避免已改绑账号中断。
		// 版本号从 1 开始，第 1 版存在说明该键名已入库，此时停用或草稿状态必须照常生效。
		if _, firstErr := s.repo.Get(ctx, key, 1); errors.Is(firstErr, ErrVideoProtocolProfileNotFound) && !s.keyExists(ctx, key) && !s.presetDeleted(ctx, key) {
			profile, err = videoProtocolPresetProfile(key, version)
		}
	}
	if err != nil {
		return nil, err
	}
	if profile == nil || profile.Status == VideoProtocolProfileDisabled || version == 0 && profile.Status != VideoProtocolProfileActive {
		return nil, ErrVideoProtocolProfileNotFound
	}
	// 数据库内容被直接改写时哈希不再匹配，拒绝用于新任务。
	if hash, hashErr := profile.Spec.Hash(); hashErr != nil || hash != profile.SpecHash {
		return nil, infraerrors.New(http.StatusConflict, "VIDEO_PROFILE_HASH_MISMATCH", "Video protocol profile content does not match its hash")
	}
	s.mu.Lock()
	s.cache[cacheKey] = videoProfileCacheEntry{profile: profile, expires: now.Add(videoProfileCacheTTL)}
	s.mu.Unlock()
	return profile, nil
}

// VideoProfilePreviewInput 是调试器请求：模板、规范请求样例与可选的上游响应样例。
type VideoProfilePreviewInput struct {
	Spec           VideoProtocolProfileSpec `json:"spec"`
	Request        json.RawMessage          `json:"request"`
	Model          string                   `json:"model"`
	CreateResponse json.RawMessage          `json:"create_response,omitempty"`
	PollResponse   json.RawMessage          `json:"poll_response,omitempty"`
}

// VideoProfilePreview 是调试结果，只包含模板生成的请求与解析出的规范字段，不访问任何上游。
type VideoProfilePreview struct {
	CreatePath   string                `json:"create_path"`
	CreateBody   json.RawMessage       `json:"create_body,omitempty"`
	Billing      VideoRequestMetadata  `json:"billing"`
	Create       *VideoProfileObserved `json:"create,omitempty"`
	PollPath     string                `json:"poll_path,omitempty"`
	Poll         *VideoProfileObserved `json:"poll,omitempty"`
	ContentPath  string                `json:"content_path,omitempty"`
	RequestError string                `json:"request_error,omitempty"`
}

// VideoProfileObserved 是一次响应解析结果。
type VideoProfileObserved struct {
	TaskID         string            `json:"task_id,omitempty"`
	TaskRefs       map[string]string `json:"task_refs,omitempty"`
	Status         string            `json:"status,omitempty"`
	UpstreamStatus string            `json:"upstream_status,omitempty"`
	VideoURL       string            `json:"video_url,omitempty"`
	Seconds        float64           `json:"seconds,omitempty"`
	Tokens         *int64            `json:"tokens,omitempty"`
	Resolution     string            `json:"resolution,omitempty"`
	MatchesTask    bool              `json:"matches_task"`
	Error          string            `json:"error,omitempty"`
}

// Preview 用样例跑通模板的创建、查询与内容路径，帮助管理员在启用前发现映射错误。
func (s *VideoProtocolProfileService) Preview(input VideoProfilePreviewInput) (*VideoProfilePreview, error) {
	if err := input.Spec.Validate(); err != nil {
		return nil, err
	}
	hash, err := input.Spec.Hash()
	if err != nil {
		return nil, err
	}
	snapshot, _ := json.Marshal(input.Spec)
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = "preview-model"
	}
	target := VideoUpstreamTarget{Version: videoTargetVersionProfile, Model: model, ProtocolAdapter: VideoProfileProtocolID("preview", 1), ProfileSnapshot: snapshot, ProfileHash: hash}
	out := &VideoProfilePreview{}
	if out.CreatePath, err = renderVideoProfilePath(input.Spec.Create.Path, target, "", false); err != nil {
		return nil, videoProfileInvalid("create.path: %v", err)
	}
	target.CreatePath = out.CreatePath
	if len(input.Request) > 0 {
		canonical, parseErr := ParseVideoCanonicalRequest(input.Request)
		if parseErr == nil {
			canonical.Model = model
			body, metadata, buildErr := buildVideoProfileCreateBody(input.Spec, canonical, false)
			if buildErr == nil {
				out.CreateBody, out.Billing = body, metadata
			} else {
				out.RequestError = buildErr.Error()
			}
		} else {
			out.RequestError = parseErr.Error()
		}
	}
	taskID := ""
	if len(input.CreateResponse) > 0 {
		out.Create = observeVideoProfileResponse(target, input.CreateResponse, "")
		taskID = out.Create.TaskID
		target.TaskRefs = out.Create.TaskRefs
	}
	if taskID == "" {
		taskID = "preview-task-id"
	}
	if path, pathErr := videoProfileTaskPath(target, taskID); pathErr == nil {
		out.PollPath = path
	}
	if len(input.PollResponse) > 0 {
		out.Poll = observeVideoProfileResponse(target, input.PollResponse, taskID)
	}
	if path, pathErr := videoProfileContentPath(target, taskID); pathErr == nil {
		out.ContentPath = path
	}
	return out, nil
}

func observeVideoProfileResponse(target VideoUpstreamTarget, body json.RawMessage, taskID string) *VideoProfileObserved {
	observed := &VideoProfileObserved{}
	if !gjson.ValidBytes(body) {
		observed.Error = "response sample must be JSON"
		return observed
	}
	observed.MatchesTask = videoProfileResponseMatchesTask(target, body, taskID)
	response, err := extractVideoProfileResponse(target, http.StatusOK, body, taskID)
	if err != nil {
		observed.Error = err.Error()
		return observed
	}
	observed.TaskID, observed.TaskRefs, observed.Status, observed.UpstreamStatus = response.TaskID, response.TaskRefs, response.Status, response.UpstreamStatus
	observed.VideoURL, observed.Seconds, observed.Tokens, observed.Resolution = response.VideoURL, response.Metadata.DurationSeconds, response.Metadata.Tokens, response.Metadata.Resolution
	if response.MetadataError != "" {
		observed.Error = response.MetadataError
	}
	return observed
}
