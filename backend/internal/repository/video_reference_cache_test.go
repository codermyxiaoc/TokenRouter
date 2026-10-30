package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type videoReferenceCacheSettings struct{ service.SettingRepository }

func (videoReferenceCacheSettings) GetValue(context.Context, string) (string, error) {
	return "https://canvas.example.com", nil
}

func videoReferenceCacheFixture(t *testing.T) (service.VideoReferenceStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewVideoReferenceCache(rdb), mr
}

func videoReferenceCacheBatch(userID int64, digests ...string) service.VideoReferenceBatch {
	now := time.Now().UTC()
	batch := service.VideoReferenceBatch{
		UserID: userID, IssuedAt: now, ExpiresAt: now.Add(service.VideoReferenceTTL),
		MaxReads: 32, HourlyMaxBytes: 64 << 20, HourlyMaxCount: 64,
	}
	for _, digest := range digests {
		batch.Images = append(batch.Images, service.VideoReferenceBlob{Digest: digest, MIMEType: "image/png", Body: []byte("content")})
	}
	return batch
}

// 新实例只依赖共享存储即可读取原始字节；能力票据本身不进入 Redis 键或值。
func TestVideoReferenceCacheSharedInstancesAndExactContent(t *testing.T) {
	store, mr := videoReferenceCacheFixture(t)
	svc := service.NewVideoReferenceService(store, service.NewSettingService(videoReferenceCacheSettings{}, &config.Config{}))
	var body bytes.Buffer
	require.NoError(t, png.Encode(&body, image.NewNRGBA(image.Rect(0, 0, 2, 2))))
	urls, err := svc.PrepareDataURLs(context.Background(), 7, []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(body.Bytes())})
	require.NoError(t, err)
	u, err := url.Parse(urls[0])
	require.NoError(t, err)
	token := strings.TrimPrefix(u.Path, "/api/v1/video-references/")
	digest := sha256.Sum256([]byte(token))
	key := videoReferenceKey(hex.EncodeToString(digest[:]))
	require.True(t, mr.Exists(key))
	for _, key := range mr.Keys() {
		require.NotContains(t, key, token)
		fields, err := mr.HKeys(key)
		require.NoError(t, err)
		for _, field := range fields {
			require.NotContains(t, mr.HGet(key, field), token)
		}
	}
	secondClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = secondClient.Close() })
	second := service.NewVideoReferenceService(NewVideoReferenceCache(secondClient), nil)
	content, err := second.Open(context.Background(), token, http.MethodGet)
	require.NoError(t, err)
	require.Equal(t, body.Bytes(), content.Body)
	require.Equal(t, "image/png", content.MIMEType)
	require.Equal(t, int64(body.Len()), content.Size)
	head, err := second.Open(context.Background(), token, http.MethodHead)
	require.NoError(t, err)
	require.Empty(t, head.Body)
	require.Equal(t, content.Size, head.Size)
}

func TestVideoReferenceCacheFixedExpiryAndReadLimit(t *testing.T) {
	store, mr := videoReferenceCacheFixture(t)
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	require.NoError(t, store.IssueBatch(context.Background(), videoReferenceCacheBatch(7, a, b)))
	before := mr.TTL(videoReferenceKey(a))
	require.Positive(t, before)
	require.LessOrEqual(t, before, service.VideoReferenceTTL)
	// GET 与 HEAD 共用访问预算，任何成功读取都不延长有效期。
	for i := 0; i < 32; i++ {
		method := http.MethodGet
		if i%2 == 0 {
			method = http.MethodHead
		}
		_, err := store.Open(context.Background(), a, method)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, mr.TTL(videoReferenceKey(a)), before)
	_, err := store.Open(context.Background(), a, http.MethodGet)
	require.ErrorIs(t, err, service.ErrVideoReferenceNotFound)
	require.False(t, mr.Exists(videoReferenceKey(a)))
	mr.FastForward(service.VideoReferenceTTL + time.Second)
	_, err = store.Open(context.Background(), b, http.MethodHead)
	require.ErrorIs(t, err, service.ErrVideoReferenceNotFound)
	require.False(t, mr.Exists(videoReferenceKey(b)))
}

