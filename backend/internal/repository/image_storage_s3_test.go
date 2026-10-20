package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

type imageStorageS3Request struct {
	method string
	path   string
}

// 测试服务器模拟真实 S3 HTTP 往返，避免仅用接口替身遗漏桶权限和对象路径。
type imageStorageS3Fixture struct {
	mu          sync.Mutex
	objects     map[string][]byte
	requests    []imageStorageS3Request
	putStatus   int
	readStatus  int
	delStatus   int
	delCode     string
	delRawBody  *string
	corrupt     bool
	putStarted  chan struct{}
	putRelease  chan struct{}
	readStarted chan struct{}
	readRelease chan struct{}
	startOnce   sync.Once
	readOnce    sync.Once
}

func newImageStorageS3Fixture(t *testing.T) (*imageStorageS3Fixture, *S3ImageStorage) {
	t.Helper()
	f := &imageStorageS3Fixture{objects: make(map[string][]byte)}
	server := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(server.Close)
	store, err := NewS3ImageStorage(context.Background(), &config.ImageStorageConfig{
		Enabled: true, Endpoint: server.URL, Region: "us-east-1", Bucket: "image-bucket",
		AccessKeyID: "test-ak", SecretAccessKey: "test-sk", ForcePathStyle: true,
		Prefix: "private/generated/", PresignExpiry: 24,
	})
	require.NoError(t, err)
	return f, store
}

func (f *imageStorageS3Fixture) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, imageStorageS3Request{method: r.Method, path: r.URL.Path})
	putStatus, readStatus, delStatus, corrupt := f.putStatus, f.readStatus, f.delStatus, f.corrupt
	delCode := f.delCode
	delRawBody := f.delRawBody
	started, release := f.putStarted, f.putRelease
	readStarted, readRelease := f.readStarted, f.readRelease
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	reject := func(status int) {
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>permission denied</Message></Error>`)
	}
	switch r.Method {
	case http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		if started != nil {
			f.startOnce.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			if r.Context().Err() != nil {
				return
			}
		}
		if putStatus != 0 {
			reject(putStatus)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			reject(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.objects[r.URL.Path] = body
		f.mu.Unlock()
		w.Header().Set("ETag", `"probe"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		if readStarted != nil {
			f.readOnce.Do(func() { close(readStarted) })
			select {
			case <-readRelease:
			case <-r.Context().Done():
				return
			}
		}
		if readStatus != 0 {
			reject(readStatus)
			return
		}
		f.mu.Lock()
		data, ok := f.objects[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			reject(http.StatusNotFound)
			return
		}
		if corrupt {
			data = []byte("incorrect object")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	case http.MethodDelete:
		if delStatus != 0 {
			if delRawBody != nil {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(delStatus)
				_, _ = io.WriteString(w, *delRawBody)
				return
			}
			if delCode != "" {
				w.WriteHeader(delStatus)
				_, _ = fmt.Fprintf(w, `<Error><Code>%s</Code><Message>delete failed</Message></Error>`, delCode)
				return
			}
			reject(delStatus)
			return
		}
		f.mu.Lock()
		delete(f.objects, r.URL.Path)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		reject(http.StatusBadRequest)
	}
}

func (f *imageStorageS3Fixture) snapshot() ([]imageStorageS3Request, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]imageStorageS3Request(nil), f.requests...), len(f.objects)
}

func imageStorageS3Count(requests []imageStorageS3Request, method string) int {
	count := 0
	for _, request := range requests {
		if request.method == method {
			count++
		}
	}
	return count
}

