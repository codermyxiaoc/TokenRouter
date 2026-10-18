// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { assetURL } from '@/platform/api';
import type { CanvasNodeData } from '@/types/canvas';
import { ImageResults } from './ImageResults';

vi.mock('@/platform/api', async importOriginal => ({ ...await importOriginal<typeof import('@/platform/api')>(), assetURL: vi.fn() }));

const baseNode: CanvasNodeData = { id: 'image-node', type: 'image', title: '海边', position: { x: 200, y: 300 }, width: 340, height: 240, metadata: { asset_id: 'asset-1', asset_ids: ['asset-1', 'asset-2', 'asset-3', 'asset-4'] } };
let host: HTMLDivElement;
let root: Root;
const onSelect = vi.fn();
const onDownload = vi.fn();
const onExtract = vi.fn();
const button = (label: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)!;
const render = async (node = baseNode, extract = true) => {
  await act(async () => root.render(<ImageResults node={node} onSelect={onSelect} onDownload={onDownload} onExtract={extract ? onExtract : undefined}/>));
};
const click = async (label: string) => { await act(async () => button(label).click()); };

beforeEach(() => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.clearAllMocks();
  vi.mocked(assetURL).mockImplementation(async id => `blob:test-${id}`);
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });

describe('同节点多图结果展示', () => {
  it('默认折叠只读取主图，展开后每张使用素材鉴权通道且不触发结果操作', async () => {
    const saved = structuredClone(baseNode);
    await render();
    // 折叠时只呈现主图；不保留会越过选中边框的偏移装饰层。
    const results = host.querySelector<HTMLElement>('.image-results')!;
    expect(getComputedStyle(results).overflow).toBe('hidden');
    expect(host.querySelectorAll('.image-results-stack-sheet')).toHaveLength(0);
    expect(host.querySelectorAll('img')).toHaveLength(1);
    expect(assetURL).toHaveBeenCalledExactlyOnceWith('asset-1');
    expect(button('展开 4 张图片').getAttribute('aria-expanded')).toBe('false');
    await click('展开 4 张图片');
    expect(getComputedStyle(host.querySelector<HTMLElement>('.image-results')!).overflow).toBe('visible');
    expect(host.querySelectorAll('img')).toHaveLength(4);
    expect(host.querySelectorAll('.image-results-card')).toHaveLength(3);
    expect(vi.mocked(assetURL).mock.calls.map(([id]) => id).sort()).toEqual(['asset-1', 'asset-2', 'asset-3', 'asset-4']);
    expect(onSelect).not.toHaveBeenCalled();
    expect(onDownload).not.toHaveBeenCalled();
    expect(onExtract).not.toHaveBeenCalled();
    expect(baseNode).toEqual(saved);
    await click('收起 4 张图片');
    expect(host.querySelectorAll('.image-results-card')).toHaveLength(0);
    expect(host.querySelectorAll('img')).toHaveLength(1);
  });

  it('展开只用节点原尺寸和 18px 间距展示八张，不产生实际节点', async () => {
    await render({ ...baseNode, metadata: { asset_id: 'asset-1', asset_ids: Array.from({ length: 8 }, (_, index) => `asset-${index + 1}`) } });
    await click('展开 8 张图片');
    const cards = [...host.querySelectorAll<HTMLElement>('.image-results-card')];
    expect(cards.map(card => [card.style.left, card.style.top])).toEqual([
      ['0px', '-258px'], ['358px', '-258px'], ['716px', '-258px'], ['1074px', '-258px'],
      ['358px', '0px'], ['716px', '0px'], ['1074px', '0px'],
    ]);
    expect(cards.every(card => card.style.width === '340px' && card.style.height === '240px')).toBe(true);
    expect(host.querySelectorAll('[data-node-id]')).toHaveLength(0);
  });

  it('设为主图、下载和提取只对明确选中的素材回调，父节点仍是结果权威', async () => {
    await render();
    await click('展开 4 张图片');
    await click('将第 3 张图片设为主图');
    expect(onSelect).toHaveBeenCalledExactlyOnceWith('asset-3');
    expect(host.querySelector('.image-results-primary img')?.getAttribute('src')).toBe('blob:test-asset-1');
    await render({ ...baseNode, metadata: { ...baseNode.metadata, asset_id: 'asset-3' } });
    expect(host.querySelector('.image-results-primary img')?.getAttribute('src')).toBe('blob:test-asset-3');
    await click('下载主图');
    await click('下载第 2 张图片');
    expect(onDownload.mock.calls).toEqual([['asset-3'], ['asset-2']]);
    await click('提取第 4 张图片到新节点');
    expect(onExtract).toHaveBeenCalledExactlyOnceWith('asset-4');
  });

  it('操作按钮阻止拖动和点击冒泡，普通图片仍可由父画布拖动', async () => {
    await render();
    const pointer = vi.fn();
    const clicked = vi.fn();
    const keydown = vi.fn();
    document.body.addEventListener('pointerdown', pointer);
    document.body.addEventListener('click', clicked);
    document.body.addEventListener('keydown', keydown);
    act(() => button('展开 4 张图片').dispatchEvent(new Event('pointerdown', { bubbles: true })));
    await click('展开 4 张图片');
    act(() => button('将第 2 张图片设为主图').dispatchEvent(new Event('pointerdown', { bubbles: true })));
    await click('将第 2 张图片设为主图');
    act(() => button('将第 2 张图片设为主图').dispatchEvent(new KeyboardEvent('keydown', { key: 'Delete', bubbles: true })));
    expect(pointer).not.toHaveBeenCalled();
    expect(clicked).not.toHaveBeenCalled();
    expect(keydown).not.toHaveBeenCalled();
    act(() => host.querySelector('.image-results-primary img')!.dispatchEvent(new Event('pointerdown', { bubbles: true })));
    expect(pointer).toHaveBeenCalledTimes(1);
    document.body.removeEventListener('pointerdown', pointer);
    document.body.removeEventListener('click', clicked);
    document.body.removeEventListener('keydown', keydown);
  });

  it('去重结果、修正失效主图，单图和空节点均不显示展开控件', async () => {
    await render({ ...baseNode, metadata: { asset_id: 'stale', asset_ids: ['asset-2', 'asset-2'] } }, false);
    expect(host.querySelector('.image-results-primary img')?.getAttribute('src')).toBe('blob:test-asset-2');
    expect(host.querySelector('.image-results-toggle')).toBeNull();
    expect(button('提取主图到新节点')).toBeNull();
    await render({ ...baseNode, metadata: {} });
    expect(host.textContent).toContain('空图片节点');
    expect(host.querySelector('button')).toBeNull();
  });

  it('素材过期只显示占位，不发起生成或绕过鉴权下载', async () => {
    await render({ ...baseNode, metadata: { asset_id: 'asset-1', asset_ids: ['asset-1'], expires_at: '2000-01-01T00:00:00Z' } });
    expect(host.textContent).toContain('素材已过期');
    expect(assetURL).not.toHaveBeenCalled();
    expect(button('下载主图')).toBeNull();
    expect(onSelect).not.toHaveBeenCalled();
    expect(onDownload).not.toHaveBeenCalled();
    expect(onExtract).not.toHaveBeenCalled();
  });
});
