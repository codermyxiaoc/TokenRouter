package handler

import (
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// ChanshuiHandler 的全部入口同时依赖管理员路由与显式角色检查，不提供用户投影。
// @project-doc docs/domains/chanshui_tests.md#chanshui_access
type ChanshuiHandler struct{ svc *service.ChanshuiService }

func NewChanshuiHandler(svc *service.ChanshuiService) *ChanshuiHandler {
	return &ChanshuiHandler{svc: svc}
}
func (h *ChanshuiHandler) List(c *gin.Context) {
	if _, ok := intelligenceActor(c, true); !ok {
		return
	}
	data, err := h.svc.AdminList(c.Request.Context())
	if !response.ErrorFrom(c, err) {
		response.Success(c, gin.H{"enabled": h.svc.IsEnabled(c.Request.Context()), "configs": data})
	}
}
func (h *ChanshuiHandler) Targets(c *gin.Context) {
	if _, ok := intelligenceActor(c, true); !ok {
		return
	}
	data, err := h.svc.Targets(c.Request.Context())
	if !response.ErrorFrom(c, err) {
		response.Success(c, data)
	}
}
func (h *ChanshuiHandler) Save(c *gin.Context) {
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
	var in service.ChanshuiConfigInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "Invalid account audit configuration")
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
func (h *ChanshuiHandler) Delete(c *gin.Context) {
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
func (h *ChanshuiHandler) Run(c *gin.Context) {
	if _, ok := intelligenceActor(c, true); !ok {
		return
	}
	id, ok := intelligenceConfigID(c)
	if !ok {
		return
	}
	data, err := h.svc.Run(c.Request.Context(), id)
	if !response.ErrorFrom(c, err) {
		response.Accepted(c, data)
	}
}
func (h *ChanshuiHandler) History(c *gin.Context) {
	if _, ok := intelligenceActor(c, true); !ok {
		return
	}
	id, ok := intelligenceConfigID(c)
	if !ok {
		return
	}
	data, err := h.svc.AdminRuns(c.Request.Context(), id)
	if !response.ErrorFrom(c, err) {
		response.Success(c, data)
	}
}
func (h *ChanshuiHandler) Detail(c *gin.Context) {
	if _, ok := intelligenceActor(c, true); !ok {
		return
	}
	data, err := h.svc.AdminDetail(c.Request.Context(), c.Param("id"))
	if !response.ErrorFrom(c, err) {
		response.Success(c, data)
	}
}