func TestVideoReferenceCacheReadBudgetIsAtomic(t *testing.T) {
	store, _ := videoReferenceCacheFixture(t)
	digest := strings.Repeat("a", 64)
	require.NoError(t, store.IssueBatch(context.Background(), videoReferenceCacheBatch(7, digest)))
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Open(context.Background(), digest, http.MethodGet); err == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(32), success.Load())
}

func TestVideoReferenceCacheQuotaIsAtomicAndUserScoped(t *testing.T) {
	store, mr := videoReferenceCacheFixture(t)
	batch := videoReferenceCacheBatch(7, strings.Repeat("a", 64))
	budget := fmt.Sprintf("video_reference:budget:7:%d", batch.IssuedAt.Unix()/3600)
	// 仅剩一个名额时，并发的两批写入必须完整成功一批、完整拒绝另一批。
	mr.HSet(budget, "count", "63")
	var success atomic.Int32
	var quota atomic.Int32
	var wg sync.WaitGroup
	for _, digest := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		wg.Add(1)
		go func(digest string) {
			defer wg.Done()
			candidate := batch
			candidate.Images = []service.VideoReferenceBlob{{Digest: digest, MIMEType: "image/png", Body: []byte("content")}}
			err := store.IssueBatch(context.Background(), candidate)
			if err == nil {
				success.Add(1)
			} else if err == service.ErrVideoReferenceQuota {
				quota.Add(1)
			}
		}(digest)
	}
	wg.Wait()
	require.Equal(t, int32(1), success.Load())
	require.Equal(t, int32(1), quota.Load())
	require.NoError(t, store.IssueBatch(context.Background(), videoReferenceCacheBatch(8, strings.Repeat("c", 64))))
	before := len(mr.Keys())
	mr.HSet(budget, "count", "0", "bytes", "67108864")
	require.ErrorIs(t, store.IssueBatch(context.Background(), videoReferenceCacheBatch(7, strings.Repeat("d", 64), strings.Repeat("e", 64))), service.ErrVideoReferenceQuota)
	require.Len(t, mr.Keys(), before)
}

func TestVideoReferenceCacheInvalidOrCollidingBatchNeverPartiallyPublishes(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, mutate := range []func(*service.VideoReferenceBatch){
		func(batch *service.VideoReferenceBatch) { batch.Images[1].Digest = "../path" },
		func(batch *service.VideoReferenceBatch) { batch.Images[1].MIMEType = "text/html" },
		func(batch *service.VideoReferenceBatch) { batch.Images[1].Body = nil },
		func(batch *service.VideoReferenceBatch) { batch.Images[1].Digest = a },
		func(batch *service.VideoReferenceBatch) { batch.ExpiresAt = batch.ExpiresAt.Add(time.Hour) },
		func(batch *service.VideoReferenceBatch) { batch.MaxReads = 33 },
	} {
		store, mr := videoReferenceCacheFixture(t)
		batch := videoReferenceCacheBatch(7, a, b)
		mutate(&batch)
		require.ErrorIs(t, store.IssueBatch(context.Background(), batch), service.ErrVideoReferenceInvalid)
		require.Empty(t, mr.Keys())
	}
	store, mr := videoReferenceCacheFixture(t)
	require.NoError(t, store.IssueBatch(context.Background(), videoReferenceCacheBatch(7, b)))
	require.ErrorIs(t, store.IssueBatch(context.Background(), videoReferenceCacheBatch(7, a, b)), service.ErrVideoReferenceUnavailable)
	require.False(t, mr.Exists(videoReferenceKey(a)))
	_, err := store.Open(context.Background(), b, http.MethodGet)
	require.NoError(t, err)
}

func TestVideoReferenceCacheFailureAndInvalidLookupFailClosed(t *testing.T) {
	store, mr := videoReferenceCacheFixture(t)
	require.NoError(t, store.Ready(context.Background()))
	for _, digest := range []string{"", "../asset", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		_, err := store.Open(context.Background(), digest, http.MethodGet)
		require.ErrorIs(t, err, service.ErrVideoReferenceNotFound)
	}
	_, err := store.Open(context.Background(), strings.Repeat("a", 64), http.MethodPost)
	require.ErrorIs(t, err, service.ErrVideoReferenceNotFound)
	mr.SetError("private Redis error")
	require.Error(t, store.Ready(context.Background()))
	require.ErrorIs(t, store.IssueBatch(context.Background(), videoReferenceCacheBatch(7, strings.Repeat("a", 64))), service.ErrVideoReferenceUnavailable)
	_, err = store.Open(context.Background(), strings.Repeat("a", 64), http.MethodGet)
	require.ErrorIs(t, err, service.ErrVideoReferenceNotFound)
}

