package config

import "fmt"

const (
	ImageStorageDefaultPresignExpiryHours = 24
	ImageStorageMaxPresignExpiryHours     = 168
	ImageStorageDefaultRetentionHours     = 24
	ImageStorageMaxRetentionHours         = 8760
)

type ImageStorageConfig struct {
	Enabled         bool   `mapstructure:"enabled"`
	Endpoint        string `mapstructure:"endpoint"` // 例如 https://<account_id>.r2.cloudflarestorage.com
	Region          string `mapstructure:"region"`   // R2 用 "auto"
	Bucket          string `mapstructure:"bucket"`
	AccessKeyID     string `mapstructure:"access_key_id"`
	SecretAccessKey string `mapstructure:"secret_access_key"`
	Prefix          string `mapstructure:"prefix"`               // S3 key 前缀，如 "images/"
	ForcePathStyle  bool   `mapstructure:"force_path_style"`     // MinIO/路径风格桶
	PublicBaseURL   string `mapstructure:"public_base_url"`      // 配了则返回 public_base_url/key 直链；否则 presigned
	PresignExpiry   int    `mapstructure:"presign_expiry_hours"` // public_base_url 为空时的 presigned 过期时长(小时)
	RetentionHours  int    `mapstructure:"retention_hours"`      // 图片对象保存小时数，上传时冻结到期时间
	MaxDownloadByte int64  `mapstructure:"max_download_bytes"`   // 下载上游 url 图片的字节上限
}

// IsConfigured 检查对象存储必要字段是否已配置
func (c *ImageStorageConfig) IsConfigured() bool {
	return c.Bucket != "" && c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// Active 返回异步图片任务是否可用：开关打开且凭证齐全
func (c *ImageStorageConfig) Active() bool {
	return c.Enabled && c.IsConfigured() && c.ValidatePresignExpiry() == nil && c.ValidateRetentionHours() == nil
}

// ValidatePresignExpiry 在建客户端或受理生成之前检查签名期限；旧配置的非正数仍沿用默认值。
// 不依赖开关，供主动测试与保存复用；禁用配置是否需要验证由调用方决定。
func (c *ImageStorageConfig) ValidatePresignExpiry() error {
	if c.PresignExpiry > ImageStorageMaxPresignExpiryHours {
		return fmt.Errorf("image_storage.presign_expiry_hours must be between 1 and %d hours (7 days)", ImageStorageMaxPresignExpiryHours)
	}
	return nil
}

// ValidateRetentionHours 将旧配置的零值视为默认值，拒绝负数，避免意外立即删除图片。
func (c *ImageStorageConfig) ValidateRetentionHours() error {
	if c.RetentionHours < 0 || c.RetentionHours > ImageStorageMaxRetentionHours {
		return fmt.Errorf("image_storage.retention_hours must be between 1 and %d hours", ImageStorageMaxRetentionHours)
	}
	return nil
}

// EffectiveRetentionHours 仅兼容缺省或零值；调用方仍须先验证非法输入。
func (c *ImageStorageConfig) EffectiveRetentionHours() int {
	if c.RetentionHours == 0 {
		return ImageStorageDefaultRetentionHours
	}
	return c.RetentionHours
}

// MissingCredentialKeys 返回 IsConfigured 所缺的配置键名。
// 用于启动日志：只说"凭证不完整"会让运维以为自己漏填了，而实际可能是值填了却没被读到。
func (c *ImageStorageConfig) MissingCredentialKeys() []string {
	var missing []string
	if c.Bucket == "" {
		missing = append(missing, "image_storage.bucket")
	}
	if c.AccessKeyID == "" {
		missing = append(missing, "image_storage.access_key_id")
	}
	if c.SecretAccessKey == "" {
		missing = append(missing, "image_storage.secret_access_key")
	}
	return missing
}
