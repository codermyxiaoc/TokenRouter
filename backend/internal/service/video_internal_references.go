package service

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 只有已经完成身份、素材归属及格式校验的内部入口可以签发参考图。
type videoReferencePublisher interface {
	PrepareDataURLs(context.Context, int64, []string) ([]string, error)
}

// 保留原始画布正文作为幂等摘要；仅在实际选中只收公网素材的协议（Yezonghui 或 public_media 模板）时替换传输副本。
// 同一次提交的选号、容量重选和安全换号共享一批链接，避免重复发布及刷新期限。
// @project-doc docs/interfaces/cloud_canvas.md#video_reference_delivery
func newCreativeVideoReferencePreparer(publisher videoReferencePublisher, userID int64) func(context.Context, VideoUpstreamTarget, []byte) ([]byte, error) {
	var mu sync.Mutex
	var published []string
	return func(ctx context.Context, target VideoUpstreamTarget, body []byte) ([]byte, error) {
		public, err := videoTargetRequiresPublicMedia(target)
		if err != nil || !public {
			return body, err
		}
		// 旧方案的发送前正文是字符串数组 images；模板协议的规范请求两种写法都接受：
		// 字符串 "images":["url"] 或对象 "images":[{"url":"..","role":".."}]，逐项识别链接所在位置。
		profile := target.Version == videoTargetVersionProfile
		images := gjson.GetBytes(body, "images")
		if !images.Exists() || images.IsArray() && len(images.Array()) == 0 {
			return body, nil
		}
		if !images.IsArray() {
			return nil, ErrVideoReferenceInvalid
		}
		inputs := make([]string, 0, len(images.Array()))
		// paths 记录每张图片链接在正文中的 sjson 路径，替换时按原写法回填。
		paths := make([]string, 0, len(images.Array()))
		for i, image := range images.Array() {
			value, path := image, "images."+strconv.Itoa(i)
			if profile && image.IsObject() {
				value, path = image.Get("url"), path+".url"
			}
			if value.Type != gjson.String {
				return nil, ErrVideoReferenceInvalid
			}
			inputs = append(inputs, value.String())
			paths = append(paths, path)
		}
		// 只发布 data URL（画布素材或 API 调用者直接上传的 Base64 图片）；已是公网链接的保持原样。
		dataIndexes := []int{}
		for i, input := range inputs {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(input)), "data:") {
				dataIndexes = append(dataIndexes, i)
			}
		}
		if len(dataIndexes) == 0 {
			return body, nil
		}
		replace := func(urls []string) ([]byte, error) {
			out := body
			for n, url := range urls {
				var err error
				if out, err = sjson.SetBytes(out, paths[dataIndexes[n]], url); err != nil {
					return nil, err
				}
			}
			return out, nil
		}
		// 先验证全部生成参数，避免无效参数消耗参考图发布额度。
		preview := make([]string, len(dataIndexes))
		for i := range preview {
			preview[i] = "https://reference.example/image.png"
		}
		validationBody, err := replace(preview)
		if err != nil {
			return nil, err
		}
		if _, _, err = PrepareVideoProtocolRequest(validationBody, "", target); err != nil {
			return nil, err
		}
		if publisher == nil {
			return nil, ErrVideoReferenceUnavailable
		}
		dataInputs := make([]string, 0, len(dataIndexes))
		for _, index := range dataIndexes {
			dataInputs = append(dataInputs, inputs[index])
		}
		mu.Lock()
		defer mu.Unlock()
		if published == nil {
			published, err = publisher.PrepareDataURLs(ctx, userID, dataInputs)
			if err != nil {
				return nil, err
			}
		}
		if len(published) != len(dataInputs) {
			return nil, ErrVideoReferenceUnavailable
		}
		return replace(published)
	}
}

// videoTargetRequiresPublicMedia 判断上游是否只接受公网素材：旧 Yezonghui 方案或声明 public_media 的模板。
func videoTargetRequiresPublicMedia(target VideoUpstreamTarget) (bool, error) {
	if target.Version == videoTargetVersionProfile {
		caps, err := videoTargetProfileCapabilities(target)
		return caps != nil && caps.PublicMedia, err
	}
	adapter, err := VideoTargetProtocolAdapter(target)
	return adapter == VideoProtocolAdapterYezonghui, err
}

// 管理员试跑复用正式协议校验，参考图先发布再提交；不创建站内计费任务。
func (s *AccountTestService) prepareVideoAccountTestPayload(ctx context.Context, target VideoUpstreamTarget, prompt, imageURL string, duration int, options AccountTestOptions) ([]byte, error) {
	public, err := videoTargetRequiresPublicMedia(target)
	if err != nil {
		return nil, err
	}
	if !public || imageURL == "" {
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
