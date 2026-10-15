//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrokMediaPoolQuotaDoesNotRestoreSameAccountRetry(t *testing.T) {
	for _, body := range []string{`{"error":{"code":"subscription:free-usage-exhausted"}}`, `{"error":{"message":"spending limit reached"}}`} {
		a := newGrokPoolAccount(1234)
		s, _ := newGrokPoolPolicyGateway(a)
		c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/images/generations", nil)
		resp := grokMediaContentStatusResponse(body)
		resp.StatusCode = 429
		_, err := s.handleGrokMediaErrorResponse(context.Background(), resp, c, a, "trace", "grok-imagine-image")
		var failover *UpstreamFailoverError
		require.ErrorAs(t, err, &failover)
		require.True(t, failover.ShouldRetryNextAccount())
		require.False(t, failover.RetryableOnSameAccount)
	}
}

func TestGrokVideoErrorWithTaskIDRetainsOwnershipWithoutFailover(t *testing.T) {
	for _, body := range []string{`{"request_id":"accepted-task","error":{"message":"service unavailable"}}`, `{"id":"accepted-task","status":"queued","error":{"message":"quota"}}`} {
		upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(body)}
		upstream.response.StatusCode = 503
		s := &OpenAIGatewayService{httpUpstream: upstream}
		c, w := grokMediaContentTestContext(http.MethodPost, "/v1/videos/generations", nil)
		DeferSeedanceResponse(c)
		result, err := s.ForwardGrokMedia(context.Background(), c, grokMediaContentTestAccount(), GrokMediaEndpointVideosGenerations, "", []byte(`{"model":"grok-imagine-video","prompt":"waves","duration":8}`), "application/json")
		require.NoError(t, err)
		require.Equal(t, "accepted-task", result.ResponseID)
		require.False(t, c.Writer.Written())
		require.Len(t, upstream.requests, 1)
		s.CommitSeedanceResponse(c)
		require.Equal(t, 503, w.Code)
		require.JSONEq(t, body, w.Body.String())
	}
}

func TestGrokVideoAmbiguousCreateFailureStopsAccountRetry(t *testing.T) {
	for _, body := range []string{`{"request_id":"task-a","id":"task-b"}`, `{"error":{"message":"internal server error"}}`} {
		upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(body)}
		upstream.response.StatusCode = 500
		s := &OpenAIGatewayService{httpUpstream: upstream}
		c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/videos/generations", nil)
		_, err := s.ForwardGrokMedia(context.Background(), c, grokMediaContentTestAccount(), GrokMediaEndpointVideosGenerations, "", []byte(`{"model":"grok-imagine-video","prompt":"waves"}`), "application/json")
		var failover *UpstreamFailoverError
		require.ErrorAs(t, err, &failover)
		require.False(t, failover.ShouldRetryNextAccount())
		require.False(t, failover.RetryableOnSameAccount)
	}
}

func TestSeedanceErrorWithTaskIDRetainsOriginalResponse(t *testing.T) {
	body := `{"id":"accepted-task","request_id":"http-trace","error":{"message":"service unavailable"}}`
	upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(body)}
	upstream.response.StatusCode = 503
	s := &OpenAIGatewayService{httpUpstream: upstream}
	c, w := grokMediaContentTestContext(http.MethodPost, "/api/v3/contents/generations/tasks", nil)
	DeferSeedanceResponse(c)
	result, err := s.ForwardSeedance(context.Background(), c, seedanceTestAccount(), SeedanceEndpointCreate, "", []byte(`{"model":"video","content":[{"type":"text","text":"waves"}]}`))
	require.NoError(t, err)
	require.Equal(t, "seedance:accepted-task", result.ResponseID)
	require.False(t, c.Writer.Written())
	s.CommitSeedanceResponse(c)
	require.Equal(t, 503, w.Code)
	require.JSONEq(t, body, w.Body.String())
}
