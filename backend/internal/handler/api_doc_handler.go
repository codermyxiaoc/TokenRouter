package handler

import (
	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// GetAPIDocCatalog 返回 API 文档目录叠加（已隐藏的条目不公开），供公开文档页合并内置目录。
// GET /api/v1/settings/api-docs
func (h *SettingHandler) GetAPIDocCatalog(c *gin.Context) {
	catalog, err := h.apiDocs.Catalog(c.Request.Context(), true)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, catalog)
}

// GetAPIDocDocument 返回单篇文档叠加（含改写正文）；没有叠加或已隐藏时返回 404，前端回退内置正文。
// GET /api/v1/settings/api-docs/:id
func (h *SettingHandler) GetAPIDocDocument(c *gin.Context) {
	doc, err := h.apiDocs.Document(c.Request.Context(), c.Param("id"), true)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, doc)
}
