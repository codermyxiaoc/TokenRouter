// @vitest-environment jsdom
import { act, useSyncExternalStore, type ReactNode, type RefObject } from 'react';
import { createPortal } from 'react-dom';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, api, assetBlob, assetMetadata, assetURL, createExecution, createImageBatch, listAssets, listExecutions, listModels, uploadAsset } from '@/platform/api';
import type { Asset, CanvasDocument, Execution, ExecutionBatch, Model } from '@/platform/types';
import type { CanvasNodeData } from '@/types/canvas';
import { Editor } from './Editor';

const cloudState = vi.hoisted(() => ({
  document: null as CanvasDocument | null,
  listeners: new Set<() => void>(),
  flush: vi.fn<() => Promise<void>>(),
  navigate: vi.fn(),
}));

vi.mock('react-router-dom', () => ({ useParams: () => ({ id: 'canvas-test' }), useNavigate: () => cloudState.navigate }));
vi.mock('@/hooks/use-cloud-document', () => {
  const update = (value: CanvasDocument | ((old: CanvasDocument) => CanvasDocument)) => {
    if (!cloudState.document) return;
    const next = typeof value === 'function' ? value(cloudState.document) : value;
    if (next === cloudState.document) return;
    cloudState.document = next;
    cloudState.listeners.forEach(notify => notify());
  };
  const subscribe = (notify: () => void) => { cloudState.listeners.add(notify); return () => { cloudState.listeners.delete(notify); }; };
  return { useCloudDocument: () => {
    const document = useSyncExternalStore(subscribe, () => cloudState.document);
    return { document, record: { id: 'canvas-test', title: '测试画布', document, revision: 1 }, update, flush: cloudState.flush, error: '', saving: false, conflict: false, reload: vi.fn() };
  } };
});
vi.mock('@/platform/api', async importOriginal => ({ ...await importOriginal<typeof import('@/platform/api')>(), api: vi.fn(), assetBlob: vi.fn(), assetMetadata: vi.fn(), assetURL: vi.fn(), createExecution: vi.fn(), createImageBatch: vi.fn(), listAssets: vi.fn(), listExecutions: vi.fn(), listModels: vi.fn(), uploadAsset: vi.fn() }));
vi.mock('@/components/canvas/infinite-canvas', () => ({ InfiniteCanvas: ({ children, containerRef, onDrop }: { children: ReactNode; containerRef: RefObject<HTMLDivElement | null>; onDrop?: React.DragEventHandler<HTMLDivElement> }) => <div ref={containerRef} data-testid="canvas-drop" onDrop={onDrop}>{children}</div> }));
vi.mock('@/components/canvas/canvas-connection-layer', () => ({ CanvasConnectionLayer: () => null }));
vi.mock('@/components/canvas/canvas-mini-map', () => ({ Minimap: () => null }));
vi.mock('@/components/canvas/canvas-zoom-controls', () => ({ CanvasZoomControls: () => null }));
vi.mock('@/components/canvas/canvas-node-crop-dialog', () => ({ CanvasNodeCropDialog: () => null }));
vi.mock('@/components/canvas/canvas-node-mask-edit-dialog', () => ({ CanvasNodeMaskEditDialog: () => null }));
vi.mock('antd', () => ({ Modal: ({ open, children, title, footer, onCancel, modalRender }: { open?: boolean; children: ReactNode; title?: ReactNode; footer?: ReactNode; onCancel?: () => void; modalRender?: (node: ReactNode) => ReactNode }) => {
  if (!open) return null;
  // 弹层脱离画布 DOM 后仍让真实编辑器处理冒泡事件，验证 Delete 不会误删节点。
  const dialog = <div role="dialog" aria-label={typeof title === 'string' ? title : undefined} onKeyDown={event => { if (event.key === 'Escape') { event.stopPropagation(); onCancel?.(); } }}>{title}{children}{footer}</div>;
  return createPortal(modalRender ? modalRender(dialog) : dialog, document.body);
} }));

const imageNode: CanvasNodeData = { id: 'image-current', title: '当前图片', type: 'image', position: { x: 100, y: 100 }, width: 340, height: 240, metadata: { prompt: '一只红色小鸟', model: '7::gpt-image-2', image_size: '1K', count: 1 } };
const model: Model = { group_id: 7, group_name: '图片分组', model: 'gpt-image-2', operations: ['generate', 'edit', 'inpaint'], image_sizes: ['1K', '2K'], aspect_ratios: ['1:1', '16:9'], qualities: ['auto', 'high'], background_options: ['auto'], output_count: 1, output_counts: [1, 2, 3, 4, 5, 6, 7, 8], max_reference_images: 4 };
const asset = (id: string): Asset => ({ id, canvas_id: 'canvas-test', media_type: 'image', mime_type: 'image/png', byte_size: 123, created_at: '2026-10-18T00:00:00Z', expires_at: '2099-10-25T00:00:00Z' });
const batch = (key: string, count: number, status: Execution['status'] = 'queued'): ExecutionBatch => ({ batch_id: key, executions: Array.from({ length: count }, (_, index) => ({ id: `execution-${index}`, canvas_id: 'canvas-test', node_id: 'image-current', kind: 'image', status, task_id: `task-${index}`, assets: status === 'succeeded' ? [asset(`result-${index}`)] : [], batch_id: key, batch_index: index, batch_size: count, batch_output_count: count, created_at: '2026-10-18T00:00:00Z', updated_at: '2026-10-18T00:01:00Z' })) });
let host: HTMLDivElement;
let root: Root;
let poll: (() => void) | undefined;
let saved: CanvasDocument[];
const currentNode = () => cloudState.document!.nodes.find(node => node.id === imageNode.id)!;
const button = (label: string) => document.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)!;
const click = async (element: HTMLElement) => { await act(async () => element.click()); };
const mount = async () => {
  await act(async () => root.render(<Editor/>));
  await click(host.querySelector<HTMLButtonElement>('.node-list-item')!);
};
const chooseCount = async (count: number) => {
  await click(button('打开生成设置'));
  const option = [...document.querySelectorAll<HTMLButtonElement>('.generation-settings-popover button')].find(item => item.textContent === `${count} 张`)!;
  await click(option);
  await click(button('关闭设置'));
};
const writePrompt = async (value: string) => {
  const input = host.querySelector<HTMLTextAreaElement>('.composer-prompt')!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(input, value);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
};

beforeEach(() => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.clearAllMocks();
  cloudState.listeners.clear();
  cloudState.document = { nodes: [structuredClone(imageNode)], connections: [], viewport: { x: 0, y: 0, k: 1 } };
  saved = [];
  cloudState.flush.mockImplementation(async () => { saved.push(structuredClone(cloudState.document!)); });
  vi.mocked(api).mockResolvedValue({ max_asset_bytes: 1_000_000, max_prompt_chars: 10_000 });
  vi.mocked(listModels).mockResolvedValue([model]);
  vi.mocked(listAssets).mockResolvedValue({ items: [], total: 0, page: 1, page_size: 50 });
  vi.mocked(listExecutions).mockResolvedValue({ items: [], total: 0, page: 1, page_size: 100 });
  vi.mocked(assetURL).mockImplementation(async id => `blob:private-${id}`);
  vi.mocked(assetBlob).mockResolvedValue(new Blob(['private image'], { type: 'image/png' }));
  vi.stubGlobal('URL', class extends URL {
    static createObjectURL = vi.fn((_blob: Blob | MediaSource) => 'blob:reference-preview');
    static revokeObjectURL = vi.fn((_url: string) => {});
  });
  vi.mocked(createImageBatch).mockImplementation(async (_canvasId, _nodeId, _params, count, key) => batch(key, count));
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} unobserve() {} });
  vi.spyOn(window, 'requestAnimationFrame').mockReturnValue(1);
  vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {});
  vi.spyOn(globalThis, 'setInterval').mockImplementation(((callback: () => void) => { poll = callback; return 123; }) as typeof setInterval);
  vi.spyOn(globalThis, 'clearInterval').mockImplementation(() => {});
  host = document.createElement('div'); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals(); poll = undefined; });

