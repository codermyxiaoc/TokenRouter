package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// VideoReferenceAssetMaxBytes 是单个参考视频的上限；视频不复制进 Redis，读取时从私有素材存储流式输出。
	VideoReferenceAssetMaxBytes    int64 = 256 << 20
	videoReferenceAssetMaxCount          = 16
	videoReferenceAssetMaxReads          = 64
	videoReferenceAssetHourlyCount       = 32
	videoReferenceAssetHourlyBytes int64 = 4 << 30
)

// VideoReferenceAsset 是调用方已完成归属校验的画布素材；只携带标识与不可变的元数据，不携带内容或路径。
type VideoReferenceAsset struct {
	AssetID  string
	MIMEType string
	Size     int64
	// Placeholder 是该素材在提交正文中的内部占位链接，准备器据此精确替换，不参与签发。
	Placeholder string
}

// VideoReferenceAssetBatch 与图片票据一样固定签发与过期时刻，并独立计算每用户每小时的视频额度。
type VideoReferenceAssetBatch struct {
	UserID                   int64
	IssuedAt, ExpiresAt      time.Time
	MaxReads, HourlyMaxCount int
	HourlyMaxBytes           int64
	Assets                   []VideoReferenceAssetGrant
}

// VideoReferenceAssetGrant 只以票据摘要为键保存素材授权；共享存储里没有可直接访问的原始票据。
type VideoReferenceAssetGrant struct {
	Digest   string
	UserID   int64
	AssetID  string
	MIMEType string
	Size     int64
}

// VideoReferenceAssetStore 原子签发整批视频票据并限次读取，与图片票据共用同一共享 Redis。
type VideoReferenceAssetStore interface {
	IssueAssetBatch(context.Context, VideoReferenceAssetBatch) error
	OpenAsset(context.Context, string, string) (*VideoReferenceAssetGrant, error)
}

// VideoReferenceAssetSource 在每次读取时重新校验素材仍属于签发用户、已就绪且未过期，再打开私有存储。
type VideoReferenceAssetSource interface {
	OpenVideoReferenceAsset(context.Context, int64, string) (io.ReadSeekCloser, *CreativeCanvasAsset, error)
}

// SetAssetSource 由画布服务在装配时注入，避免依赖注入出现循环；未注入时视频票据不可签发也不可读取。
func (s *VideoReferenceService) SetAssetSource(source VideoReferenceAssetSource) {
	if s != nil {
		s.assets = source
	}
}

func videoReferenceAssetMIME(mime string) bool {
	return mime == "video/mp4" || mime == "video/webm"
}

// PrepareAssetURLs 为画布参考视频签发短期链接。userID 必须来自已验证的持久执行，调用方先校验素材归属；
// 这里只接受素材 ID 与元数据，不接受 URL 或文件路径，也不会下载任意目标。整批先校验再原子签发。
// @project-doc docs/interfaces/cloud_canvas.md#video_reference_delivery
func (s *VideoReferenceService) PrepareAssetURLs(ctx context.Context, userID int64, assets []VideoReferenceAsset) ([]string, error) {
	if len(assets) == 0 {
		return []string{}, nil
	}
	if s == nil || userID <= 0 || len(assets) > videoReferenceAssetMaxCount {
		return nil, ErrVideoReferenceInvalid
	}
	assetStore, ok := s.store.(VideoReferenceAssetStore)
	if !ok || s.assets == nil {
		return nil, ErrVideoReferenceUnavailable
	}
	if err := s.ValidateReady(ctx); err != nil {
		return nil, err
	}
	base, err := s.publicBase(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	batch := VideoReferenceAssetBatch{UserID: userID, IssuedAt: now, ExpiresAt: now.Add(VideoReferenceTTL), MaxReads: videoReferenceAssetMaxReads, HourlyMaxBytes: videoReferenceAssetHourlyBytes, HourlyMaxCount: videoReferenceAssetHourlyCount}
	urls := make([]string, 0, len(assets))
	seen := map[string]bool{}
	for _, asset := range assets {
		if !validCanvasID(asset.AssetID) || seen[asset.AssetID] || !videoReferenceAssetMIME(asset.MIMEType) || asset.Size < 1 || asset.Size > VideoReferenceAssetMaxBytes {
			return nil, ErrVideoReferenceInvalid
		}
		seen[asset.AssetID] = true
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return nil, ErrVideoReferenceUnavailable
		}
		token := hex.EncodeToString(secret[:])
		batch.Assets = append(batch.Assets, VideoReferenceAssetGrant{Digest: videoReferenceDigest(token), UserID: userID, AssetID: asset.AssetID, MIMEType: asset.MIMEType, Size: asset.Size})
		urls = append(urls, base+videoReferencePath+token)
	}
	if err := assetStore.IssueAssetBatch(ctx, batch); err != nil {
		return nil, err
	}
	return urls, nil
}

// VideoReferenceAssetContent 是一次已扣减读取次数的视频读取；调用方负责关闭 Reader。
type VideoReferenceAssetContent struct {
	MIMEType string
	Size     int64
	Reader   io.ReadSeekCloser
}

// OpenAsset 消费视频票据：先在共享存储中原子扣减读取次数，再按签发用户重新校验素材并打开私有存储。
// 素材已删除、过期或内容与签发时的类型、大小不一致时一律按不存在处理，不泄露原因。
func (s *VideoReferenceService) OpenAsset(ctx context.Context, token, method string) (*VideoReferenceAssetContent, error) {
	if s == nil || s.assets == nil || len(token) != 64 || (method != http.MethodGet && method != http.MethodHead) {
		return nil, ErrVideoReferenceNotFound
	}
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 || token != strings.ToLower(token) {
		return nil, ErrVideoReferenceNotFound
	}
	assetStore, ok := s.store.(VideoReferenceAssetStore)
	if !ok {
		return nil, ErrVideoReferenceNotFound
	}
	grant, err := assetStore.OpenAsset(ctx, videoReferenceDigest(token), method)
	if err != nil || grant == nil || grant.UserID <= 0 || !validCanvasID(grant.AssetID) || !videoReferenceAssetMIME(grant.MIMEType) || grant.Size < 1 || grant.Size > VideoReferenceAssetMaxBytes {
		return nil, ErrVideoReferenceNotFound
	}
	reader, asset, err := s.assets.OpenVideoReferenceAsset(ctx, grant.UserID, grant.AssetID)
	if err != nil || reader == nil {
		return nil, ErrVideoReferenceNotFound
	}
	if asset == nil || asset.UserID != grant.UserID || asset.ID != grant.AssetID || asset.MIMEType != grant.MIMEType || asset.ByteSize != grant.Size {
		reader.Close()
		return nil, ErrVideoReferenceNotFound
	}
	return &VideoReferenceAssetContent{MIMEType: grant.MIMEType, Size: grant.Size, Reader: reader}, nil
}
