package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 默认配置只识别计费维度，未知扩展及上游请求字节保持原样。
func TestVideoOpenAIParametersPassthrough(t *testing.T) {
	for _, profile := range []string{"", VideoOpenAIProfilePassthrough} {
		for _, seconds := range []string{`8`, `"8"`} {
			body := []byte(`{ "model":"m", "seconds":` + seconds + `,"size":"1280X720","input_reference":{"image_url":"data:image/jpeg;base64,test"},"extra":{"n":9007199254740993} }`)
			prepared, metadata, err := PrepareVideoOpenAIRequest(body, "", profile)
			require.NoError(t, err)
			require.Equal(t, body, prepared)
			require.Equal(t, 8., metadata.DurationSeconds)
			require.Equal(t, "1280x720", metadata.Resolution)
			require.Equal(t, "1280x720", metadata.Size)
			require.Equal(t, 1, *metadata.ReferenceImageCount)
			require.False(t, metadata.HasReferenceVideo)
		}
	}
	_, metadata, err := PrepareVideoOpenAIRequest([]byte(`{"model":"m","duration":8,"seconds":"8","resolution":"720P","size":"720p"}`), "", "")
	require.NoError(t, err)
	require.Equal(t, 8., metadata.DurationSeconds)
	require.Equal(t, "720p", metadata.Resolution)
	// 未启用适配的老自动时长请求继续由账号的预算上限处理。
	_, metadata, err = PrepareVideoOpenAIRequest([]byte(`{"model":"m","duration":-1,"resolution":"720p"}`), "", "")
	require.NoError(t, err)
	require.Equal(t, -1., metadata.DurationSeconds)
}

// 秒数与尺寸冲突、重复键、大小写歧义必须在预扣和上游调用前拒绝。
func TestVideoOpenAIParametersRejectAmbiguousAndInvalid(t *testing.T) {
	for _, fragment := range []string{
		`"seconds":0`, `"seconds":-1`, `"seconds":4.5`, `"seconds":null`, `"seconds":true`, `"seconds":[]`, `"seconds":{}`,
		`"seconds":""`, `"seconds":" 8"`, `"seconds":"08"`, `"seconds":"+8"`, `"seconds":"1e2"`, `"seconds":"NaN"`,
		`"seconds":2147483648`, `"seconds":"2147483648"`, `"seconds":1e100`,
		`"seconds":8,"duration":5`, `"seconds":"8","parameters":{"duration":5}`,
		`"seconds":8,"seconds":4`, `"Seconds":8`, `"size":"1280x720","Size":"720x1280"`,
		`"size":"1280x720","resolution":"720p"`, `"size":"01280x720"`, `"size":"1280x0"`, `"size":"1280x65537"`,
		`"size":720`, `"size":null`, `"resolution":"720p","parameters":{"size":"1080p"}`,
		`"Input_Reference":{"image_url":"a"}`,
	} {
		t.Run(fragment, func(t *testing.T) {
			_, _, err := PrepareVideoOpenAIRequest([]byte(`{"model":"m",`+fragment+`}`), "", "")
			require.Error(t, err)
		})
	}
}

// 固定附加费不能把未知图片结构或不同表示混用当作无图片免费处理。
func TestVideoOpenAIReferencePricing(t *testing.T) {
	price, imagePrice := .3, .15
	group := &Group{ID: 12, Platform: PlatformVideo}
	resolver := videoPricingTestResolver(group, &ChannelModelPricing{Platform: PlatformVideo, BillingMode: BillingModeVideo, VideoFallbackPrice: &price,
		VideoImageInputPricing: &VideoImageInputPricing{FreeImages: 0, Price: &imagePrice}})
	for _, reference := range []string{`{"image_url":"https://image.example/a.jpg"}`, `{"image_url":"data:image/jpeg;base64,test"}`} {
		_, metadata, err := PrepareVideoOpenAIRequest([]byte(`{"model":"video-model","seconds":8,"size":"1280x720","input_reference":`+reference+`}`), "", "")
		require.NoError(t, err)
		quote, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: metadata.Model, Resolution: metadata.Resolution, ReferenceImageCount: metadata.ReferenceImageCount, RateMultiplier: 2})
		require.NoError(t, err)
		cost, err := quote.Calculate(metadata.DurationSeconds, nil)
		require.NoError(t, err)
		require.InDelta(t, 4.95, cost.ActualCost, 1e-12)
		require.InDelta(t, .15, cost.ImageInputCost, 1e-12)
	}
	for _, fragment := range []string{
		`"input_reference":null`, `"input_reference":[]`, `"input_reference":"a"`, `"input_reference":{}`, `"input_reference":{"image_url":""}`,
		`"input_reference":{"Image_URL":"a"}`, `"input_reference":{"image_url":["a","b"]}`,
		`"input_reference":{"image_url":"a"},"images":["a"]`, `"input_reference":{"image_url":"a"},"input":{"images":["b"]}`,
	} {
		_, metadata, err := PrepareVideoOpenAIRequest([]byte(`{"model":"video-model","seconds":8,"size":"1280x720",`+fragment+`}`), "", "")
		require.NoError(t, err)
		require.Nil(t, metadata.ReferenceImageCount, fragment)
		_, err = resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: metadata.Model, Resolution: metadata.Resolution, ReferenceImageCount: metadata.ReferenceImageCount, RateMultiplier: 1})
		require.ErrorIs(t, err, ErrVideoUsageUnavailable)
	}
}

