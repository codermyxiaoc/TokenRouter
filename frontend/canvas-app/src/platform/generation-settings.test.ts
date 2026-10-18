import { describe, expect, it } from 'vitest';
import { getImageOutputCounts, getImageSizes, normalizeCustomVideoResolution, resolveGenerationSettings } from './generation-settings';
import type { Model } from './types';

const image: Model = { group_id: 1, group_name: '图片', model: 'image-model', operations: ['generate'], image_sizes: ['4K', '1K'], aspect_ratios: ['1:1', '16:9'], qualities: ['auto', 'high'], background_options: ['opaque'], max_reference_images: 1, output_count: 4, price_1k: 1, price_2k: 0, price_4k: 2 };
const video: Model = { ...image, media_type: 'video', model: 'video-model', video_capabilities: { durations: [10, 5], resolutions: ['720p'], aspect_ratios: ['16:9'] } };
const audio: Model = { ...image, media_type: 'audio', model: 'grok-voice-tts', audio_capabilities: { voices: ['eve', 'ara', 'rex', 'sal', 'leo'], formats: ['mp3', 'wav'], speed_min: .7, speed_max: 1.5, default_voice: 'eve', default_format: 'mp3', default_speed: 1, max_input_chars: 15000 } };

describe('画布生成参数的共享白名单解析', () => {
  it('音频只使用当前目录的声音、格式、连续语速，不继承图像参数或旧插件设置', () => {
    expect(resolveGenerationSettings(audio, { voice: 'ara', audio_format: 'wav', speed: .88, image_size: '4K', aspect_ratio: '16:9' }, 'audio')).toMatchObject({ voice: 'ara', audioFormat: 'wav', speed: .88, maxInputChars: 15000, imageSize: undefined, aspectRatio: undefined, duration: undefined, outputCount: 1 });
    const defaults = { voice: 'eve', audioFormat: 'mp3', speed: 1 };
    expect(resolveGenerationSettings(audio, { audioVoice: 'alloy', audioFormat: 'opus', audioSpeed: '2' }, 'audio')).toMatchObject(defaults);
    for (const speed of [NaN, Infinity, 0, .69, 1.51]) expect(resolveGenerationSettings(audio, { voice: 'alloy', audio_format: 'pcm', speed }, 'audio')).toMatchObject(defaults);
    expect(resolveGenerationSettings({ ...audio, audio_capabilities: { ...audio.audio_capabilities!, voices: ['rex'], formats: ['wav'], default_voice: 'rex', default_format: 'wav', default_speed: .75 } }, { voice: 'eve', audio_format: 'mp3', speed: 2 }, 'audio')).toMatchObject({ voice: 'rex', audioFormat: 'wav', speed: .75 });
    expect(resolveGenerationSettings(undefined, { voice: 'eve', audio_format: 'mp3', speed: 1 }, 'audio')).toMatchObject({ voice: undefined, audioFormat: undefined, speed: undefined, outputCount: undefined });
  });
  it('开放自定义视频参数时保留原默认值，并让自定义值用于摘要与提交', () => {
    const catalog = { ...video, video_capabilities: {
      durations: Array.from({ length: 60 }, (_, index) => index + 1), resolutions: ['480p', '720p', '1080p'], aspect_ratios: ['1:1', '16:9', 'adaptive'],
      allow_custom_resolution: true, default_duration: 4, default_resolution: '720p', default_aspect_ratio: '16:9',
    } };
    expect(resolveGenerationSettings(catalog, undefined, 'video')).toMatchObject({ duration: 4, resolution: '720p', aspectRatio: '16:9' });
    expect(resolveGenerationSettings(catalog, { seconds: '60', resolution: '768P', aspect_ratio: '1:1' }, 'video')).toMatchObject({ duration: 60, resolution: '768p', aspectRatio: '1:1' });
    // 移除自定义比例后，旧节点的自由比例回落到目录默认值，不影响已冻结的提交意图。
    expect(resolveGenerationSettings(catalog, { aspect_ratio: '5:4' }, 'video').aspectRatio).toBe('16:9');
    expect(resolveGenerationSettings(catalog, { seconds: '61', resolution: '2k', aspect_ratio: '0:1' }, 'video')).toMatchObject({ duration: 4, resolution: '720p', aspectRatio: '16:9' });
    expect(resolveGenerationSettings(catalog, { aspect_ratio: 'adaptive' }, 'video').aspectRatio).toBe('adaptive');
  });

  it('自定义分辨率严格校验，不把原生档位和非法值降档发送', () => {
    for (const value of ['768', '768p', '768P', ' 768p ']) expect(normalizeCustomVideoResolution(value)).toBe('768p');
    expect(normalizeCustomVideoResolution('65536')).toBe('65536p');
    for (const value of ['', '0', '-1', '65537', '720.5', '0768', '2k', '4k', '1280x720', 'Infinity', '1e3']) expect(normalizeCustomVideoResolution(value)).toBeUndefined();
  });

  it('旧视频 768p 参数在当前只允许 720p 时回落到同一个合法显示与提交值', () => {
    const stored = { resolution: '768p', aspect_ratio: '9:16', seconds: '7' };
    const result = resolveGenerationSettings(video, stored, 'video');
    expect(result.resolution).toBe('720p');
    expect(result.aspectRatio).toBe('16:9');
    expect(result.duration).toBe(5);
    expect(result.imageSize).toBeUndefined();
    expect(result.quality).toBeUndefined();
    expect(stored).toEqual({ resolution: '768p', aspect_ratio: '9:16', seconds: '7' });
  });

  it('旧图片 2K 已关闭时优先选择允许的 1K，并移除过时质量和背景', () => {
    const result = resolveGenerationSettings(image, { image_size: '2K', quality: 'low', background: 'transparent', aspect_ratio: '16:9' }, 'image');
    expect(result.imageSize).toBe('1K');
    expect(result.aspectRatio).toBe('16:9');
    expect(result.quality).toBe('auto');
    expect(result.background).toBe('opaque');
    expect(result.outputCount).toBe(4);
    expect(getImageSizes(image)).toEqual(['4K', '1K']);
    expect(resolveGenerationSettings({ ...image, image_sizes: ['4K', '2K'] }, undefined, 'image').imageSize).toBe('4K');
  });

  it('保留仍允许的值，仅接受离散时长集合，不放开其间的秒数', () => {
    expect(resolveGenerationSettings(image, { image_size: '4K', quality: 'high' }, 'image')).toMatchObject({ imageSize: '4K', quality: 'high' });
    expect(resolveGenerationSettings(video, { seconds: '10' }, 'video').duration).toBe(10);
    for (const seconds of ['6', 'NaN', 'Infinity', '0', '61', '', '5.5']) {
      expect(resolveGenerationSettings(video, { seconds }, 'video').duration).toBe(5);
    }
    expect(resolveGenerationSettings(video, undefined, 'video').durations).toEqual([5, 10]);
  });

  it('空目录不复用旧节点参数，显式空白名单不被旧顶层字段或价格重新开放', () => {
    const metadata = { image_size: '2K', resolution: '768p', aspect_ratio: '16:9', quality: 'high', background: 'transparent', seconds: '10' };
    for (const kind of ['image', 'video'] as const) {
      const missing = resolveGenerationSettings(undefined, metadata, kind);
      expect([missing.imageSize, missing.resolution, missing.aspectRatio, missing.quality, missing.background, missing.duration, missing.outputCount]).toEqual(Array(7).fill(undefined));
      expect([missing.imageSizes, missing.resolutions, missing.aspectRatios, missing.qualities, missing.backgroundOptions, missing.durations]).toEqual(Array.from({ length: 6 }, () => []));
    }
    expect(resolveGenerationSettings({ ...image, image_sizes: [] }, metadata, 'image').imageSize).toBeUndefined();
    const disabled = resolveGenerationSettings({ ...video, video_resolutions: ['768p'], video_durations: [10], video_aspect_ratios: ['16:9'], video_capabilities: { resolutions: [], durations: [], aspect_ratios: [] } }, metadata, 'video');
    expect([disabled.resolution, disabled.aspectRatio, disabled.duration]).toEqual([undefined, undefined, undefined]);
  });

  it('旧目录可用显式价格兼容尺寸，不把缺价或非有限值当作开放档位', () => {
    expect(getImageSizes({ ...image, image_sizes: undefined, price_1k: NaN, price_2k: 0, price_4k: undefined })).toEqual(['2K']);
    expect(getImageSizes({ ...image, image_sizes: undefined, sizes: [] })).toEqual([]);
  });

  it('显式张数白名单支持多张且与摘要和提交保持同一个有效值', () => {
    const catalog = { ...image, output_count: 1, output_counts: [4, 1, 2, 3, 2] };
    expect(getImageOutputCounts(catalog)).toEqual([1, 2, 3, 4]);
    expect(resolveGenerationSettings(catalog, { count: 3 }, 'image')).toMatchObject({ outputCounts: [1, 2, 3, 4], outputCount: 3 });
    expect(resolveGenerationSettings(catalog, undefined, 'image').outputCount).toBe(1);
    for (const count of [0, -1, 1.5, NaN, Infinity, 5, 16]) {
      expect(resolveGenerationSettings(catalog, { count }, 'image').outputCount).toBe(1);
    }
  });

  it('旧目录与 MJ 维持固定输出，不凭 max_output_count 自动放开额外付费请求', () => {
    expect(getImageOutputCounts({ ...image, max_output_count: 4 })).toEqual([4]);
    expect(resolveGenerationSettings(image, { count: 2 }, 'image').outputCount).toBe(4);
    expect(getImageOutputCounts({ ...image, output_count: undefined, max_output_count: 10 })).toEqual([1]);
    expect(getImageOutputCounts(undefined)).toEqual([]);
    expect(resolveGenerationSettings({ ...image, output_counts: [] }, { count: 4 }, 'image').outputCount).toBeUndefined();
    expect(getImageOutputCounts({ ...image, output_counts: [0, -1, 1.5, 9, 16, Infinity, '2', 2, 2] })).toEqual([2]);
    expect(resolveGenerationSettings({ ...video, output_counts: [1, 2] }, { count: 2 }, 'video').outputCount).toBe(1);
  });

  it('MJ 批量张数表示总数，仅接受四张或八张，不把两次四张扩成三十二张', () => {
    const mj = { ...image, model: 'MJ-v7', output_count: 4, output_counts: [4, 8] };
    expect(resolveGenerationSettings(mj, { count: 8 }, 'image')).toMatchObject({ outputCount: 8, outputCounts: [4, 8] });
    expect(resolveGenerationSettings(mj, { count: 2 }, 'image').outputCount).toBe(4);
    expect(resolveGenerationSettings(mj, { count: 32 }, 'image').outputCount).toBe(4);
  });
});