describe('画布编辑器生成与恢复集成', () => {
  it('视频使用说明出现在按钮下方，详情纯文本且查看不改变节点或创建任务', async () => {
    const notes = '首帧比例请选择自动。\n<img src=x onerror="alert(1)">\n'+ '说明正文'.repeat(80);
    vi.mocked(listModels).mockResolvedValue([{ ...model, media_type: 'video', model: 'video-notes', usage_notes: notes, operations: ['generate'] }]);
    cloudState.document!.nodes = [{ ...structuredClone(imageNode), type: 'video', metadata: { model: '7::video-notes', prompt: '原提示词' } }];
    await mount();
    const footer = host.querySelector('.composer-footer')!;
    const preview = host.querySelector('.model-usage-notes')!;
    expect(footer.compareDocumentPosition(preview) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(preview.textContent).toContain('首帧比例请选择自动。');
    expect(preview.querySelector('img')).toBeNull();
    const before = structuredClone(cloudState.document);
    await click(button('查看视频模型使用说明'));
    const details = document.querySelector('[role="dialog"][aria-label="视频模型使用说明"]')!;
    expect(details.textContent).toContain(notes);
    expect(details.querySelector('img')).toBeNull();
    await act(async () => details.dispatchEvent(new KeyboardEvent('keydown', { key: 'Delete', bubbles: true })));
    await click(button('关闭使用说明'));
    expect(document.querySelector('[role="dialog"][aria-label="视频模型使用说明"]')).toBeNull();
    expect(cloudState.document).toEqual(before);
    expect(createExecution).not.toHaveBeenCalled();
    expect(createImageBatch).not.toHaveBeenCalled();
    expect(cloudState.flush).not.toHaveBeenCalled();
  });

  it('相同视频型号按分组切换说明，换模型关闭旧详情，无说明时隐藏', async () => {
    vi.mocked(listModels).mockResolvedValue([
      { ...model, media_type: 'video', model: 'shared-video', usage_notes: '甲分组首帧说明' },
      { ...model, group_id: 8, group_name: '乙分组', media_type: 'video', model: 'shared-video', usage_notes: '乙分组参考说明' },
      { ...model, media_type: 'video', model: 'old-video' },
    ]);
    cloudState.document!.nodes = [{ ...structuredClone(imageNode), type: 'video', metadata: { model: '7::shared-video' } }];
    await mount();
    await click(button('查看视频模型使用说明'));
    await act(async () => {
      cloudState.document = { ...cloudState.document!, nodes: [{ ...currentNode(), metadata: { model: '8::shared-video' } }] };
      cloudState.listeners.forEach(notify => notify());
    });
    expect(host.querySelector('.model-usage-notes')?.textContent).toContain('乙分组参考说明');
    expect(host.textContent).not.toContain('甲分组首帧说明');
    expect(document.querySelector('[role="dialog"][aria-label="视频模型使用说明"]')).toBeNull();
    await act(async () => {
      cloudState.document = { ...cloudState.document!, nodes: [{ ...currentNode(), metadata: { model: '7::old-video' } }] };
      cloudState.listeners.forEach(notify => notify());
    });
    expect(host.querySelector('.model-usage-notes')).toBeNull();
    expect(button('查看视频模型使用说明')).toBeNull();
  });

  it('失败原因可打开完整详情，查看和关闭不移动节点或提交收费任务', async () => {
    const message = '视频模型不支持当前分辨率，请修改后重试；预扣已释放';
    currentNode().metadata = { ...currentNode().metadata, status: 'error', errorDetails: message };
    await mount();
    const before = structuredClone(cloudState.document);
    await click(button('生成失败详情'));
    const dialog = document.querySelector('[role="dialog"][aria-label="生成失败详情"]');
    expect(dialog?.textContent).toContain(message);
    expect(dialog?.textContent).not.toContain('task_id');
    await act(async () => { button('关闭任务详情').dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, button: 0, clientX: 160, clientY: 160 })); });
    await click(button('关闭任务详情'));
    expect(document.querySelector('[role="dialog"][aria-label="生成失败详情"]')).toBeNull();
    expect(cloudState.document).toEqual(before);
    expect(createExecution).not.toHaveBeenCalled();
    expect(createImageBatch).not.toHaveBeenCalled();
  });

  it('任务查询超时保留在途任务且自动继续查询，恢复后清除提示并展示结果', async () => {
    currentNode().metadata = { ...currentNode().metadata, execution_id: 'pending-image', status: 'loading' };
    vi.mocked(listExecutions).mockRejectedValueOnce(new DOMException('timeout', 'TimeoutError'));
    await mount();
    expect(host.querySelector('[role="alert"]')?.textContent).toContain('任务状态查询超时，将自动重试');
    expect(currentNode().metadata).toMatchObject({ execution_id: 'pending-image', status: 'loading' });
    expect(button('生成中…').disabled).toBe(true);
    vi.mocked(listExecutions).mockResolvedValue({
      items: [{ id: 'pending-image', canvas_id: 'canvas-test', node_id: imageNode.id, kind: 'image', status: 'succeeded', task_id: 'upstream-existing', assets: [asset('recovered')], created_at: '2026-10-18T00:00:00Z', updated_at: '2026-10-18T00:01:00Z' }],
      total: 1, page: 1, page_size: 100,
    });
    await act(async () => poll!());
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(currentNode().metadata).toMatchObject({ execution_id: 'pending-image', status: 'success', asset_id: 'recovered' });
    expect(createExecution).not.toHaveBeenCalled();
    expect(createImageBatch).not.toHaveBeenCalled();
  });

  it('查询未完成时不会并行堆积轮询，退出画布会取消只读查询', async () => {
    let finish: ((value: Awaited<ReturnType<typeof listExecutions>>) => void) | undefined;
    vi.mocked(listExecutions).mockImplementation(() => new Promise(resolve => { finish = resolve; }));
    await mount();
    await act(async () => { poll!(); poll!(); });
    expect(listExecutions).toHaveBeenCalledTimes(1);
    const signal = vi.mocked(listExecutions).mock.calls[0][2]!;
    expect(signal.aborted).toBe(false);
    await act(async () => root.render(null));
    expect(signal.aborted).toBe(true);
    await act(async () => finish!({ items: [], total: 0, page: 1, page_size: 100 }));
    expect(createExecution).not.toHaveBeenCalled();
    expect(createImageBatch).not.toHaveBeenCalled();
  });

  it.each([false, true])('视频参数从设置面板准确提交且只创建一个任务（自定义=%s）', async custom => {
    const videoModel: Model = {
      ...model, model: 'video-test', media_type: 'video', video_endpoint: 'openai_videos', usage_notes: '首帧比例选择自动。\n这段说明不能加入提示词或生成请求。',
      video_capabilities: {
        resolutions: ['480p', '720p', '1080p'], durations: Array.from({ length: 60 }, (_, index) => index + 1),
        aspect_ratios: ['1:1', '3:4', '4:3', '16:9', '9:16', '21:9', 'adaptive'], max_reference_images: 1,
        allow_custom_resolution: true, allow_custom_aspect_ratio: true,
        default_duration: 4, default_resolution: '720p', default_aspect_ratio: '16:9',
      },
    };
    cloudState.document!.nodes = [{ ...imageNode, type: 'video', title: '当前视频', metadata: { prompt: '海面日出', model: '7::video-test' } }];
    vi.mocked(listModels).mockResolvedValue([videoModel]);
    vi.mocked(createExecution).mockImplementation(async (canvasId, nodeId) => ({ id: 'video-new', canvas_id: canvasId, node_id: nodeId, kind: 'video', status: 'queued', task_id: 'upstream-video', assets: [], created_at: '2026-10-18T00:00:00Z', updated_at: '2026-10-18T00:01:00Z' }));
    await mount();
    await click(button('打开生成设置'));
    expect(document.querySelector<HTMLInputElement>('input[aria-label="视频时长秒数"]')?.value).toBe('4');
    const pressed = [...document.querySelectorAll<HTMLButtonElement>('.generation-settings-popover button[aria-pressed="true"]')].map(item => item.textContent);
    expect(pressed).toContain('720p');
    expect(pressed).toContain('16:9');
    if (custom) {
      // 真正经过输入、失焦提交和 Editor 参数构造，避免只验证 metadata 的静态值。
      for (const [label, value] of [['自定义视频分辨率', '768'], ['视频时长秒数', '60']]) {
        const input = document.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!;
        await act(async () => {
          input.focus();
          Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value);
          input.dispatchEvent(new Event('input', { bubbles: true }));
        });
        await act(async () => input.blur());
      }
      expect(document.querySelector('[aria-label="自定义视频比例"]')).toBeNull();
      await click([...document.querySelectorAll<HTMLButtonElement>('.generation-settings-popover button')].find(item => item.textContent === '4:3')!);
      expect(currentNode().metadata).toMatchObject({ resolution: '768p', aspect_ratio: '4:3', seconds: '60' });
    }
    await click(button('关闭设置'));
    const generate = button('生成视频');
    await act(async () => { generate.click(); generate.click(); });
    expect(createExecution).toHaveBeenCalledTimes(1);
    const [canvasId, nodeId, kind, params, key] = vi.mocked(createExecution).mock.calls[0];
    expect([canvasId, nodeId, kind]).toEqual(['canvas-test', imageNode.id, 'video']);
    expect(params).toEqual({ group_id: 7, model: 'video-test', operation: 'generate', prompt: '海面日出', references: [], endpoint: 'openai_videos', duration: custom ? 60 : 4, resolution: custom ? '768p' : '720p', aspect_ratio: custom ? '4:3' : '16:9' });
    expect(saved.some(document => document.nodes[0].metadata?.generation_request?.key === key && document.nodes[0].metadata?.generation_request?.params.resolution === params.resolution)).toBe(true);
    expect(currentNode().metadata).toMatchObject({ execution_id: 'video-new', status: 'loading' });
    expect(cloudState.document?.nodes).toHaveLength(1);
    expect(createImageBatch).not.toHaveBeenCalled();
  });

  it.each([0, 1])('Grok 视频按目录默认参数提交并通过私有素材播放结果（参考图=%s）', async referenceCount => {
    // Grok 沿用统一视频表单、提交与归档结果；不能要求独立 Video 平台或把视频降为图片任务。
    const videoModel: Model = {
      ...model, platform: 'grok', group_name: 'Grok', model: 'grok-imagine-video', media_type: 'video',
      operations: ['generate'], video_endpoint: 'grok', video_endpoints: ['grok'],
      video_capabilities: {
        resolutions: ['480p', '720p', '1080p'], durations: Array.from({ length: 15 }, (_, index) => index + 1),
        aspect_ratios: ['16:9', '9:16', 'adaptive'], max_reference_images: 1, allow_custom_resolution: false,
        default_duration: 5, default_resolution: '720p', default_aspect_ratio: '16:9',
      },
    };
    cloudState.document!.nodes = [{ ...structuredClone(imageNode), type: 'video', title: 'Grok 视频', metadata: { prompt: '海面日出', model: '7::grok-imagine-video' } }];
    if (referenceCount) {
      cloudState.document!.nodes.push({ ...structuredClone(imageNode), id: 'reference-image', metadata: { asset_id: 'private-reference', expires_at: '2099-10-25T00:00:00Z' } });
      cloudState.document!.connections = [{ id: 'reference-edge', fromNodeId: 'reference-image', toNodeId: imageNode.id }];
    }
    vi.mocked(listModels).mockResolvedValue([videoModel]);
    const execution: Execution = { id: 'grok-video-execution', canvas_id: 'canvas-test', node_id: imageNode.id, kind: 'video', status: 'queued', task_id: 'grok-upstream-task', assets: [], created_at: '2026-10-18T00:00:00Z', updated_at: '2026-10-18T00:01:00Z' };
    vi.mocked(createExecution).mockResolvedValue(execution);
    await mount();
    expect(button('打开生成设置').textContent).toContain('720p · 16:9 · 5s');
    await click(button('打开生成设置'));
    expect(document.querySelector<HTMLInputElement>('[aria-label="自定义视频分辨率"]')?.disabled).toBe(true);
    expect(document.querySelector<HTMLInputElement>('[aria-label="视频时长秒数"]')?.max).toBe('15');
    await click(button('关闭设置'));
    await click(button('生成视频'));
    expect(createExecution).toHaveBeenCalledTimes(1);
    expect(vi.mocked(createExecution).mock.calls[0][3]).toEqual({
      group_id: 7, model: 'grok-imagine-video', operation: 'generate', prompt: '海面日出',
      endpoint: 'grok', duration: 5, resolution: '720p', aspect_ratio: '16:9',
      references: referenceCount ? [{ asset_id: 'private-reference', role: 'first_frame' }] : [],
    });
    vi.mocked(listExecutions).mockResolvedValue({ items: [{ ...execution, status: 'succeeded', assets: [{ ...asset('grok-video-result'), media_type: 'video', mime_type: 'video/mp4' }] }], total: 1, page: 1, page_size: 100 });
    await act(async () => poll!());
    expect(currentNode().metadata).toMatchObject({ status: 'success', asset_id: 'grok-video-result', execution_id: execution.id });
    expect(host.querySelector('video')?.getAttribute('src')).toBe('blob:private-grok-video-result');
    expect(assetURL).toHaveBeenCalledWith('grok-video-result');
    expect(createExecution).toHaveBeenCalledTimes(1);
    expect(createImageBatch).not.toHaveBeenCalled();
  });

  it('Grok 视频超过服务端单张参考图限制时不提交生成', async () => {
    // 参考图数量来自候选能力，避免旧节点保留多图连线时越过 Grok 的输入合同。
    vi.mocked(listModels).mockResolvedValue([{
      ...model, platform: 'grok', model: 'grok-imagine-video', media_type: 'video', operations: ['generate'], video_endpoint: 'grok',
      video_capabilities: { durations: [5], resolutions: ['720p'], aspect_ratios: ['16:9'], max_reference_images: 1 },
    }]);
    cloudState.document!.nodes = [
      { ...structuredClone(imageNode), type: 'video', metadata: { prompt: '海面日出', model: '7::grok-imagine-video' } },
      ...[1, 2].map(index => ({ ...structuredClone(imageNode), id: `reference-${index}`, metadata: { asset_id: `private-reference-${index}` } })),
    ];
    cloudState.document!.connections = [1, 2].map(index => ({ id: `edge-${index}`, fromNodeId: `reference-${index}`, toNodeId: imageNode.id }));
    await mount();
    await click(button('生成视频'));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain('当前模型最多支持 1 张参考图');
    expect(createExecution).not.toHaveBeenCalled();
    expect(createImageBatch).not.toHaveBeenCalled();
    expect(cloudState.flush).not.toHaveBeenCalled();
  });

  it('选择多张后以当前节点提交一个批次，先保存意图，双击不会新增结果节点或重复提交', async () => {
    let finish: ((value: ExecutionBatch) => void) | undefined;
    vi.mocked(createImageBatch).mockImplementation((_canvasId, _nodeId, _params, _count, _key) => new Promise(resolve => { finish = resolve; }));
    await mount();
    await chooseCount(3);
    const generate = button('生成图片');
    await act(async () => { generate.click(); generate.click(); });
    expect(createImageBatch).toHaveBeenCalledTimes(1);
    const [canvasId, nodeId, params, count, key] = vi.mocked(createImageBatch).mock.calls[0];
    expect([canvasId, nodeId, count]).toEqual(['canvas-test', 'image-current', 3]);
    expect(params).toMatchObject({ prompt: '一只红色小鸟', image_size: '1K', operation: 'generate', references: [] });
    expect(saved.some(document => document.nodes[0].metadata?.generation_request?.key === key)).toBe(true);
    expect(saved.at(-1)?.nodes[0].metadata?.generation_request?.count).toBe(3);
    expect(cloudState.document?.nodes.map(node => node.id)).toEqual(['image-current']);
    expect(createExecution).not.toHaveBeenCalled();
    const loading = host.querySelector('[role="status"][aria-label="图片生成中"]');
    expect(loading?.parentElement?.getAttribute('data-node-id')).toBe('image-current');
    expect(loading?.textContent).toBe('生成中');
    expect(host.querySelector('.node-heading .animate-spin')).toBeNull();
    await act(async () => finish!(batch(key, count, 'succeeded')));
    expect(currentNode().metadata).toMatchObject({ asset_ids: ['result-0', 'result-1', 'result-2'], status: 'success' });
    expect(host.querySelectorAll('[data-node-id]')).toHaveLength(1);
    expect(button('展开 3 张图片')).not.toBeNull();
    expect(host.querySelector('.canvas-generation-loading')).toBeNull();
  });

  it('未持久化成功时不请求生成，保留原意图供保存恢复后核对', async () => {
    cloudState.flush.mockRejectedValue(new Error('保存连接中断'));
    await mount();
    await click(button('生成图片'));
    expect(createImageBatch).not.toHaveBeenCalled();
    expect(createExecution).not.toHaveBeenCalled();
    expect(host.querySelector('[role="alert"]')?.textContent).toContain('保存连接中断');
    expect(currentNode().metadata?.generation_request?.params.prompt).toBe('一只红色小鸟');
    expect(currentNode().metadata?.status).toBe('error');
    expect(cloudState.document?.nodes).toHaveLength(1);
  });

  it('确定的余额不足保留准确错误，释放前端待核对状态且不伪装参考图错误', async () => {
    vi.mocked(createImageBatch).mockRejectedValue(new ApiError(403, '余额不足，请充值后重试', 'INSUFFICIENT_BALANCE'));
    await mount();
    await click(button('生成图片'));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain('余额不足');
    expect(host.querySelector('.node-error')?.textContent).toBe('余额不足，请充值后重试');
    expect(currentNode().metadata?.generation_request).toBeUndefined();
    expect(currentNode().metadata?.generation_key).toBeUndefined();
    expect(currentNode().metadata?.status).toBe('error');
    expect(button('生成图片').disabled).toBe(false);
    expect(createImageBatch).toHaveBeenCalledTimes(1);
    expect(host.textContent).not.toContain('生成参数或权限校验失败');
    expect(host.querySelector('.canvas-generation-loading')).toBeNull();
  });

  it('重新生成图片时保留原图，排队期间在原节点中心覆盖加载提示', async () => {
    cloudState.document!.nodes[0].metadata = { ...imageNode.metadata, asset_id: 'previous-image', asset_ids: ['previous-image'], status: 'success' };
    await mount();
    expect(host.querySelector('.canvas-generation-loading')).toBeNull();
    await click(button('生成图片'));
    expect(currentNode().metadata?.status).toBe('loading');
    expect(host.querySelector('.image-results-primary img')?.getAttribute('src')).toBe('blob:private-previous-image');
    const loading = host.querySelector('.canvas-generation-loading.has-media');
    expect(loading?.getAttribute('aria-label')).toBe('图片生成中');
    expect(loading?.querySelector('button')).toBeNull();
    expect(host.querySelector('[data-node-id="image-current"] .handle.source')).not.toBeNull();
    expect(createImageBatch).toHaveBeenCalledTimes(1);
  });

  it.each(['image','video'] as const)('%s 容量准入拒绝显示等待提示，清除未受理意图后允许修改并新建请求',async kind=>{
    const capacityMessage='同时进行的生成任务已达上限，请等待现有任务完成后再试';
    const submit=kind==='image'?vi.mocked(createImageBatch):vi.mocked(createExecution);
    if(kind==='video'){
      currentNode().type='video';currentNode().metadata={prompt:'原视频提示词',model:'7::video-test'};
      vi.mocked(listModels).mockResolvedValue([{...model,model:'video-test',media_type:'video',video_endpoint:'openai_videos',video_capabilities:{resolutions:['720p'],durations:[5],aspect_ratios:['16:9'],max_reference_images:1}}]);
      vi.mocked(createExecution).mockImplementation(async(canvasId,nodeId,mediaKind)=>({id:'video-accepted',canvas_id:canvasId,node_id:nodeId,kind:mediaKind,status:'queued',task_id:'video-task',assets:[],created_at:'2026-10-18T00:00:00Z',updated_at:'2026-10-18T00:01:00Z'}));
    }
    submit.mockRejectedValueOnce(new ApiError(429,capacityMessage,'CANVAS_EXECUTION_LIMIT'));
    await mount();await click(button(kind==='image'?'生成图片':'生成视频'));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain(capacityMessage);expect(host.querySelector('.node-error')?.textContent).toBe(capacityMessage);
    expect(currentNode().metadata).toMatchObject({status:'error',generation_key:undefined,generation_request:undefined});
    expect(button('核对提交')).toBeNull();expect(submit).toHaveBeenCalledTimes(1);
    const firstKey=submit.mock.calls[0].at(-1);
    await writePrompt('修改后的新提示词');await click(button(kind==='image'?'生成图片':'生成视频'));
    expect(submit).toHaveBeenCalledTimes(2);expect(submit.mock.calls[1].at(-1)).not.toBe(firstKey);
    const sent=kind==='image'?vi.mocked(createImageBatch).mock.calls[1][2]:vi.mocked(createExecution).mock.calls[1][3];
    expect(sent.prompt).toBe('修改后的新提示词');expect(currentNode().metadata?.status).toBe('loading');
  });

  it.each(['image','video'] as const)('%s 普通 429 不当作容量未受理，仍保留原键与原参数核对',async kind=>{
    const submit=kind==='image'?vi.mocked(createImageBatch):vi.mocked(createExecution);
    if(kind==='video'){
      currentNode().type='video';currentNode().metadata={prompt:'原视频提示词',model:'7::video-test'};
      vi.mocked(listModels).mockResolvedValue([{...model,model:'video-test',media_type:'video',video_endpoint:'openai_videos',video_capabilities:{resolutions:['720p'],durations:[5],aspect_ratios:['16:9'],max_reference_images:1}}]);
    }
    submit.mockRejectedValue(new ApiError(429,'上游请求过多','UPSTREAM_RATE_LIMIT'));
    await mount();await click(button(kind==='image'?'生成图片':'生成视频'));
    const frozen=structuredClone(currentNode().metadata?.generation_request);
    expect(frozen).toBeDefined();expect(button('核对提交').disabled).toBe(false);
    await writePrompt('不应进入核对请求的新提示词');await click(button('核对提交'));
    expect(submit).toHaveBeenCalledTimes(2);expect(submit.mock.calls[1]).toEqual(submit.mock.calls[0]);
    expect(currentNode().metadata?.generation_request).toEqual(frozen);
  });

  it.each(['succeeded', 'failed', 'result_lost'] as const)('视频排队、运行恢复始终中心显示，终态 %s 停止动画且保留媒体', async finalStatus => {
    const video: CanvasNodeData = { ...imageNode, type: 'video', title: '当前视频', metadata: { prompt: '视频提示词', model: '7::video-test', asset_id: 'previous-video', execution_id: 'video-execution', status: 'loading' } };
    cloudState.document!.nodes = [video];
    vi.mocked(listModels).mockResolvedValue([{ ...model, model: 'video-test', media_type: 'video', video_endpoint: 'openai_videos', video_capabilities: { resolutions: ['720p'], durations: [5], aspect_ratios: ['16:9'], max_reference_images: 1 } }]);
    const execution: Execution = { id: 'video-execution', canvas_id: 'canvas-test', node_id: imageNode.id, kind: 'video', status: 'queued', task_id: 'upstream-video', assets: [], created_at: '2026-10-18T00:00:00Z', updated_at: '2026-10-18T00:01:00Z' };
    vi.mocked(listExecutions).mockResolvedValue({ items: [execution], total: 1, page: 1, page_size: 100 });
    await mount();
    expect(host.querySelector('[role="status"][aria-label="视频生成中"]')?.parentElement?.getAttribute('data-node-id')).toBe(imageNode.id);
    expect(host.querySelector('video')?.getAttribute('src')).toBe('blob:private-previous-video');
    vi.mocked(listExecutions).mockResolvedValue({ items: [{ ...execution, status: 'running', updated_at: '2026-10-18T00:02:00Z' }], total: 1, page: 1, page_size: 100 });
    await act(async () => poll!());
    expect(host.querySelector('.canvas-generation-loading.has-media')).not.toBeNull();
    vi.mocked(listExecutions).mockResolvedValue({ items: [{ ...execution, status: finalStatus, assets: finalStatus === 'succeeded' ? [{ ...asset('completed-video'), media_type: 'video', mime_type: 'video/mp4' }] : [], error_message: finalStatus === 'succeeded' ? undefined : '视频生成失败', updated_at: '2026-10-18T00:03:00Z' }], total: 1, page: 1, page_size: 100 });
    await act(async () => poll!());
    expect(host.querySelector('.canvas-generation-loading')).toBeNull();
    expect(host.querySelector('video')?.getAttribute('src')).toBe(finalStatus === 'succeeded' ? 'blob:private-completed-video' : 'blob:private-previous-video');
    expect(createExecution).not.toHaveBeenCalled();
    expect(createImageBatch).not.toHaveBeenCalled();
  });

  it.each(['running', 'succeeded', 'failed'] as const)('视频核对阶段停止普通旋转、禁止重复生成，原任务恢复至 %s 时自动更新', async nextStatus => {
    cloudState.document!.nodes = [{ ...imageNode, type: 'video', title: '当前视频', metadata: { prompt: '海面日出', model: '7::video-test', asset_id: 'previous-video', execution_id: 'video-execution', status: 'loading' } }];
    vi.mocked(listModels).mockResolvedValue([{ ...model, model: 'video-test', media_type: 'video', video_endpoint: 'openai_videos', video_capabilities: { resolutions: ['720p'], durations: [5], aspect_ratios: ['16:9'], max_reference_images: 1 } }]);
    const message = '上游受理结果待核对，请勿重复生成；预扣尚未结算';
    const execution: Execution = { id: 'video-execution', canvas_id: 'canvas-test', node_id: imageNode.id, kind: 'video', status: 'running', task_id: 'vid-same', error_code: 'CREATIVE_VIDEO_SUBMISSION_UNCONFIRMED', error_message: message, assets: [], created_at: '2026-10-18T00:00:00Z', updated_at: '2026-10-18T00:01:00Z' };
    vi.mocked(listExecutions).mockResolvedValue({ items: [execution], total: 1, page: 1, page_size: 100 });
    await mount();
    const indicator = host.querySelector('[role="status"][aria-label="视频任务核对中"]');
    expect(indicator?.textContent).toContain(message);
    expect(indicator?.querySelector('.animate-spin')).toBeNull();
    expect(host.querySelector('.node-error')).toBeNull();
    expect(host.querySelector('video')?.getAttribute('src')).toBe('blob:private-previous-video');
    expect(button('任务核对中…').disabled).toBe(true);
    await click(button('任务核对中…'));
    expect(currentNode().metadata).toMatchObject({ execution_id: 'video-execution', status: 'loading', generation_phase: 'reconciliation' });
    vi.mocked(listExecutions).mockResolvedValue({ items: [{ ...execution, status: nextStatus, error_code: nextStatus === 'failed' ? 'CREATIVE_VIDEO_FAILED' : undefined, error_message: nextStatus === 'failed' ? '视频任务未完成，预扣已释放' : undefined, assets: nextStatus === 'succeeded' ? [{ ...asset('completed-video'), media_type: 'video', mime_type: 'video/mp4' }] : [] }], total: 1, page: 1, page_size: 100 });
    await act(async () => poll!());
    expect(currentNode().metadata?.generation_phase).toBeUndefined();
    expect(currentNode().metadata?.execution_id).toBe('video-execution');
    expect(host.querySelector('[aria-label="视频任务核对中"]')).toBeNull();
    if (nextStatus === 'running') expect(host.querySelector('[aria-label="视频生成中"] .animate-spin')).not.toBeNull();
    else expect(host.querySelector('.canvas-generation-loading')).toBeNull();
    if (nextStatus === 'failed') expect(host.querySelector('.node-error')?.textContent).toContain('预扣已释放');
    if (nextStatus === 'succeeded') expect(host.querySelector('video')?.getAttribute('src')).toBe('blob:private-completed-video');
    expect(createExecution).not.toHaveBeenCalled();
    expect(createImageBatch).not.toHaveBeenCalled();
  });

  it('图片上游受理不明时显示核对状态并禁止重复生成', async () => {
    cloudState.document!.nodes = [{ ...imageNode, metadata: { ...imageNode.metadata, execution_id: 'image-execution', status: 'loading' } }];
    const message = '图片请求结果正在核对，请勿重复生成；费用以最终结算为准';
    const execution: Execution = { id: 'image-execution', canvas_id: 'canvas-test', node_id: imageNode.id, kind: 'image', status: 'running', task_id: 'image-task', error_code: 'CREATIVE_IMAGE_SUBMISSION_UNCONFIRMED', error_message: message, assets: [], created_at: '2026-10-18T00:00:00Z', updated_at: '2026-10-18T00:01:00Z' };
    vi.mocked(listExecutions).mockResolvedValue({ items: [execution], total: 1, page: 1, page_size: 100 });
    await mount();
    const indicator = host.querySelector('[role="status"][aria-label="图片任务核对中"]');
    expect(indicator?.textContent).toContain(message);
    expect(indicator?.querySelector('.animate-spin')).toBeNull();
    expect(button('任务核对中…').disabled).toBe(true);
    await click(button('任务核对详情'));
    const dialog = document.querySelector('[role="dialog"][aria-label="任务核对详情"]');
    expect(dialog?.textContent).toContain(message);
    expect(dialog?.textContent).toContain('以服务端最终结算为准');
    expect(dialog?.textContent).not.toContain('预扣已释放');
    expect(currentNode().metadata?.status).toBe('loading');
    await click(button('关闭任务详情'));
    expect(createImageBatch).not.toHaveBeenCalled();
  });

  it.each(['running','succeeded','failed'] as const)('图片批次核对中保留已完成图片且禁止重复提交，恢复为 %s 后同步展示', async nextStatus => {
    currentNode().metadata = { ...currentNode().metadata, generation_key: 'image-reconcile', status: 'loading' };
    const entries = batch('image-reconcile', 2, 'succeeded').executions;
    entries[1] = { ...entries[1], status: 'running', assets: [], error_code: 'CREATIVE_IMAGE_SUBMISSION_UNCONFIRMED', error_message: '上游受理结果正在核对，请勿重复生成' };
    vi.mocked(listExecutions).mockResolvedValue({ items: entries, total: 2, page: 1, page_size: 100 });
    await mount();
    expect(host.querySelector('[role="status"][aria-label="图片任务核对中"]')?.textContent).toContain('上游受理结果正在核对');
    expect(currentNode().metadata?.asset_ids).toEqual(['result-0']);
    expect(button('任务核对中…').disabled).toBe(true);
    await click(button('任务核对中…'));
    expect(createImageBatch).not.toHaveBeenCalled();
    entries[1] = { ...entries[1], status: nextStatus, assets: nextStatus === 'succeeded' ? [asset('result-1')] : [], error_code: undefined, error_message: nextStatus === 'failed' ? '预扣已释放' : undefined, updated_at: '2026-10-18T00:02:00Z' };
    await act(async () => poll?.());
    expect(currentNode().metadata?.generation_phase).toBeUndefined();
    expect(host.querySelector('[aria-label="图片任务核对中"]')).toBeNull();
    expect(currentNode().metadata?.status).toBe(nextStatus === 'running' ? 'loading' : nextStatus === 'succeeded' ? 'success' : 'error');
    expect(createImageBatch).not.toHaveBeenCalled();
  });

  it('批次返回部分成功和资金拒绝时，同节点保留成功图片并显示准确失败原因', async () => {
    vi.mocked(createImageBatch).mockImplementation(async (_canvasId, _nodeId, _params, count, key) => {
      const result = batch(key, count, 'succeeded');
      result.executions[1] = { ...result.executions[1], status: 'failed', assets: [], error_code: 'INSUFFICIENT_BALANCE', error_message: '余额不足，请充值后重试' };
      return result;
    });
    await mount();
    await chooseCount(3);
    await click(button('生成图片'));
    expect(currentNode().metadata).toMatchObject({ status: 'error', asset_ids: ['result-0', 'result-2'] });
    expect(host.querySelector('.node-error')?.textContent).toBe('已完成 2 张，部分任务失败：余额不足，请充值后重试');
    expect(button('展开 2 张图片')).not.toBeNull();
    expect(currentNode().metadata?.generation_request).toBeUndefined();
    expect(cloudState.document?.nodes).toHaveLength(1);
    expect(createImageBatch).toHaveBeenCalledTimes(1);
  });

  it('响应丢失后的核对重放原键、原参数和原张数，不使用用户随后编辑的值', async () => {
    vi.mocked(createImageBatch).mockRejectedValueOnce(new TypeError('Failed to fetch'));
    await mount();
    await chooseCount(2);
    await click(button('生成图片'));
    const first = structuredClone(vi.mocked(createImageBatch).mock.calls[0]);
    expect(currentNode().metadata?.generation_request?.count).toBe(2);
    await click(button('任务核对详情'));
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('提交结果待确认');
    expect(document.querySelector('[role="dialog"]')?.textContent).not.toContain('预扣已释放');
    await click(button('关闭任务详情'));
    expect(createImageBatch).toHaveBeenCalledTimes(1);
    await writePrompt('用户后来修改的提示词');
    await chooseCount(4);
    expect(currentNode().metadata).toMatchObject({ count: 4, prompt: '用户后来修改的提示词' });
    await click(button('核对提交'));
    expect(createImageBatch).toHaveBeenCalledTimes(2);
    expect(vi.mocked(createImageBatch).mock.calls[1]).toEqual(first);
    expect(currentNode().metadata?.generation_request).toBeUndefined();
    expect(currentNode().metadata?.execution_ids).toEqual(['execution-0', 'execution-1']);
    expect(cloudState.document?.nodes).toHaveLength(1);
  });

  it('撤销参数编辑不会擦掉已提交任务，处理中不能误建第二个收费批次', async () => {
    await mount();
    await chooseCount(2);
    await click(button('生成图片'));
    const key = vi.mocked(createImageBatch).mock.calls[0][4];
    expect(currentNode().metadata?.status).toBe('loading');
    await click(host.querySelector<HTMLButtonElement>('[title="撤销"]')!);
    expect(currentNode().metadata).toMatchObject({ generation_key: key, execution_ids: ['execution-0', 'execution-1'], status: 'loading' });
    const generate = host.querySelector<HTMLButtonElement>('.generate-icon')!;
    expect(generate.disabled).toBe(true);
    await click(generate);
    expect(createImageBatch).toHaveBeenCalledTimes(1);
    expect(cloudState.document?.nodes).toHaveLength(1);
  });

  it('刷新后的轮询按持久批次键归档当前节点，其他画布及旧批次不覆盖，也不重发请求', async () => {
    const request = { key: 'persisted-batch', kind: 'image' as const, params: { group_id: 7, model: 'gpt-image-2', operation: 'generate', prompt: '原提示词' }, count: 2 };
    cloudState.document!.nodes[0].metadata = { ...imageNode.metadata, generation_key: request.key, generation_request: request, status: 'loading' };
    await mount();
    const successful = batch(request.key, 2, 'succeeded').executions;
    const foreign = { ...successful[0], id: 'foreign', canvas_id: 'other-canvas', assets: [asset('forbidden')] };
    const stale = { ...successful[0], id: 'stale', batch_id: 'old-batch', assets: [asset('old-result')] };
    vi.mocked(listExecutions).mockResolvedValue({ items: [...successful, foreign, stale], total: 4, page: 1, page_size: 100 });
    await act(async () => poll!());
    expect(currentNode().metadata).toMatchObject({ asset_ids: ['result-0', 'result-1'], status: 'success', execution_ids: ['execution-0', 'execution-1'] });
    expect(currentNode().metadata?.generation_request).toBeUndefined();
    expect(cloudState.document?.nodes.map(node => node.id)).toEqual(['image-current']);
    expect(createImageBatch).not.toHaveBeenCalled();
    expect(createExecution).not.toHaveBeenCalled();
  });

  it('参考上传连接当前生成节点，生成使用该素材且不会把旧产物自动当参考图', async () => {
    cloudState.document!.nodes[0].metadata = { ...imageNode.metadata, asset_id: 'old-output', asset_ids: ['old-output'], status: 'success' };
    vi.mocked(uploadAsset).mockResolvedValue(asset('uploaded-reference'));
    await mount();
    await click(host.querySelector<HTMLButtonElement>('[title="添加参考素材"]')!);
    const file = new File(['picture'], 'reference.png', { type: 'image/png' });
    const input = host.querySelector<HTMLInputElement>('input[type="file"]')!;
    Object.defineProperty(input, 'files', { configurable: true, value: [file] });
    await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })));
    expect(uploadAsset).toHaveBeenCalledExactlyOnceWith(file, 'canvas-test', 'reference.png');
    const reference = cloudState.document!.nodes.find(node => node.metadata?.asset_id === 'uploaded-reference')!;
    expect(reference.metadata?.bytes).toBe(123);
    expect(cloudState.document?.connections).toEqual([{ id: expect.any(String), fromNodeId: reference.id, toNodeId: 'image-current' }]);
    expect(host.querySelector('.node-panel')?.closest('[data-node-id]')?.getAttribute('data-node-id')).toBe('image-current');
    await click(button('生成图片'));
    expect(vi.mocked(createImageBatch).mock.calls[0][2]).toMatchObject({ operation: 'edit', references: [{ asset_id: 'uploaded-reference' }] });
    expect(cloudState.document?.nodes).toHaveLength(2);
  });

  it('参考素材批量上传先整体校验，混入非图片时不留下半批素材', async () => {
    vi.mocked(uploadAsset).mockResolvedValue(asset('should-not-upload'));
    await mount();
    await click(host.querySelector<HTMLButtonElement>('[title="添加参考素材"]')!);
    const input = host.querySelector<HTMLInputElement>('input[type="file"]')!;
    Object.defineProperty(input, 'files', { configurable: true, value: [
      new File(['picture'], 'reference.png', { type: 'image/png' }),
      new File(['movie'], 'reference.mp4', { type: 'video/mp4' }),
    ] });
    await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })));
    expect(uploadAsset).not.toHaveBeenCalled();
    expect(host.textContent).toContain('参考素材请选择图片');
    expect(cloudState.document?.nodes).toHaveLength(1);
    expect(cloudState.document?.connections).toEqual([]);
  });

  it('上传参考图期间删除目标节点，回包不创建悬空连线或影响付费任务', async () => {
    let finishUpload: ((value: Asset) => void) | undefined;
    vi.mocked(uploadAsset).mockImplementation(() => new Promise(resolve => { finishUpload = resolve; }));
    await mount();
    await click(host.querySelector<HTMLButtonElement>('[title="添加参考素材"]')!);
    const input = host.querySelector<HTMLInputElement>('input[type="file"]')!;
    Object.defineProperty(input, 'files', { configurable: true, value: [new File(['picture'], 'reference.png', { type: 'image/png' })] });
    await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })));
    expect(uploadAsset).toHaveBeenCalledTimes(1);
    await click(host.querySelector<HTMLButtonElement>('[title="删除节点"]')!);
    expect(cloudState.document?.nodes).toHaveLength(0);
    await act(async () => finishUpload!(asset('late-reference')));
    expect(cloudState.document).toMatchObject({ nodes: [], connections: [] });
    expect(createImageBatch).not.toHaveBeenCalled();
    expect(createExecution).not.toHaveBeenCalled();
  });

  it.each(['picker','drop'] as const)('通过 %s 上传混合非法文件时整批拒绝，不产生半批素材或节点', async path => {
    await mount();
    const files=[new File(['picture'],'valid.png',{type:'image/png'}),new File(['script'],'invalid.svg',{type:'image/svg+xml'})];
    if(path==='picker'){
      await click(host.querySelector<HTMLButtonElement>('[title="上传素材"]')!);
      const input=host.querySelector<HTMLInputElement>('input[type=file]')!;Object.defineProperty(input,'files',{configurable:true,value:files});
      await act(async()=>input.dispatchEvent(new Event('change',{bubbles:true})));
    }else{
      const event=new Event('drop',{bubbles:true,cancelable:true});Object.defineProperty(event,'dataTransfer',{value:{files}});
      await act(async()=>host.querySelector('[data-testid=canvas-drop]')!.dispatchEvent(event));
    }
    expect(host.textContent).toContain('素材格式不支持');expect(uploadAsset).not.toHaveBeenCalled();expect(cloudState.document?.nodes).toHaveLength(1);
  });

  it('替换素材不能抹掉响应未知的提交意图和防重键',async()=>{
    const request={key:'unconfirmed-image',kind:'image' as const,count:1,params:{group_id:7,model:'gpt-image-2',operation:'generate',prompt:'原始请求'}};
    currentNode().metadata={...currentNode().metadata,status:'error',generation_key:request.key,generation_request:request};
    await mount();await click(host.querySelector<HTMLButtonElement>('[title="上传图片"]')!);
    const input=host.querySelector<HTMLInputElement>('input[type=file]')!;Object.defineProperty(input,'files',{configurable:true,value:[new File(['picture'],'replace.png',{type:'image/png'})]});
    await act(async()=>input.dispatchEvent(new Event('change',{bubbles:true})));
    expect(uploadAsset).not.toHaveBeenCalled();expect(currentNode().metadata?.generation_request).toEqual(request);expect(currentNode().metadata?.generation_key).toBe(request.key);
    expect(host.textContent).toContain('提交待核对');expect(createImageBatch).not.toHaveBeenCalled();
  });

  it('上传替换回包迟到时不能覆盖恢复中的新任务或新结果',async()=>{
    let finishUpload:((asset:Asset)=>void)|undefined;
    vi.mocked(uploadAsset).mockImplementation(()=>new Promise(resolve=>{finishUpload=resolve;}));
    await mount();await click(host.querySelector<HTMLButtonElement>('[title="上传图片"]')!);
    const input=host.querySelector<HTMLInputElement>('input[type=file]')!;Object.defineProperty(input,'files',{configurable:true,value:[new File(['picture'],'replace.png',{type:'image/png'})]});
    await act(async()=>input.dispatchEvent(new Event('change',{bubbles:true})));
    await act(async()=>{currentNode().metadata={...currentNode().metadata,status:'success',generation_key:'new-batch',execution_id:'new-execution',asset_id:'new-result'};cloudState.listeners.forEach(notify=>notify());});
    await act(async()=>finishUpload!(asset('late-replace')));
    expect(currentNode().metadata).toMatchObject({generation_key:'new-batch',execution_id:'new-execution',asset_id:'new-result'});
    expect(host.textContent).toContain('节点任务或素材已更新');expect(createImageBatch).not.toHaveBeenCalled();
  });

  it('连接空图片不会悄悄变成不带参考图的收费请求',async()=>{
    cloudState.document!.nodes.push({...imageNode,id:'empty-ref',metadata:{}});cloudState.document!.connections=[{id:'empty-edge',fromNodeId:'empty-ref',toNodeId:imageNode.id}];
    await mount();await click(button('生成图片'));
    expect(host.textContent).toContain('引用素材尚未生成');expect(createImageBatch).not.toHaveBeenCalled();expect(cloudState.flush).not.toHaveBeenCalled();
  });

  it('旧主图查询延迟返回不能覆盖新批次的主图或导致下载和参考图错配', async () => {
    let finishMetadata: ((value: Asset) => void) | undefined;
    cloudState.document!.nodes[0].metadata = { ...imageNode.metadata, asset_id: 'previous-a', asset_ids: ['previous-a', 'previous-b'], status: 'success' };
    vi.mocked(assetMetadata).mockImplementation(() => new Promise(resolve => { finishMetadata = resolve; }));
    vi.mocked(createImageBatch).mockImplementation(async (_canvasId, _nodeId, _params, count, key) => batch(key, count, 'succeeded'));
    await mount();
    await click(button('展开 2 张图片'));
    await click(button('将第 2 张图片设为主图'));
    expect(assetMetadata).toHaveBeenCalledExactlyOnceWith('previous-b');
    await click(button('生成图片'));
    expect(currentNode().metadata).toMatchObject({ asset_id: 'result-0', asset_ids: ['result-0'], status: 'success' });
    await act(async () => finishMetadata!(asset('previous-b')));
    expect(currentNode().metadata).toMatchObject({ asset_id: 'result-0', asset_ids: ['result-0'], status: 'success' });
    expect(host.querySelector('.image-results-primary img')?.getAttribute('src')).toBe('blob:private-result-0');
    expect(createImageBatch).toHaveBeenCalledTimes(1);
  });
});

