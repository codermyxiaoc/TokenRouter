package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// videoLifecycleAccountsDown 模拟原上游账号被删除或长期不可读。
type videoLifecycleAccountsDown struct {
	AccountRepository
}

func (videoLifecycleAccountsDown) GetByID(context.Context, int64) (*Account, error) {
	return nil, errors.New("account unavailable")
}

// submitVideoForAudit 受理一个任务并返回其本地 ID 与仓储内记录。
func submitVideoForAudit(t *testing.T, s *VideoTaskService, key *APIKey) (string, *videoLifecycleRepo) {
	t.Helper()
	created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","resolution":"720p"}`), IdempotencyKey: "audit-" + t.Name()})
	require.NoError(t, err)
	return created.LocalTaskID, s.repo.(*videoLifecycleRepo)
}

// ageVideoTask 把任务的创建与完成时间推到所有期限之前，模拟 48 小时后的恢复轮。
func ageVideoTask(repo *videoLifecycleRepo, id string) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	old := time.Now().UTC().Add(-72 * time.Hour)
	repo.tasks[id].CreatedAt = old
	if repo.tasks[id].CompletedAt != nil {
		completed := time.Now().UTC().Add(-48 * time.Hour)
		repo.tasks[id].CompletedAt = &completed
	}
}

func advanceVideoTask(t *testing.T, s *VideoTaskService, id string) *VideoTaskRecord {
	t.Helper()
	task, err := s.repo.Get(context.Background(), id)
	require.NoError(t, err)
	s.advance(context.Background(), task)
	fresh, err := s.repo.Get(context.Background(), id)
	require.NoError(t, err)
	return fresh
}

func videoContentStatusClient(status int, contentType string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, ContentLength: 1, Body: io.NopCloser(strings.NewReader("v"))}, nil
	})}
}

// P1-02：上游返回完成和格式正确的 URL，但内容持续 404：不能扣费；超过交付期限后释放。
func TestVideoCompletedButUnretrievableNeverCaptures(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	s.deliveryProbe = s.probeVideoContent
	s.contentHTTP = videoContentStatusClient(http.StatusNotFound, "text/html")
	id, repo := submitVideoForAudit(t, s, key)
	count := int64(200_000)
	u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{Tokens: &count, DurationSeconds: 8}, VideoURL: "https://cdn.example.com/video.mp4", Body: []byte(`{"status":"completed"}`)}

	fresh := advanceVideoTask(t, s, id)
	require.Equal(t, "completed", fresh.Status)
	require.NotEqual(t, "settled", fresh.BillingStatus)
	require.Zero(t, b.captures, "成品读取 404 时不能扣费")
	require.Equal(t, videoContentProbePendingMessage, fresh.ErrorMessage)

	// 期限内再次推进必须重新查询以取得新地址，而不是走免查询快速结算。
	polls := u.polls
	fresh = advanceVideoTask(t, s, id)
	require.Greater(t, u.polls, polls)
	require.Zero(t, b.captures)

	ageVideoTask(repo, id)
	fresh = advanceVideoTask(t, s, id)
	require.Equal(t, "failed", fresh.Status)
	require.Equal(t, "released", fresh.BillingStatus)
	require.Zero(t, b.captures)
	require.Equal(t, 1, b.releases)

	// 释放后迟到的可读结果不能补扣。
	s.contentHTTP = videoContentStatusClient(http.StatusPartialContent, "video/mp4")
	fresh = advanceVideoTask(t, s, id)
	require.Equal(t, "released", fresh.BillingStatus)
	require.Zero(t, b.captures)
}

// 可取回的成品正常结算，探测只读首字节且不改变原记录。
func TestVideoCompletedAndRetrievableCapturesOnce(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	s.deliveryProbe = s.probeVideoContent
	var ranges []string
	s.contentHTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		ranges = append(ranges, req.Header.Get("Range"))
		return &http.Response{StatusCode: http.StatusPartialContent, Header: http.Header{"Content-Type": {"video/mp4"}}, ContentLength: 1, Body: io.NopCloser(strings.NewReader("v"))}, nil
	})}
	id, _ := submitVideoForAudit(t, s, key)
	count := int64(200_000)
	u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{Tokens: &count, DurationSeconds: 8}, VideoURL: "https://cdn.example.com/video.mp4", Body: []byte(`{"status":"completed"}`)}
	fresh := advanceVideoTask(t, s, id)
	require.Equal(t, "settled", fresh.BillingStatus)
	require.Equal(t, 1, b.captures)
	require.Equal(t, []string{"bytes=0-0"}, ranges)
	fresh = advanceVideoTask(t, s, id)
	require.Equal(t, 1, b.captures, "重复推进不能重复扣费")
}

