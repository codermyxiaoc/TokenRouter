package service

import (
	"context"
	"regexp"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

// API 文档目录采用“内置 + 叠加”：内置分类与文档由前端生成，数据库只保存管理员改动或新增的条目。
// 可空字段为 nil 表示沿用内置值；自定义条目（Custom）没有内置内容可回退，必须完整。
// @project-doc docs/interfaces/api_reference.md#api_docs_markdown

const (
	// 单篇正文、标题长度与条目数量上限，避免公开接口返回过大的目录或正文。
	maxAPIDocMarkdownRunes = 200000
	maxAPIDocTitleRunes    = 200
	maxAPIDocEntries       = 1000
)

var apiDocIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

var (
	ErrAPIDocNotFound = infraerrors.NotFound("API_DOC_NOT_FOUND", "API document not found")
	// ErrAPIDocConflict 表示条目在编辑期间已被他人修改（或已存在），需重新载入后再保存。
	ErrAPIDocConflict = infraerrors.Conflict("API_DOC_CONFLICT", "This document was changed by someone else; reload it before saving")
	// ErrAPIDocCategoryNotEmpty 表示自定义分类下仍有文档，删除前须先移走或删除这些文档。
	ErrAPIDocCategoryNotEmpty = infraerrors.Conflict("API_DOC_CATEGORY_NOT_EMPTY", "Move or delete the documents in this category first")
)

// APIDocCategory 是分类的叠加记录。
type APIDocCategory struct {
	ID        string    `json:"id"`
	Title     *string   `json:"title"`
	Sort      *int      `json:"sort"`
	Hidden    bool      `json:"hidden"`
	Custom    bool      `json:"custom"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// APIDocEntry 是文档的叠加记录；目录接口不携带正文，HasMarkdown 表示存在改写正文。
type APIDocEntry struct {
	ID          string    `json:"id"`
	CategoryID  *string   `json:"category_id"`
	Title       *string   `json:"title"`
	Markdown    *string   `json:"markdown,omitempty"`
	HasMarkdown bool      `json:"has_markdown"`
	Sort        *int      `json:"sort"`
	Hidden      bool      `json:"hidden"`
	Custom      bool      `json:"custom"`
	Version     int64     `json:"version"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// APIDocCatalog 是全部叠加记录；公开接口会去掉已隐藏的条目。
type APIDocCatalog struct {
	Categories []APIDocCategory `json:"categories"`
	Documents  []APIDocEntry    `json:"documents"`
}

// APIDocCategoryInput 保存分类叠加；Version 为 0 表示新建（已存在则冲突）。
type APIDocCategoryInput struct {
	Title   *string `json:"title"`
	Hidden  bool    `json:"hidden"`
	Custom  bool    `json:"custom"`
	Version int64   `json:"version"`
}

// APIDocEntryInput 保存文档叠加；Version 为 0 表示新建（已存在则冲突）。排序由单独的排序接口维护。
type APIDocEntryInput struct {
	CategoryID *string `json:"category_id"`
	Title      *string `json:"title"`
	Markdown   *string `json:"markdown"`
	Hidden     bool    `json:"hidden"`
	Custom     bool    `json:"custom"`
	Version    int64   `json:"version"`
}

// APIDocOrderInput 按给定顺序重排分类或某分类内的文档；未列出的条目保持原排序。
type APIDocOrderInput struct {
	Categories []string            `json:"categories"`
	Documents  map[string][]string `json:"documents"`
}

// APIDocRepository 持久化叠加记录；保存与删除以 version 做乐观并发控制。
type APIDocRepository interface {
	Catalog(ctx context.Context) (*APIDocCatalog, error)
	GetDocument(ctx context.Context, id string) (*APIDocEntry, error)
	SaveCategory(ctx context.Context, id string, in APIDocCategoryInput, actorID int64) (*APIDocCategory, error)
	DeleteCategory(ctx context.Context, id string, version int64) error
	SaveDocument(ctx context.Context, id string, in APIDocEntryInput, actorID int64) (*APIDocEntry, error)
	DeleteDocument(ctx context.Context, id string, version int64) error
	Reorder(ctx context.Context, in APIDocOrderInput, actorID int64) error
}

// APIDocService 校验管理员输入后交给仓储；公开读取过滤隐藏条目。
type APIDocService struct {
	repo APIDocRepository
}

func NewAPIDocService(repo APIDocRepository) *APIDocService {
	return &APIDocService{repo: repo}
}

func apiDocInvalid(message string) error {
	return infraerrors.BadRequest("API_DOC_INVALID", message)
}

func validateAPIDocID(id string) error {
	if !apiDocIDPattern.MatchString(id) {
		return apiDocInvalid("id must use lowercase letters, digits, _ or - (max 128)")
	}
	return nil
}

// normalizeAPIDocTitle 去掉首尾空白；空标题按“沿用内置”处理（nil）。
func normalizeAPIDocTitle(title *string) (*string, error) {
	if title == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*title)
	if trimmed == "" {
		return nil, nil
	}
	if len([]rune(trimmed)) > maxAPIDocTitleRunes {
		return nil, apiDocInvalid("title is too long")
	}
	return &trimmed, nil
}

// Catalog 返回目录叠加；publicOnly 时去掉隐藏条目，访客看不到已删除的内容。
func (s *APIDocService) Catalog(ctx context.Context, publicOnly bool) (*APIDocCatalog, error) {
	catalog, err := s.repo.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	if catalog.Categories == nil {
		catalog.Categories = []APIDocCategory{}
	}
	if catalog.Documents == nil {
		catalog.Documents = []APIDocEntry{}
	}
	if !publicOnly {
		return catalog, nil
	}
	// 公开目录保留隐藏条目的隐藏标记：内置文档/分类在前端重建，若在这里删掉叠加，
	// 前端就看不到“已隐藏”，会重新显示内置内容。目录本身不含正文，保留标记不泄漏内容；
	// 单篇正文仍由 Document 按文档与所属分类的隐藏状态拒绝。
	return catalog, nil
}

// Document 返回单篇叠加（含正文）；publicOnly 时隐藏条目视为不存在。
func (s *APIDocService) Document(ctx context.Context, id string, publicOnly bool) (*APIDocEntry, error) {
	if err := validateAPIDocID(id); err != nil {
		return nil, ErrAPIDocNotFound
	}
	doc, err := s.repo.GetDocument(ctx, id)
	if err != nil {
		return nil, err
	}
	if !publicOnly {
		return doc, nil
	}
	if doc.Hidden {
		return nil, ErrAPIDocNotFound
	}
	// 隐藏分类下的文档对访客同样不存在，不能靠已知地址直达读到草稿。
	if doc.CategoryID != nil {
		hidden, err := s.categoryHidden(ctx, *doc.CategoryID)
		if err != nil {
			return nil, err
		}
		if hidden {
			return nil, ErrAPIDocNotFound
		}
	}
	return doc, nil
}

// categoryHidden 查询分类是否被隐藏；没有叠加记录的内置分类视为不隐藏。
func (s *APIDocService) categoryHidden(ctx context.Context, id string) (bool, error) {
	catalog, err := s.repo.Catalog(ctx)
	if err != nil {
		return false, err
	}
	for _, category := range catalog.Categories {
		if category.ID == id {
			return category.Hidden, nil
		}
	}
	return false, nil
}

// SaveCategory 新建或修改分类叠加。
func (s *APIDocService) SaveCategory(ctx context.Context, id string, in APIDocCategoryInput, actorID int64) (*APIDocCategory, error) {
	if err := validateAPIDocID(id); err != nil {
		return nil, err
	}
	title, err := normalizeAPIDocTitle(in.Title)
	if err != nil {
		return nil, err
	}
	if in.Custom && title == nil {
		return nil, apiDocInvalid("custom category requires a title")
	}
	if in.Version < 0 {
		return nil, apiDocInvalid("invalid version")
	}
	in.Title = title
	return s.repo.SaveCategory(ctx, id, in, actorID)
}

// DeleteCategory 删除叠加记录：自定义分类即被删除，内置分类恢复默认。
func (s *APIDocService) DeleteCategory(ctx context.Context, id string, version int64) error {
	if err := validateAPIDocID(id); err != nil {
		return err
	}
	return s.repo.DeleteCategory(ctx, id, version)
}

// SaveDocument 新建或修改文档叠加；空正文表示沿用内置正文。
func (s *APIDocService) SaveDocument(ctx context.Context, id string, in APIDocEntryInput, actorID int64) (*APIDocEntry, error) {
	if err := validateAPIDocID(id); err != nil {
		return nil, err
	}
	title, err := normalizeAPIDocTitle(in.Title)
	if err != nil {
		return nil, err
	}
	in.Title = title
	if in.CategoryID != nil {
		if *in.CategoryID == "" {
			in.CategoryID = nil
		} else if err := validateAPIDocID(*in.CategoryID); err != nil {
			return nil, apiDocInvalid("invalid category id")
		}
	}
	if in.Markdown != nil {
		if strings.TrimSpace(*in.Markdown) == "" {
			in.Markdown = nil
		} else if len([]rune(*in.Markdown)) > maxAPIDocMarkdownRunes {
			return nil, apiDocInvalid("document is too long")
		}
	}
	if in.Custom && (in.Title == nil || in.CategoryID == nil || in.Markdown == nil) {
		return nil, apiDocInvalid("custom document requires a title, category and markdown")
	}
	if in.Version < 0 {
		return nil, apiDocInvalid("invalid version")
	}
	return s.repo.SaveDocument(ctx, id, in, actorID)
}

// DeleteDocument 删除叠加记录：自定义文档即被删除，内置文档恢复默认。
func (s *APIDocService) DeleteDocument(ctx context.Context, id string, version int64) error {
	if err := validateAPIDocID(id); err != nil {
		return err
	}
	return s.repo.DeleteDocument(ctx, id, version)
}

// Reorder 校验 ID 后整体写入新顺序，同一请求内不允许重复。
func (s *APIDocService) Reorder(ctx context.Context, in APIDocOrderInput, actorID int64) error {
	total := len(in.Categories)
	// 同一文档出现在多个分类时仓储会按遍历顺序反复改排序，最终顺序不确定，必须在入口拒绝。
	global := map[string]bool{}
	check := func(ids []string) error {
		seen := map[string]bool{}
		for _, id := range ids {
			if err := validateAPIDocID(id); err != nil {
				return err
			}
			if seen[id] || global[id] {
				return apiDocInvalid("duplicate id in order")
			}
			seen[id], global[id] = true, true
		}
		return nil
	}
	if err := check(in.Categories); err != nil {
		return err
	}
	for category, ids := range in.Documents {
		if err := validateAPIDocID(category); err != nil {
			return err
		}
		if err := check(ids); err != nil {
			return err
		}
		total += len(ids)
	}
	if total == 0 {
		return apiDocInvalid("nothing to reorder")
	}
	if total > maxAPIDocEntries {
		return apiDocInvalid("too many entries")
	}
	return s.repo.Reorder(ctx, in, actorID)
}
