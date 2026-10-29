package service

import (
	"context"
	"net/http"
	"strings"
)

// videoContentPath 返回需要原账号凭据读取的冻结内容路径：OpenAI Videos 固定 /content，模板协议按快照声明；
// 其余协议只能使用响应中的公开直链。
func videoContentPath(target VideoUpstreamTarget, taskID string) (string, bool, error) {
	if target.Version == videoTargetVersionProfile {
		path, err := videoProfileContentPath(target, taskID)
		return path, err == nil && path != "", err
	}
	if target.Endpoint != VideoEndpointOpenAIVideos {
		return "", false, nil
	}
	path, err := VideoProtocolTaskPath(target, taskID)
	if err != nil {
		return "", false, err
	}
	return path + "/content", true, nil
}

// OpenContent 只访问冻结的内容路径，原账号鉴权请求禁止自动跟随重定向。
func (s *VideoUpstreamService) OpenContent(ctx context.Context, account *Account, target VideoUpstreamTarget, taskID, byteRange string) (*http.Response, error) {
	if s == nil || s.gateway == nil || s.gateway.httpUpstream == nil || account == nil || account.ID != target.AccountID ||
		account.Platform != PlatformVideo || account.Type != AccountTypeAPIKey || ValidateVideoProtocolTarget(target) != nil {
		return nil, errVideoContentUnavailable
	}
	path, ok, err := videoContentPath(target, taskID)
	if err != nil || !ok {
		return nil, errVideoContentUnavailable
	}
	if byteRange != "" && (len(byteRange) > 100 || !mediaTaskPreviewRange.MatchString(byteRange)) {
		return nil, errVideoContentUnavailable
	}
	base, err := s.gateway.validateUpstreamBaseURL(target.BaseURL)
	if err != nil {
		return nil, errVideoContentUnavailable
	}
	token := strings.TrimSpace(account.GetCredential("api_key"))
	if token == "" {
		return nil, errVideoContentUnavailable
	}
	request, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(ctx), http.MethodGet, videoEndpointURL(base, path), nil)
	if err != nil {
		return nil, errVideoContentUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if target.Version == videoTargetVersionProfile {
		spec, specErr := videoProfileSpecFromTarget(target)
		if specErr != nil {
			return nil, errVideoContentUnavailable
		}
		videoProfileAuthHeader(spec, request.Header, token)
	}
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
