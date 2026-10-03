package handler

import (
	"net/http"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// IntelligenceHandler 对管理配置与用户观测分别校验身份，永不接受用户触发收费检测。
type IntelligenceHandler struct {
	svc     *service.IntelligenceService
	preview *intelligencePreviewAccess
}

func NewIntelligenceHandler(svc *service.IntelligenceService) *IntelligenceHandler {
	return &IntelligenceHandler{svc: svc}
}

func intelligenceActor(c *gin.Context, admin bool) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	if admin {
		role, _ := middleware.GetUserRoleFromContext(c)
		if role != service.RoleAdmin {
			response.Forbidden(c, "Administrator access required")
			return 0, false
		}
	}
	return subject.UserID, true
}
func intelligenceConfigID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid intelligence test identifier")
		return 0, false
	}
	return id, true
}
func (h *IntelligenceHandler) AdminList(c *gin.Context) {
	if _, ok := intelligenceActor(c, true); !ok {
		return
	}
	data, err := h.svc.AdminList(c.Request.Context())
	if !response.ErrorFrom(c, err) {
		response.Success(c, data)
	}
}
func (h *IntelligenceHandler) AdminSave(c *gin.Context) {
	if _, ok := intelligenceActor(c, true); !ok {
		return
	}
	var id int64
	if c.Param("id") != "" {
		var ok bool
		id, ok = intelligenceConfigID(c)
		if !ok {
			return
		}
	}
	var in service.IntelligenceConfigInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "Invalid intelligence test configuration")
		return
	}
	data, err := h.svc.SaveConfig(c.Request.Context(), id, in)
	if !response.ErrorFrom(c, err) {
		if id == 0 {
			response.Created(c, data)
		} else {
			response.Success(c, data)
		}
	}
}
func (h *IntelligenceHandler) AdminDelete(c *gin.Context) {
	if _, ok := intelligenceActor(c, true); !ok {
		return
	}
	id, ok := intelligenceConfigID(c)
	if !ok {
		return
	}
	if !response.ErrorFrom(c, h.svc.DeleteConfig(c.Request.Context(), id)) {
		response.Success(c, gin.H{"deleted": true})
	}
}
func (h *IntelligenceHandler) AdminRun(c *gin.Context) {
	if _, ok := intelligenceActor(c, true); !ok {
		return
	}
	id, ok := intelligenceConfigID(c)
	if !ok {
		return
	}
	run, err := h.svc.Run(c.Request.Context(), id)
	if !response.ErrorFrom(c, err) {
		response.Accepted(c, run)
	}
}
func (h *IntelligenceHandler) AdminRuns(c *gin.Context) {
	if _, ok := intelligenceActor(c, true); !ok {
		return
	}
	id, ok := intelligenceConfigID(c)
	if !ok {
		return
	}
	runs, err := h.svc.AdminRuns(c.Request.Context(), id)
	if !response.ErrorFrom(c, err) {
		response.Success(c, runs)
	}
}
func (h *IntelligenceHandler) AdminDetail(c *gin.Context) {
	if _, ok := intelligenceActor(c, true); !ok {
		return
	}
	run, err := h.svc.AdminDetail(c.Request.Context(), c.Param("id"))
	if !response.ErrorFrom(c, err) {
		response.Success(c, run)
	}
}
func (h *IntelligenceHandler) UserList(c *gin.Context) {
	id, ok := intelligenceActor(c, false)
	if !ok {
		return
	}
	data, err := h.svc.UserList(c.Request.Context(), id)
	if !response.ErrorFrom(c, err) {
		response.Success(c, data)
	}
}
func (h *IntelligenceHandler) UserDetail(c *gin.Context) {
	id, ok := intelligenceActor(c, false)
	if !ok {
		return
	}
	data, err := h.svc.UserDetail(c.Request.Context(), id, c.Param("id"))
	if !response.ErrorFrom(c, err) {
		response.Success(c, data)
	}
}