// 显式 H3 配置对齐真实参数；同价档横竖屏保留明确的不同像素尺寸。
func TestVideoOpenAIMetasoH3Conversion(t *testing.T) {
	for _, tc := range []struct{ resolution, ratio, size string }{
		{"768p", "16:9", "1280x720"}, {"768P", "9:16", "720x1280"}, {"2k", "16:9", "1792x1024"}, {"2K", "9:16", "1024x1792"},
	} {
		body := []byte(fmt.Sprintf(`{"model":"m","duration":5,"resolution":%q,"ratio":%q,"images":["data:image/jpeg;base64,test"],"prompt":"p","extra":{"n":9007199254740993}}`, tc.resolution, tc.ratio))
		prepared, metadata, err := PrepareVideoOpenAIRequest(body, "", VideoOpenAIProfileMetasoH3)
		require.NoError(t, err)
		require.Equal(t, 5., metadata.DurationSeconds)
		require.Equal(t, tc.size, metadata.Size)
		tier, _ := metasoH3SizeTier(tc.size)
		require.Equal(t, tier, metadata.Resolution)
		require.Equal(t, 1, *metadata.ReferenceImageCount)
		require.Equal(t, int64(5), gjson.GetBytes(prepared, "seconds").Int())
		require.Equal(t, tc.size, gjson.GetBytes(prepared, "size").String())
		require.Equal(t, "data:image/jpeg;base64,test", gjson.GetBytes(prepared, "input_reference.image_url").String())
		require.Equal(t, "9007199254740993", gjson.GetBytes(prepared, "extra.n").Raw)
		for _, field := range []string{"duration", "resolution", "ratio", "images", "image_urls"} {
			require.False(t, gjson.GetBytes(prepared, field).Exists())
		}
		// 已准备好的规范请求再经过同一适配仍保持同样合同和图片数。
		again, againMetadata, err := PrepareVideoOpenAIRequest(prepared, "", VideoOpenAIProfileMetasoH3)
		require.NoError(t, err)
		require.JSONEq(t, string(prepared), string(again))
		require.Equal(t, metadata, againMetadata)
	}
	prepared, metadata, err := PrepareVideoOpenAIRequest([]byte(`{"model":"m","seconds":"8","duration":8,"size":"1280x720","resolution":"768p","ratio":"16:9","images":["url"],"image_urls":["url"],"input_reference":{"image_url":"url"}}`), "", VideoOpenAIProfileMetasoH3)
	require.NoError(t, err)
	require.Equal(t, 1, *metadata.ReferenceImageCount)
	require.Equal(t, "url", gjson.GetBytes(prepared, "input_reference.image_url").String())
}

// 不支持的媒体和有歧义的参数不能进入真实创建请求。
func TestVideoOpenAIMetasoH3RejectsUnsafeConversions(t *testing.T) {
	for _, fragment := range []string{
		`"duration":-1`, `"duration":5.5`, `"duration":5,"seconds":8`, `"duration":"5"`,
		`"resolution":"768p"`, `"ratio":"16:9"`, `"resolution":"480p","ratio":"16:9"`,
		`"resolution":"768p","ratio":"1:1"`, `"size":"1280x720","ratio":"9:16"`,
		`"size":"1280x720","resolution":"2k"`, `"size":"1920x1080"`,
		`"images":["a","b"]`, `"image_urls":["a","b"]`, `"images":[null]`, `"images":[{}]`, `"images":null`,
		`"images":["a"],"input_reference":{"image_url":"b"}`, `"images":[],"input_reference":{"image_url":"b"}`,
		`"images":["a"],"image_urls":["b"]`, `"input_reference":{"image_url":""}`, `"input_reference":[]`,
		`"Images":["a"]`, `"input_reference":{"Image_URL":"a"}`, `"input":{"images":["a"]}`,
		`"content":[]`, `"parameters":{"duration":5}`, `"video_url":"v"`, `"audios":["a"]`,
	} {
		t.Run(fragment, func(t *testing.T) {
			_, _, err := PrepareVideoOpenAIRequest([]byte(`{"model":"m",`+fragment+`}`), "", VideoOpenAIProfileMetasoH3)
			require.Error(t, err)
		})
	}
}

