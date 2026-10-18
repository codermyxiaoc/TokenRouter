import type { CanvasNodeMetadata } from '@/types/canvas';
import type { GenerationKind, Model } from './types';

type VideoCapabilities = {
  durations?: number[]; resolutions?: string[]; aspect_ratios?: string[];
  allow_custom_resolution?: boolean;
  default_duration?: number; default_resolution?: string; default_aspect_ratio?: string;
};
export type ResolvedGenerationSettings = {
  imageSize: string | undefined;
  aspectRatio: string | undefined;
  quality: string | undefined;
  background: string | undefined;
  duration: number | undefined;
  resolution: string | undefined;
  imageSizes: string[];
  aspectRatios: string[];
  qualities: string[];
  backgroundOptions: string[];
  durations: number[];
  resolutions: string[];
  outputCounts: number[];
  outputCount: number | undefined;
  allowCustomResolution: boolean;
  voices: string[];
  audioFormats: string[];
  voice: string | undefined;
  audioFormat: string | undefined;
  speed: number | undefined;
  speedMin: number | undefined;
  speedMax: number | undefined;
  maxInputChars: number | undefined;
  maxOutputTokens: number | undefined;
  maxOutputTokensLimit: number | undefined;
};

const imageSizes = ['512', '1K', '2K', '4K'] as const;
const strings = (value: unknown): string[] => Array.isArray(value) ? [...new Set(value.filter((item): item is string => typeof item === 'string' && item.length > 0))] : [];

export const videoResolutionPresets = ['480p', '720p', '1080p'];

// 与后端价卡使用相同的数字 p 范围；不把 2K/4K 或精确尺寸偷偷转换成另一价格档位。
export function normalizeCustomVideoResolution(value: string): string | undefined {
  const digits = value.trim().toLowerCase().replace(/p$/, '');
  if (!/^[1-9]\d{0,4}$/.test(digits)) return;
  const edge = Number(digits);
  return edge <= 65536 ? `${edge}p` : undefined;
}

// 生成张数由服务端显式开放，旧目录继续保持原来的单次固定输出合同。
export function getImageOutputCounts(model?: Model): number[] {
  if (!model) return [];
  if (Array.isArray(model.output_counts)) {
    return [...new Set(model.output_counts.filter((value): value is number => typeof value === 'number' && Number.isInteger(value) && value >= 1 && value <= 8))].sort((a, b) => a - b);
  }
  const fixed = model.output_count;
  return typeof fixed === 'number' && Number.isInteger(fixed) && fixed >= 1 && fixed <= 8 ? [fixed] : [1];
}

// 服务端尺寸白名单优先；仅旧目录未提供白名单时兼容其显式价格档位。
export function getImageSizes(model?: Model): string[] {
  if (!model) return [];
  if (Array.isArray(model.image_sizes)) return strings(model.image_sizes);
  if (Array.isArray(model.sizes)) return strings(model.sizes);
  return imageSizes.filter(size => {
    const price = model[`price_${size.toLowerCase()}`];
    return typeof price === 'number' && Number.isFinite(price) && price >= 0;
  });
}

