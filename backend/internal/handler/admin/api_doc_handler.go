package admin

import (
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// SetAPIDocService 注入 API 文档目录管理服务。
func (h *SettingHandler) SetAPIDocService(apiDocs *service.APIDocService) {
	h.apiDocs = apiDocs
}

// apiDocActor 返回当前管理员 ID，用于记录最后修改人。
func apiDocActor(c *gin.Context) int64 {
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		return subject.UserID
	}
	return 0
}

// GetAPIDocCatalog 返回全部叠加记录（含已隐藏条目），供后台管理目录。
// GET /api/v1/admin/settings/api-docs
func (h *SettingHandler) GetAPIDocCatalog(c *gin.Context) {
	catalog, err := h.apiDocs.Catalog(c.Request.Context(), false)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, catalog)
}

// GetAPIDocDocument 返回单篇叠加（含正文与版本），没有叠加时返回 404。
// GET /api/v1/admin/settings/api-docs/documents/:id
func (h *SettingHandler) GetAPIDocDocument(c *gin.Context) {
	doc, err := h.apiDocs.Document(c.Request.Context(), c.Param("id"), false)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, doc)
}

// SaveAPIDocDocument 新建（version=0）或修改文档；他人已修改时返回 409。
// PUT /api/v1/admin/settings/api-docs/documents/:id
func (h *SettingHandler) SaveAPIDocDocument(c *gin.Context) {
	var req service.APIDocEntryInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	doc, err := h.apiDocs.SaveDocument(c.Request.Context(), c.Param("id"), req, apiDocActor(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, doc)
}

// DeleteAPIDocDocument 删除自定义文档或把内置文档恢复默认；需携带 ?version= 做冲突检测。
// DELETE /api/v1/admin/settings/api-docs/documents/:id
func (h *SettingHandler) DeleteAPIDocDocument(c *gin.Context) {
	version, err := strconv.ParseInt(c.Query("version"), 10, 64)
	if err != nil || version <= 0 {
		response.BadRequest(c, "Invalid version")
		return
	}
	if err := h.apiDocs.DeleteDocument(c.Request.Context(), c.Param("id"), version); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// SaveAPIDocCategory 新建（version=0）或修改分类；他人已修改时返回 409。
// PUT /api/v1/admin/settings/api-docs/categories/:id
func (h *SettingHandler) SaveAPIDocCategory(c *gin.Context) {
	var req service.APIDocCategoryInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	category, err := h.apiDocs.SaveCategory(c.Request.Context(), c.Param("id"), req, apiDocActor(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, category)
}

// DeleteAPIDocCategory 删除自定义分类（需先清空）或把内置分类恢复默认；需携带 ?version=。
// DELETE /api/v1/admin/settings/api-docs/categories/:id
func (h *SettingHandler) DeleteAPIDocCategory(c *gin.Context) {
	version, err := strconv.ParseInt(c.Query("version"), 10, 64)
	if err != nil || version <= 0 {
		response.BadRequest(c, "Invalid version")
		return
	}
	if err := h.apiDocs.DeleteCategory(c.Request.Context(), c.Param("id"), version); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// ReorderAPIDocs 保存分类顺序及分类内文档顺序。
// PUT /api/v1/admin/settings/api-docs/order
func (h *SettingHandler) ReorderAPIDocs(c *gin.Context) {
	var req service.APIDocOrderInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.apiDocs.Reorder(c.Request.Context(), req, apiDocActor(c)); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"saved": true})
}