describe('视频参考图预览与生成状态隔离', () => {
  const videoModel: Model = { ...model, model: 'MiniMax-H3', media_type: 'video', video_endpoint: 'openai_videos', video_capabilities: { resolutions: ['768p'], durations: [5], aspect_ratios: ['16:9'], max_reference_images: 1 } };
  const prepare = () => {
    cloudState.document!.nodes = [
      { ...structuredClone(imageNode), type: 'video', title: '当前视频', metadata: { prompt: '海面日出', model: '7::MiniMax-H3', resolution: '768p', aspect_ratio: '16:9', seconds: '5' } },
      { ...structuredClone(imageNode), id: 'reference-image', title: '人物参考图', position: { x: -400, y: 100 }, metadata: { asset_id: 'private-reference', expires_at: '2099-10-25T00:00:00Z', status: 'success' } },
    ];
    cloudState.document!.connections = [{ id: 'reference-edge', fromNodeId: 'reference-image', toNodeId: imageNode.id }];
    vi.mocked(listModels).mockResolvedValue([videoModel]);
  };
  const previewButton = () => button('预览参考图 1：人物参考图');
  const dialog = () => document.querySelector<HTMLElement>('[role="dialog"][aria-label="参考图预览"]');
  const expectNoGenerationOrSave = () => {
    expect(createExecution).not.toHaveBeenCalled();
    expect(createImageBatch).not.toHaveBeenCalled();
    expect(uploadAsset).not.toHaveBeenCalled();
    expect(cloudState.flush).not.toHaveBeenCalled();
  };
  const openAndCheckReadOnly = async () => {
    const before = structuredClone(cloudState.document!);
    expect(assetBlob).not.toHaveBeenCalled();
    await click(previewButton());
    expect(assetBlob).toHaveBeenCalledExactlyOnceWith('private-reference');
    expect(dialog()?.querySelector('img')?.getAttribute('src')).toBe('blob:reference-preview');
    await act(async () => {
      for (const key of ['Delete', 'Backspace']) dialog()!.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
    });
    expect(cloudState.document).toEqual(before);
    expectNoGenerationOrSave();
    return before;
  };

  it('视频成功回填后参考图仍可点开和关闭，不删除节点、改布局或再次生成', async () => {
    prepare();
    currentNode().metadata = { ...currentNode().metadata, execution_id: 'completed-execution', status: 'loading' };
    const execution: Execution = { id: 'completed-execution', canvas_id: 'canvas-test', node_id: imageNode.id, kind: 'video', status: 'succeeded', task_id: 'upstream-completed', assets: [{ ...asset('completed-video'), media_type: 'video', mime_type: 'video/mp4' }], created_at: '2026-10-18T00:00:00Z', updated_at: '2026-10-18T00:01:00Z' };
    vi.mocked(listExecutions).mockResolvedValue({ items: [execution], total: 1, page: 1, page_size: 100 });
    await mount();
    expect(currentNode().metadata).toMatchObject({ status: 'success', asset_id: 'completed-video', execution_id: 'completed-execution' });
    const before = await openAndCheckReadOnly();
    await click(button('关闭参考预览'));
    expect(dialog()).toBeNull();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:reference-preview');
    expect(cloudState.document).toEqual(before);
    expectNoGenerationOrSave();
  });

  it('没有选择模型且提示词为空也可预览，Escape 关闭后保留当前选择和连线', async () => {
    prepare();
    currentNode().metadata = { prompt: '' };
    vi.mocked(listModels).mockResolvedValue([]);
    await mount();
    expect(button('生成视频').disabled).toBe(true);
    const before = await openAndCheckReadOnly();
    await act(async () => dialog()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })));
    expect(dialog()).toBeNull();
    expect(previewButton()).not.toBeNull();
    expect(host.querySelector('.node-panel')?.closest('[data-node-id]')?.getAttribute('data-node-id')).toBe(imageNode.id);
    expect(cloudState.document).toEqual(before);
    expectNoGenerationOrSave();
  });

  it('预览保留待核对的原始提交意图，读取轮询不自动重放模型请求', async () => {
    prepare();
    const request = { key: 'unconfirmed-video-key', kind: 'video' as const, count: 1, params: { group_id: 7, model: 'MiniMax-H3', operation: 'generate', prompt: '原始提示词', endpoint: 'openai_videos', resolution: '768p', aspect_ratio: '16:9', duration: 5, references: [{ asset_id: 'private-reference', role: 'first_frame' }] } };
    currentNode().metadata = { ...currentNode().metadata, generation_request: request, status: 'loading' };
    await mount();
    const before = await openAndCheckReadOnly();
    await act(async () => poll!());
    expect(currentNode().metadata?.generation_request).toEqual(request);
    expect(currentNode().metadata?.status).toBe('loading');
    await click(button('关闭参考预览'));
    expect(button('核对提交').disabled).toBe(false);
    expect(cloudState.document).toEqual(before);
    expectNoGenerationOrSave();
  });
});

