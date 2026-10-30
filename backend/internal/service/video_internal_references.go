package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 只有已经完成身份、素材归属及格式校验的内部入口可以签发参考图。
type videoReferencePublisher interface {
	PrepareDataURLs(context.Context, int64, []string) ([]string, error)
	// PrepareAssetURLs 为已校验归属的画布参考视频签发短期链接。
	PrepareAssetURLs(context.Context, int64, []VideoReferenceAsset) ([]string, error)
}

// creativeVideoAssetURLPrefix 标记画布参考视频的内部占位链接；只有画布执行器的准备器能把它换成短期链接。
const creativeVideoAssetURLPrefix = "creative-asset://"

// creativeVideoAssetPlaceholder 生成绑定执行与素材的占位链接：摘要不可预测，提示词不可能恰好等于它而被误替换；
// 同一执行重试时值不变，正文与幂等摘要保持一致。不含原始素材 ID，提交正文也不暴露素材标识。
func creativeVideoAssetPlaceholder(executionID, assetID string) string {
	sum := sha256.Sum256([]byte("creative-video-reference|" + executionID + "|" + assetID))
	return creativeVideoAssetURLPrefix + hex.EncodeToString(sum[:16])
}

// 保留原始画布正文作为幂等摘要；参考视频的占位对任何目标都替换为短期链接，参考图仅在实际选中只收公网素材的
// 协议（Yezonghui 或 public_media 模板）时替换传输副本。同一次提交的选号、容量重选和安全换号共享一批链接，
// 避免重复发布及刷新期限。videos 必须是画布执行器已校验归属的素材，外部 API 请求不传。
// @project-doc docs/interfaces/cloud_canvas.md#video_reference_delivery
func newCreativeVideoReferencePreparer(publisher videoReferencePublisher, userID int64, videos ...VideoReferenceAsset) func(context.Context, VideoUpstreamTarget, []byte) ([]byte, error) {
	var mu sync.Mutex
	var publishedVideos []string
	prepareImages := newCreativeVideoImagePreparer(publisher, userID)
	return func(ctx context.Context, target VideoUpstreamTarget, body []byte) ([]byte, error) {
		placeholderBody, placeholders, err := replaceCreativeVideoAssetURLs(body, videos)
		if err != nil {
			return nil, err
		}
		out, err := prepareImages(ctx, target, placeholderBody)
		if err != nil || len(videos) == 0 {
			return out, err
		}
		// 先用占位链接完整校验协议参数，避免无效请求消耗参考视频签发额度。
		if _, _, err = PrepareVideoProtocolRequest(out, "", target); err != nil {
			return nil, err
		}
		if publisher == nil {
			return nil, ErrVideoReferenceUnavailable
		}
		mu.Lock()
		defer mu.Unlock()
		if publishedVideos == nil {
			if publishedVideos, err = publisher.PrepareAssetURLs(ctx, userID, videos); err != nil {
				publishedVideos = nil
				return nil, err
			}
		}
		if len(publishedVideos) != len(videos) {
			return nil, ErrVideoReferenceUnavailable
		}
		for i, placeholder := range placeholders {
			out = bytes.ReplaceAll(out, placeholder, creativeVideoJSONString(publishedVideos[i]))
		}
		return out, nil
	}
}

// replaceCreativeVideoAssetURLs 把每个参考视频占位按精确 JSON 字符串替换为校验用的公网样例链接。内置协议会把规范请求
// 转换为厂商原生结构，因此不按字段路径查找，而是在整个正文中替换；某个视频未出现在正文中或仍有残留占位时拒绝。
// 只处理执行器声明的占位：普通请求或提示词里出现 creative-asset:// 文字不受影响；外部伪造的占位不会被签发，
// 按普通无效链接交给协议校验。声明的视频未出现在正文中时拒绝，避免静默丢失参考视频。
func replaceCreativeVideoAssetURLs(body []byte, videos []VideoReferenceAsset) ([]byte, [][]byte, error) {
	if len(videos) == 0 {
		return body, nil, nil
	}
	// 样例链接带每次随机的标记：第二阶段按整段正文替换为签发链接，提示词不可能恰好等于该样例而被误改。
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, nil, ErrVideoReferenceUnavailable
	}
	sample := "https://reference.example/video-" + hex.EncodeToString(nonce[:]) + "-"
	out := body
	placeholders := make([][]byte, 0, len(videos))
	for i, video := range videos {
		if !strings.HasPrefix(video.Placeholder, creativeVideoAssetURLPrefix) {
			return nil, nil, ErrVideoReferenceInvalid
		}
		token := creativeVideoJSONString(video.Placeholder)
		if !bytes.Contains(out, token) {
			return nil, nil, ErrVideoReferenceInvalid
		}
		placeholder := creativeVideoJSONString(sample + strconv.Itoa(i) + ".mp4")
		out = bytes.ReplaceAll(out, token, placeholder)
		placeholders = append(placeholders, placeholder)
	}
	return out, placeholders, nil
}

func creativeVideoJSONString(value string) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

// newCreativeVideoImagePreparer 只处理参考图：选中只收公网素材的协议时把 data URL 发布为短期链接。
func newCreativeVideoImagePreparer(publisher videoReferencePublisher, userID int64) func(context.Context, VideoUpstreamTarget, []byte) ([]byte, error) {
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
