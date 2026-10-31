package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// signingImageStorage 记录重签请求；err 非空时模拟对象已删除等无法重签的情况。
type signingImageStorage struct {
	fakeImageStorage
	keys      []string
	deadlines []time.Time
	err       error
}

func (s *signingImageStorage) SignObjectURL(_ context.Context, key string, expiresAt time.Time) (string, error) {
	s.keys = append(s.keys, key)
	s.deadlines = append(s.deadlines, expiresAt)
	if s.err != nil {
		return "", s.err
	}
	return "https://bucket.example.test/" + key + "?X-Amz-Date=" + time.Now().UTC().Format("20060102T150405Z") + "&X-Amz-Expires=3600&X-Amz-Signature=new", nil
}

func s3SignedURL(key string, signedAt time.Time, seconds int) string {
	return "https://bucket.example.test/" + key + "?X-Amz-Date=" + signedAt.UTC().Format("20060102T150405Z") + "&X-Amz-Expires=" + strconv.Itoa(seconds) + "&X-Amz-Signature=old"
}

// 只识别带签名参数的链接：S3 预签名与本地签名按各自期限判断，公开直链不算过期。
func TestImageSignedURLExpired(t *testing.T) {
	now := time.Now()
	require.True(t, imageSignedURLExpired(s3SignedURL("images/a.png", now.Add(-2*time.Hour), 3600), now))
	require.False(t, imageSignedURLExpired(s3SignedURL("images/a.png", now.Add(-10*time.Minute), 3600), now))
	require.True(t, imageSignedURLExpired("https://site.test/api/v1/image-files/images/a.png?exp="+strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)+"&sig=x", now))
	require.False(t, imageSignedURLExpired("https://site.test/api/v1/image-files/images/a.png?exp="+strconv.FormatInt(now.Add(time.Hour).Unix(), 10)+"&sig=x", now))
	require.False(t, imageSignedURLExpired("https://cdn.example.test/images/a.png", now))
}

// 只重签已过期且对象键与转存规则一致的链接；未过期、公开直链、键不匹配或无法重签时保留原链接。
func TestImageResultUploaderRefreshExpiredURLs(t *testing.T) {
	taskID := "imgtask_" + strings.Repeat("a", 32)
	expiredKey := "images/" + taskID + "-0.png"
	freshKey := "images/" + taskID + "-1.png"
	expired := s3SignedURL(expiredKey, time.Now().Add(-2*time.Hour), 3600)
	fresh := s3SignedURL(freshKey, time.Now(), 3600)
	foreign := s3SignedURL("other/"+taskID+"-2.png", time.Now().Add(-2*time.Hour), 3600)
	result, _ := json.Marshal(map[string]any{"created": 1, "data": []map[string]string{{"url": expired}, {"url": fresh}, {"url": foreign}, {"url": "https://cdn.example.test/x.png"}}})
	deadline := time.Now().Add(time.Hour)

	storage := &signingImageStorage{}
	out := NewImageResultUploader(storage, "images/", 0, nil).RefreshExpiredURLs(context.Background(), taskID, result, deadline)
	require.Equal(t, []string{expiredKey}, storage.keys, "只为过期且键匹配的链接重签")
	require.Equal(t, deadline, storage.deadlines[0])
	var parsed struct {
		Created int `json:"created"`
		Data    []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.Equal(t, 1, parsed.Created, "其它字段保留")
	require.Contains(t, parsed.Data[0].URL, "X-Amz-Signature=new")
	require.Equal(t, fresh, parsed.Data[1].URL)
	require.Equal(t, foreign, parsed.Data[2].URL)
	require.Equal(t, "https://cdn.example.test/x.png", parsed.Data[3].URL)

	// 无法重签（如对象已删除）时保留原链接；过期的截止时间不重签。
	failing := &signingImageStorage{err: errors.New("object deleted")}
	require.JSONEq(t, string(result), string(NewImageResultUploader(failing, "images/", 0, nil).RefreshExpiredURLs(context.Background(), taskID, result, deadline)))
	unsigned := &signingImageStorage{}
	require.JSONEq(t, string(result), string(NewImageResultUploader(unsigned, "images/", 0, nil).RefreshExpiredURLs(context.Background(), taskID, result, time.Now().Add(-time.Minute))))
	require.Empty(t, unsigned.keys)
	// 不支持重签的存储原样返回。
	require.JSONEq(t, string(result), string(NewImageResultUploader(&fakeImageStorage{}, "images/", 0, nil).RefreshExpiredURLs(context.Background(), taskID, result, deadline)))
}

// 查询已完成任务时重签过期链接，但不改写存储的记录；签名期限不超过任务记录有效期。
func TestImageTaskGetResignsExpiredLinksWithoutPersisting(t *testing.T) {
	store := &imageTaskMemoryStore{}
	storage := &signingImageStorage{}
	svc := NewImageTaskServiceWithUploader(store, NewImageResultUploader(storage, "images/", 0, nil), time.Hour, time.Minute)
	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)
	key := "images/" + created.ID + "-0.png"
	expired := s3SignedURL(key, time.Now().Add(-2*time.Hour), 3600)
	result, _ := json.Marshal(map[string]any{"data": []map[string]string{{"url": expired}}})
	require.NoError(t, svc.finish(context.Background(), created.ID, ImageTaskStatusCompleted, http.StatusOK, result, nil))

	got, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Contains(t, got.ImageURL, "X-Amz-Signature=new")
	require.Equal(t, []string{key}, storage.keys)
	stored, err := store.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Contains(t, string(stored.Result), "X-Amz-Signature=old", "重签只作用于返回结果，不改写存储记录")
	require.Equal(t, time.Unix(stored.ExpiresAt, 0), storage.deadlines[0])
}

// 保存期包装层只为本服务生成的对象键委托重签，任意键一律拒绝；底层不支持重签时报错。
func TestRetainedImageStorageSignObjectURLOwnership(t *testing.T) {
	inner := &signingImageStorage{}
	wrapped := &retainedImageStorage{ImageStorage: inner, config: config.ImageStorageConfig{Prefix: "images/"}}
	owned := "images/imgtask_" + strings.Repeat("b", 32) + "-0.png"
	link, err := wrapped.SignObjectURL(context.Background(), owned, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Contains(t, link, owned)
	for _, key := range []string{"images/../secret.png", "backups/db.sql", "images/manual.png", "other/imgtask_" + strings.Repeat("b", 32) + "-0.png"} {
		_, err := wrapped.SignObjectURL(context.Background(), key, time.Now().Add(time.Hour))
		require.Error(t, err, key)
	}
	require.Equal(t, []string{owned}, inner.keys)
	plain := &retainedImageStorage{ImageStorage: &fakeImageStorage{}, config: config.ImageStorageConfig{Prefix: "images/"}}
	_, err = plain.SignObjectURL(context.Background(), owned, time.Now().Add(time.Hour))
	require.Error(t, err)
}
