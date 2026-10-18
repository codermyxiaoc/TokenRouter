package routes

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

const creativeTextResponseLimit = service.CreativeTextOutputLimit

// creativeTextGatewayAdapter 只在进程内进入原网关，不监听端口，也不绕过真实 Key 鉴权。
// @project-doc docs/domains/cloud_canvas.md#execution
type creativeTextGatewayAdapter struct{ router http.Handler }

func newCreativeTextGatewayAdapter(chain ...gin.HandlerFunc) *creativeTextGatewayAdapter {
	router := gin.New()
	router.POST("/v1/chat/completions", chain...)
	return &creativeTextGatewayAdapter{router: router}
}

func (a *creativeTextGatewayAdapter) Generate(ctx context.Context, key *service.APIKey, body []byte) (service.CreativeTextGatewayResponse, error) {
	if a == nil || a.router == nil || key == nil || key.ManagedBy == nil || *key.ManagedBy != service.CreativeManagedBy || key.Key == "" {
		return service.CreativeTextGatewayResponse{}, service.ErrCanvasUnavailable
	}
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	writer := &creativeTextResponseWriter{header: make(http.Header), cancel: cancel}
	writer.closed = make(chan bool, 1)
	stopNotify := context.AfterFunc(requestCtx, func() { writer.closed <- true })
	defer stopNotify()
	// 原网关在同步结算前从此读取已返回文本；只绑定可信内部 context，不接受请求字段开关。
	service.BindCreativeTextResponseSource(requestCtx, func() (int, []byte) { return writer.snapshot() })
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, "http://canvas-gateway.internal/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return service.CreativeTextGatewayResponse{}, err
	}
	req.RemoteAddr = "127.0.0.1:0"
	req.Header.Set("Authorization", "Bearer "+key.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TokenRouter-Canvas")
	a.router.ServeHTTP(writer, req)
	status, response := writer.snapshot()
	return service.CreativeTextGatewayResponse{StatusCode: status, Body: response}, writer.failure()
}

// 有界响应缓冲供非流式文本使用；超限即取消读取，不能让异常上游无限占用内存。
type creativeTextResponseWriter struct {
	header http.Header
	mu     sync.Mutex
	status int
	body   bytes.Buffer
	err    error
	cancel context.CancelFunc
	closed chan bool
}

func (w *creativeTextResponseWriter) Header() http.Header { return w.header }
func (w *creativeTextResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 && status >= 200 {
		w.status = status
	}
}
func (w *creativeTextResponseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.err != nil {
		return 0, w.err
	}
	if len(data) > creativeTextResponseLimit-w.body.Len() {
		w.err = errors.New("creative text response exceeded size limit")
		if w.cancel != nil {
			w.cancel()
		}
		return 0, w.err
	}
	return w.body.Write(data)
}
func (w *creativeTextResponseWriter) Flush()                   { w.WriteHeader(http.StatusOK) }
func (w *creativeTextResponseWriter) CloseNotify() <-chan bool { return w.closed }
func (w *creativeTextResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("creative text gateway does not support connection upgrades")
}
func (w *creativeTextResponseWriter) Push(string, *http.PushOptions) error {
	return http.ErrNotSupported
}
func (w *creativeTextResponseWriter) snapshot() (int, []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	return status, bytes.Clone(w.body.Bytes())
}
func (w *creativeTextResponseWriter) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

var _ service.CreativeTextGateway = (*creativeTextGatewayAdapter)(nil)
