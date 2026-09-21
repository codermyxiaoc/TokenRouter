package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// requestDetailCaptureWriter 在不改变响应行为的前提下保存有界响应片段。
// 该包装器只在运维中间件内部使用，响应正文超过上限会被截断并标记。
type requestDetailCaptureWriter struct {
	gin.ResponseWriter
	mu        sync.Mutex
	response  bytes.Buffer
	truncated bool
	maxBytes  int
}

func newRequestDetailCaptureWriter(writer gin.ResponseWriter) *requestDetailCaptureWriter {
	return &requestDetailCaptureWriter{ResponseWriter: writer, maxBytes: service.OpsRequestPayloadMaxBytes}
}

func (w *requestDetailCaptureWriter) capture(chunk []byte) {
	if len(chunk) == 0 || w.maxBytes <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.maxBytes + 1 - w.response.Len()
	if remaining <= 0 {
		w.truncated = true
		return
	}
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
		w.truncated = true
	}
	_, _ = w.response.Write(chunk)
	if w.response.Len() > w.maxBytes {
		w.truncated = true
	}
}

func (w *requestDetailCaptureWriter) responseSnapshot() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	value := w.response.Bytes()
	if len(value) > w.maxBytes {
		value = value[:w.maxBytes]
	}
	return string(value), w.truncated
}

func (w *requestDetailCaptureWriter) Write(body []byte) (int, error) {
	w.capture(body)
	return w.ResponseWriter.Write(body)
}

func (w *requestDetailCaptureWriter) WriteString(body string) (int, error) {
	w.capture([]byte(body))
	return w.ResponseWriter.WriteString(body)
}

func (w *requestDetailCaptureWriter) Flush() {
	w.ResponseWriter.Flush()
}

func (w *requestDetailCaptureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.Hijack()
}

func (w *requestDetailCaptureWriter) CloseNotify() <-chan bool {
	return w.ResponseWriter.CloseNotify()
}

func (w *requestDetailCaptureWriter) Pusher() http.Pusher {
	return w.ResponseWriter.Pusher()
}

func (w *requestDetailCaptureWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

var _ gin.ResponseWriter = (*requestDetailCaptureWriter)(nil)

type requestDetailBodyReader struct {
	inner  io.ReadCloser
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (r *requestDetailBodyReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if n > 0 {
		r.mu.Lock()
		// 失败请求的正文需要完整复现；请求大小仍由入口层 MaxRequestBodySize 控制。
		_, _ = r.buffer.Write(p[:n])
		r.mu.Unlock()
	}
	return n, err
}

func (r *requestDetailBodyReader) Close() error { return r.inner.Close() }

func (r *requestDetailBodyReader) snapshot() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buffer.String(), false
}

func requestDetailHeaders(headers http.Header) string {
	values := make(map[string][]string, len(headers))
	for key, items := range headers {
		copied := make([]string, 0, len(items))
		for _, item := range items {
			copied = append(copied, item)
		}
		values[key] = copied
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func requestDetailRequestID(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if value, ok := c.Request.Context().Value(ctxkey.RequestID).(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(c.Writer.Header().Get("X-Sub2API-Request-ID"))
}

func recordRequestPayloadDetail(c *gin.Context, ops *service.OpsService, writer *requestDetailCaptureWriter, bodyReader *requestDetailBodyReader, startedAt time.Time) {
	if c == nil || ops == nil || writer == nil || !ops.IsMonitoringEnabled(c.Request.Context()) {
		return
	}
	requestID := requestDetailRequestID(c)
	if requestID == "" {
		return
	}
	modelName := ""
	if value, ok := c.Get(opsModelKey); ok {
		modelName, _ = value.(string)
	}
	platform := resolveOpsPlatform(getOpsAPIKey(c), guessPlatformFromPath(c.Request.URL.Path))
	if apiKey := getOpsAPIKey(c); apiKey != nil && apiKey.Group != nil && apiKey.Group.Platform != "" {
		platform = apiKey.Group.Platform
	}
	stream := c.GetBool(opsStreamKey)
	requestBody, requestTruncated := bodyReader.snapshot()
	responseBody, responseTruncated := writer.responseSnapshot()
	clientRequestID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
	detail := &service.OpsRequestPayloadDetail{
		RequestID:         requestID,
		ClientRequestID:   clientRequestID,
		Method:            c.Request.Method,
		Path:              c.Request.URL.Path,
		InboundEndpoint:   GetInboundEndpoint(c),
		UpstreamEndpoint:  GetUpstreamEndpoint(c, platform),
		Platform:          platform,
		Model:             modelName,
		StatusCode:        c.Writer.Status(),
		Stream:            stream,
		RequestHeaders:    requestDetailHeaders(c.Request.Header),
		RequestBody:       requestBody,
		ResponseHeaders:   requestDetailHeaders(c.Writer.Header()),
		ResponseBody:      responseBody,
		RequestTruncated:  requestTruncated,
		ResponseTruncated: responseTruncated,
		CreatedAt:         startedAt,
		CompletedAt:       time.Now().UTC(),
	}
	// 中间件不能阻塞用户响应，数据库写入使用短超时的后台上下文。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := ops.RecordRequestPayloadDetail(ctx, detail); err != nil {
			// 详情写入失败不能影响已发送的响应，但必须留下可检索的 request_id，
			// 否则管理端只能看到后续的“Request detail not found”。
			logger.LegacyPrintf("handler.ops_request_detail", "[OpsRequestDetail] persist failed: request_id=%s err=%v", detail.RequestID, err)
		}
	}()
}
