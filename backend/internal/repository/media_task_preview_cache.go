package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
)

type mediaTaskPreviewCache struct{ rdb *redis.Client }

func NewMediaTaskPreviewCache(rdb *redis.Client) service.MediaTaskPreviewCache {
	return &mediaTaskPreviewCache{rdb: rdb}
}

// 地址和票据均短期存储；Redis 键使用摘要，避免签名链接或完整访问票据进入诊断信息。
func mediaTaskVideoPreviewKey(identity service.MediaTaskPreviewIdentity) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d:%s", identity.Source, identity.UserID, identity.APIKeyID, identity.TaskID)))
	return "media_task:preview:video:" + hex.EncodeToString(digest[:])
}

func mediaTaskPreviewTicketKey(ticket string) string {
	digest := sha256.Sum256([]byte(ticket))
	return "media_task:preview:ticket:" + hex.EncodeToString(digest[:])
}

func (r *mediaTaskPreviewCache) save(ctx context.Context, key string, value any, ttl time.Duration) error {
	if r == nil || r.rdb == nil || ttl <= 0 {
		return service.ErrMediaTaskNotFound
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return r.rdb.Set(ctx, key, raw, ttl).Err()
}

func (r *mediaTaskPreviewCache) get(ctx context.Context, key string, value any) error {
	if r == nil || r.rdb == nil {
		return service.ErrMediaTaskNotFound
	}
	raw, err := r.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return err
	}
	if len(raw) > 32768 {
		return service.ErrMediaTaskNotFound
	}
	return json.Unmarshal(raw, value)
}

func (r *mediaTaskPreviewCache) SaveVideo(ctx context.Context, record *service.MediaTaskVideoPreviewRecord, ttl time.Duration) error {
	return r.save(ctx, mediaTaskVideoPreviewKey(record.Identity), record, ttl)
}

func (r *mediaTaskPreviewCache) GetVideo(ctx context.Context, identity service.MediaTaskPreviewIdentity) (*service.MediaTaskVideoPreviewRecord, error) {
	var record service.MediaTaskVideoPreviewRecord
	err := r.get(ctx, mediaTaskVideoPreviewKey(identity), &record)
	return &record, err
}

func (r *mediaTaskPreviewCache) SaveTicket(ctx context.Context, ticket string, record *service.MediaTaskPreviewTicket, ttl time.Duration) error {
	return r.save(ctx, mediaTaskPreviewTicketKey(ticket), record, ttl)
}

func (r *mediaTaskPreviewCache) GetTicket(ctx context.Context, ticket string) (*service.MediaTaskPreviewTicket, error) {
	var record service.MediaTaskPreviewTicket
	err := r.get(ctx, mediaTaskPreviewTicketKey(ticket), &record)
	return &record, err
}
