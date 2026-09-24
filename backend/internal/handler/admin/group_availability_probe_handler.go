package admin

import (
	"context"
	"strconv"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// groupAvailabilityProbeRunner 保留窄接口，方便隔离验证管理端配置保存与执行边界。
type groupAvailabilityProbeRunner interface {
	RunOnce(context.Context, int64, service.GroupAvailabilityProbeConfig) (*service.GroupAvailabilityProbeResult, error)
}

// TestAvailabilityProbe 只保存探测配置并执行一次；不提交弹窗内其它分组草稿。
func (h *GroupHandler) TestAvailabilityProbe(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	var req struct {
		Config *service.GroupAvailabilityProbeConfig `json:"availability_probe_config" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if !req.Config.Enabled {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_AVAILABILITY_PROBE_CONFIG", "Enable availability probing before testing"))
		return
	}
	if h.availabilityProbeRunner == nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("GROUP_AVAILABILITY_PROBE_UNAVAILABLE", "Availability probe service is unavailable"))
		return
	}
	group, err := h.adminService.GetGroup(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !group.IsActive() {
		response.ErrorFrom(c, infraerrors.BadRequest("GROUP_AVAILABILITY_PROBE_INACTIVE", "Activate this group before testing"))
		return
	}
	if err := service.ValidateGroupAvailabilityProbeProtocol(group.Platform, req.Config.Protocol); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_AVAILABILITY_PROBE_CONFIG", err.Error()))
		return
	}
	// 配置保存与租约领取交给同一事务，忙碌时不能改配置，也不能整行写回分组业务字段。
	result, err := h.availabilityProbeRunner.RunOnce(c.Request.Context(), groupID, *req.Config)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// 探测失败也是已成功保存的观测结果，使用 success/status 表达，避免客户端误重发。
	response.Success(c, result)
}
