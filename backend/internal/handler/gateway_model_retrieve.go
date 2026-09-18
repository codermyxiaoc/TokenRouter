package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// writeGatewayModelsResponse 从列表最终投影中检索单个模型，权限、别名和平台元数据与列表完全一致。
// 通配路由只删除 Gin 添加的一个前导斜杠，不清理路径或二次解码，保留 vendor/model 与复合 Key 前缀。
func writeGatewayModelsResponse(c *gin.Context, models any) {
	requested, retrieve := c.Params.Get("model")
	if !retrieve {
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": models})
		return
	}
	requested = strings.TrimPrefix(requested, "/")
	encoded, err := json.Marshal(models)
	var entries []json.RawMessage
	if err != nil || json.Unmarshal(encoded, &entries) != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "server_error", "code": "model_catalog_error", "message": "Unable to read model catalog",
		}})
		return
	}
	for _, entry := range entries {
		var item struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(entry, &item) == nil && requested != "" && item.ID == requested {
			c.Data(http.StatusOK, "application/json; charset=utf-8", entry)
			return
		}
	}
	// 不存在和不可见统一返回 404，不能借单模型接口探测其它分组的目录。
	c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
		"type": "invalid_request_error", "code": "model_not_found", "param": "model",
		"message": "The requested model is not available in the visible catalog",
	}})
}
