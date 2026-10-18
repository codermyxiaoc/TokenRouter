import { describe, expect, it } from 'vitest';
import type { CanvasNodeData } from '@/types/canvas';
import type { Asset, Model } from './types';
import { referenceImageSizeWarning } from './reference-image-limit';

const limit = 2 * 1024 * 1024;
const model: Model = { group_id: 1, group_name: '视频', model: 'MiniMax-H3', media_type: 'video', operations: ['generate'], aspect_ratios: ['16:9'], qualities: [], background_options: [], max_reference_images: 2, video_capabilities: { max_reference_image_bytes: limit } };
const reference = (bytes?: number, id = 'reference'): CanvasNodeData => ({ id, type: 'image', title: '参考图片', position: { x: 0, y: 0 }, width: 100, height: 100, metadata: { asset_id: id, bytes } });
const asset = (id: string, byte_size: number): Asset => ({ id, byte_size, media_type: 'image', mime_type: 'image/png', expires_at: '2099-01-01T00:00:00Z', created_at: 'now' });

describe('本站视频参考图大小提醒', () => {
  it.each([limit - 1, limit, undefined, NaN, Infinity, -1])('大小 %s 不超过限制或未知时交由服务端最终校验', bytes => {
    expect(referenceImageSizeWarning(model, [reference(bytes)], [])).toBeUndefined();
  });
  it('超过单图限制一个字节也提示，显示站内规则及换图操作', () => {
    const warning = referenceImageSizeWarning(model, [reference(limit + 1)], []);
    expect(warning).toContain('参考图过大：1 张');
    expect(warning).toContain('本站当前模型的单图上限为 2 MiB');
    expect(warning).toContain('请更换较小图片后生成');
  });
  it('按每张而非合计限制，多条引用同一素材不会重复统计', () => {
    expect(referenceImageSizeWarning(model, [reference(limit, 'a'), reference(limit, 'b')], [])).toBeUndefined();
    expect(referenceImageSizeWarning(model, [reference(12_268_339, 'a'), reference(limit + 1, 'b'), reference(12_268_339, 'a')], [])).toContain('2 张（最大 11.7 MiB）');
  });
  it('已获取的素材大小优先，忽略叠卡中没有作为主图发送的输出', () => {
    const node = reference(limit + 1, 'main');
    node.metadata!.asset_ids = ['main', 'unselected-large'];
    expect(referenceImageSizeWarning(model, [node], [asset('main', 123), asset('unselected-large', limit * 10)])).toBeUndefined();
    expect(referenceImageSizeWarning(model, [reference(123, 'main')], [asset('main', limit * 3)])).toContain('最大 6 MiB');
  });
  it('图片模型、非图片节点和未发布限制的模型不受影响', () => {
    expect(referenceImageSizeWarning({ ...model, media_type: 'image' }, [reference(limit * 10)], [])).toBeUndefined();
    expect(referenceImageSizeWarning({ ...model, video_capabilities: {} }, [reference(limit * 10)], [])).toBeUndefined();
    expect(referenceImageSizeWarning(model, [{ ...reference(limit * 10), type: 'video' }], [])).toBeUndefined();
  });
});