func TestS3ImageStorageTestConnectionVerifiesPrefixReadWriteAndCleanup(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	require.NoError(t, store.TestConnection(context.Background()))
	requests, objects := f.snapshot()
	require.Zero(t, objects)
	require.Len(t, requests, 3)
	require.Equal(t, http.MethodPut, requests[0].method)
	require.Equal(t, http.MethodGet, requests[1].method)
	require.Equal(t, http.MethodDelete, requests[2].method)
	require.True(t, strings.HasPrefix(requests[0].path, "/image-bucket/private/generated/.readiness-"))
	require.True(t, strings.HasSuffix(requests[0].path, ".png"))
	require.Equal(t, requests[0].path, requests[1].path)
	require.Equal(t, requests[0].path, requests[2].path)

	// 管理员测试不能命中成功缓存，每次独立对象防止覆盖和误读旧内容。
	require.NoError(t, store.TestConnection(context.Background()))
	requests, objects = f.snapshot()
	require.Len(t, requests, 6)
	require.NotEqual(t, requests[0].path, requests[3].path)
	require.Zero(t, objects)
}

func TestS3ImageStorageTestConnectionRejectsUnreadableUnwritableAndUncleanableStorage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		putStatus   int
		readStatus  int
		delStatus   int
		corrupt     bool
		wantMessage string
		wantGets    int
		wantObjects int
	}{
		{"head_allowed_put_denied", 403, 0, 0, false, "readiness write", 0, 0},
		{"put_allowed_read_denied", 0, 403, 0, false, "readiness read", 1, 0},
		{"incorrect_read_back", 0, 0, 0, true, "unexpected content", 1, 0},
		{"cleanup_denied", 0, 0, 403, false, "readiness cleanup", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, store := newImageStorageS3Fixture(t)
			f.putStatus, f.readStatus, f.delStatus, f.corrupt = tc.putStatus, tc.readStatus, tc.delStatus, tc.corrupt
			require.ErrorContains(t, store.TestConnection(context.Background()), tc.wantMessage)
			requests, objects := f.snapshot()
			require.Equal(t, 1, imageStorageS3Count(requests, http.MethodPut))
			require.Equal(t, tc.wantGets, imageStorageS3Count(requests, http.MethodGet))
			require.Equal(t, 1, imageStorageS3Count(requests, http.MethodDelete))
			require.Zero(t, imageStorageS3Count(requests, http.MethodHead), "桶可访问不能作为可写性证据")
			require.Equal(t, tc.wantObjects, objects)
		})
	}
}

func TestS3ImageStorageReadinessChecksActualPresignAndCleansFailure(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	// 模拟构造后损坏的签名配置，确保探测确实进入 Save 的签名路径；超七天另由构造校验拒绝。
	store.presignExpiry = -time.Hour
	require.ErrorContains(t, store.TestConnection(context.Background()), "presign")
	requests, objects := f.snapshot()
	require.Equal(t, 1, imageStorageS3Count(requests, http.MethodPut))
	require.Equal(t, 1, imageStorageS3Count(requests, http.MethodDelete))
	require.Zero(t, objects)
}

func TestS3ImageStorageRejectsInvalidPresignBeforeBuildingClient(t *testing.T) {
	store, err := NewS3ImageStorage(context.Background(), &config.ImageStorageConfig{
		PresignExpiry: config.ImageStorageMaxPresignExpiryHours + 1,
	})
	require.Error(t, err)
	require.Nil(t, store)
}

func TestS3ImageStorageNormalizesNonPositiveExpiryBeforeDurationConversion(t *testing.T) {
	for _, hours := range []int{0, -1, -int(^uint(0)>>1) - 1} {
		t.Run(fmt.Sprint(hours), func(t *testing.T) {
			store, err := NewS3ImageStorage(context.Background(), &config.ImageStorageConfig{
				PresignExpiry: hours, Region: "us-east-1", Bucket: "image-bucket",
				AccessKeyID: "test-ak", SecretAccessKey: "test-sk",
			})
			require.NoError(t, err)
			require.Equal(t, config.ImageStorageDefaultPresignExpiryHours*time.Hour, store.presignExpiry)
		})
	}
}

