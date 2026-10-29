package admin

import (
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// VideoProtocolProfileHandler 管理声明式视频协议模板：保存新版本、启停版本与离线调试预览。
type VideoProtocolProfileHandler struct {
	service *service.VideoProtocolProfileService
	ai      *service.VideoProfileAIService
}

// NewVideoProtocolProfileHandler 创建视频协议模板处理器。
func NewVideoProtocolProfileHandler(service *service.VideoProtocolProfileService, ai *service.VideoProfileAIService) *VideoProtocolProfileHandler {
	return &VideoProtocolProfileHandler{service: service, ai: ai}
}

// List 返回全部模板版本。
// GET /api/v1/admin/video-protocol-profiles
func (h *VideoProtocolProfileHandler) List(c *gin.Context) {
	profiles, err := h.service.List(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, profiles)
}

// Save 校验模板并保存为新版本；可选择同时启用。
// POST /api/v1/admin/video-protocol-profiles
func (h *VideoProtocolProfileHandler) Save(c *gin.Context) {
	var req service.VideoProtocolProfileInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	var createdBy int64
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		createdBy = subject.UserID
	}
	profile, err := h.service.Save(c.Request.Context(), req, createdBy)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, profile)
}

// videoProtocolProfileStatusRequest 是启停版本的请求。
type videoProtocolProfileStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

// SetStatus 启用、停用或改回草稿；启用时同键名原启用版本降为草稿。
// PUT /api/v1/admin/video-protocol-profiles/:key/versions/:version/status
func (h *VideoProtocolProfileHandler) SetStatus(c *gin.Context) {
	version, err := strconv.Atoi(c.Param("version"))
	if err != nil || version <= 0 {
		response.BadRequest(c, "Invalid profile version")
		return
	}
	var req videoProtocolProfileStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.service.SetStatus(c.Request.Context(), c.Param("key"), version, req.Status); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"profile_key": c.Param("key"), "version": version, "status": req.Status})
}

// Delete 删除模板的单个版本；DeleteProfile 删除整个模板。仍被账号绑定或版本启用中时返回 409。
// DELETE /api/v1/admin/video-protocol-profiles/:key/versions/:version
func (h *VideoProtocolProfileHandler) Delete(c *gin.Context) {
	version, err := strconv.Atoi(c.Param("version"))
	if err != nil || version <= 0 {
		response.BadRequest(c, "Invalid profile version")
		return
	}
	if err := h.service.Delete(c.Request.Context(), c.Param("key"), version); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"profile_key": c.Param("key"), "version": version})
}

// DeleteProfile 删除整个模板的全部版本。
// DELETE /api/v1/admin/video-protocol-profiles/:key
func (h *VideoProtocolProfileHandler) DeleteProfile(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), c.Param("key"), 0); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"profile_key": c.Param("key")})
}

// Preview 用请求与响应样例离线调试模板，不访问任何上游。
// POST /api/v1/admin/video-protocol-profiles/preview
func (h *VideoProtocolProfileHandler) Preview(c *gin.Context) {
	var req service.VideoProfilePreviewInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	preview, err := h.service.Preview(req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, preview)
}

// GetAIConfig 返回模板 AI 生成配置，不回显密钥。
// GET /api/v1/admin/video-protocol-profiles/ai-config
func (h *VideoProtocolProfileHandler) GetAIConfig(c *gin.Context) {
	cfg, err := h.ai.GetConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// UpdateAIConfig 保存模板 AI 生成配置；密钥留空表示保留原值。
// PUT /api/v1/admin/video-protocol-profiles/ai-config
func (h *VideoProtocolProfileHandler) UpdateAIConfig(c *gin.Context) {
	var req service.VideoProfileAIConfigInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	cfg, err := h.ai.UpdateConfig(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// Generate 把上游文档交给 AI 生成模板草稿；结果只回填编辑器，不保存也不启用。
// POST /api/v1/admin/video-protocol-profiles/ai-generate
func (h *VideoProtocolProfileHandler) Generate(c *gin.Context) {
	var req service.VideoProfileAIGenerateInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.ai.Generate(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