describe('视频参考图大小提示与已有任务隔离', () => {
  const limit = 2 * 1024 * 1024;
  const videoModel: Model = { ...model, model: 'MiniMax-H3', media_type: 'video', video_endpoint: 'openai_videos', video_capabilities: { resolutions: ['768p'], durations: [5], aspect_ratios: ['16:9'], max_reference_images: 4, max_reference_image_bytes: limit } };
  const setReferences = (sizes: (number | undefined)[], grouped = false) => {
    const references: CanvasNodeData[] = sizes.map((bytes, index) => ({ ...structuredClone(imageNode), id: `reference-${index}`, title: `参考图片 ${index + 1}`, metadata: { asset_id: `asset-${index}`, bytes, status: 'success', ...(grouped ? { groupId: 'references-group' } : {}) } }));
    cloudState.document!.nodes = [{ ...structuredClone(imageNode), type: 'video', title: '当前视频', metadata: { prompt: '海面日出', model: '7::MiniMax-H3' } }, ...references];
    if (grouped) cloudState.document!.nodes.push({ ...structuredClone(imageNode), id: 'references-group', type: 'group', metadata: {} });
    cloudState.document!.connections = (grouped ? ['references-group'] : references.map(node => node.id)).map(id => ({ id: `edge-${id}`, fromNodeId: id, toNodeId: imageNode.id }));
    vi.mocked(listModels).mockResolvedValue([videoModel]);
    vi.mocked(createExecution).mockImplementation(async (canvasId, nodeId) => ({ id: 'video-new', canvas_id: canvasId, node_id: nodeId, kind: 'video', status: 'queued', task_id: 'upstream-video', assets: [], created_at: '2026-10-18T00:00:00Z', updated_at: '2026-10-18T00:01:00Z' }));
    return references;
  };

  it.each([false, true])('连线或分组参考图过大先提示并禁用生成，不新增任务或更改原图（分组=%s）', async grouped => {
    setReferences([123, 12_268_339], grouped);
    const original = structuredClone(cloudState.document!);
    await mount();
    expect(host.querySelector('.reference-size-warning')?.textContent).toContain('1 张（最大 11.7 MiB）');
    expect(host.querySelector('.reference-size-warning')?.textContent).toContain('本站当前模型的单图上限为 2 MiB');
    expect(button('生成视频').disabled).toBe(true);
    await click(button('生成视频'));
    expect(cloudState.document!.nodes).toEqual(original.nodes);
    expect(cloudState.document!.connections).toEqual(original.connections);
    expect(host.querySelector('.node-error')).toBeNull();
    expect(createExecution).not.toHaveBeenCalled(); expect(createImageBatch).not.toHaveBeenCalled();
    expect(uploadAsset).not.toHaveBeenCalled(); expect(cloudState.flush).not.toHaveBeenCalled();
  });

  it('素材目录补齐旧节点大小后也能提示，未发送的叠卡输出不误拦', async () => {
    setReferences([undefined]);
    cloudState.document!.nodes[1].metadata!.asset_ids = ['asset-0', 'not-selected-large'];
    vi.mocked(listAssets).mockResolvedValue({ items: [{ ...asset('asset-0'), byte_size: limit }, { ...asset('not-selected-large'), byte_size: limit * 10 }], total: 2, page: 1, page_size: 50 });
    await mount();
    expect(host.querySelector('.reference-size-warning')).toBeNull();
    expect(button('生成视频').disabled).toBe(false);
    vi.mocked(listAssets).mockResolvedValue({ items: [{ ...asset('asset-0'), byte_size: limit * 3 }], total: 1, page: 1, page_size: 50 });
    await act(async () => poll!());
    expect(host.querySelector('.reference-size-warning')?.textContent).toContain('最大 6 MiB');
    expect(button('生成视频').disabled).toBe(true);
    expect(createExecution).not.toHaveBeenCalled();
  });

  it.each(['删除', '替换'])('用户%s大图后自动解除限制，素材保留且只在主动点击时提交', async operation => {
    setReferences([limit * 3]);
    await mount();
    expect(button('生成视频').disabled).toBe(true);
    await click([...host.querySelectorAll<HTMLButtonElement>('.node-list-item')].find(item => item.textContent?.includes('参考图片 1'))!);
    if (operation === '删除') {
      await click(host.querySelector<HTMLButtonElement>('[title="删除节点"]')!);
    } else {
      vi.mocked(uploadAsset).mockResolvedValue({ ...asset('small-replacement'), byte_size: 123 });
      await click(host.querySelector<HTMLButtonElement>('[title="上传图片"]')!);
      const input = host.querySelector<HTMLInputElement>('input[type="file"]')!;
      Object.defineProperty(input, 'files', { configurable: true, value: [new File(['small'], 'small.png', { type: 'image/png' })] });
      await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })));
      expect(cloudState.document!.nodes[1].metadata).toMatchObject({ asset_id: 'small-replacement', bytes: 123 });
    }
    await click([...host.querySelectorAll<HTMLButtonElement>('.node-list-item')].find(item => item.textContent?.includes('当前视频'))!);
    expect(host.querySelector('.reference-size-warning')).toBeNull();
    expect(button('生成视频').disabled).toBe(false);
    expect(createExecution).not.toHaveBeenCalled();
    await click(button('生成视频'));
    expect(createExecution).toHaveBeenCalledTimes(1);
    expect(vi.mocked(createExecution).mock.calls[0][3].references).toEqual(operation === '删除' ? [] : [{ asset_id: 'small-replacement', role: 'first_frame' }]);
    expect(api).not.toHaveBeenCalledWith(expect.stringContaining('/assets/'), expect.objectContaining({ method: 'DELETE' }));
  });

  it('每张恰好达到上限时允许提交，多图总大小不当成单图大小', async () => {
    setReferences([limit, limit]);
    await mount();
    expect(button('生成视频').disabled).toBe(false);
    await click(button('生成视频'));
    expect(createExecution).toHaveBeenCalledTimes(1);
    expect(vi.mocked(createExecution).mock.calls[0][3].references).toHaveLength(2);
  });

  it('未知大小不猜测，保留服务端权威拒绝与准确提示', async () => {
    setReferences([undefined]);
    vi.mocked(createExecution).mockRejectedValue(new ApiError(413, '参考图超过本站单图 2 MiB 限制，请更换较小图片', 'CREATIVE_VIDEO_REFERENCE_TOO_LARGE'));
    await mount();
    expect(button('生成视频').disabled).toBe(false);
    await click(button('生成视频'));
    expect(createExecution).toHaveBeenCalledTimes(1);
    expect(host.querySelector('.node-error')?.textContent).toContain('请更换较小图片');
    expect(currentNode().metadata?.generation_request).toBeUndefined();
    expect(cloudState.document!.nodes[1].metadata?.asset_id).toBe('asset-0');
  });

  it('图片生成不适用视频单图限制', async () => {
    setReferences([limit * 10]);
    cloudState.document!.nodes[0] = structuredClone(imageNode);
    vi.mocked(listModels).mockResolvedValue([{ ...model, video_capabilities: { max_reference_image_bytes: limit } }]);
    await mount();
    expect(host.querySelector('.reference-size-warning')).toBeNull();
    await click(button('生成图片'));
    expect(createImageBatch).toHaveBeenCalledTimes(1);
    expect(createExecution).not.toHaveBeenCalled();
  });

  it('原提交结果未知时优先核对原键和参数，新限制不得清除意图或改成失败', async () => {
    setReferences([limit * 10]);
    const request = { key: 'persisted-video', kind: 'video' as const, params: { group_id: 7, model: 'MiniMax-H3', operation: 'generate', prompt: '原提示词', endpoint: 'openai_videos', duration: 5, resolution: '768p', aspect_ratio: '16:9', references: [{ asset_id: 'asset-0', role: 'first_frame' }] }, count: 1 };
    currentNode().metadata = { ...currentNode().metadata, generation_request: request, status: 'loading' };
    await mount();
    expect(host.querySelector('.reference-size-warning')).toBeNull();
    expect(currentNode().metadata?.generation_request).toEqual(request);
    expect(button('核对提交').disabled).toBe(false);
    await click(button('核对提交'));
    expect(createExecution).toHaveBeenCalledExactlyOnceWith('canvas-test', imageNode.id, 'video', request.params, request.key);
    expect(currentNode().metadata).toMatchObject({ status: 'loading', execution_id: 'video-new' });
  });

  it('已受理且正在核对的任务保留状态并继续轮询，不重新提交', async () => {
    setReferences([limit * 10]);
    currentNode().metadata = { ...currentNode().metadata, execution_id: 'accepted-video', status: 'loading', generation_phase: 'reconciliation' };
    const execution: Execution = { id: 'accepted-video', canvas_id: 'canvas-test', node_id: imageNode.id, kind: 'video', status: 'running', task_id: 'upstream-existing', assets: [], error_code: 'CREATIVE_VIDEO_SUBMISSION_UNCONFIRMED', error_message: '上游受理待核对', created_at: '2026-10-18T00:00:00Z', updated_at: '2026-10-18T00:01:00Z' };
    vi.mocked(listExecutions).mockResolvedValue({ items: [execution], total: 1, page: 1, page_size: 100 });
    await mount();
    await act(async () => poll!());
    expect(host.querySelector('.reference-size-warning')).toBeNull();
    expect(host.querySelector('.node-error')).toBeNull();
    expect(currentNode().metadata).toMatchObject({ status: 'loading', execution_id: 'accepted-video', generation_phase: 'reconciliation' });
    expect(button('任务核对中…').disabled).toBe(true);
    expect(listExecutions).toHaveBeenCalledTimes(2);
    expect(createExecution).not.toHaveBeenCalled();
  });
});