// 精确尺寸保持方向和档位，不自动向已有数字分辨率降价；合法未知尺寸只走显式兜底。
func TestVideoOpenAISizePricingAndBounds(t *testing.T) {
	for _, raw := range []string{"1280x720", "720X1280", " 65536x1 "} {
		_, err := NormalizeVideoPriceResolution(raw)
		require.NoError(t, err)
	}
	for _, raw := range []string{"0x720", "01280x720", "1280x0720", "65537x1", "1x65537", "1280xx720", "1280×720", "1280 x720", "-1x720", "+1x720"} {
		_, err := NormalizeVideoPriceResolution(raw)
		require.Error(t, err)
	}
	tierPrice, fallback := .2, .7
	group := &Group{ID: 12, Platform: PlatformVideo}
	pricing := &ChannelModelPricing{Platform: PlatformVideo, BillingMode: BillingModeVideo, VideoPrices: []VideoPriceTier{{Resolution: "1280x720", Price: &tierPrice}}, VideoFallbackPrice: &fallback}
	resolver := videoPricingTestResolver(group, pricing)
	for _, tc := range []struct {
		size     string
		price    float64
		fallback bool
	}{{"1280X720", .2, false}, {"720x1280", .7, true}, {"1920x1080", .7, true}, {"720p", .7, true}} {
		quote, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: "video-model", Resolution: tc.size, RateMultiplier: 1})
		require.NoError(t, err)
		require.Equal(t, tc.price, quote.UnitPrice)
		require.Equal(t, tc.fallback, quote.FallbackUsed)
	}
	pricing.VideoFallbackPrice = nil
	_, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: "video-model", Resolution: "1920x1080", RateMultiplier: 1})
	require.ErrorIs(t, err, ErrVideoPricingUnavailable)
}

// 只有被冻结的显式配置映射价档；账号后来关闭适配不能改变已受理任务解释。
func TestVideoOpenAIProfileConfigurationAndFrozenResponse(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	account.Credentials["video_openai_profile"] = VideoOpenAIProfileMetasoH3
	require.NoError(t, normalizeVideoCredentials(account))
	svc := NewVideoUpstreamService(&OpenAIGatewayService{})
	target, err := svc.resolveTarget(account, "m", VideoTaskSubmitRequest{InboundProtocol: "openai_videos"})
	require.NoError(t, err)
	require.Equal(t, VideoOpenAIProfileMetasoH3, target.OpenAIProfile)
	raw, err := json.Marshal(target)
	require.NoError(t, err)
	var restored VideoUpstreamTarget
	require.NoError(t, json.Unmarshal(raw, &restored))
	account.Credentials["video_openai_profile"] = VideoOpenAIProfilePassthrough
	fixture := &videoHTTPFixture{status: http.StatusOK, response: `{"id":"owned","status":"completed","seconds":"4","size":"1280x720"}`}
	svc.gateway.httpUpstream = fixture
	response, err := svc.Poll(context.Background(), account, restored, "owned")
	require.NoError(t, err)
	require.Empty(t, response.MetadataError)
	require.Equal(t, 4., response.Metadata.DurationSeconds)
	require.Equal(t, "768p", response.Metadata.Resolution)
	require.Equal(t, "1280x720", response.Metadata.Size)
	for _, value := range []any{"auto", "METASO_H3", " metaso_h3", 1, true} {
		account.Credentials["video_openai_profile"] = value
		require.Error(t, normalizeVideoCredentials(account))
	}
	account.Credentials["video_openai_profile"] = VideoOpenAIProfileMetasoH3
	account.Credentials["video_endpoints"] = []string{"compat"}
	require.NoError(t, normalizeVideoCredentials(account))
	legacy, err := svc.resolveTarget(account, "m", VideoTaskSubmitRequest{InboundProtocol: "unified"})
	require.NoError(t, err)
	require.Empty(t, legacy.OpenAIProfile)
}