func TestS3ImageStorageReadinessCachesSuccessAndInvalidatesOnSaveFailure(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	require.NoError(t, store.CheckReadiness(context.Background()))
	require.NoError(t, store.CheckReadiness(context.Background()))
	requests, _ := f.snapshot()
	require.Equal(t, 1, imageStorageS3Count(requests, http.MethodPut))

	f.mu.Lock()
	f.putStatus = http.StatusForbidden
	f.mu.Unlock()
	_, err := store.Save(context.Background(), "private/generated/real-image.png", "image/png", imageStorageProbePNG)
	require.Error(t, err)
	require.Error(t, store.CheckReadiness(context.Background()), "上传失败必须使缓存失效")
	requests, _ = f.snapshot()
	require.Equal(t, 3, imageStorageS3Count(requests, http.MethodPut))

	f.mu.Lock()
	f.putStatus = 0
	f.mu.Unlock()
	require.NoError(t, store.CheckReadiness(context.Background()), "失败不缓存，权限修复后可以恢复")
	store.readinessMu.Lock()
	store.readinessExpiresAt = time.Now().Add(-time.Second)
	store.readinessMu.Unlock()
	require.NoError(t, store.CheckReadiness(context.Background()), "缓存过期必须重新探测")
	requests, _ = f.snapshot()
	require.Equal(t, 5, imageStorageS3Count(requests, http.MethodPut))
}

func TestS3ImageStorageReadinessCoalescesAndHonorsCallerCancellation(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	f.putStarted, f.putRelease = make(chan struct{}), make(chan struct{})
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	firstResult := make(chan error, 1)
	go func() { firstResult <- store.CheckReadiness(firstCtx) }()
	<-f.putStarted

	// 首个调用者取消不能让其他正在等待同一检查的请求一起失败。
	firstCancel()
	require.ErrorIs(t, <-firstResult, context.Canceled)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer waitCancel()
	require.ErrorIs(t, store.CheckReadiness(waitCtx), context.DeadlineExceeded)
	results := make(chan error, 12)
	for i := 0; i < cap(results); i++ {
		go func() { results <- store.CheckReadiness(context.Background()) }()
	}
	close(f.putRelease)
	for i := 0; i < cap(results); i++ {
		require.NoError(t, <-results)
	}
	requests, objects := f.snapshot()
	require.Equal(t, 1, imageStorageS3Count(requests, http.MethodPut))
	require.Equal(t, 1, imageStorageS3Count(requests, http.MethodGet))
	require.Equal(t, 1, imageStorageS3Count(requests, http.MethodDelete))
	require.Zero(t, objects)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, store.CheckReadiness(cancelled), context.Canceled, "命中缓存也不能忽略调用者取消")
}

func TestS3ImageStorageReadinessCannotRestoreCacheAfterConcurrentSaveFailure(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	f.readStarted, f.readRelease = make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- store.CheckReadiness(context.Background()) }()
	<-f.readStarted
	f.mu.Lock()
	f.putStatus = http.StatusForbidden
	f.mu.Unlock()
	_, err := store.Save(context.Background(), "private/generated/real.png", "image/png", imageStorageProbePNG)
	require.Error(t, err)
	close(f.readRelease)
	require.ErrorContains(t, <-result, "invalidated during verification")
	require.Error(t, store.CheckReadiness(context.Background()))
	requests, _ := f.snapshot()
	require.Equal(t, 3, imageStorageS3Count(requests, http.MethodPut))
}

func TestS3ImageStorageTestConnectionCleansAfterCallerCancellation(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	f.putStarted, f.putRelease = make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- store.TestConnection(ctx) }()
	<-f.putStarted
	cancel()
	err := <-result
	require.True(t, errors.Is(err, context.Canceled))
	close(f.putRelease)
	requests, objects := f.snapshot()
	require.Equal(t, 1, imageStorageS3Count(requests, http.MethodDelete), "原请求取消后仍执行独立清理")
	require.Zero(t, objects)
}

func TestS3ImageStorageSaveSuccessRetainsImageAndReturnsSignedURL(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	url, err := store.Save(context.Background(), "private/generated/image.png", "image/png", imageStorageProbePNG)
	require.NoError(t, err)
	require.Contains(t, url, "/image-bucket/private/generated/image.png?")
	require.Contains(t, url, "X-Amz-Signature=")
	f.mu.Lock()
	require.True(t, bytes.Equal(imageStorageProbePNG, f.objects["/image-bucket/private/generated/image.png"]))
	f.mu.Unlock()
	requests, objects := f.snapshot()
	require.Len(t, requests, 1)
	require.Equal(t, 1, objects, "正常图片不得被探针清理逻辑删除")
}