describe('画布节点手势收尾集成', () => {
  const pointer = (type: string, target: EventTarget, x: number, y: number, buttons = type === 'pointerup' ? 0 : 1) => {
    const event = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, buttons, clientX: x, clientY: y });
    Object.defineProperties(event, { pointerId: { value: 9 }, pointerType: { value: 'mouse' } });
    target.dispatchEvent(event);
  };

  it.each(['move', 'resize'])('%s 在设置输入框上松手后不再随鼠标改变', async kind => {
    await mount();
    const node = host.querySelector<HTMLElement>('[data-node-id="image-current"]')!;
    const target = kind === 'resize' ? node.querySelector<HTMLElement>('.resize-handle')! : node;
    const input = host.querySelector('textarea')!;
    const scale = cloudState.document!.viewport.k;
    input.addEventListener('pointerup', event => event.stopPropagation());
    await act(async () => { pointer('pointerdown', target, 100, 100); pointer('pointermove', window, 140, 130); pointer('pointerup', input, 140, 130); });
    expect(kind === 'resize' ? { width: currentNode().width, height: currentNode().height } : currentNode().position)
      .toEqual(kind === 'resize' ? { width: 340 + 40 / scale, height: 240 + 30 / scale } : { x: 100 + 40 / scale, y: 100 + 30 / scale });
    const released = structuredClone(currentNode());
    // 即使测试调度器迟到执行旧帧，也必须由手势身份检查挡住结束后的写入。
    await act(async () => {
      pointer('pointermove', window, 900, 800, 0);
      for (const [callback] of vi.mocked(window.requestAnimationFrame).mock.calls) callback(16);
    });
    expect(currentNode()).toEqual(released);
    expect(createImageBatch).not.toHaveBeenCalled(); expect(createExecution).not.toHaveBeenCalled();
  });

  it('节点拖动中切换窗口取消待执行帧，不移动节点也不触发生成', async () => {
    await mount();
    const node = host.querySelector<HTMLElement>('[data-node-id="image-current"]')!;
    const before = structuredClone(currentNode());
    await act(async () => {
      pointer('pointerdown', node, 100, 100); pointer('pointermove', window, 300, 300);
      window.dispatchEvent(new Event('blur'));
      for (const [callback] of vi.mocked(window.requestAnimationFrame).mock.calls) callback(16);
      pointer('pointermove', window, 900, 800, 0);
    });
    expect(currentNode()).toEqual(before);
    expect(createImageBatch).not.toHaveBeenCalled(); expect(createExecution).not.toHaveBeenCalled();
  });
});

