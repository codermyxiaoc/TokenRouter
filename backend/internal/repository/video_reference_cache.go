package repository

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
)

// 参考图缓存只依赖共享 Redis，服务层只持有原子签发和限次读取接口。
type videoReferenceCache struct{ rdb *redis.Client }

func NewVideoReferenceCache(rdb *redis.Client) service.VideoReferenceStore {
	return &videoReferenceCache{rdb: rdb}
}

func (s *videoReferenceCache) Ready(ctx context.Context) error {
	if s == nil || s.rdb == nil {
		return service.ErrVideoReferenceUnavailable
	}
	return s.rdb.Ping(ctx).Err()
}

func videoReferenceKey(digest string) string { return "video_reference:content:" + digest }
func validVideoReferenceDigest(digest string) bool {
	_, err := hex.DecodeString(digest)
	return len(digest) == 64 && strings.ToLower(digest) == digest && err == nil
}

// 仓储再次约束大小和标识格式，防止错误的内部调用写入任意 Redis 键或无期限数据。
func (s *videoReferenceCache) IssueBatch(ctx context.Context, batch service.VideoReferenceBatch) error {
	if s == nil || s.rdb == nil {
		return service.ErrVideoReferenceUnavailable
	}
	if batch.UserID <= 0 || len(batch.Images) == 0 || len(batch.Images) > 16 || batch.MaxReads <= 0 || batch.MaxReads > 32 || batch.HourlyMaxBytes <= 0 || batch.HourlyMaxCount <= 0 || !batch.ExpiresAt.After(time.Now()) || batch.ExpiresAt.Sub(batch.IssuedAt) > service.VideoReferenceTTL || !batch.ExpiresAt.After(batch.IssuedAt) {
		return service.ErrVideoReferenceInvalid
	}
	keys := []string{fmt.Sprintf("video_reference:budget:%d:%d", batch.UserID, batch.IssuedAt.Unix()/3600)}
	args := []any{0, batch.HourlyMaxBytes, batch.HourlyMaxCount, batch.ExpiresAt.UnixMilli(), batch.IssuedAt.UTC().Truncate(time.Hour).Add(3 * time.Hour).UnixMilli(), batch.MaxReads}
	total := 0
	seen := make(map[string]bool)
	for _, blob := range batch.Images {
		total += len(blob.Body)
		if !validVideoReferenceDigest(blob.Digest) || seen[blob.Digest] || len(blob.Body) == 0 || total > service.VideoReferenceMaxBytes || (blob.MIMEType != "image/png" && blob.MIMEType != "image/jpeg" && blob.MIMEType != "image/webp") {
			return service.ErrVideoReferenceInvalid
		}
		seen[blob.Digest] = true
		keys = append(keys, videoReferenceKey(blob.Digest))
		args = append(args, blob.MIMEType, blob.Body)
	}
	args[0] = total
	result, err := videoReferenceIssueScript.Run(ctx, s.rdb, keys, args...).Int()
	if err != nil {
		return service.ErrVideoReferenceUnavailable
	}
	if result == -1 {
		return service.ErrVideoReferenceQuota
	}
	if result != 1 {
		return service.ErrVideoReferenceUnavailable
	}
	return nil
}

func (s *videoReferenceCache) Open(ctx context.Context, digest, method string) (*service.VideoReferenceContent, error) {
	if s == nil || s.rdb == nil || !validVideoReferenceDigest(digest) || (method != http.MethodGet && method != http.MethodHead) {
		return nil, service.ErrVideoReferenceNotFound
	}
	values, err := videoReferenceReadScript.Run(ctx, s.rdb, []string{videoReferenceKey(digest)}, method).Slice()
	if err != nil || len(values) != 3 {
		return nil, service.ErrVideoReferenceNotFound
	}
	mime, mimeOK := values[0].(string)
	body, bodyOK := values[1].(string)
	size, sizeOK := values[2].(int64)
	if !mimeOK || !bodyOK || !sizeOK || size < 1 || size > service.VideoReferenceMaxBytes || (method == http.MethodGet && int64(len(body)) != size) {
		return nil, service.ErrVideoReferenceNotFound
	}
	if mime != "image/png" && mime != "image/jpeg" && mime != "image/webp" {
		return nil, service.ErrVideoReferenceNotFound
	}
	return &service.VideoReferenceContent{MIMEType: mime, Body: []byte(body), Size: size}, nil
}