func TestS3ImageStorageDeleteOnlyExactRegisteredKey(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	target := "private/generated/task-0.png"
	otherKeys := []string{"private/generated/task-0.png.other", "private/generated/task-1.png", "backup/database.gz"}
	for _, key := range append([]string{target}, otherKeys...) {
		_, err := store.Save(context.Background(), key, "image/png", imageStorageProbePNG)
		require.NoError(t, err)
	}
	require.NoError(t, store.Delete(context.Background(), target))
	require.NoError(t, store.Delete(context.Background(), target), "重复执行同一清理任务必须幂等")
	requests, objects := f.snapshot()
	require.Equal(t, len(otherKeys), objects)
	require.Len(t, requests, len(otherKeys)+3)
	for _, request := range requests[len(otherKeys)+1:] {
		require.Equal(t, http.MethodDelete, request.method)
		require.Equal(t, "/image-bucket/"+target, request.path)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, key := range otherKeys {
		require.Equal(t, imageStorageProbePNG, f.objects["/image-bucket/"+key], "相邻对象和备份对象不得被清理")
	}
}

func TestS3ImageStorageDeleteRejectsEmptyKeyWithoutRequest(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	for _, key := range []string{"", " ", "\t\r\n"} {
		require.Error(t, store.Delete(context.Background(), key))
	}
	requests, _ := f.snapshot()
	require.Empty(t, requests, "缺失完整对象键时不得请求桶或执行批量删除")
}

func TestS3ImageStorageDeleteAbsentObjectsIsIdempotent(t *testing.T) {
	for _, code := range []string{"NoSuchKey", "NoSuchObject", "NoSuchBucket"} {
		t.Run(code, func(t *testing.T) {
			f, store := newImageStorageS3Fixture(t)
			f.delStatus, f.delCode = http.StatusNotFound, code
			require.NoError(t, store.Delete(context.Background(), "private/generated/already-gone.png"))
			requests, _ := f.snapshot()
			require.Len(t, requests, 1)
			require.Equal(t, http.MethodDelete, requests[0].method)
			require.Equal(t, "/image-bucket/private/generated/already-gone.png", requests[0].path)
		})
	}
}

func TestS3ImageStorageDeleteFailuresCanBeRetriedWithoutLosingTarget(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f, store := newImageStorageS3Fixture(t)
			target := "private/generated/retry.png"
			_, err := store.Save(context.Background(), target, "image/png", imageStorageProbePNG)
			require.NoError(t, err)
			f.mu.Lock()
			f.delStatus = status
			f.mu.Unlock()
			err = store.Delete(context.Background(), target)
			require.Error(t, err)
			var responseError interface{ HTTPStatusCode() int }
			require.ErrorAs(t, err, &responseError, "必须保留结构化错误供持久 worker 决定重试")
			require.Equal(t, status, responseError.HTTPStatusCode())
			_, objects := f.snapshot()
			require.Equal(t, 1, objects, "失败不能假报删除成功")
			f.mu.Lock()
			f.delStatus = 0
			f.mu.Unlock()
			require.NoError(t, store.Delete(context.Background(), target))
			requests, objects := f.snapshot()
			require.Zero(t, objects)
			for _, request := range requests[1:] {
				require.Equal(t, http.MethodDelete, request.method)
				require.Equal(t, "/image-bucket/"+target, request.path)
			}
		})
	}
}

func TestS3ImageStorageDeleteDoesNotMistakeMaskedPermissionForAbsence(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	f.delStatus, f.delCode = http.StatusNotFound, "AccessDenied"
	require.Error(t, store.Delete(context.Background(), "private/generated/retry.png"))
}

