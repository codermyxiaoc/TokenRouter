package service

import (
	"context"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 只有已经完成身份、素材归属及格式校验的内部入口可以签发参考图。
type videoReferencePublisher interface {
	PrepareDataURLs(context.Context, int64, []string) ([]string, error)
}

// 保留原始画布正文作为幂等摘要；仅在实际选中 Yezonghui 时替换传输副本。
// 同一次提交的选号、容量重选和安全换号共享一批链接，避免重复发布及刷新期限。
// @project-doc docs/interfaces/cloud_canvas.md#video_reference_delivery
func newCreativeVideoReferencePreparer(publisher videoReferencePublisher, userID int64) func(context.Context, VideoUpstreamTarget, []byte) ([]byte, error) {
	var mu sync.Mutex
	var published []string
	return func(ctx context.Context, target VideoUpstreamTarget, body []byte) ([]byte, error) {
		adapter, err := VideoTargetProtocolAdapter(target)
		if err != nil || adapter != VideoProtocolAdapterYezonghui {
			return body, err
		}
		images := gjson.GetBytes(body, "images")
		if !images.Exists() || images.IsArray() && len(images.Array()) == 0 {
			return body, nil
		}
		if !images.IsArray() {
			return nil, ErrVideoReferenceInvalid
		}
		inputs := make([]string, 0, len(images.Array()))
		preview := make([]string, 0, len(images.Array()))
		for _, image := range images.Array() {
			if image.Type != gjson.String {
				return nil, ErrVideoReferenceInvalid
			}
			inputs = append(inputs, image.String())
			preview = append(preview, "https://reference.example/image.png")
		}
		// 先验证全部生成参数，避免无效参数消耗参考图发布额度。
		validationBody, err := sjson.SetBytes(body, "images", preview)
		if err != nil {
			return nil, err
		}
		if _, _, err = PrepareVideoProtocolRequest(validationBody, "", target); err != nil {
			return nil, err
		}
		if publisher == nil {
			return nil, ErrVideoReferenceUnavailable
		}
		mu.Lock()
		defer mu.Unlock()
		if published == nil {
			published, err = publisher.PrepareDataURLs(ctx, userID, inputs)
			if err != nil {
				return nil, err
			}
		}
		if len(published) != len(inputs) {
			return nil, ErrVideoReferenceUnavailable
		}
		return sjson.SetBytes(body, "images", published)
	}
}

// 管理员试跑复用正式协议校验，参考图先发布再提交；不创建站内计费任务。
func (s *AccountTestService) prepareVideoAccountTestPayload(ctx context.Context, target VideoUpstreamTarget, prompt, imageURL string, duration int, options AccountTestOptions) ([]byte, error) {
	adapter, err := VideoTargetProtocolAdapter(target)
	if err != nil {
		return nil, err
	}
	if adapter != VideoProtocolAdapterYezonghui || imageURL == "" {
		return buildVideoAccountTestPayload(target, prompt, imageURL, duration, options)
	}
	if _, err = buildVideoAccountTestPayload(target, prompt, "https://reference.example/image.png", duration, options); err != nil {
		return nil, err
	}
	if s.videoReferences == nil {
		return nil, ErrVideoReferenceUnavailable
	}
	urls, err := s.videoReferences.PrepareDataURLs(ctx, options.AuthenticatedUserID, []string{imageURL})
	if err != nil {
		return nil, err
	}
	if len(urls) != 1 {
		return nil, ErrVideoReferenceUnavailable
	}
	return buildVideoAccountTestPayload(target, prompt, urls[0], duration, options)
}
