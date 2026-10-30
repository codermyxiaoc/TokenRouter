package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// asyncImageIdempotency 读取 Idempotency-Key 并计算请求指纹；没有幂等键时返回空值。
// 指纹包含接口类型（生成/编辑）与请求内容：JSON 按原始字节；multipart 按字段名、文件名与各字段内容的摘要依序计算，
// 不受每次随机生成的 boundary 影响，同一 SDK 重试得到相同指纹。
// @project-doc docs/domains/media_tasks.md#async_image_idempotency
func asyncImageIdempotency(c *gin.Context, body []byte) (service.ImageTaskIdempotency, error) {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		return service.ImageTaskIdempotency{}, nil
	}
	if len(key) > service.ImageTaskIdempotencyMaxKeyLength {
		return service.ImageTaskIdempotency{}, errors.New("Idempotency-Key must be at most 128 characters")
	}
	hash, err := asyncImagePayloadHash(c.Request.URL.Path, c.GetHeader("Content-Type"), body)
	if err != nil {
		return service.ImageTaskIdempotency{}, err
	}
	return service.ImageTaskIdempotency{Key: key, PayloadHash: hash}, nil
}

func asyncImagePayloadHash(path, contentType string, body []byte) (string, error) {
	kind := "generations"
	if strings.Contains(path, "/edits") {
		kind = "edits"
	}
	digest := sha256.New()
	digest.Write([]byte("async-image\x00" + kind + "\x00"))
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		digest.Write([]byte("raw\x00"))
		digest.Write(body)
		return hex.EncodeToString(digest.Sum(nil)), nil
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	digest.Write([]byte("multipart\x00"))
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", errors.New("invalid multipart image request")
		}
		content := sha256.New()
		if _, err := io.Copy(content, part); err != nil {
			_ = part.Close()
			return "", errors.New("invalid multipart image request")
		}
		_ = part.Close()
		// 依序写入字段，重复字段（如多张 image）的顺序也参与指纹。
		digest.Write([]byte(part.FormName() + "\x00" + part.FileName() + "\x00"))
		digest.Write(content.Sum(nil))
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
