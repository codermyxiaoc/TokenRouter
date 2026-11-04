package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// MJ 兼容上游的异步语义（实测 token.xinhankr.com）：同步等待约 280 秒，超时后对生成请求返回
// HTTP 200 + {"id":"...","status":"pending"}（无 data），上游在后台继续生成并计费；
// 之后 GET {images/generations}/{id} 查询，完成时返回与同步相同的 data。
// 以 b64_json 请求的任务转为异步后，完成查询不再返回图片，因此 MJ 请求一律改用 url。
// midjourneyAsyncPollInterval 是查询异步任务的间隔（测试可缩短）。
var midjourneyAsyncPollInterval = 5 * time.Second

const (
	// midjourneyAsyncPollRequestTimeout 是单次查询的超时时间。
	midjourneyAsyncPollRequestTimeout = 30 * time.Second
	// midjourneyAsyncMaxResponseBytes 限制查询响应大小；url 结果只有很小的 JSON。
	midjourneyAsyncMaxResponseBytes = 1 << 20
	// MidjourneyImageExecutionBudget 是 MJ 生成的总等待预算：上游同步段约 5 分钟，异步完成再需数分钟。
	MidjourneyImageExecutionBudget = 15 * time.Minute
)

// 异步查询的结论：成功拿到 data、上游明确失败、或结果无法确认（任务完成却无数据、查询被拒、超时）。
var (
	errMidjourneyTaskFailed      = errors.New("Midjourney task failed upstream")
	errMidjourneyTaskUnconfirmed = errors.New("Midjourney task result is unconfirmed")
)

// rewriteMidjourneyImagesBodyToURL 只删除 response_format=b64_json，让上游返回图片 url；
// 已是 url 或未指定时保持原请求不变。只用于 MJ：b64_json 任务一旦转为异步就永远取不回图片。
func rewriteMidjourneyImagesBodyToURL(body []byte) ([]byte, error) {
	if !strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "response_format").String()), "b64_json") {
		return body, nil
	}
	return sjson.DeleteBytes(body, "response_format")
}

// midjourneyAsyncTaskID 在响应是异步任务（有 id、没有 data、未完成）时返回任务 ID。
func midjourneyAsyncTaskID(body []byte) (string, bool) {
	if !gjson.ValidBytes(body) || gjson.GetBytes(body, "data").IsArray() {
		return "", false
	}
	id := strings.TrimSpace(gjson.GetBytes(body, "id").String())
	if id == "" || !gjson.GetBytes(body, "status").Exists() {
		return "", false
	}
	return id, true
}

// midjourneyTaskURL 由生成端点地址推导任务查询地址：{生成地址}/{任务 ID}。
func midjourneyTaskURL(generationsURL, taskID string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(generationsURL), "/"))
	if err != nil || !parsed.IsAbs() || !strings.HasSuffix(parsed.Path, "/images/generations") {
		return "", errors.New("invalid Midjourney generations url")
	}
	return parsed.String() + "/" + url.PathEscape(taskID), nil
}

// midjourneyTaskRequester 构造一次携带账号凭据的任务查询请求，由调用方负责鉴权头与代理。
type midjourneyTaskRequester func(ctx context.Context, taskURL string) (*http.Response, error)

// pollMidjourneyTask 轮询异步任务直到拿到图片数据、上游明确失败或超过 deadline；绝不重新提交生成。
// 返回值是与同步响应相同结构、带 data 的响应体。
func pollMidjourneyTask(ctx context.Context, taskURL string, deadline time.Time, request midjourneyTaskRequester) ([]byte, error) {
	if request == nil {
		return nil, errMidjourneyTaskUnconfirmed
	}
	for {
		if !time.Now().Before(deadline) {
			return nil, errMidjourneyTaskUnconfirmed
		}
		body, status, err := fetchMidjourneyTask(ctx, taskURL, request)
		if err == nil {
			switch {
			case status >= http.StatusBadRequest && status < http.StatusInternalServerError && status != http.StatusTooManyRequests && status != http.StatusRequestTimeout:
				// 任务不存在或无权查询：无法确认结果，由各入口沿用自己的资金处理规则。
				return nil, errMidjourneyTaskUnconfirmed
			case status == http.StatusOK && gjson.ValidBytes(body):
				if data := gjson.GetBytes(body, "data"); data.IsArray() && len(data.Array()) > 0 {
					return body, nil
				}
				switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "status").String())) {
				case "failed", "failure", "error", "cancelled", "canceled":
					return nil, errMidjourneyTaskFailed
				case "completed", "succeeded", "success":
					// 完成但没有图片数据（b64 任务异步后的上游行为）：图片无法取回，不能当作成功。
					return nil, errMidjourneyTaskUnconfirmed
				}
			}
		}
		wait := midjourneyAsyncPollInterval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errMidjourneyTaskUnconfirmed
		case <-timer.C:
		}
	}
}