// P1-03：48 小时前已完成但缺产物，随后查询持续报错：不能无限冻结，必须释放。
func TestVideoCompletedUnusablePollFailureReleasesPastDeadline(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	// 无论夹具协议是否声明内容路径，成品都不可取回。
	s.deliveryProbe = func(context.Context, *VideoTaskRecord) error { return errVideoContentUnavailable }
	id, repo := submitVideoForAudit(t, s, key)
	repo.mu.Lock()
	completed := time.Now().UTC()
	repo.tasks[id].Status, repo.tasks[id].BillingStatus, repo.tasks[id].VideoURL, repo.tasks[id].CompletedAt = "completed", "reconciliation", "", &completed
	repo.mu.Unlock()
	u.poll, u.pollErr = nil, errors.New("upstream query down")

	fresh := advanceVideoTask(t, s, id)
	require.Equal(t, "completed", fresh.Status, "期限内查询失败只等待，不提前释放")
	require.Zero(t, b.captures+b.releases)

	ageVideoTask(repo, id)
	for range 3 {
		fresh = advanceVideoTask(t, s, id)
	}
	require.Equal(t, "failed", fresh.Status)
	require.Equal(t, "released", fresh.BillingStatus)
	require.Zero(t, b.captures)
	require.Equal(t, 1, b.releases, "重复推进只释放一次")
}

// P1-04：原账号永久不可读时，过期任务也必须释放，不能因提前返回而一直冻结。
func TestVideoAccountUnavailablePastDeadlineReleases(t *testing.T) {
	s, key, _, b, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	id, repo := submitVideoForAudit(t, s, key)
	s.accounts = videoLifecycleAccountsDown{}

	fresh := advanceVideoTask(t, s, id)
	require.NotEqual(t, "failed", fresh.Status, "期限内账号暂不可读只等待")
	require.Zero(t, b.captures+b.releases)

	ageVideoTask(repo, id)
	for range 3 {
		fresh = advanceVideoTask(t, s, id)
	}
	require.Equal(t, "failed", fresh.Status)
	require.Equal(t, "released", fresh.BillingStatus)
	require.Zero(t, b.captures)
	require.Equal(t, 1, b.releases)
}

// R1：200/206 但正文为空或首读报错，都不能证明成品可取回，不能扣费。
func TestVideoProbeMustReadContentBeforeCapture(t *testing.T) {
	for name, body := range map[string]io.Reader{
		"empty_200":  strings.NewReader(""),
		"read_error": iotestErrReader{},
	} {
		t.Run(name, func(t *testing.T) {
			s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoToken)
			s.deliveryProbe = s.probeVideoContent
			s.contentHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusPartialContent, Header: http.Header{"Content-Type": {"video/mp4"}}, ContentLength: -1, Body: io.NopCloser(body)}, nil
			})}
			id, _ := submitVideoForAudit(t, s, key)
			count := int64(200_000)
			u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{Tokens: &count, DurationSeconds: 8}, VideoURL: "https://cdn.example.com/video.mp4", Body: []byte(`{"status":"completed"}`)}
			fresh := advanceVideoTask(t, s, id)
			require.NotEqual(t, "settled", fresh.BillingStatus)
			require.Zero(t, b.captures, "空正文或读取失败不能扣费")
		})
	}
}

type iotestErrReader struct{}

func (iotestErrReader) Read([]byte) (int, error) { return 0, errors.New("stream reset") }

// R2：探测耗时接近结算预算时，扣费仍拿到完整预算，不能因上下文过期长期冻结。
func TestSlowVideoProbeDoesNotExpireSettlementContext(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	var expired atomic.Int32
	s.billing = ctxCheckingVideoBilling{videoLifecycleBilling: b, expired: &expired}
	s.deliveryProbe = func(ctx context.Context, _ *VideoTaskRecord) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		// 模拟探测用尽调用方预算：等到结算上下文过期后才返回成功。
		time.Sleep(time.Until(deadline) + 50*time.Millisecond)
		return nil
	}
	id, _ := submitVideoForAudit(t, s, key)
	count := int64(200_000)
	u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{Tokens: &count, DurationSeconds: 8}, VideoURL: "https://cdn.example.com/video.mp4", Body: []byte(`{"status":"completed"}`)}
	fresh := advanceVideoTask(t, s, id)
	require.Equal(t, "settled", fresh.BillingStatus)
	require.Equal(t, 1, b.captures)
	require.Zero(t, expired.Load(), "扣费不能收到已过期的上下文")
}

// ctxCheckingVideoBilling 像真实数据库一样拒绝已过期上下文的扣费。
type ctxCheckingVideoBilling struct {
	*videoLifecycleBilling
	expired *atomic.Int32
}

func (b ctxCheckingVideoBilling) CaptureBatchImageBalance(ctx context.Context, c *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	if err := ctx.Err(); err != nil {
		b.expired.Add(1)
		return nil, err
	}
	return b.videoLifecycleBilling.CaptureBatchImageBalance(ctx, c)
}
