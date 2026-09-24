package handler

import (
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// MediaTaskHandler 只查询元数据，不调用上游或执行费用结算。
type MediaTaskHandler struct{ service *service.MediaTaskService }

func NewMediaTaskHandler(s *service.MediaTaskService) *MediaTaskHandler {
	return &MediaTaskHandler{service: s}
}

func mediaTaskActor(c *gin.Context, admin bool) (service.MediaTaskActor, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return service.MediaTaskActor{}, false
	}
	if admin {
		role, _ := middleware.GetUserRoleFromContext(c)
		if role != "admin" {
			response.Forbidden(c, "Administrator access required")
			return service.MediaTaskActor{}, false
		}
	}
	return service.MediaTaskActor{UserID: subject.UserID, IsAdmin: admin}, true
}

func (h *MediaTaskHandler) List(c *gin.Context)      { h.list(c, false) }
func (h *MediaTaskHandler) AdminList(c *gin.Context) { h.list(c, true) }
func (h *MediaTaskHandler) Get(c *gin.Context)       { h.get(c, false) }
func (h *MediaTaskHandler) AdminGet(c *gin.Context)  { h.get(c, true) }

func (h *MediaTaskHandler) list(c *gin.Context, admin bool) {
	actor, ok := mediaTaskActor(c, admin)
	if !ok {
		return
	}
	page, errPage := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, errSize := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	userID, errUser := strconv.ParseInt(c.DefaultQuery("user_id", "0"), 10, 64)
	if errPage != nil || errSize != nil || (admin && errUser != nil) {
		response.BadRequest(c, "Invalid pagination or user identifier")
		return
	}
	result, err := h.service.List(c.Request.Context(), actor, service.MediaTaskFilter{
		Page: page, PageSize: size, UserID: userID, MediaType: c.Query("media_type"),
		Source: c.Query("source"), Status: c.Query("status"), Model: c.Query("model"),
	})
	if !response.ErrorFrom(c, err) {
		c.Header("Cache-Control", "no-store")
		response.Success(c, result)
	}
}

func (h *MediaTaskHandler) get(c *gin.Context, admin bool) {
	actor, ok := mediaTaskActor(c, admin)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid media task identifier")
		return
	}
	result, err := h.service.Get(c.Request.Context(), actor, id)
	if !response.ErrorFrom(c, err) {
		c.Header("Cache-Control", "no-store")
		response.Success(c, result)
	}
}
