package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// DailyActivityHandler 只使用已认证用户身份，领取参数不接受金额、次数或其他用户编号。
// @project-doc docs/domains/daily_activities.md#daily_activity_claims
type DailyActivityHandler struct{ svc dailyActivityService }

type dailyActivityService interface {
	GetConfig(context.Context) (*service.DailyActivityConfig, error)
	SaveConfig(context.Context, service.DailyActivityConfig) (*service.DailyActivityConfig, error)
	Status(context.Context, int64) (*service.DailyActivityStatus, error)
	Claim(context.Context, int64, service.DailyActivityClaimInput) (*service.DailyActivityClaimResult, error)
	History(context.Context, int64, string, int, int) (*service.DailyActivityHistory, error)
}

func NewDailyActivityHandler(svc *service.DailyActivityService) *DailyActivityHandler {
	return &DailyActivityHandler{svc: svc}
}

// 管理员使用用户入口时仍领取本人奖励；管理配置另行校验管理员角色。
func dailyActivityActor(c *gin.Context, admin bool) (int64, bool) {
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

func (h *DailyActivityHandler) Status(c *gin.Context) {
	uid, ok := dailyActivityActor(c, false)
	if !ok {
		return
	}
	out, err := h.svc.Status(c.Request.Context(), uid)
	if !response.ErrorFrom(c, err) {
		response.Success(c, out)
	}
}

func (h *DailyActivityHandler) History(c *gin.Context) {
	uid, ok := dailyActivityActor(c, false)
	if !ok {
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 || page > 1000000 {
		response.BadRequest(c, "Invalid page")
		return
	}
	size, err := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || size < 1 || size > 100 {
		response.BadRequest(c, "Invalid page size")
		return
	}
	out, err := h.svc.History(c.Request.Context(), uid, c.Query("activity_type"), page, size)
	if !response.ErrorFrom(c, err) {
		response.Success(c, out)
	}
}

func (h *DailyActivityHandler) Claim(c *gin.Context) {
	uid, ok := dailyActivityActor(c, false)
	if !ok {
		return
	}
	var in service.DailyActivityClaimInput
	if !decodeDailyActivityInput(c, &in) {
		return
	}
	out, err := h.svc.Claim(c.Request.Context(), uid, in)
	if !response.ErrorFrom(c, err) {
		response.Success(c, out)
	}
}

func (h *DailyActivityHandler) GetConfig(c *gin.Context) {
	if _, ok := dailyActivityActor(c, true); !ok {
		return
	}
	out, err := h.svc.GetConfig(c.Request.Context())
	if !response.ErrorFrom(c, err) {
		response.Success(c, out)
	}
}

func (h *DailyActivityHandler) SaveConfig(c *gin.Context) {
	if _, ok := dailyActivityActor(c, true); !ok {
		return
	}
	var in service.DailyActivityConfig
	if !decodeDailyActivityInput(c, &in) {
		return
	}
	out, err := h.svc.SaveConfig(c.Request.Context(), in)
	if !response.ErrorFrom(c, err) {
		response.Success(c, out)
	}
}

// 限制请求体并拒绝未知字段和拼接 JSON，避免客户端误以为伪造的奖励参数生效。
func decodeDailyActivityInput(c *gin.Context, target any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		response.BadRequest(c, "Invalid daily activity request")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		response.BadRequest(c, "Invalid daily activity request")
		return false
	}
	return true
}
