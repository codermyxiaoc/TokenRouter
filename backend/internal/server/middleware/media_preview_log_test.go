package middleware

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 未授权或已失效票据同样必须脱敏，不能等认证成功后才隐藏路径中的秘密。
func TestMediaTaskPreviewLogPathRedactsTicket(t *testing.T) {
	for _, token := range []string{strings.Repeat("a", 64), "invalid-ticket", "invalid/nested"} {
		require.Equal(t, "/api/v1/media-tasks/preview-content/:ticket", redactMediaPreviewPath("/api/v1/media-tasks/preview-content/"+token))
	}
	require.Equal(t, "/api/v1/media-tasks/123/preview", redactMediaPreviewPath("/api/v1/media-tasks/123/preview"))
}