describe('已生成视频节点交互集成', () => {
  const videoNode = () => host.querySelector<HTMLElement>('[data-node-id="image-current"]')!;
  const media = () => videoNode().querySelector<HTMLVideoElement>('video')!;
  const dragSurface = () => videoNode().querySelector<HTMLButtonElement>('button[aria-label="拖动视频画面"]')!;
  const pointer = (type: string, target: EventTarget, x: number, y: number, pointerType = 'mouse', buttons = type === 'pointerup' ? 0 : 1) => {
    const event = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, buttons, clientX: x, clientY: y });
    Object.defineProperties(event, { pointerId: { value: 29 }, pointerType: { value: pointerType } });
    target.dispatchEvent(event);
    return event;
  };
  const setupCompletedVideo = async (scale = 1) => {
    cloudState.document!.viewport = { x: 0, y: 0, k: scale };
    cloudState.document!.nodes = [{ ...structuredClone(imageNode), type: 'video', title: '已生成视频', width: 420, height: 236,
      metadata: { asset_id: 'completed-video', execution_id: 'completed-execution', status: 'success', expires_at: '2099-10-25T00:00:00Z' } }];
    await act(async () => root.render(<Editor/>));
    expect(media().getAttribute('src')).toBe('blob:private-completed-video');
    expect(videoNode().classList.contains('selected')).toBe(false);
  };
  const expectNoGeneration = () => {
    expect(createExecution).not.toHaveBeenCalled();
    expect(createImageBatch).not.toHaveBeenCalled();
    expect(cloudState.flush).not.toHaveBeenCalled();
    expect(uploadAsset).not.toHaveBeenCalled();
  };

  it('点击原生播放控件只选中节点，不取消默认操作或开始移动', async () => {
    await setupCompletedVideo();
    const before = cloudState.document;
    const player = media();
    player.currentTime = 2;
    const assetReads = vi.mocked(assetURL).mock.calls.length;
    let down: MouseEvent;
    await act(async () => {
      down = pointer('pointerdown', player, 80, 220);
      pointer('pointermove', window, 250, 220);
      pointer('pointerup', window, 250, 220);
    });
    expect(down!.defaultPrevented).toBe(false);
    expect(player.controls).toBe(true);
    expect(videoNode().classList.contains('selected')).toBe(true);
    expect(host.querySelector('.node-actions')).not.toBeNull();
    expect(cloudState.document).toBe(before);
    expect(media()).toBe(player);
    expect(player.currentTime).toBe(2);
    expect(assetURL).toHaveBeenCalledTimes(assetReads);
    expectNoGeneration();
  });

  it.each([0, 2])('视频画面点击或 %s 像素微移只选中，不产生文档写入和撤销历史', async distance => {
    await setupCompletedVideo();
    const before = cloudState.document;
    const player = media();
    const undo = host.querySelector<HTMLButtonElement>('button[title="撤销"]')!;
    expect(undo.disabled).toBe(true);
    await act(async () => {
      pointer('pointerdown', dragSurface(), 100, 100);
      if (distance) pointer('pointermove', window, 100 + distance, 100 + distance);
      pointer('pointerup', window, 100 + distance, 100 + distance);
    });
    expect(videoNode().classList.contains('selected')).toBe(true);
    expect(cloudState.document).toBe(before);
    expect(undo.disabled).toBe(true);
    expect(media()).toBe(player);
    expectNoGeneration();
  });

  it.each([['mouse', 1], ['touch', 0.5]] as const)('%s 拖动视频画面按缩放换算坐标，松手后保持播放状态且可撤销', async (pointerType, scale) => {
    await setupCompletedVideo(scale);
    const initial = structuredClone(currentNode().position);
    const player = media();
    player.currentTime = 3;
    const originalURL = player.getAttribute('src');
    const assetReads = vi.mocked(assetURL).mock.calls.length;
    await act(async () => {
      pointer('pointerdown', dragSurface(), 100, 100, pointerType);
      pointer('pointermove', window, 140, 130, pointerType, pointerType === 'touch' ? 0 : 1);
      pointer('pointerup', window, 140, 130, pointerType);
    });
    expect(currentNode().position).toEqual({ x: initial.x + 40 / scale, y: initial.y + 30 / scale });
    expect(media()).toBe(player);
    expect(media().getAttribute('src')).toBe(originalURL);
    expect(player.currentTime).toBe(3);
    expect(assetURL).toHaveBeenCalledTimes(assetReads);
    const released = structuredClone(currentNode());
    await act(async () => {
      pointer('pointermove', window, 800, 700, pointerType, 0);
      for (const [callback] of vi.mocked(window.requestAnimationFrame).mock.calls) callback(16);
    });
    expect(currentNode()).toEqual(released);
    const undo = host.querySelector<HTMLButtonElement>('button[title="撤销"]')!;
    expect(undo.disabled).toBe(false);
    await click(undo);
    expect(currentNode().position).toEqual(initial);
    expect(media()).toBe(player);
    expectNoGeneration();
  });

  it.each([' ', 'Delete', 'Backspace'])('视频聚焦后 %s 保留原生默认行为，不删除节点或重新生成', async key => {
    await setupCompletedVideo();
    await act(async () => {
      pointer('pointerdown', media(), 80, 220);
      pointer('pointerup', window, 80, 220);
    });
    const before = cloudState.document;
    const player = media();
    const down = new KeyboardEvent('keydown', { key, code: key === ' ' ? 'Space' : key, bubbles: true, cancelable: true });
    const up = new KeyboardEvent('keyup', { key, code: key === ' ' ? 'Space' : key, bubbles: true, cancelable: true });
    await act(async () => { player.dispatchEvent(down); player.dispatchEvent(up); });
    expect(down.defaultPrevented).toBe(false);
    expect(up.defaultPrevented).toBe(false);
    expect(cloudState.document).toBe(before);
    expect(media()).toBe(player);
    expectNoGeneration();
  });
});

