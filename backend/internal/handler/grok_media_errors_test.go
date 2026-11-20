//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGrokImagesPreservesUpstreamErrorOnSyncAndAsyncRoutes(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		accountType := "api-key"
		if oauth {
			accountType = "oauth"
		}
		for _, async := range []bool{false, true} {
			route := "sync"
			if async {
				route = "async"
			}
			t.Run(accountType+"/"+route, func(t *testing.T) {
				h, slots, _, upstream := newGrokMediaSlotHandler(t, oauth, false)
				h.grokMediaEligibilityProber = grokMediaSlotProber(func(context.Context, int64) (bool, string, error) {
					return true, "test account is eligible", nil
				})
				upstream.call = func(req *http.Request, _ int64) (*http.Response, error) {
					assert.Equal(t, "/v1/images/edits", req.URL.Path)
					return &http.Response{
						StatusCode: http.StatusBadRequest,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"code":"invalid_image","error":"Image could not be decoded: https://example.test/image?access_token=secret&size=1","debug":"private upstream diagnostic"}`)),
					}, nil
				}
				c, recorder := grokMediaSlotContext(context.Background(), true)
				path := "/v1/images/edits"
				if async {
					path += "/async"
				}
				c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"grok-imagine-image","prompt":"test","image":{"url":"https://example.test/input.png"}}`))
				c.Request.Header.Set("Content-Type", "application/json")

				var responseBody []byte
				if async {
					store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
					tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
					asyncHandler := NewAsyncImageHandler(tasks, h)
					asyncHandler.Submit(c)
					require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
					taskID := gjson.GetBytes(recorder.Body.Bytes(), "task_id").String()
					owner := service.ImageTaskOwner{UserID: 10, APIKeyID: 20}
					require.Eventually(t, func() bool {
						task, err := tasks.Get(context.Background(), owner, taskID)
						return err == nil && task.Status != service.ImageTaskStatusProcessing
					}, time.Second, 10*time.Millisecond)
					poll, pollRecorder := grokMediaSlotContext(context.Background(), false)
					poll.Params[0].Key, poll.Params[0].Value = "task_id", taskID
					asyncHandler.Get(poll)
					require.Equal(t, http.StatusOK, pollRecorder.Code)
					responseBody = pollRecorder.Body.Bytes()
					require.Equal(t, service.ImageTaskStatusFailed, gjson.GetBytes(responseBody, "status").String())
					require.Equal(t, int64(http.StatusBadRequest), gjson.GetBytes(responseBody, "http_status").Int())
				} else {
					h.GrokImages(c)
					require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
					responseBody = recorder.Body.Bytes()
				}
				require.Equal(t, "Image could not be decoded: https://example.test/image?access_token=***&size=1", gjson.GetBytes(responseBody, "error.message").String())
				require.Equal(t, "invalid_image", gjson.GetBytes(responseBody, "error.code").String())
				require.Equal(t, "invalid_request_error", gjson.GetBytes(responseBody, "error.type").String())
				require.NotContains(t, string(responseBody), "secret")
				require.NotContains(t, string(responseBody), "private upstream diagnostic")
				require.Equal(t, 1, upstream.calls, "deterministic image errors must not retry another account")
				slots.assertReleased(t)
			})
		}
	}
}

// 超出源图上限应由同步或异步入口处理，且不会触发账号资格探测及预占。
func TestGrokEditSourceLimitBeforeScheduling(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "async"}[async], func(t *testing.T) {
			h, slots, _, upstream := newGrokMediaSlotHandler(t, false, false)
			c, rec := grokMediaSlotContext(context.Background(), true)
			path := "/v1/images/edits"
			if async {
				path += "/async"
			}
			c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"grok-imagine-image","prompt":"edit","images":["https://example.test/1","https://example.test/2","https://example.test/3","https://example.test/4","https://example.test/5","https://example.test/6"]}`))
			c.Request.Header.Set("Content-Type", "application/json")
			if async {
				store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
				tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
				asyncHandler := NewAsyncImageHandler(tasks, h)
				asyncHandler.Submit(c)
				require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
				taskID := gjson.GetBytes(rec.Body.Bytes(), "task_id").String()
				owner := service.ImageTaskOwner{UserID: 10, APIKeyID: 20}
				require.Eventually(t, func() bool {
					task, err := tasks.Get(context.Background(), owner, taskID)
					return err == nil && task.Status == service.ImageTaskStatusFailed
				}, time.Second, 10*time.Millisecond)
				poll, res := grokMediaSlotContext(context.Background(), false)
				poll.Params[0].Key, poll.Params[0].Value = "task_id", taskID
				asyncHandler.Get(poll)
				require.Contains(t, res.Body.String(), "maximum of 5 source images")
			} else {
				h.GrokImages(c)
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			}
			require.Zero(t, upstream.calls)
			slots.assertReleased(t)
		})
	}
}
