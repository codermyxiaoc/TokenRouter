package routes

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

type creativeGrokVideoTaskLookup func(context.Context, int64, int64, string) (*service.LegacyVideoTask, error)

// 分组平台在目录准入后变化时拒绝，不能误交给另一套视频资金链路。
func requireCreativeGrokVideoPlatform(c *gin.Context) {
	if getGroupPlatform(c) != service.PlatformGrok {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{"type": "permission_error", "message": "Creative Grok video requires the original Grok group"}})
		return
	}
	c.Next()
}

// Grok 私有桥复用正式鉴权、调度、参考图与账务；不监听额外端口。
type creativeGrokVideoGatewayAdapter struct {
	router http.Handler
	lookup creativeGrokVideoTaskLookup
}

func newCreativeGrokVideoGatewayAdapter(lookup creativeGrokVideoTaskLookup, common []gin.HandlerFunc, create, content gin.HandlerFunc) *creativeGrokVideoGatewayAdapter {
	router := gin.New()
	router.POST("/v1/videos/generations", append(append([]gin.HandlerFunc{}, common...), create)...)
	router.GET("/v1/videos/:request_id/content", append(append([]gin.HandlerFunc{}, common...), content)...)
	return &creativeGrokVideoGatewayAdapter{router: router, lookup: lookup}
}

func (a *creativeGrokVideoGatewayAdapter) request(ctx context.Context, key *service.APIKey, method, path string, body []byte) (*http.Request, error) {
	if a == nil || a.router == nil || key == nil || key.ManagedBy == nil || *key.ManagedBy != service.CreativeManagedBy || key.Key == "" {
		return nil, service.ErrCanvasUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://canvas-gateway.internal"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.RemoteAddr = "127.0.0.1:0"
	req.Header.Set("Authorization", "Bearer "+key.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TokenRouter-Canvas")
	return req, nil
}

func (a *creativeGrokVideoGatewayAdapter) Generate(ctx context.Context, key *service.APIKey, body []byte) (service.CreativeTextGatewayResponse, error) {
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := a.request(work, key, http.MethodPost, "/v1/videos/generations", body)
	if err != nil {
		return service.CreativeTextGatewayResponse{}, err
	}
	// 创建响应仅包含有界 JSON，复用现有非流式响应缓冲，不缓存视频正文。
	writer := &creativeTextResponseWriter{header: make(http.Header), cancel: cancel, closed: make(chan bool, 1)}
	stop := context.AfterFunc(work, func() {
		select {
		case writer.closed <- true:
		default:
		}
	})
	defer stop()
	a.router.ServeHTTP(writer, req)
	status, response := writer.snapshot()
	return service.CreativeTextGatewayResponse{StatusCode: status, Body: response}, writer.failure()
}

func (a *creativeGrokVideoGatewayAdapter) Task(ctx context.Context, userID, keyID int64, id string) (*service.LegacyVideoTask, error) {
	if a == nil || a.lookup == nil {
		return nil, service.ErrCanvasUnavailable
	}
	return a.lookup(ctx, userID, keyID, id)
}

func (a *creativeGrokVideoGatewayAdapter) OpenContent(ctx context.Context, key *service.APIKey, id string) (*http.Response, error) {
	work, cancel := context.WithCancel(ctx)
	req, err := a.request(work, key, http.MethodGet, "/v1/videos/"+url.PathEscape(id)+"/content", nil)
	if err != nil {
		cancel()
		return nil, err
	}
	reader, pipe := io.Pipe()
	writer := &creativeGrokVideoStreamWriter{header: make(http.Header), pipe: pipe, ready: make(chan *http.Response, 1), closed: make(chan bool, 1)}
	service.BindCreativeGrokVideoContentError(work, func() { _ = pipe.CloseWithError(errors.New("creative video upstream content interrupted")) })
	stop := context.AfterFunc(work, func() {
		_ = reader.CloseWithError(work.Err())
		select {
		case writer.closed <- true:
		default:
		}
	})
	go func() {
		defer stop()
		defer func() {
			if recover() != nil {
				_ = pipe.CloseWithError(errors.New("creative video download failed"))
			}
			writer.WriteHeader(http.StatusInternalServerError)
			_ = pipe.CloseWithError(writer.err)
		}()
		a.router.ServeHTTP(writer, req)
		writer.WriteHeader(http.StatusOK)
	}()
	select {
	case response := <-writer.ready:
		response.Body = &creativeGrokVideoReadCloser{ReadCloser: reader, cancel: cancel}
		return response, nil
	case <-work.Done():
		cancel()
		_ = reader.CloseWithError(work.Err())
		return nil, work.Err()
	}
}

// 逐块流式交给私有素材存储，保留真实响应头，并在超限或调用方关闭时终止上游读取。
type creativeGrokVideoStreamWriter struct {
	header  http.Header
	pipe    *io.PipeWriter
	ready   chan *http.Response
	closed  chan bool
	once    sync.Once
	written int64
	err     error
}

func (w *creativeGrokVideoStreamWriter) Header() http.Header { return w.header }
func (w *creativeGrokVideoStreamWriter) WriteHeader(status int) {
	if status < 200 {
		return
	}
	w.once.Do(func() {
		length := int64(-1)
		if raw := w.header.Get("Content-Length"); raw != "" {
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 0 {
				length = n
			}
		}
		w.ready <- &http.Response{StatusCode: status, Header: w.header.Clone(), ContentLength: length}
	})
}
func (w *creativeGrokVideoStreamWriter) Write(data []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if int64(len(data)) > service.CreativeCanvasOutputLimit-w.written {
		w.err = errors.New("creative video content exceeded size limit")
		return 0, w.err
	}
	n, err := w.pipe.Write(data)
	w.written += int64(n)
	if err != nil {
		w.err = err
	}
	return n, err
}
func (w *creativeGrokVideoStreamWriter) Flush()                   { w.WriteHeader(http.StatusOK) }
func (w *creativeGrokVideoStreamWriter) CloseNotify() <-chan bool { return w.closed }
func (w *creativeGrokVideoStreamWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("creative video gateway does not support connection upgrades")
}
func (w *creativeGrokVideoStreamWriter) Push(string, *http.PushOptions) error {
	return http.ErrNotSupported
}

type creativeGrokVideoReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *creativeGrokVideoReadCloser) Close() error { r.cancel(); return r.ReadCloser.Close() }

var _ service.CreativeGrokVideoGateway = (*creativeGrokVideoGatewayAdapter)(nil)