describe('Grok TTS 音频节点生成集成', () => {
  const audioModel: Model = { ...model, model: 'grok-voice-tts', media_type: 'audio', display_name: 'Grok TTS', operations: ['generate'], max_reference_images: 0, audio_capabilities: { voices: ['eve','ara','rex','sal','leo'], formats: ['mp3','wav'], speed_min: .7, speed_max: 1.5, default_voice: 'eve', default_format: 'mp3', default_speed: 1, max_input_chars: 15000 } };
  const audioExecution = (status: Execution['status'] = 'queued'): Execution => ({ id: 'audio-execution', canvas_id: 'canvas-test', node_id: imageNode.id, kind: 'audio', status, task_id: 'tts-run', assets: status === 'succeeded' ? [{ ...asset('audio-result'), media_type: 'audio', mime_type: 'audio/mpeg' }] : [], created_at: '2026-10-18T00:00:00Z', updated_at: '2026-10-18T00:01:00Z' });
  const setupAudio = () => {
    cloudState.document!.nodes = [{ ...structuredClone(imageNode), type: 'audio', title: '当前音频', height: 100, metadata: { prompt: '你好，世界。', model: '7::grok-voice-tts', image_size: '4K', count: 8, seconds: '60', resolution: '1080p', aspect_ratio: '16:9' } }];
    vi.mocked(listModels).mockResolvedValue([model, audioModel]);
    vi.mocked(createExecution).mockResolvedValue(audioExecution());
  };
  const settingOption = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('.generation-settings-popover button')].find(item => item.textContent === label)!;

  it('只发送语音参数和文本，先持久化单输出意图，双击不重复提交并在当前节点播放结果', async () => {
    setupAudio();
    cloudState.document!.nodes.push({ ...imageNode, id: 'text-ref', type: 'text', metadata: { content: '这是参考文本。' } });
    cloudState.document!.connections = [{ id: 'reference-edge', fromNodeId: 'text-ref', toNodeId: imageNode.id }];
    await mount();
    expect(host.querySelector<HTMLTextAreaElement>('.composer-prompt')?.placeholder).toBe('输入要朗读的文本');
    expect(host.querySelector('[title="添加参考素材"]')).toBeNull();
    expect(host.querySelector('[title="添加参考文本"]')).not.toBeNull();
    await click(button('打开生成设置'));
    await click(settingOption('Ara')); await click(settingOption('WAV')); await click(settingOption('1.25x'));
    await click(button('关闭设置'));
    const generate = button('生成音频');
    await act(async () => { generate.click(); generate.click(); });
    expect(createExecution).toHaveBeenCalledTimes(1);
    const [canvasId, nodeId, kind, params, key] = vi.mocked(createExecution).mock.calls[0];
    expect([canvasId, nodeId, kind]).toEqual(['canvas-test', imageNode.id, 'audio']);
    expect(params).toEqual({ group_id: 7, model: 'grok-voice-tts', operation: 'generate', prompt: '你好，世界。\n\n这是参考文本。', voice: 'ara', audio_format: 'wav', speed: 1.25 });
    expect(saved.some(document => document.nodes[0].metadata?.generation_request?.key === key && document.nodes[0].metadata?.generation_request?.count === 1)).toBe(true);
    expect(host.querySelector('[aria-label="音频生成中"]')?.parentElement?.getAttribute('data-node-id')).toBe(imageNode.id);
    expect(cloudState.document?.nodes).toHaveLength(2);
    expect(createImageBatch).not.toHaveBeenCalled();
    vi.mocked(listExecutions).mockResolvedValue({ items: [audioExecution('succeeded')], total: 1, page: 1, page_size: 100 });
    await act(async () => poll!());
    expect(currentNode().metadata).toMatchObject({ status: 'success', asset_id: 'audio-result', voice: 'ara', audio_format: 'wav', speed: 1.25 });
    expect(currentNode().metadata?.prompt).toBe('你好，世界。');
    expect(host.querySelector('audio')?.controls).toBe(true);
    expect(host.querySelector('audio')?.getAttribute('src')).toBe('blob:private-audio-result');
    expect(host.querySelector('.canvas-generation-loading')).toBeNull();
  });

  it('生成后的音频控件可选中且保留 Space 播放行为，留白可拖动并保持播放位置', async () => {
    setupAudio(); currentNode().metadata = { ...currentNode().metadata, asset_id: 'audio-result', status: 'success' };
    await act(async () => root.render(<Editor/>));
    const node = host.querySelector<HTMLElement>(`[data-node-id="${imageNode.id}"]`)!;
    const audio = node.querySelector('audio')!;
    const pointer = (type: string, target: EventTarget, x: number, y: number) => {
      const event = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, buttons: type === 'pointerup' ? 0 : 1, clientX: x, clientY: y });
      Object.defineProperties(event, { pointerId: { value: 91 }, pointerType: { value: 'mouse' } });
      target.dispatchEvent(event); return event;
    };
    const before = structuredClone(currentNode().position); audio.currentTime = 3;
    let down: MouseEvent;
    await act(async () => { down = pointer('pointerdown', audio, 120, 170); pointer('pointermove', window, 350, 300); pointer('pointerup', window, 350, 300); });
    expect(down!.defaultPrevented).toBe(false); expect(node.classList.contains('selected')).toBe(true);
    expect(currentNode().position).toEqual(before);
    const space = new KeyboardEvent('keydown', { key: ' ', code: 'Space', bubbles: true, cancelable: true });
    await act(async () => audio.dispatchEvent(space)); expect(space.defaultPrevented).toBe(false);
    await act(async () => {
      pointer('pointerdown', node.querySelector('.audio-node-media')!, 120, 120);
      pointer('pointermove', window, 220, 180); pointer('pointerup', window, 220, 180);
    });
    expect(currentNode().position).toEqual({ x: before.x + 100, y: before.y + 60 });
    expect(node.querySelector('audio')).toBe(audio); expect(audio.currentTime).toBe(3);
    expect(createExecution).not.toHaveBeenCalled(); expect(createImageBatch).not.toHaveBeenCalled();
  });

  it('音频下拉框只列出管理员开放的音频，未配置时不开放生成', async () => {
    setupAudio();
    vi.mocked(listModels).mockResolvedValue([model]);
    await mount();
    expect(host.textContent).toContain('管理员尚未开放此类模型');
    expect(button('生成音频').disabled).toBe(true);
    expect(host.querySelector('[aria-label="选择模型"]')?.getAttribute('title')).toBeNull();
    expect(host.querySelector('select')).toBeNull();
    await click(button('生成音频'));
    expect(createExecution).not.toHaveBeenCalled();
  });

  it('音频参考加号只创建文本节点与连线，不打开上传或发起收费调用', async () => {
    setupAudio(); await mount();
    await click(host.querySelector<HTMLButtonElement>('[title="添加参考文本"]')!);
    expect(cloudState.document!.nodes).toHaveLength(2);
    const text = cloudState.document!.nodes[1];
    expect(text.type).toBe('text');
    expect(cloudState.document!.connections).toMatchObject([{ fromNodeId: text.id, toNodeId: imageNode.id }]);
    expect(host.querySelector('.reference-text')?.textContent).toContain('参考文本');
    expect(createExecution).not.toHaveBeenCalled(); expect(uploadAsset).not.toHaveBeenCalled();
  });

  it.each(['image','video','audio'])('音频拒绝 %s 媒体参考，不降为图片生成或默默丢弃连线', async type => {
    setupAudio();
    cloudState.document!.nodes.push({ ...imageNode, id: 'media-ref', type, metadata: { asset_id: 'reference-asset' } });
    cloudState.document!.connections = [{ id: 'reference-edge', fromNodeId: 'media-ref', toNodeId: imageNode.id }];
    await mount(); await click(button('生成音频'));
    expect(host.textContent).toContain('语音生成只支持文本参考');
    expect(createExecution).not.toHaveBeenCalled(); expect(createImageBatch).not.toHaveBeenCalled();
    expect(cloudState.flush).not.toHaveBeenCalled();
  });

  it.each([15000,15001])('按 Unicode 字符计数校验 %s 个字符，不按 UTF-16 长度或旧图片上限拒绝', async count => {
    setupAudio(); currentNode().metadata!.prompt = '😀'.repeat(count);
    await mount(); await click(button('生成音频'));
    if (count === 15000) { expect(createExecution).toHaveBeenCalledTimes(1); expect(vi.mocked(createExecution).mock.calls[0][3].prompt).toBe('😀'.repeat(count)); }
    else { expect(createExecution).not.toHaveBeenCalled(); expect(host.textContent).toContain('15,000'); expect(cloudState.flush).not.toHaveBeenCalled(); }
  });

  it('音频响应丢失后使用原语速声音及原键核对，不发送后来编辑的新文本', async () => {
    setupAudio(); vi.mocked(createExecution).mockRejectedValueOnce(new TypeError('Failed to fetch'));
    await mount(); await click(button('生成音频'));
    const first = structuredClone(vi.mocked(createExecution).mock.calls[0]);
    expect(currentNode().metadata?.generation_request?.kind).toBe('audio');
    await writePrompt('后来编辑的文本'); await click(button('打开生成设置')); await click(settingOption('Rex')); await click(settingOption('1.5x')); await click(button('关闭设置'));
    await click(button('核对提交'));
    expect(vi.mocked(createExecution).mock.calls[1]).toEqual(first);
    expect(currentNode().metadata?.generation_request).toBeUndefined();
    expect(currentNode().metadata).toMatchObject({ execution_id: 'audio-execution', status: 'loading' });
    expect(createImageBatch).not.toHaveBeenCalled();
  });

  it.each(['CREATIVE_AUDIO_SUBMISSION_UNCONFIRMED','CREATIVE_AUDIO_SETTLEMENT_PENDING','CREATIVE_AUDIO_RELEASE_PENDING'])('已受理音频 %s 持续查询原任务，核对期间不能再发生成', async error_code => {
    setupAudio(); currentNode().metadata = { ...currentNode().metadata, status: 'loading', execution_id: 'audio-execution' };
    vi.mocked(listExecutions).mockResolvedValue({ items: [{ ...audioExecution('running'), error_code, error_message: '任务结果正在核对，请勿重复生成' }], total: 1, page: 1, page_size: 100 });
    await mount();
    expect(host.querySelector('[aria-label="音频任务核对中"]')).not.toBeNull();
    expect(button('任务核对中…').disabled).toBe(true);
    await act(async () => poll!());
    expect(createExecution).not.toHaveBeenCalled(); expect(createImageBatch).not.toHaveBeenCalled();
    expect(currentNode().metadata).toMatchObject({ status: 'loading', generation_phase: 'reconciliation' });
  });
});