// fetchMidjourneyTask 执行一次查询；网络错误与 5xx 由调用方继续重试。
func fetchMidjourneyTask(ctx context.Context, taskURL string, request midjourneyTaskRequester) ([]byte, int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, midjourneyAsyncPollRequestTimeout)
	defer cancel()
	resp, err := request(reqCtx, taskURL)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, midjourneyAsyncMaxResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(body) > midjourneyAsyncMaxResponseBytes {
		return nil, resp.StatusCode, errors.New("Midjourney task response is too large")
	}
	return bytes.TrimSpace(body), resp.StatusCode, nil
}

// midjourneyPollDeadline 取上下文截止时间，没有时用 MJ 总预算。
func midjourneyPollDeadline(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Now().Add(MidjourneyImageExecutionBudget)
}

// resolveMidjourneyAsyncResponse 在 API 路径收到 MJ 异步任务时轮询到完成，并用最终结果替换响应体，
// 后续沿用原非流式响应处理（计数、计费与 b64 回填）。上游明确失败按拒绝处理以便释放预占；
// 无法确认结果时保留预占交人工核对，绝不重新提交生成。
// @project-doc docs/domains/creative_studio.md#midjourney_images
func (s *OpenAIGatewayService) resolveMidjourneyAsyncResponse(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, upstreamReq *http.Request, token string, tlsRouterMatch ...TLSFingerprintRouterMatchResult) error {
	// 只窥探有界前缀，保留整个原始流及关闭行为：上游仍可能同步返回较大的 b64 图片。
	original := resp.Body
	buffered := bufio.NewReaderSize(original, midjourneyAsyncMaxResponseBytes+1)
	body, err := buffered.Peek(midjourneyAsyncMaxResponseBytes + 1)
	resp.Body = struct {
		io.Reader
		io.Closer
	}{buffered, original}
	if err != nil && !errors.Is(err, io.EOF) {
		// 读取中断不能被重建后的内存流隐藏，否则残缺响应可能被误计为成功。
		return newOpenAIUpstreamStreamReadError(err)
	}
	if err == nil {
		// 大于异步信封上限的同步响应仍由原响应大小限制负责处理，不截断图片。
		return nil
	}
	taskID, async := midjourneyAsyncTaskID(body)
	if !async {
		return nil
	}
	_ = original.Close()
	taskURL, err := midjourneyTaskURL(upstreamReq.URL.String(), taskID)
	if err != nil {
		return fmt.Errorf("Midjourney generation result is unknown; request will not be resubmitted")
	}
	logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Midjourney task became asynchronous account_id=%d; polling task status", account.ID)
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	polled, err := pollMidjourneyTask(ctx, taskURL, midjourneyPollDeadline(ctx), func(reqCtx context.Context, target string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(WithHTTPUpstreamProfile(reqCtx, HTTPUpstreamProfileOpenAI), http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		headers, err := s.buildOpenAIAuthenticationHeaders(reqCtx, account, token)
		if err != nil {
			return nil, err
		}
		for key, values := range headers {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		account.ApplyHeaderOverrides(req.Header)
		return s.doOpenAIUpstream(req, proxyURL, account, s.resolveOpenAITLSProfile(account, tlsRouterMatch...))
	})
	if errors.Is(err, errMidjourneyTaskFailed) {
		setOpsUpstreamError(c, http.StatusBadGateway, "Midjourney task failed upstream", "")
		return WrapImageAttemptError(errors.New("Midjourney task failed upstream"), ImageAttemptDecision{Outcome: ImageAttemptRejected, Reason: "midjourney_task_failed"})
	}
	if err != nil {
		setOpsUpstreamError(c, http.StatusBadGateway, "Midjourney task result is unconfirmed", "")
		return fmt.Errorf("Midjourney generation result is unknown after asynchronous polling; request will not be resubmitted")
	}
	resp.Body = io.NopCloser(bytes.NewReader(polled))
	resp.ContentLength = int64(len(polled))
	resp.Header.Del("Content-Length")
	return nil
}