// 所有输入先完整校验，再用一次 Lua 原子签发整批；失败不会留下部分可访问图片。
var videoReferenceIssueScript = redis.NewScript(`
local used = tonumber(redis.call('HGET', KEYS[1], 'bytes') or '0')
local count = tonumber(redis.call('HGET', KEYS[1], 'count') or '0')
if used + tonumber(ARGV[1]) > tonumber(ARGV[2]) or count + #KEYS - 1 > tonumber(ARGV[3]) then return -1 end
for i = 2, #KEYS do if redis.call('EXISTS', KEYS[i]) ~= 0 then return -2 end end
for i = 2, #KEYS do
  local offset = 7 + (i - 2) * 2
  redis.call('HSET', KEYS[i], 'mime', ARGV[offset], 'body', ARGV[offset + 1], 'reads', ARGV[6], 'expires', ARGV[4])
  redis.call('PEXPIREAT', KEYS[i], ARGV[4])
end
redis.call('HINCRBY', KEYS[1], 'bytes', ARGV[1])
redis.call('HINCRBY', KEYS[1], 'count', #KEYS - 1)
redis.call('PEXPIREAT', KEYS[1], ARGV[5])
return 1
`)

var videoReferenceReadScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return {} end
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
if tonumber(redis.call('HGET', KEYS[1], 'expires') or '0') <= now or tonumber(redis.call('HGET', KEYS[1], 'reads') or '0') <= 0 then
  redis.call('DEL', KEYS[1]); return {}