func TestS3ImageStorageDeleteDoesNotDiscardEvidenceOnAmbiguous404(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		code string
	}{
		{"empty_proxy_response", "", ""},
		{"html_proxy_response", "<html><body>404 route not found</body></html>", ""},
		{"generic_not_found_code", "", "NotFound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, store := newImageStorageS3Fixture(t)
			target := "private/generated/still-present.png"
			_, err := store.Save(context.Background(), target, "image/png", imageStorageProbePNG)
			require.NoError(t, err)
			f.mu.Lock()
			f.delStatus = http.StatusNotFound
			f.delCode = tc.code
			if tc.code == "" {
				body := tc.body
				f.delRawBody = &body
			}
			f.mu.Unlock()

			err = store.Delete(context.Background(), target)
			require.Error(t, err, "普通 404 必须返回失败，持久清理队列才能保留证据并重试")
			var responseError interface{ HTTPStatusCode() int }
			require.ErrorAs(t, err, &responseError)
			require.Equal(t, http.StatusNotFound, responseError.HTTPStatusCode())
			_, objects := f.snapshot()
			require.Equal(t, 1, objects, "反代失败时真实对象仍存在")

			f.mu.Lock()
			f.delStatus, f.delCode, f.delRawBody = 0, "", nil
			f.mu.Unlock()
			require.NoError(t, store.Delete(context.Background(), target), "路由恢复后仍能重试相同完整对象键")
			requests, objects := f.snapshot()
			require.Zero(t, objects)
			for _, request := range requests[1:] {
				require.Equal(t, http.MethodDelete, request.method)
				require.Equal(t, "/image-bucket/"+target, request.path)
			}
		})
	}
}

func TestS3ImageStoragePresignExpiryNeverExceedsRetention(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPut, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name      string
		presign   int
		retention int
		wantHours int
	}{
		{"default", 0, 0, 24},
		{"short_retention", 24, 1, 1},
		{"short_signature", 2, 48, 2},
		{"long_signature", 168, 48, 48},
		{"default_retention", 168, 0, 24},
		{"long_retention", 168, 8760, 168},
		{"legacy_negative_signature", -1, 12, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewS3ImageStorage(context.Background(), &config.ImageStorageConfig{
				Enabled: true, Endpoint: server.URL, Region: "us-east-1", Bucket: "image-bucket",
				AccessKeyID: "test-ak", SecretAccessKey: "test-sk", ForcePathStyle: true,
				PresignExpiry: tc.presign, RetentionHours: tc.retention,
			})
			require.NoError(t, err)
			rawURL, err := store.Save(context.Background(), "image.png", "image/png", imageStorageProbePNG)
			require.NoError(t, err)
			signedURL, err := url.Parse(rawURL)
			require.NoError(t, err)
			require.Equal(t, strconv.Itoa(tc.wantHours*3600), signedURL.Query().Get("X-Amz-Expires"))
		})
	}
}

func TestS3ImageStorageRejectsInvalidRetentionBeforeBuildingClient(t *testing.T) {
	for _, hours := range []int{-1, config.ImageStorageMaxRetentionHours + 1} {
		store, err := NewS3ImageStorage(context.Background(), &config.ImageStorageConfig{RetentionHours: hours})
		require.Error(t, err)
		require.Nil(t, store)
	}
}

func TestS3ImageStorageSaveUntilRejectsExpiredBeforeUpload(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	for _, deadline := range []time.Time{time.Time{}, time.Now().Add(-time.Second)} {
		_, err := store.SaveUntil(context.Background(), "private/generated/expired.png", "image/png", imageStorageProbePNG, deadline)
		require.Error(t, err)
	}
	requests, objects := f.snapshot()
	require.Empty(t, requests)
	require.Zero(t, objects, "过期的登记不得重新写入对象")
}