// 不可信终态仍保留归属与状态，由生命周期服务阻止回退旧请求量结算。
func TestVideoOpenAIResponseMetadataValidation(t *testing.T) {
	for _, fragment := range []string{
		`"seconds":"NaN"`, `"seconds":"0"`, `"seconds":0`, `"seconds":-1`, `"seconds":4.5`, `"seconds":2147483648`,
		`"seconds":"4","duration":5`, `"seconds":"4","Seconds":"8"`, `"seconds":"4","seconds":"8"`,
		`"size":"1280x720","resolution":"720p"`, `"size":"1920x1080"`, `"Size":"1280x720"`, `"size":null`,
		`"task_result":{"videos":[{"duration":4,"duration":8}]}`, `"task_result":{"videos":[{"duration":4}]},"task_result":{"videos":[{"duration":8}]}`,
	} {
		response := ExtractVideoUpstreamResponseWithProfile(VideoEndpointOpenAIVideos, []byte(`{"id":"owned","status":"completed",`+fragment+`}`), "owned", VideoOpenAIProfileMetasoH3)
		require.Equal(t, "owned", response.TaskID)
		require.Equal(t, "completed", response.Status)
		require.NotEmpty(t, response.MetadataError, fragment)
	}
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointSeedance, VideoEndpointWan, VideoEndpointKling, VideoEndpointMiniMax} {
		response := ExtractVideoUpstreamResponseWithProfile(endpoint, []byte(`{"id":"owned","status":"completed","duration":5,"seconds":"4","resolution":"720p","size":"1280x720"}`), "owned", VideoOpenAIProfileMetasoH3)
		require.Empty(t, response.MetadataError)
		require.Equal(t, 5., response.Metadata.DurationSeconds)
		require.Equal(t, "720p", response.Metadata.Resolution)
		metadata, err := ParseVideoEndpointRequestMetadata([]byte(`{"model":"m","duration":5,"seconds":"4","resolution":"720p","input_reference":{"image_url":"a"}}`), "", endpoint)
		require.NoError(t, err)
		require.Equal(t, 5., metadata.DurationSeconds)
		require.Zero(t, *metadata.ReferenceImageCount)
	}
	standard := ExtractVideoUpstreamResponse(VideoEndpointOpenAIVideos, []byte(`{"id":"owned","status":"completed","seconds":"4","size":"1280x720"}`), "owned")
	require.Empty(t, standard.MetadataError)
	require.Equal(t, "1280x720", standard.Metadata.Resolution)
	projection := videoProtocolResponse(&VideoTaskRecord{ID: "vid_owned", Status: "completed", Metadata: standard.Metadata}, "openai_videos", false)
	require.Equal(t, "4", gjson.GetBytes(projection.Body, "seconds").String())
	require.Equal(t, 4., gjson.GetBytes(projection.Body, "duration").Float())
	require.Equal(t, "1280x720", gjson.GetBytes(projection.Body, "size").String())
}

// 已收到响应头后读正文超时必须标为响应阶段故障，不能误进入纯传输超时自动退款。
func TestVideoOpenAIResponseReadTimeoutClassification(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	svc := NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: &videoOpenAIReadFailureHTTP{}})
	target, err := svc.resolveTarget(account, "m", VideoTaskSubmitRequest{InboundProtocol: "openai_videos"})
	require.NoError(t, err)
	response, err := svc.Submit(context.Background(), &VideoUpstreamSelection{Account: account, Target: target, Body: []byte(`{"model":"m"}`)})
	require.Nil(t, response)
	var responseErr *VideoSubmissionResponseError
	require.ErrorAs(t, err, &responseErr)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	// 替代 HTTP 客户端可能同时给出 response 和 error，这也属于已有响应证据。
	svc.gateway.httpUpstream = &videoOpenAIReadFailureHTTP{transportError: true}
	response, err = svc.Submit(context.Background(), &VideoUpstreamSelection{Account: account, Target: target, Body: []byte(`{"model":"m"}`)})
	require.Nil(t, response)
	require.ErrorAs(t, err, &responseErr)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// 内存替身只模拟响应体断流，不发送网络请求。
type videoOpenAIReadFailureHTTP struct{ transportError bool }

func (f *videoOpenAIReadFailureHTTP) Do(*http.Request, string, int64, int) (*http.Response, error) {
	response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(videoOpenAIReadFailureBody{})}
	if f.transportError {
		return response, context.DeadlineExceeded
	}
	return response, nil
}

func (f *videoOpenAIReadFailureHTTP) DoWithTLS(req *http.Request, proxy string, id int64, limit int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return f.Do(req, proxy, id, limit)
}

type videoOpenAIReadFailureBody struct{}

func (videoOpenAIReadFailureBody) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }
