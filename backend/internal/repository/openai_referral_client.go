package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/imroc/req/v3"
)

// 协议参考桌面端 26.908.40834 的 app-primary-44ec287874b7.js：SIt、EIt、PIt、FIt。
// 邀请接口使用 /backend-api/referrals 路径。
const openAIReferralURL = "https://chatgpt.com/backend-api/referrals/invite"
const openAIReferralEntrypoint = "persistent"

type openAIReferralClient struct {
	clientFactory service.PrivacyClientFactory
	baseURL       string
}

func NewOpenAIReferralClient(factory service.PrivacyClientFactory) service.OpenAIReferralClient {
	return &openAIReferralClient{clientFactory: factory, baseURL: openAIReferralURL}
}

func (c *openAIReferralClient) request(ctx context.Context, call service.OpenAIReferralCall) (*req.Request, error) {
	client, err := c.clientFactory(call.ProxyURL)
	if err != nil {
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_REFERRAL_CLIENT_ERROR", "failed to build referral client")
	}
	// 邀请发送没有已知幂等键，必须覆盖客户端工厂的自动重试配置。
	return client.R().SetContext(ctx).SetHeaders(call.Headers).SetRetryCount(0), nil
}

func (c *openAIReferralClient) QueryEligibility(ctx context.Context, call service.OpenAIReferralCall) (*service.OpenAIReferralEligibility, error) {
	body, status, err := c.do(ctx, call, http.MethodGet, c.baseURL+"/eligibility", map[string]string{"program_id": call.ProgramID, "entrypoint": openAIReferralEntrypoint}, nil)
	if err != nil {
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_REFERRAL_QUERY_FAILED", "failed to query invitation eligibility")
	}
	if status < 200 || status >= 300 {
		return nil, referralHTTPError(status)
	}
	var result *service.OpenAIReferralEligibility
	if err := json.Unmarshal(body, &result); err != nil || result == nil {
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_REFERRAL_INVALID_RESPONSE", "invalid invitation eligibility response")
	}
	return result, nil
}

func (c *openAIReferralClient) SendInvite(ctx context.Context, call service.OpenAIReferralCall, email string) error {
	body, status, err := c.do(ctx, call, http.MethodPost, c.baseURL, nil, map[string]any{"program_id": call.ProgramID, "entrypoint": openAIReferralEntrypoint, "emails": []string{email}})
	if err != nil {
		// 尚未建立客户端时确定没有发送，无需标记为发送结果未知。
		if infraerrors.Reason(err) == "OPENAI_REFERRAL_CLIENT_ERROR" {
			return err
		}
		return referralSendUnknown()
	}
	if status < 200 || status >= 300 {
		return referralHTTPError(status)
	}
	var payload struct {
		Invites []json.RawMessage `json:"invites"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Invites) != 1 || string(payload.Invites[0]) == "null" {
		return referralSendUnknown()
	}
	return nil
}

func referralSendUnknown() error {
	return infraerrors.New(http.StatusBadGateway, "OPENAI_REFERRAL_SEND_UNKNOWN", "invitation outcome is unknown; check the invitation status in Codex before sending again")
}

// 上游响应体可能包含个人资料或凭据，仅返回用于区分验证失败、重复邀请
// 和名额限制的稳定错误码。
func referralHTTPError(status int) error {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return infraerrors.New(http.StatusBadRequest, "OPENAI_REFERRAL_REJECTED", "the invitation request was rejected; check the email and eligibility")
	case http.StatusUnauthorized:
		return infraerrors.New(http.StatusBadGateway, "OPENAI_REFERRAL_AUTH_ERROR", "upstream authentication failed; refresh the account credentials")
	case http.StatusForbidden:
		return infraerrors.New(http.StatusForbidden, "OPENAI_REFERRAL_FORBIDDEN", "this account is not eligible for invitations")
	case http.StatusConflict:
		return infraerrors.New(http.StatusConflict, "OPENAI_REFERRAL_ALREADY_EXISTS", "an invitation already exists for this recipient")
	case http.StatusTooManyRequests:
		return infraerrors.New(http.StatusTooManyRequests, "OPENAI_REFERRAL_RATE_LIMITED", "invitation limit reached; try again later")
	default:
		return infraerrors.New(http.StatusBadGateway, "OPENAI_REFERRAL_UPSTREAM_ERROR", "invitation service is unavailable")
	}
}

// do 优先使用当前账号的统一传输层，保留代理和 TLS 指纹；发送不做应用层重试。
func (c *openAIReferralClient) do(ctx context.Context, call service.OpenAIReferralCall, method, target string, query map[string]string, payload map[string]any) ([]byte, int, error) {
	if call.Do == nil {
		r, err := c.request(ctx, call)
		if err != nil {
			return nil, 0, err
		}
		r.SetQueryParams(query)
		if payload != nil {
			r.SetBody(payload)
		}
		resp, err := r.Send(method, target)
		if err != nil {
			return nil, 0, err
		}
		return resp.Bytes(), resp.StatusCode, nil
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil, 0, err
	}
	values := u.Query()
	for k, v := range query {
		values.Set(k, v)
	}
	u.RawQuery = values.Encode()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, 0, err
	}
	// 避免标准传输层基于可回放请求体重放没有幂等凭据的邀请请求。
	request.GetBody = nil
	for key, value := range call.Headers {
		request.Header.Set(key, value)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	resp, err := call.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(data) > 512*1024 {
		return nil, resp.StatusCode, io.ErrUnexpectedEOF
	}
	return data, resp.StatusCode, nil
}
