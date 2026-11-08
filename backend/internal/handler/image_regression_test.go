//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type regressionAsyncResult struct {
	code                                 int
	task                                 service.ImageTask
	generated, saved, captured, released int32
}

// 使用内存账务计数桩与真实异步交付流程，不调用上游、不修改任何本地用户余额。
func regressionRunAsyncOutput(t *testing.T, format string, imageBytes []byte) regressionAsyncResult {
	t.Helper()
	storage := &asyncPreflightStorage{}
	h, _ := newPreflightHandler(storage)
	var generated, captured, released atomic.Int32
	h.execute = func(_ string, c *gin.Context) {
		generated.Add(1)
		holder := service.AsyncImageDeferredCaptureFromContext(c.Request.Context())
		registered := holder.Defer(func(context.Context) error { captured.Add(1); return nil }, func(context.Context) error { released.Add(1); return nil })
		if !registered {
			t.Error("无法登记异步延迟结算回调")
		}
		c.JSON(http.StatusOK, gin.H{"output_format": format, "data": []gin.H{{"b64_json": base64.StdEncoding.EncodeToString(imageBytes)}}})
	}
	router := asyncCapacityRouter(h)
	router.GET("/v1/images/tasks/:task_id", h.Get)
	body := fmt.Sprintf(`{"model":"gpt-image-2.6-flare","prompt":"a moving red square","output_format":%q}`, format)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	accepted := httptest.NewRecorder()
	router.ServeHTTP(accepted, req)
	result := regressionAsyncResult{code: accepted.Code}
	if accepted.Code != http.StatusAccepted {
		result.generated, result.saved, result.captured, result.released = generated.Load(), storage.saves.Load(), captured.Load(), released.Load()
		return result
	}
	require.Eventually(t, func() bool {
		poll := httptest.NewRecorder()
		router.ServeHTTP(poll, httptest.NewRequest(http.MethodGet, accepted.Header().Get("Location"), nil))
		if json.Unmarshal(poll.Body.Bytes(), &result.task) != nil {
			return false
		}
		terminal := result.task.Status == service.ImageTaskStatusCompleted || result.task.Status == service.ImageTaskStatusFailed
		return terminal && captured.Load()+released.Load() > 0
	}, 5*time.Second, 5*time.Millisecond, "任务必须结束并完成唯一账务动作")
	// 重复轮询只读取持久终态，不能重新执行上游或账务动作。
	for range 5 {
		poll := httptest.NewRecorder()
		router.ServeHTTP(poll, httptest.NewRequest(http.MethodGet, accepted.Header().Get("Location"), nil))
		require.Equal(t, http.StatusOK, poll.Code)
	}
	result.generated, result.saved, result.captured, result.released = generated.Load(), storage.saves.Load(), captured.Load(), released.Load()
	return result
}

// 不能受理SVG并付费生成以后才发现存储不支持；支持交付或在生成之前拒绝均可。
func TestRegressionAsyncSVGCanDeliverOrRejectBeforeGeneration(t *testing.T) {
	vector := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><rect width="16" height="16" fill="red"/></svg>`)
	result := regressionRunAsyncOutput(t, "svg", vector)
	if result.code == http.StatusBadRequest || result.code == http.StatusUnsupportedMediaType {
		require.Zero(t, result.generated)
		require.Zero(t, result.captured)
		return
	}
	t.Logf("SVG: status=%s HTTP=%d 生成=%d 存储=%d 捕获=%d 释放=%d error=%s", result.task.Status, result.task.HTTPStatus, result.generated, result.saved, result.captured, result.released, result.task.Error)
	require.Equal(t, http.StatusAccepted, result.code)
	require.Equal(t, service.ImageTaskStatusCompleted, result.task.Status, "SVG不支持归档时应在付费生成前拒绝")
	require.EqualValues(t, 1, result.captured)
	require.Zero(t, result.released)
}

// 动图只有完整交付才可捕获；后续帧缺失时应失败释放且重复轮询不能重复扣费。
func TestRegressionAsyncGIFDeliveryAndBilling(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	frames := []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 4, 4), palette), image.NewPaletted(image.Rect(0, 0, 4, 4), palette)}
	frames[1].SetColorIndex(1, 1, 1)
	var buf bytes.Buffer
	require.NoError(t, gif.EncodeAll(&buf, &gif.GIF{Image: frames, Delay: []int{10, 10}}))
	full := buf.Bytes()
	var truncated []byte
	for cut := 1; cut < len(full); cut++ {
		_, firstErr := gif.Decode(bytes.NewReader(full[:cut]))
		_, allErr := gif.DecodeAll(bytes.NewReader(full[:cut]))
		if firstErr == nil && allErr != nil {
			truncated = full[:cut]
			break
		}
	}
	require.NotEmpty(t, truncated)
	for _, test := range []struct {
		name      string
		data      []byte
		delivered bool
	}{
		{"complete_animation", full, true},
		{"truncated_after_first_frame", truncated, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := regressionRunAsyncOutput(t, "gif", test.data)
			t.Logf("GIF: status=%s 生成=%d 存储=%d 捕获=%d 释放=%d", result.task.Status, result.generated, result.saved, result.captured, result.released)
			require.Equal(t, http.StatusAccepted, result.code)
			require.EqualValues(t, 1, result.generated)
			if test.delivered {
				require.Equal(t, service.ImageTaskStatusCompleted, result.task.Status)
				require.EqualValues(t, 1, result.saved)
				require.EqualValues(t, 1, result.captured)
				require.Zero(t, result.released)
			} else {
				require.Equal(t, service.ImageTaskStatusFailed, result.task.Status, "动画不完整不能报告交付成功")
				require.Zero(t, result.captured, "动画不完整必须不扣费")
				require.EqualValues(t, 1, result.released)
			}
		})
	}
}
