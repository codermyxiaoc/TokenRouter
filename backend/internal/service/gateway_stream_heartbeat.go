package service

import "github.com/gin-gonic/gin"

const gatewayStreamHeartbeatBytesKey = "gateway_stream_heartbeat_bytes"

// RecordGatewayStreamHeartbeat 只累计网关自己成功写出的 SSE 心跳字节。
// 用户/账号排队与普通流保活共用计数，上游事件不能借此绕过安全重放边界。
func RecordGatewayStreamHeartbeat(c *gin.Context, written int) {
	if c == nil || written <= 0 {
		return
	}
	current := c.GetInt(gatewayStreamHeartbeatBytesKey)
	c.Set(gatewayStreamHeartbeatBytesKey, current+written)
}

// GatewayStreamHasOnlyHeartbeats 保留响应头已提交的真实状态，只判断是否没有业务字节。
func GatewayStreamHasOnlyHeartbeats(c *gin.Context) bool {
	return c != nil && c.Writer != nil &&
		OpenAICompactKeepaliveAdjustedWrittenSize(c) < 0 && c.Writer.Written()
}

// ResetGatewayStreamOutputAccounting 仅在换成全新尝试写入器后调用。
// Gin.Copy 会复制上下文键；新分组必须清除旧字节与心跳器引用，同一写入器内换账号则保留。
// @project-doc docs/domains/smart_routing_api_keys.md#group_failover
func ResetGatewayStreamOutputAccounting(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(gatewayStreamHeartbeatBytesKey, 0)
	c.Set(openAICompactSSEKeepaliveKey, nil)
}
