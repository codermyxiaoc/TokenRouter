package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// LocalImageFileRoutePrefix 是本地存储图片的公开下载路由；链接自带 HMAC 签名与到期时间。
const LocalImageFileRoutePrefix = "/api/v1/image-files/"

// ErrLocalImageNotFound 统一表示签名无效、过期或文件不存在，避免向外暴露具体原因。
var ErrLocalImageNotFound = errors.New("local image not found")

// LocalImageFileOpener 由本地存储实现，下载路由校验签名后只读打开确切文件。
type LocalImageFileOpener interface {
	OpenLocalImage(key string, expiresUnix int64, signature string) (*os.File, os.FileInfo, error)
}

// LocalImageSigningKey 从部署密钥派生图片链接签名密钥，与登录令牌用途隔离。
func LocalImageSigningKey(seed string) []byte {
	sum := sha256.Sum256([]byte("tokenrouter-image-files\x00" + seed))
	return sum[:]
}

func localImageSignature(secret []byte, key string, expiresUnix int64) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(key))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(expiresUnix, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignLocalImageURL 生成带签名的下载链接；对象键的每一段单独转义，保留目录分隔符。
func SignLocalImageURL(baseURL string, secret []byte, key string, expiresAt time.Time) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	expires := expiresAt.Unix()
	return strings.TrimRight(baseURL, "/") + LocalImageFileRoutePrefix + strings.Join(segments, "/") +
		"?exp=" + strconv.FormatInt(expires, 10) + "&sig=" + localImageSignature(secret, key, expires)
}

// VerifyLocalImageSignature 使用常量时间比较校验签名，并拒绝已过期的链接。
func VerifyLocalImageSignature(secret []byte, key string, expiresUnix int64, signature string, now time.Time) bool {
	if len(secret) == 0 || key == "" || expiresUnix <= now.Unix() {
		return false
	}
	expected := localImageSignature(secret, key, expiresUnix)
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(strings.TrimSpace(signature))))
}

// OpenLocalImage 由下载路由调用：仅当当前存储为本地目录时按签名打开文件。
// 切换到 S3 后旧的本地链接随之失效，由持久清理队列删除残留文件。
func (s *ImageTaskService) OpenLocalImage(_ context.Context, key string, expiresUnix int64, signature string) (*os.File, os.FileInfo, error) {
	uploader, _ := s.current()
	if uploader == nil || uploader.storage == nil {
		return nil, nil, ErrLocalImageNotFound
	}
	storage := uploader.storage
	if retained, ok := storage.(*retainedImageStorage); ok {
		storage = retained.ImageStorage
	}
	opener, ok := storage.(LocalImageFileOpener)
	if !ok {
		return nil, nil, ErrLocalImageNotFound
	}
	return opener.OpenLocalImage(key, expiresUnix, signature)
}
