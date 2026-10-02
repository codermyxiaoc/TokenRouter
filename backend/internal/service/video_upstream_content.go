package service

import (
	"context"
	"net/http"
	"strings"
)

// OpenContent 只访问冻结的 OpenAI 视频内容路径，原账号鉴权请求禁止自动跟随重定向。
func (s *VideoUpstreamService) OpenContent(ctx context.Context, account *Account, target VideoUpstreamTarget, taskID, byteRange string) (*http.Response, error) {
	if s == nil || s.gateway == nil || s.gateway.httpUpstream == nil || account == nil || account.ID != target.AccountID ||
		account.Platform != PlatformVideo || account.Type != AccountTypeAPIKey || target.Version != 1 || target.Endpoint != VideoEndpointOpenAIVideos {
		return nil, errVideoContentUnavailable
	}
	if byteRange != "" && (len(byteRange) > 100 || !mediaTaskPreviewRange.MatchString(byteRange)) {
		return nil, errVideoContentUnavailable
	}
	base, err := s.gateway.validateUpstreamBaseURL(target.BaseURL)
	if err != nil {
		return nil, errVideoContentUnavailable
	}
	path, err := videoTaskPath(target, taskID)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(account.GetCredential("api_key"))
	if token == "" {
		return nil, errVideoContentUnavailable
	}
	request, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(ctx), http.MethodGet, videoEndpointURL(base, path+"/content"), nil)
	if err != nil {
		return nil, errVideoContentUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+token)
	account.ApplyHeaderOverrides(request.Header)
	request.Header.Set("Accept", "video/*, application/octet-stream")
	request.Header.Del("Range")
	if byteRange != "" {
		request.Header.Set("Range", byteRange)
	}
	release := func() {}
	if s.gateway.concurrencyService != nil {
		slot, slotErr := s.gateway.concurrencyService.AcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if slotErr != nil || !slot.Acquired {
			return nil, errVideoContentUnavailable
		}
		release = slot.ReleaseFunc
	}
	response, err := s.gateway.httpUpstream.DoWithTLS(request, accountProxyURL(account), account.ID, account.Concurrency, s.gateway.resolveOpenAITLSProfile(account))
	if err != nil || response == nil || response.Body == nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		release()
		return nil, errVideoContentUnavailable
	}
	// 内容读取期间保留账号槽，交调用方关闭流后归还。
	response.Body = &mediaTaskPreviewBody{Reader: response.Body, closer: response.Body, release: release}
	return response, nil
}