func videoReferenceAssetCacheBatch(userID int64, size int64, digests ...string) service.VideoReferenceAssetBatch {
	now := time.Now().UTC()
	batch := service.VideoReferenceAssetBatch{UserID: userID, IssuedAt: now, ExpiresAt: now.Add(service.VideoReferenceTTL), MaxReads: 64, HourlyMaxBytes: 4 << 30, HourlyMaxCount: 32}
	for i, digest := range digests {
		batch.Assets = append(batch.Assets, service.VideoReferenceAssetGrant{Digest: digest, UserID: userID, AssetID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), MIMEType: "video/mp4", Size: size})
	}
	return batch
}

// 参考视频票据只保存摘要到素材授权的映射，不复制内容；读取次数用尽或到期后删除，额度按用户每小时计算。
func TestVideoReferenceCacheAssetGrants(t *testing.T) {
	store, mr := videoReferenceCacheFixture(t)
	assets := store.(service.VideoReferenceAssetStore)
	a, b := strings.Repeat("c", 64), strings.Repeat("d", 64)
	require.NoError(t, assets.IssueAssetBatch(context.Background(), videoReferenceAssetCacheBatch(7, 1024, a, b)))
	require.Empty(t, mr.HGet(videoReferenceAssetKey(a), "body"), "视频内容不进入 Redis")
	grant, err := assets.OpenAsset(context.Background(), a, http.MethodGet)
	require.NoError(t, err)
	require.Equal(t, service.VideoReferenceAssetGrant{Digest: a, UserID: 7, AssetID: "00000000-0000-4000-8000-000000000000", MIMEType: "video/mp4", Size: 1024}, *grant)
	for i := 1; i < 64; i++ {
		_, err = assets.OpenAsset(context.Background(), a, http.MethodHead)
		require.NoError(t, err)
	}
	_, err = assets.OpenAsset(context.Background(), a, http.MethodGet)
	require.ErrorIs(t, err, service.ErrVideoReferenceNotFound)
	require.False(t, mr.Exists(videoReferenceAssetKey(a)))
	// 图片读取入口不能读出视频票据。
	_, err = store.Open(context.Background(), b, http.MethodGet)
	require.ErrorIs(t, err, service.ErrVideoReferenceNotFound)
	mr.FastForward(service.VideoReferenceTTL + time.Second)
	_, err = assets.OpenAsset(context.Background(), b, http.MethodGet)
	require.ErrorIs(t, err, service.ErrVideoReferenceNotFound)
	// 每小时额度按字节和数量原子检查，超额时整批不签发。
	big := videoReferenceAssetCacheBatch(8, 200<<20, strings.Repeat("e", 64))
	big.HourlyMaxBytes = 300 << 20
	require.NoError(t, assets.IssueAssetBatch(context.Background(), big))
	over := videoReferenceAssetCacheBatch(8, 200<<20, strings.Repeat("f", 64))
	over.HourlyMaxBytes = 300 << 20
	require.ErrorIs(t, assets.IssueAssetBatch(context.Background(), over), service.ErrVideoReferenceQuota)
	require.False(t, mr.Exists(videoReferenceAssetKey(strings.Repeat("f", 64))))
	// 非法批次在写入前拒绝。
	invalid := videoReferenceAssetCacheBatch(9, 10, strings.Repeat("1", 64))
	invalid.Assets[0].MIMEType = "image/png"
	require.ErrorIs(t, assets.IssueAssetBatch(context.Background(), invalid), service.ErrVideoReferenceInvalid)
	invalid = videoReferenceAssetCacheBatch(9, 10, strings.Repeat("2", 64))
	invalid.Assets[0].UserID = 10
	require.ErrorIs(t, assets.IssueAssetBatch(context.Background(), invalid), service.ErrVideoReferenceInvalid)
}
