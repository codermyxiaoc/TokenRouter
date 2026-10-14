//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type legacyVideoTransportStub struct {
	do func(*http.Request) (*http.Response, error)
}

func (s *legacyVideoTransportStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return s.do(req)
}

func (s *legacyVideoTransportStub) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxy, accountID, concurrency)
}

// legacyVideoBlockedBody 模拟已返回响应头、正文始终未结束的上游；取消请求才终止读取。
type legacyVideoBlockedBody struct {
	ctx    context.Context
	closed bool
}

func (b *legacyVideoBlockedBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *legacyVideoBlockedBody) Close() error { b.closed = true; return nil }

func forwardLegacyVideoForTransportTest(s *OpenAIGatewayService, ctx context.Context, c *gin.Context, endpoint GrokMediaEndpoint) (*OpenAIForwardResult, error) {
	if endpoint.IsSeedance() {
		return s.ForwardSeedance(ctx, c, seedanceTestAccount(), endpoint, "task-1", []byte(`{"model":"video","content":[{"type":"text","text":"waves"}]}`))
	}
	return s.ForwardGrokMedia(ctx, c, grokMediaContentTestAccount(), endpoint, "task-1", []byte(`{"model":"grok-imagine-video","prompt":"waves"}`), "application/json")
}

// 完整转发链路必须在收到响应头后仍能结束挂起的正文，并关闭 Body 释放传输连接。
func TestLegacyVideoTransportBoundsStalledResponseBody(t *testing.T) {
	for _, endpoint := range []GrokMediaEndpoint{
		GrokMediaEndpointVideosGenerations, GrokMediaEndpointVideosEdits, GrokMediaEndpointVideosExtensions,
		GrokMediaEndpointVideoStatus, SeedanceEndpointCreate, SeedanceEndpointStatus,
	} {
		t.Run(string(endpoint), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var body *legacyVideoBlockedBody
				calls := 0
				upstream := &legacyVideoTransportStub{do: func(req *http.Request) (*http.Response, error) {
					calls++
					body = &legacyVideoBlockedBody{ctx: req.Context()}
					if !endpoint.IsSeedance() {
						require.Equal(t, HTTPUpstreamProfileGrok, HTTPUpstreamProfileFromContext(req.Context()))
					}
					if isGrokAsyncVideoCreateEndpoint(endpoint) || endpoint == SeedanceEndpointCreate {
						require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
				}}
				svc := &OpenAIGatewayService{httpUpstream: upstream}
				c, recorder := grokMediaContentTestContext(http.MethodPost, "/v1/videos/generations", nil)
				started := time.Now()
				result, err := forwardLegacyVideoForTransportTest(svc, context.Background(), c, endpoint)
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.Nil(t, result, "查询超时不得生成失败任务观测或计费用量")
				require.True(t, body.closed)
				require.Equal(t, 1, calls)
				require.Empty(t, recorder.Body.String(), "正文不完整时不能提交伪成功响应")
				want := legacyVideoCreateTimeout
				if endpoint == GrokMediaEndpointVideoStatus || endpoint == SeedanceEndpointStatus {
					want = legacyVideoStatusTimeout
				}
				require.Equal(t, want, time.Since(started))
			})
		})
	}
}

func TestLegacyVideoStatusPreservesCallerDeadlineAndCancellation(t *testing.T) {
	for _, endpoint := range []GrokMediaEndpoint{GrokMediaEndpointVideoStatus, SeedanceEndpointStatus} {
		t.Run(string(endpoint), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				upstreamCtx, release := legacyVideoUpstreamContext(ctx, endpoint)
				defer release()
				deadline, ok := upstreamCtx.Deadline()
				require.True(t, ok)
				require.Equal(t, time.Second, time.Until(deadline))
				cancel()
				require.ErrorIs(t, upstreamCtx.Err(), context.Canceled)
			})
		})
	}
}

func TestLegacyVideoCreateTransportFailureDoesNotReplay(t *testing.T) {
	for _, endpoint := range []GrokMediaEndpoint{GrokMediaEndpointVideosGenerations, GrokMediaEndpointVideosEdits, GrokMediaEndpointVideosExtensions, SeedanceEndpointCreate} {
		t.Run(string(endpoint), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				upstream := &legacyVideoTransportStub{do: func(req *http.Request) (*http.Response, error) {
					calls++
					<-req.Context().Done()
					return nil, req.Context().Err()
				}}
				svc := &OpenAIGatewayService{httpUpstream: upstream}
				c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/videos/generations", nil)
				result, err := forwardLegacyVideoForTransportTest(svc, context.Background(), c, endpoint)
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, 1, calls)
				var failover *UpstreamFailoverError
				if errors.As(err, &failover) {
					require.False(t, failover.ShouldRetryNextAccount())
					require.False(t, failover.RetryableOnSameAccount)
				}
			})
		})
	}
}

func TestGrokAsyncVideoCreateRedirectIsNotExposedOrReplayed(t *testing.T) {
	upstream := &legacyVideoTransportStub{do: func(req *http.Request) (*http.Response, error) {
		require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"https://other.example/create"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}}
	c, recorder := grokMediaContentTestContext(http.MethodPost, "/v1/videos/generations", nil)
	result, err := forwardLegacyVideoForTransportTest(&OpenAIGatewayService{httpUpstream: upstream}, context.Background(), c, GrokMediaEndpointVideosGenerations)
	require.Error(t, err)
	require.Nil(t, result)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.Empty(t, recorder.Header().Get("Location"))
}

func TestLegacyVideoTransportKeepsOtherMediaContextContracts(t *testing.T) {
	for _, endpoint := range []GrokMediaEndpoint{GrokMediaEndpointImagesGenerations, GrokMediaEndpointImagesEdits, GrokMediaEndpointVideoContent} {
		ctx, cancel := context.WithCancel(context.Background())
		upstreamCtx, release := legacyVideoUpstreamContext(ctx, endpoint)
		cancel()
		require.NoError(t, upstreamCtx.Err(), "同步图片和下载保留原脱离客户端取消的约定")
		_, hasDeadline := upstreamCtx.Deadline()
		require.False(t, hasDeadline, "不向同步图片和下载套用短视频查询预算")
		release()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	upstreamCtx, release := legacyVideoUpstreamContext(ctx, SeedanceEndpointDelete)
	defer release()
	require.ErrorIs(t, upstreamCtx.Err(), context.Canceled, "Seedance 删除保留原调用方取消")
}
