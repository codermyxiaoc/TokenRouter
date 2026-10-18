package xai

import (
	"errors"
	"net/url"
	"strings"
)

// BuildVoiceURLWithValidator 与聊天/媒体共享 Base URL 规范化，仅空路径自动补 /v1。
// 显式反代前缀原样保留，声音资源逐段编码，不能借路径或查询参数改变凭据目的地。
func BuildVoiceURLWithValidator(baseURL, endpoint string, validator BaseURLValidator) (string, error) {
	base, err := validatedBaseURLWithValidator(baseURL, validator)
	if err != nil {
		return "", err
	}
	ep := strings.Trim(strings.TrimSpace(endpoint), "/")
	if ep == "" {
		return "", errors.New("voice endpoint is required")
	}
	parts := strings.Split(ep, "/")
	encoded := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" || part == "." || part == ".." {
			return "", errors.New("invalid voice endpoint path")
		}
		encoded = append(encoded, url.PathEscape(part))
	}
	return base + "/" + strings.Join(encoded, "/"), nil
}
