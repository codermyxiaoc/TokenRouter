package handler

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/tidwall/gjson"
)

// validateAsyncImagePrompt 在受理任务前校验提示词，避免无效请求进入生成队列或触发上游调用。
// 只检查异步接口的必填字段，不改写正文，也不改变同步图片的协议、模型映射或计费规则。
func validateAsyncImagePrompt(contentType string, body []byte) error {
	mediaType, params, mediaErr := mime.ParseMediaType(contentType)
	if strings.EqualFold(mediaType, "multipart/form-data") || isMultipartImagesContentType(contentType) {
		if mediaErr != nil || strings.TrimSpace(params["boundary"]) == "" {
			return errors.New("invalid multipart content-type or missing boundary")
		}
		return validateAsyncImageMultipartPrompt(body, params["boundary"])
	}
	trimmed := bytes.TrimSpace(body)
	if !gjson.ValidBytes(body) || len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("request body must be a valid JSON object")
	}
	prompt := gjson.GetBytes(body, "prompt")
	if !prompt.Exists() || prompt.Type == gjson.Null {
		return errors.New("prompt is required")
	}
	if prompt.Type != gjson.String {
		return errors.New("prompt must be a string")
	}
	if strings.TrimSpace(prompt.String()) == "" {
		return errors.New("prompt must not be empty")
	}
	return nil
}

// validateAsyncImageMultipartPrompt 只读取文本提示词，其余文件直接流式跳过，避免重复复制编辑图片。
func validateAsyncImageMultipartPrompt(body []byte, boundary string) error {
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	found, nonEmpty := false, false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid multipart request body")
		}
		if strings.TrimSpace(part.FormName()) == "prompt" && strings.TrimSpace(part.FileName()) == "" {
			value, readErr := io.ReadAll(part)
			_ = part.Close()
			if readErr != nil {
				return errors.New("failed to read prompt field")
			}
			// 与同步 multipart 解析器一致，重复文本字段按最后一个值校验。
			found, nonEmpty = true, len(bytes.TrimSpace(value)) > 0
			continue
		}
		if _, err := io.Copy(io.Discard, part); err != nil {
			_ = part.Close()
			return errors.New("invalid multipart request body")
		}
		_ = part.Close()
	}
	if !found {
		return errors.New("prompt is required")
	}
	if !nonEmpty {
		return errors.New("prompt must not be empty")
	}
	return nil
}