// 面板、参数摘要和真正提交共用当前能力快照，旧节点参数不得越过最新白名单。
export function resolveGenerationSettings(model: Model | undefined, metadata: CanvasNodeMetadata | undefined, kind: GenerationKind): ResolvedGenerationSettings {
  const video = model?.video_capabilities as VideoCapabilities | undefined;
  const audio = kind === 'audio' ? model?.audio_capabilities : undefined;
  const text = kind === 'text' ? model?.text_capabilities : undefined;
  const outputLimit = text && Number.isInteger(text.max_output_tokens) && text.max_output_tokens > 0 ? text.max_output_tokens : undefined;
  const validOutputLimit = (value: unknown): value is number => typeof value === 'number' && Number.isInteger(value) && value > 0 && outputLimit !== undefined && value <= outputLimit;
  const voices = strings(audio?.voices);
  const audioFormats = strings(audio?.formats);
  const speedMin = audio && Number.isFinite(audio.speed_min) ? audio.speed_min : undefined;
  const speedMax = audio && Number.isFinite(audio.speed_max) ? audio.speed_max : undefined;
  const validSpeed = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value) && speedMin !== undefined && speedMax !== undefined && value >= speedMin && value <= speedMax;
  const sizes = kind === 'image' ? getImageSizes(model) : [];
  const resolutions = kind === 'video' ? strings(video?.resolutions ?? model?.video_resolutions) : [];
  const ratios = kind === 'audio' || kind === 'text' ? [] : strings(kind === 'image' ? model?.aspect_ratios : video?.aspect_ratios ?? model?.video_aspect_ratios ?? model?.aspect_ratios);
  const qualities = kind === 'image' ? strings(model?.qualities) : [];
  const backgrounds = kind === 'image' ? strings(model?.background_options) : [];
  const durationValues = kind === 'video' ? video?.durations ?? model?.video_durations : undefined;
  const durations = Array.isArray(durationValues)
    ? [...new Set(durationValues.filter(value => Number.isInteger(value) && value > 0 && value <= 60))].sort((a, b) => a - b)
    : [];
  const storedDuration = Number(metadata?.seconds);
  const outputCounts = kind === 'image' ? getImageOutputCounts(model) : model ? [1] : [];
  const storedCount = metadata?.count;
  const defaultCount = typeof model?.output_count === 'number' && outputCounts.includes(model.output_count) ? model.output_count : outputCounts[0];
  const allowCustomResolution = kind === 'video' && video?.allow_custom_resolution === true;
  const customResolution = allowCustomResolution && metadata?.resolution ? normalizeCustomVideoResolution(metadata.resolution) : undefined;
  return {
    imageSize: resolveString(metadata?.image_size, sizes, '1K'),
    aspectRatio: resolveString(metadata?.aspect_ratio, ratios, video?.default_aspect_ratio),
    quality: resolveString(metadata?.quality, qualities),
    background: resolveString(metadata?.background, backgrounds),
    duration: durations.includes(storedDuration) ? storedDuration : video?.default_duration !== undefined && durations.includes(video.default_duration) ? video.default_duration : durations[0],
    resolution: customResolution || resolveString(metadata?.resolution, resolutions, video?.default_resolution),
    imageSizes: sizes,
    aspectRatios: ratios,
    qualities,
    backgroundOptions: backgrounds,
    durations,
    resolutions,
    outputCounts,
    outputCount: typeof storedCount === 'number' && outputCounts.includes(storedCount) ? storedCount : defaultCount,
    allowCustomResolution,
    voices,
    audioFormats,
    voice: resolveString(metadata?.voice, voices, audio?.default_voice),
    audioFormat: resolveString(metadata?.audio_format, audioFormats, audio?.default_format),
    speed: validSpeed(metadata?.speed) ? metadata.speed : validSpeed(audio?.default_speed) ? audio.default_speed : speedMin,
    speedMin,
    speedMax,
    maxInputChars: audio && Number.isInteger(audio.max_input_chars) && audio.max_input_chars > 0 ? audio.max_input_chars : undefined,
    maxOutputTokens: validOutputLimit(metadata?.max_output_tokens) ? metadata.max_output_tokens : validOutputLimit(text?.default_max_output_tokens) ? text.default_max_output_tokens : outputLimit,
    maxOutputTokensLimit: outputLimit,
  };
}

// 配置节点和普通生成面板使用同一个摘要来源，不能显示一套参数却提交另一套。
export function generationSettingsSummary(model: Model | undefined, metadata: CanvasNodeMetadata | undefined, kind: GenerationKind): string {
  const settings = resolveGenerationSettings(model, metadata, kind);
  const ratio = ['auto', 'adaptive'].includes(settings.aspectRatio || '') ? '自动' : settings.aspectRatio || '自动';
  if (kind === 'text') return `最多 ${settings.maxOutputTokens || '—'} Token`;
  if (kind === 'audio') return `${settings.voice ? settings.voice[0].toUpperCase() + settings.voice.slice(1) : '声音'} · ${settings.audioFormat?.toUpperCase() || '格式'} · ${settings.speed ?? '—'}x`;
  if (kind === 'video') return `${settings.resolution || '自动'} · ${ratio} · ${settings.duration || '—'}s`;
  const quality = settings.quality || 'auto';
  return `${({ auto: '自动', low: '低', medium: '中', high: '高' } as Record<string, string>)[quality] || quality} · ${settings.imageSize || '自动'} · ${ratio} · ${settings.outputCount || 1} 张`;
}

function resolveString(stored: string | undefined, allowed: string[], preferred?: string): string | undefined {
  if (stored !== undefined && allowed.includes(stored)) return stored;
  if (preferred !== undefined && allowed.includes(preferred)) return preferred;
  return allowed[0];
}
