package service

import (
	"strings"
	"testing"
	"time"
)

func TestPrepareRequestPayloadDetailRedactsAndKeepsFullRequestBody(t *testing.T) {
	largeInput := strings.Repeat("x", OpsRequestPayloadMaxBytes+1024)
	detail := &OpsRequestPayloadDetail{
		RequestID:      "req-1",
		RequestHeaders: `{"Authorization":["Bearer secret"],"Content-Type":["application/json"]}`,
		RequestBody:    `{"model":"gpt-6","api_key":"secret","input":"` + largeInput + `"}`,
		ResponseBody:   `{"output":"ok","cookie":"secret"}`,
	}
	prepareRequestPayloadDetail(detail)
	if detail.RequestHeaders == "" || strings.Contains(detail.RequestHeaders, "Bearer secret") {
		t.Fatalf("request headers were not redacted: %s", detail.RequestHeaders)
	}
	if strings.Contains(detail.RequestBody, "secret") || strings.Contains(detail.ResponseBody, "secret") {
		t.Fatalf("payload secret was not redacted: request=%s response=%s", detail.RequestBody, detail.ResponseBody)
	}
	if len(detail.RequestBody) <= OpsRequestPayloadMaxBytes || !strings.Contains(detail.RequestBody, largeInput) {
		t.Fatalf("request body was truncated: len=%d", len(detail.RequestBody))
	}
	if len(detail.ResponseBody) > OpsRequestPayloadMaxBytes {
		t.Fatal("response body exceeded configured limit")
	}
}

func TestOpsRequestPayloadDetailZeroValueIsSafe(t *testing.T) {
	detail := &OpsRequestPayloadDetail{RequestID: "req-2", CreatedAt: time.Now(), CompletedAt: time.Now()}
	prepareRequestPayloadDetail(detail)
	if detail.RequestHeaders != "" || detail.ResponseHeaders != "" {
		t.Fatalf("empty headers should remain empty: %+v", detail)
	}
}
