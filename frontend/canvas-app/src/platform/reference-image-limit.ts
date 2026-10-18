import type { CanvasNodeData } from '@/types/canvas';
import { mediaType, type Asset, type Model } from './types';

const knownBytes = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
const formatMiB = (bytes: number) => `${Number((bytes / 1024 / 1024).toFixed(1))} MiB`;

// 只检查实际发送的主图 asset_id；叠卡中未选中的图片与未知大小均不作推断。
export function referenceImageSizeWarning(model: Model | undefined, references: CanvasNodeData[], assets: readonly Asset[]): string | undefined {
  const limit = model?.video_capabilities?.max_reference_image_bytes;
  if (!model || mediaType(model) !== 'video' || !knownBytes(limit) || limit <= 0) return;
  const oversized = new Map<string, number>();
  for (const reference of references) {
    const assetId = reference.metadata?.asset_id;
    if (reference.type !== 'image' || !assetId) continue;
    const asset = assets.find(item => item.id === assetId);
    const size = asset ? asset.byte_size : reference.metadata?.bytes;
    if (knownBytes(size) && size > limit) oversized.set(assetId, size);
  }
  if (!oversized.size) return;
  return `参考图过大：${oversized.size} 张（最大 ${formatMiB(Math.max(...oversized.values()))}）。本站当前模型的单图上限为 ${formatMiB(limit)}，请更换较小图片后生成。`;
}