describe('生成配置节点与四类模型集成', () => {
  it('视频配置节点可以查看说明，按钮不撑出节点且不会触发生成', async () => {
    const videoModel: Model = { ...model, media_type: 'video', model: 'config-video', usage_notes: '使用首帧时请选择自动比例。', operations: ['generate'] };
    cloudState.document!.nodes = [{ ...structuredClone(imageNode), type: 'config', title: '生成配置', width: 340, height: 240, metadata: { generationMode: 'video', model: '7::config-video', prompt: '海边日出' } }];
    vi.mocked(listModels).mockResolvedValue([videoModel]);
    await mount();
    expect(host.querySelector('.config-generate-actions .model-usage-notes-compact')).not.toBeNull();
    const before = structuredClone(cloudState.document);
    await click(button('查看视频模型使用说明'));
    expect(document.querySelector('.model-usage-notes-details')?.textContent).toContain(videoModel.usage_notes);
    await click(button('关闭使用说明'));
    expect(cloudState.document).toEqual(before);
    expect(createExecution).not.toHaveBeenCalled();
    expect(createImageBatch).not.toHaveBeenCalled();
  });

  const textModel: Model={...model,model:'grok-4.5',display_name:'Grok 文字',media_type:'text',operations:['generate'],max_reference_images:0,text_capabilities:{max_input_chars:8000,max_output_tokens:4096,default_max_output_tokens:1024,max_reference_images:0}};
  const config=(kind:'image'|'text'='image')=>({ ...structuredClone(imageNode),type:'config',title:'生成配置',width:340,height:240,metadata:{generationMode:kind,prompt:'写一句欢迎语',model:kind==='text'?'7::grok-4.5':'7::gpt-image-2',image_size:'2K',count:3,max_output_tokens:1024} } as CanvasNodeData);
  const result=()=>cloudState.document!.nodes.find(node=>node.id===currentNode().metadata?.generation_target_id)!;
  const configure=(kind:'image'|'text'='image')=>{cloudState.document!.nodes=[config(kind)];vi.mocked(listModels).mockResolvedValue([model,textModel]);vi.mocked(createImageBatch).mockImplementation(async(_canvas,nodeId,_params,count,key)=>({...batch(key,count),executions:batch(key,count).executions.map(execution=>({...execution,node_id:nodeId}))}));vi.mocked(createExecution).mockImplementation(async(canvasId,nodeId,kind)=>({id:'config-exec',canvas_id:canvasId,node_id:nodeId,kind,status:'queued',task_id:'task',assets:[],created_at:'2026-10-18T00:00:00Z',updated_at:'2026-10-18T00:01:00Z'}));};
  const selectConfig=()=>click(host.querySelector<HTMLButtonElement>('.node-list-item')!);
  const key=async(value:string)=>act(async()=>document.body.dispatchEvent(new KeyboardEvent('keydown',{key:value,ctrlKey:true,bubbles:true,cancelable:true})));

  it('配置内容在340×240节点内部，四tab可用且无手动输出点，切换类型清空过时参数',async()=>{
    configure();await mount();const node=host.querySelector('[data-node-id="image-current"]')!;
    expect([...node.querySelectorAll('[role=tab]')].map(tab=>tab.textContent)).toEqual(['生图','文本','视频','音频']);
    expect(node.querySelector('.handle.target')).not.toBeNull();expect(node.querySelector('.handle.source')).toBeNull();expect(node.querySelector('.config-generate')).not.toBeNull();expect(node.querySelector('.node-panel')).toBeNull();
    await click([...node.querySelectorAll<HTMLButtonElement>('[role=tab]')].find(tab=>tab.textContent==='文本')!);
    expect(currentNode().metadata).toMatchObject({generationMode:'text',model:undefined,image_size:undefined,count:undefined,max_output_tokens:undefined});expect(button('开始生成').disabled).toBe(true);
    await click([...node.querySelectorAll<HTMLButtonElement>('[role=tab]')].find(tab=>tab.textContent==='音频')!);
    expect(currentNode().metadata?.generationMode).toBe('audio');expect(node.textContent).toContain('管理员尚未开放此类模型');
    expect(createExecution).not.toHaveBeenCalled();expect(createImageBatch).not.toHaveBeenCalled();
  });
  it('配置工具条使用生成配置按钮切换组装，新增文本入口建立实际引用且不生成',async()=>{
    configure();await mount();const toolbar=host.querySelector('.node-actions')!;
    const toggle=toolbar.querySelector<HTMLButtonElement>('[title="生成配置"]')!;
    expect(toggle.textContent).toBe('生成配置');expect(toolbar.querySelector('[title="编辑提示词"]')).toBeNull();
    await click(toggle);expect(host.querySelector('.config-composer')).not.toBeNull();expect(toggle.getAttribute('aria-expanded')).toBe('true');
    const scroll=HTMLElement.prototype.scrollIntoView;HTMLElement.prototype.scrollIntoView=vi.fn();
    try{
      const trigger=host.querySelector<HTMLButtonElement>('[aria-label="添加参考内容"]')!;
      await act(async()=>trigger.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true,cancelable:true})));
      const option=[...document.querySelectorAll<HTMLElement>('[role=option]')].find(item=>item.textContent==='新增文本并连接')!;
      await act(async()=>option.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true})));
      const text=cloudState.document!.nodes.find(node=>node.type==='text')!;expect(text).toBeDefined();expect(cloudState.document!.connections).toMatchObject([{fromNodeId:text.id,toNodeId:imageNode.id}]);
      expect(host.querySelector('.config-input-summary')?.textContent).toContain('1 条');
    }finally{HTMLElement.prototype.scrollIntoView=scroll;}
    await click(toggle);expect(host.querySelector('.config-composer')).toBeNull();expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(createExecution).not.toHaveBeenCalled();expect(createImageBatch).not.toHaveBeenCalled();expect(uploadAsset).not.toHaveBeenCalled();
  });
  it('组装参考菜单上传图片沿用私有素材接口并连接当前配置，不触发模型请求',async()=>{
    configure();await mount();await click(host.querySelector<HTMLButtonElement>('.node-actions [title="生成配置"]')!);
    const scroll=HTMLElement.prototype.scrollIntoView;HTMLElement.prototype.scrollIntoView=vi.fn();
    try{
      await act(async()=>host.querySelector<HTMLButtonElement>('[aria-label="添加参考内容"]')!.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true,cancelable:true})));
      const option=[...document.querySelectorAll<HTMLElement>('[role=option]')].find(item=>item.textContent==='上传参考图片')!;
      await act(async()=>option.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true})));
      const file=new File(['reference'],'reference.png',{type:'image/png'});vi.mocked(uploadAsset).mockResolvedValue(asset('new-reference'));
      const input=host.querySelector<HTMLInputElement>('input[type=file]')!;Object.defineProperty(input,'files',{configurable:true,value:[file]});
      await act(async()=>input.dispatchEvent(new Event('change',{bubbles:true})));
      expect(uploadAsset).toHaveBeenCalledWith(file,'canvas-test','reference.png');
      const reference=cloudState.document!.nodes.find(node=>node.metadata?.asset_id==='new-reference')!;expect(reference.type).toBe('image');
      expect(cloudState.document!.connections).toMatchObject([{fromNodeId:reference.id,toNodeId:imageNode.id}]);expect(host.querySelector('.config-input-summary')?.textContent).toContain('1 张');
    }finally{HTMLElement.prototype.scrollIntoView=scroll;}
    expect(createExecution).not.toHaveBeenCalled();expect(createImageBatch).not.toHaveBeenCalled();
  });
  it('配置生成图片创建一个新结果节点，多张属于该结果并持久保存源节点关联',async()=>{
    configure();await mount();const generate=button('开始生成');await act(async()=>{generate.click();generate.click();});
    expect(createImageBatch).toHaveBeenCalledTimes(1);const[,nodeId,params,count]=vi.mocked(createImageBatch).mock.calls[0];
    expect(nodeId).not.toBe(imageNode.id);expect(count).toBe(3);expect(params.image_size).toBe('2K');expect(cloudState.document!.nodes).toHaveLength(2);expect(result().type).toBe('image');expect(result().metadata?.execution_ids).toHaveLength(3);
    expect(saved.some(doc=>doc.nodes.find(n=>n.id===imageNode.id)?.metadata?.generation_target_id===nodeId&&doc.nodes.find(n=>n.id===nodeId)?.metadata?.generation_request?.count===3)).toBe(true);
    await selectConfig();expect(button('生成中…').disabled).toBe(true);await click(button('生成中…'));expect(createImageBatch).toHaveBeenCalledTimes(1);
  });
  it('配置文本仅发送合法文本参数，并把返回内容当纯文本放在新结果节点',async()=>{
    configure('text');await mount();await click(button('打开配置节点设置'));
    const input=document.querySelector<HTMLInputElement>('[aria-label="文本最大输出Token"]')!;
    await act(async()=>{input.focus();Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value')!.set!.call(input,'2048');input.dispatchEvent(new Event('input',{bubbles:true}));});await act(async()=>input.blur());await click(button('关闭设置'));
    await click(button('开始生成'));const[canvasId,nodeId,kind,params]=vi.mocked(createExecution).mock.calls[0];expect([canvasId,kind]).toEqual(['canvas-test','text']);expect(params).toEqual({group_id:7,model:'grok-4.5',operation:'generate',prompt:'写一句欢迎语',max_output_tokens:2048});expect(result().metadata?.status).toBe('loading');
    const execution:Execution={id:'config-exec',canvas_id:canvasId,node_id:nodeId,kind:'text',status:'succeeded',task_id:'task',assets:[],output_text:'<img src=x onerror=alert(1)>欢迎',created_at:'2026-10-18T00:00:00Z',updated_at:'2026-10-18T00:02:00Z'};
    vi.mocked(listExecutions).mockResolvedValue({items:[execution],total:1,page:1,page_size:100});await act(async()=>poll!());
    expect(result().metadata?.content).toBe(execution.output_text);expect(host.querySelector<HTMLTextAreaElement>('.text-node-content')?.value).toBe(execution.output_text);expect(host.querySelector('img[src=x]')).toBeNull();expect(createImageBatch).not.toHaveBeenCalled();
  });
  it('普通文本节点能展示和打开参考文本，生成只拼接一次引用内容',async()=>{
    configure('text');currentNode().type='text';
    cloudState.document!.nodes.push({...imageNode,id:'text-reference',type:'text',title:'参考段落',metadata:{content:'参考正文'}});
    cloudState.document!.connections=[{id:'text-ref-edge',fromNodeId:'text-reference',toNodeId:imageNode.id}];
    await mount();
    const reference=host.querySelector<HTMLButtonElement>('.reference-text')!;expect(reference.textContent).toContain('参考正文');
    await click(reference);expect(host.querySelector('.node-panel')?.closest('[data-node-id]')?.getAttribute('data-node-id')).toBe('text-reference');
    await click([...host.querySelectorAll<HTMLButtonElement>('.node-list-item')].find(item=>item.textContent?.includes('生成配置'))!);
    await click(button('生成文本'));
    expect(vi.mocked(createExecution).mock.calls[0][3].prompt).toBe('写一句欢迎语\n\n参考正文');expect(createImageBatch).not.toHaveBeenCalled();
  });
  it('组装失效引用阻止新任务；已有未知提交仍用原键参数核对，不随提及变化重新组装',async()=>{
    configure('text');currentNode().metadata!.composerContent='@[node:foreign]';await mount();await click(button('开始生成'));expect(createExecution).not.toHaveBeenCalled();expect(host.textContent).toContain('未连接的引用');
    await act(async()=>{currentNode().metadata!.composerContent='固定原文';cloudState.listeners.forEach(notify=>notify());});
    vi.mocked(createExecution).mockRejectedValueOnce(new TypeError('network'));await click(button('开始生成'));const frozen=structuredClone(vi.mocked(createExecution).mock.calls[0]);expect(result().metadata?.generation_request).toBeDefined();
    await act(async()=>{currentNode().metadata!.composerContent='@[node:foreign]';cloudState.listeners.forEach(notify=>notify());});await selectConfig();await click(button('核对提交'));expect(vi.mocked(createExecution).mock.calls[1]).toEqual(frozen);expect(cloudState.document!.nodes).toHaveLength(2);
  });
  it('未知提交的结果和配置在undo/redo/删除后不丢失原意图，终态后可删除',async()=>{
    configure('text');await mount();await click([...host.querySelectorAll<HTMLButtonElement>('button')].find(item=>item.textContent==='组装提示词')!);
    // 先形成普通编辑历史，确保真正执行一次撤销，而非只有空历史。
    await act(async()=>{const editor=host.querySelector<HTMLElement>('[aria-label="组装提示词内容"]')!;editor.textContent='编辑后';editor.dispatchEvent(new Event('input',{bubbles:true}));});
    expect(host.querySelector<HTMLButtonElement>('[title="撤销"]')?.disabled).toBe(false);
    vi.mocked(createExecution).mockRejectedValueOnce(new TypeError('network'));await click(button('开始生成'));const before=result(),frozen=structuredClone(before.metadata?.generation_request);
    await key('z');expect(cloudState.document!.nodes.some(node=>node.id===before.id)).toBe(true);expect(result().metadata?.generation_request).toEqual(frozen);
    await key('y');expect(result().metadata?.generation_request).toEqual(frozen);await selectConfig();await click(host.querySelector<HTMLButtonElement>('[title="删除所选"]')!);expect(currentNode()).toBeDefined();expect(host.textContent).toContain('任务尚未结束');expect(createExecution).toHaveBeenCalledTimes(1);
    await act(async()=>{cloudState.document={...cloudState.document!,nodes:cloudState.document!.nodes.map(node=>node.id===before.id?{...node,metadata:{...node.metadata,generation_request:undefined,status:'success',execution_id:'done',content:'完成'}}:node)};cloudState.listeners.forEach(notify=>notify());});
    await click(host.querySelector<HTMLButtonElement>('[title="删除所选"]')!);expect(cloudState.document!.nodes.some(node=>node.id===imageNode.id)).toBe(false);expect(createExecution).toHaveBeenCalledTimes(1);
  });
  it('复制组时包含子节点并重映射组装引用/连线，副本不携带原执行意图',async()=>{
    configure('text');const cfg=currentNode();cfg.metadata={...cfg.metadata,composerContent:'@[node:ref-text]',generation_target_id:'old-result',groupId:'g'};
    const ref={...imageNode,id:'ref-text',type:'text',metadata:{content:'参考',groupId:'g'}};
    cloudState.document!.nodes=[{...imageNode,id:'g',type:'group',title:'组',metadata:{}},cfg,ref];cloudState.document!.connections=[{id:'refedge',fromNodeId:'ref-text',toNodeId:cfg.id}];await mount();await key('c');await key('v');
    expect(cloudState.document!.nodes).toHaveLength(6);const copy=cloudState.document!.nodes.find(node=>node.type==='config'&&node.id!==cfg.id)!;const copyRef=cloudState.document!.nodes.find(node=>node.type==='text'&&node.id!=='ref-text')!;
    expect(copy.metadata?.composerContent).toBe(`@[node:${copyRef.id}]`);expect(copy.metadata?.generation_target_id).toBeUndefined();expect(copy.metadata?.groupId).toBe(copyRef.metadata?.groupId);expect(copy.metadata?.groupId).not.toBe('g');expect(cloudState.document!.connections.some(edge=>edge.fromNodeId===copyRef.id&&edge.toNodeId===copy.id)).toBe(true);expect(createExecution).not.toHaveBeenCalled();
  });
});