end
redis.call('HINCRBY', KEYS[1], 'reads', -1)
local mime = redis.call('HGET', KEYS[1], 'mime')
local size = redis.call('HSTRLEN', KEYS[1], 'body')
if ARGV[1] == 'HEAD' then return {mime, '', size} end
return {mime, redis.call('HGET', KEYS[1], 'body'), size}
`)

func videoReferenceAssetKey(digest string) string { return "video_reference:asset:" + digest }

// IssueAssetBatch 只保存票据摘要到素材授权的映射，不复制视频内容；额度与图片票据分开计算。
func (s *videoReferenceCache) IssueAssetBatch(ctx context.Context, batch service.VideoReferenceAssetBatch) error {
	if s == nil || s.rdb == nil {
		return service.ErrVideoReferenceUnavailable
	}
	if batch.UserID <= 0 || len(batch.Assets) == 0 || len(batch.Assets) > 16 || batch.MaxReads <= 0 || batch.MaxReads > 64 || batch.HourlyMaxBytes <= 0 || batch.HourlyMaxCount <= 0 || !batch.ExpiresAt.After(time.Now()) || batch.ExpiresAt.Sub(batch.IssuedAt) > service.VideoReferenceTTL || !batch.ExpiresAt.After(batch.IssuedAt) {
		return service.ErrVideoReferenceInvalid
	}
	keys := []string{fmt.Sprintf("video_reference:asset_budget:%d:%d", batch.UserID, batch.IssuedAt.Unix()/3600)}
	args := []any{0, batch.HourlyMaxBytes, batch.HourlyMaxCount, batch.ExpiresAt.UnixMilli(), batch.IssuedAt.UTC().Truncate(time.Hour).Add(3 * time.Hour).UnixMilli(), batch.MaxReads}
	var total int64
	seen := make(map[string]bool)
	for _, grant := range batch.Assets {
		total += grant.Size
		if !validVideoReferenceDigest(grant.Digest) || seen[grant.Digest] || grant.UserID != batch.UserID || grant.AssetID == "" || len(grant.AssetID) > 64 || grant.Size < 1 || grant.Size > service.VideoReferenceAssetMaxBytes || (grant.MIMEType != "video/mp4" && grant.MIMEType != "video/webm") {
			return service.ErrVideoReferenceInvalid
		}
		seen[grant.Digest] = true
		keys = append(keys, videoReferenceAssetKey(grant.Digest))
		args = append(args, grant.UserID, grant.AssetID, grant.MIMEType, grant.Size)
	}
	args[0] = total
	result, err := videoReferenceAssetIssueScript.Run(ctx, s.rdb, keys, args...).Int()
	if err != nil {
		return service.ErrVideoReferenceUnavailable
	}
	if result == -1 {
		return service.ErrVideoReferenceQuota
	}
	if result != 1 {
		return service.ErrVideoReferenceUnavailable
	}
	return nil
}

// OpenAsset 原子扣减一次读取并返回授权；过期或次数用尽时删除票据。
func (s *videoReferenceCache) OpenAsset(ctx context.Context, digest, method string) (*service.VideoReferenceAssetGrant, error) {
	if s == nil || s.rdb == nil || !validVideoReferenceDigest(digest) || (method != http.MethodGet && method != http.MethodHead) {
		return nil, service.ErrVideoReferenceNotFound
	}
	values, err := videoReferenceAssetReadScript.Run(ctx, s.rdb, []string{videoReferenceAssetKey(digest)}).Slice()
	if err != nil || len(values) != 4 {
		return nil, service.ErrVideoReferenceNotFound
	}
	user, userOK := values[0].(string)
	asset, assetOK := values[1].(string)
	mime, mimeOK := values[2].(string)
	size, sizeOK := values[3].(string)
	if !userOK || !assetOK || !mimeOK || !sizeOK {
		return nil, service.ErrVideoReferenceNotFound
	}
	userID, userErr := strconv.ParseInt(user, 10, 64)
	byteSize, sizeErr := strconv.ParseInt(size, 10, 64)
	if userErr != nil || sizeErr != nil || userID <= 0 || byteSize < 1 || byteSize > service.VideoReferenceAssetMaxBytes {
		return nil, service.ErrVideoReferenceNotFound
	}
	return &service.VideoReferenceAssetGrant{Digest: digest, UserID: userID, AssetID: asset, MIMEType: mime, Size: byteSize}, nil
}

var videoReferenceAssetIssueScript = redis.NewScript(`
local used = tonumber(redis.call('HGET', KEYS[1], 'bytes') or '0')
local count = tonumber(redis.call('HGET', KEYS[1], 'count') or '0')
if used + tonumber(ARGV[1]) > tonumber(ARGV[2]) or count + #KEYS - 1 > tonumber(ARGV[3]) then return -1 end
for i = 2, #KEYS do if redis.call('EXISTS', KEYS[i]) ~= 0 then return -2 end end
for i = 2, #KEYS do
  local offset = 7 + (i - 2) * 4
  redis.call('HSET', KEYS[i], 'user', ARGV[offset], 'asset', ARGV[offset + 1], 'mime', ARGV[offset + 2], 'size', ARGV[offset + 3], 'reads', ARGV[6], 'expires', ARGV[4])
  redis.call('PEXPIREAT', KEYS[i], ARGV[4])
end
redis.call('HINCRBY', KEYS[1], 'bytes', ARGV[1])
redis.call('HINCRBY', KEYS[1], 'count', #KEYS - 1)
redis.call('PEXPIREAT', KEYS[1], ARGV[5])
return 1
`)

var videoReferenceAssetReadScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return {} end
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
if tonumber(redis.call('HGET', KEYS[1], 'expires') or '0') <= now or tonumber(redis.call('HGET', KEYS[1], 'reads') or '0') <= 0 then
  redis.call('DEL', KEYS[1]); return {}
end
redis.call('HINCRBY', KEYS[1], 'reads', -1)
return redis.call('HMGET', KEYS[1], 'user', 'asset', 'mime', 'size')
`)
