package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

// SettingKeyAPIDocOverrides 保存管理员改写的 API 文档 Markdown（JSON 对象：文档 ID → Markdown）。
// 未改写的文档继续使用前端内置的默认正文，内置文档更新后自动生效。
const SettingKeyAPIDocOverrides = "api_doc_overrides"

const (
	// 单篇与全部改写的长度上限，避免公开接口返回过大的正文。
	maxAPIDocMarkdownRunes = 200000
	maxAPIDocOverrideBytes = 2 << 20
	maxAPIDocOverrides     = 500
)

var apiDocIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

// GetAPIDocOverrides 返回已改写的 API 文档；设置不存在或损坏时返回空集合，不影响默认文档展示。
// @project-doc docs/interfaces/api_reference.md#api_docs_markdown
func (s *SettingService) GetAPIDocOverrides(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	if s == nil || s.settingRepo == nil {
		return out, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyAPIDocOverrides)
	if errors.Is(err, ErrSettingNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &out) != nil {
		return map[string]string{}, nil
	}
	return out, nil
}

// UpdateAPIDocOverrides 整体替换改写集合；空正文表示恢复该文档的默认内容。
func (s *SettingService) UpdateAPIDocOverrides(ctx context.Context, overrides map[string]string) (map[string]string, error) {
	if s == nil || s.settingRepo == nil {
		return nil, infraerrors.ServiceUnavailable("SETTINGS_UNAVAILABLE", "Settings are unavailable")
	}
	invalid := func(message string) error { return infraerrors.BadRequest("API_DOC_INVALID", message) }
	cleaned := map[string]string{}
	for id, markdown := range overrides {
		if !apiDocIDPattern.MatchString(id) {
			return nil, invalid("document id must use lowercase letters, digits, _ or - (max 128)")
		}
		if strings.TrimSpace(markdown) == "" {
			continue
		}
		if len([]rune(markdown)) > maxAPIDocMarkdownRunes {
			return nil, invalid("document " + id + " is too long")
		}
		cleaned[id] = markdown
	}
	if len(cleaned) > maxAPIDocOverrides {
		return nil, invalid("too many customized documents")
	}
	raw, err := json.Marshal(cleaned)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxAPIDocOverrideBytes {
		return nil, invalid("customized documents are too large")
	}
	if err := s.settingRepo.Set(ctx, SettingKeyAPIDocOverrides, string(raw)); err != nil {
		return nil, err
	}
	return cleaned, nil
}

// APIDocOverridePatch 是单篇文档的修改：Markdown 为空表示恢复默认；
// Base 是编辑器载入时该文档的已保存正文（未改写为空），用于检测并发修改。
type APIDocOverridePatch struct {
	Markdown string `json:"markdown"`
	Base     string `json:"base"`
}

// ErrAPIDocConflict 表示该文档在编辑期间已被他人修改，需重新载入后再保存。
var ErrAPIDocConflict = infraerrors.Conflict("API_DOC_CONFLICT", "This document was changed by someone else; reload it before saving")

// apiDocPatchMu 串行化本进程内的读改写；支持原子更新的仓储还会在数据库事务内加锁，覆盖多实例。
var apiDocPatchMu sync.Mutex

// PatchAPIDocOverrides 只修改请求中的文档并与已保存集合合并：不同管理员改不同文档不会互相覆盖；
// 同一文档若已被他人修改（与 Base 不符）返回冲突，而不是静默覆盖。
// @project-doc docs/interfaces/api_reference.md#api_docs_markdown
func (s *SettingService) PatchAPIDocOverrides(ctx context.Context, patches map[string]APIDocOverridePatch) (map[string]string, error) {
	if s == nil || s.settingRepo == nil {
		return nil, infraerrors.ServiceUnavailable("SETTINGS_UNAVAILABLE", "Settings are unavailable")
	}
	if len(patches) == 0 || len(patches) > maxAPIDocOverrides {
		return nil, infraerrors.BadRequest("API_DOC_INVALID", "at least one document is required")
	}
	merge := func(raw string) (map[string]string, string, error) {
		current := map[string]string{}
		if strings.TrimSpace(raw) != "" {
			_ = json.Unmarshal([]byte(raw), &current)
		}
		for id, patch := range patches {
			if current[id] != patch.Base {
				return nil, "", ErrAPIDocConflict
			}
			current[id] = patch.Markdown
		}
		cleaned, encoded, err := validateAPIDocOverrides(current)
		return cleaned, encoded, err
	}
	apiDocPatchMu.Lock()
	defer apiDocPatchMu.Unlock()
	if atomic, ok := s.settingRepo.(SettingAtomicUpdater); ok {
		var result map[string]string
		err := atomic.UpdateMultiple(ctx, []string{SettingKeyAPIDocOverrides}, func(values map[string]string) (map[string]string, error) {
			cleaned, encoded, err := merge(values[SettingKeyAPIDocOverrides])
			if err != nil {
				return nil, err
			}
			result = cleaned
			return map[string]string{SettingKeyAPIDocOverrides: encoded}, nil
		})
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyAPIDocOverrides)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, err
	}
	cleaned, encoded, err := merge(raw)
	if err != nil {
		return nil, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyAPIDocOverrides, encoded); err != nil {
		return nil, err
	}
	return cleaned, nil
}

// validateAPIDocOverrides 校验并清理改写集合，返回清理结果与编码后的 JSON。
func validateAPIDocOverrides(overrides map[string]string) (map[string]string, string, error) {
	invalid := func(message string) error { return infraerrors.BadRequest("API_DOC_INVALID", message) }
	cleaned := map[string]string{}
	for id, markdown := range overrides {
		if !apiDocIDPattern.MatchString(id) {
			return nil, "", invalid("document id must use lowercase letters, digits, _ or - (max 128)")
		}
		if strings.TrimSpace(markdown) == "" {
			continue
		}
		if len([]rune(markdown)) > maxAPIDocMarkdownRunes {
			return nil, "", invalid("document " + id + " is too long")
		}
		cleaned[id] = markdown
	}
	if len(cleaned) > maxAPIDocOverrides {
		return nil, "", invalid("too many customized documents")
	}
	raw, err := json.Marshal(cleaned)
	if err != nil {
		return nil, "", err
	}
	if len(raw) > maxAPIDocOverrideBytes {
		return nil, "", invalid("customized documents are too large")
	}
	return cleaned, string(raw), nil
}
