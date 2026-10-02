package service

// sanitizeVideoGroup 清理共用表单携带的文本默认值，避免视频分组进入聊天回退或主动探活链路。
func sanitizeVideoGroup(group *Group) {
	if group == nil || group.Platform != PlatformVideo {
		return
	}
	group.SchedulerType = GroupSchedulerTypeBasic
	group.AdvancedSchedulerOverrides = GroupAdvancedSchedulerOverrides{}
	group.SessionIsolationEnabled = false
	group.ClaudeCodeOnly = false
	group.MCPXMLInject = false
	group.RequireOAuthOnly = false
	group.RequirePrivacySet = false
	group.AllowLive = false
	group.FallbackGroupID = nil
	group.FallbackGroupIDOnInvalidRequest = nil
	group.UnavailableFallbackGroupID = nil
	group.AvailabilityProbeConfig = GroupAvailabilityProbeConfig{}
	group.AllowedClientProtocols = []GroupClientProtocol{}
}