func TestS3ImageStorageSaveUntilUsesRemainingFrozenDeadline(t *testing.T) {
	f, store := newImageStorageS3Fixture(t)
	f.putStarted, f.putRelease = make(chan struct{}), make(chan struct{})
	deadline := time.Now().Add(10 * time.Second)
	type result struct {
		url string
		err error
	}
	done := make(chan result, 1)
	go func() {
		rawURL, err := store.SaveUntil(context.Background(), "private/generated/frozen.png", "image/png", imageStorageProbePNG, deadline)
		done <- result{rawURL, err}
	}()
	select {
	case <-f.putStarted:
	case got := <-done:
		t.Fatalf("upload returned before the test server received it: %v", got.err)
	}
	// 模拟上传耗时；签名必须从剩余寿命计算，不能重置为配置的 24 小时或原 10 秒。
	<-time.After(1100 * time.Millisecond)
	close(f.putRelease)
	got := <-done
	require.NoError(t, got.err)
	require.True(t, imageStorageSignedURLWithinDeadline(got.url, deadline))
	signedURL, err := url.Parse(got.url)
	require.NoError(t, err)
	seconds, err := strconv.Atoi(signedURL.Query().Get("X-Amz-Expires"))
	require.NoError(t, err)
	require.Positive(t, seconds)
	require.LessOrEqual(t, seconds, 8)
}

func TestS3ImageStorageSaveUntilDoesNotReturnLinkIfUploadOutlivesDeadline(t *testing.T) {
	for _, publicURL := range []string{"", "https://images.example.test"} {
		t.Run(publicURL, func(t *testing.T) {
			f, store := newImageStorageS3Fixture(t)
			store.publicBaseURL = publicURL
			f.putStarted, f.putRelease = make(chan struct{}), make(chan struct{})
			deadline := time.Now().Add(time.Second)
			done := make(chan error, 1)
			go func() {
				rawURL, err := store.SaveUntil(context.Background(), "private/generated/expired-upload.png", "image/png", imageStorageProbePNG, deadline)
				if rawURL != "" {
					done <- fmt.Errorf("unexpected result URL after expiry")
					return
				}
				done <- err
			}()
			select {
			case <-f.putStarted:
			case err := <-done:
				t.Fatalf("upload returned before the test server received it: %v", err)
			}
			<-time.After(time.Until(deadline) + 10*time.Millisecond)
			close(f.putRelease)
			require.ErrorContains(t, <-done, "deadline expired during upload")
			_, objects := f.snapshot()
			require.Equal(t, 1, objects, "已上传对象交给已登记的持久队列清理，不能以新链接延长")
		})
	}
}

func TestS3ImageStorageSaveUntilAlsoHonorsConfiguredExpiry(t *testing.T) {
	_, store := newImageStorageS3Fixture(t)
	store.presignExpiry = time.Hour
	started := time.Now()
	rawURL, err := store.SaveUntil(context.Background(), "private/generated/short-signature.png", "image/png", imageStorageProbePNG, started.Add(2*time.Hour))
	require.NoError(t, err)
	require.True(t, imageStorageSignedURLWithinDeadline(rawURL, started.Add(time.Hour+time.Second)))
	signedURL, err := url.Parse(rawURL)
	require.NoError(t, err)
	seconds, err := strconv.Atoi(signedURL.Query().Get("X-Amz-Expires"))
	require.NoError(t, err)
	require.LessOrEqual(t, seconds, 3600)
}

func TestS3ImageStorageSignedURLDeadlineRejectsMalformedOrExtendedSignature(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	expiresAt := now.Add(time.Minute)
	for _, rawURL := range []string{
		"https://images.example.test/a.png",
		"https://images.example.test/a.png?X-Amz-Date=invalid&X-Amz-Expires=1",
		"https://images.example.test/a.png?X-Amz-Date=" + now.Format("20060102T150405Z") + "&X-Amz-Expires=0",
		"https://images.example.test/a.png?X-Amz-Date=" + now.Format("20060102T150405Z") + "&X-Amz-Expires=61",
		"https://images.example.test/a.png?X-Amz-Date=" + now.Add(-time.Minute).Format("20060102T150405Z") + "&X-Amz-Expires=1",
	} {
		require.False(t, imageStorageSignedURLWithinDeadline(rawURL, expiresAt))
	}
}
