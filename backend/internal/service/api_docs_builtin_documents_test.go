//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// apiDocMemoryRepo 只提供公开读取所需的目录与单篇查询。
type apiDocMemoryRepo struct {
	APIDocRepository
	catalog APIDocCatalog
}

func (r *apiDocMemoryRepo) Catalog(context.Context) (*APIDocCatalog, error) {
	copy := r.catalog
	return &copy, nil
}

func (r *apiDocMemoryRepo) GetDocument(_ context.Context, id string) (*APIDocEntry, error) {
	for _, doc := range r.catalog.Documents {
		if doc.ID == id {
			copy := doc
			return &copy, nil
		}
	}
	return nil, ErrAPIDocNotFound
}

// 内置文档改写正文但沿用默认分类（category_id 为空）时，默认分类或其上级被隐藏，访客同样读不到改写正文。
func TestAPIDocBuiltinDocumentFollowsDefaultCategoryVisibility(t *testing.T) {
	markdown := "# 改写"
	repo := &apiDocMemoryRepo{catalog: APIDocCatalog{Documents: []APIDocEntry{
		{ID: "responses", Markdown: &markdown, HasMarkdown: true},
		{ID: "video-create", Markdown: &markdown, HasMarkdown: true},
	}}}
	svc := NewAPIDocService(repo)
	ctx := context.Background()
	for _, id := range []string{"responses", "video-create"} {
		_, err := svc.Document(ctx, id, true)
		require.NoError(t, err, id)
	}
	// 隐藏一级分类 text：直接归属的内置文档不公开。
	repo.catalog.Categories = []APIDocCategory{{ID: "text", Hidden: true}}
	_, err := svc.Document(ctx, "responses", true)
	require.ErrorIs(t, err, ErrAPIDocNotFound)
	_, err = svc.Document(ctx, "responses", false)
	require.NoError(t, err, "管理端仍可读取")
	// 隐藏一级分类 video：其内置二级分类下的内置文档同样不公开。
	_, err = svc.Document(ctx, "video-create", true)
	require.NoError(t, err)
	repo.catalog.Categories = []APIDocCategory{{ID: "video", Hidden: true}}
	_, err = svc.Document(ctx, "video-create", true)
	require.ErrorIs(t, err, ErrAPIDocNotFound)
	// 显式改到可见分类时以叠加分类为准。
	moved := "text"
	repo.catalog.Documents[1].CategoryID = &moved
	_, err = svc.Document(ctx, "video-create", true)
	require.NoError(t, err)
}
