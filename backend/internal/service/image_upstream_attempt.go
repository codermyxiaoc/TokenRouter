package service

import "github.com/gin-gonic/gin"

const imageUpstreamAttemptCountKey = "image_upstream_attempt_count"

// ImageUpstreamAttemptCount 只统计本次请求实际交给 HTTP 客户端的图片生成调用。
func ImageUpstreamAttemptCount(c *gin.Context) int {
	if c == nil {
		return 0
	}
	return c.GetInt(imageUpstreamAttemptCountKey)
}

// markImageUpstreamAttempt 在调用 HTTP 客户端前递增，内部协议回退不能覆盖先前发送记录。
func markImageUpstreamAttempt(c *gin.Context) {
	if c != nil {
		c.Set(imageUpstreamAttemptCountKey, ImageUpstreamAttemptCount(c)+1)
	}
}
