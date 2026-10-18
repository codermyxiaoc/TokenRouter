// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { assetURL } from '@/platform/api';
import { AssetView } from './AssetView';

vi.mock('@/platform/api', async original => ({ ...await original<typeof import('@/platform/api')>(), assetURL: vi.fn() }));
let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.clearAllMocks(); vi.mocked(assetURL).mockResolvedValue('blob:existing-video');
  host = document.createElement('div'); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.restoreAllMocks(); });

describe('画布视频播放器与拖动区域隔离', () => {
  it('音频原生控件只选择节点，不拦截播放操作或冒泡拖动，重渲染保持进度', async () => {
    const select = vi.fn(), move = vi.fn();
    await act(async () => root.render(<div onPointerDown={move}><AssetView id="audio-asset" type="audio" audioNodeInteraction={{ onSelect: select }}/></div>));
    const audio = host.querySelector('audio')!;
    audio.currentTime = 2;
    const event = new MouseEvent('pointerdown', { bubbles: true, cancelable: true, button: 0, shiftKey: true });
    await act(async () => audio.dispatchEvent(event));
    expect(select).toHaveBeenCalledOnce();
    expect(select.mock.calls[0][0].shiftKey).toBe(true);
    expect(move).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
    expect(audio.controls).toBe(true);
    expect(host.querySelector('.video-node-drag-surface')).toBeNull();
    await act(async () => root.render(<div onPointerDown={move}><AssetView id="audio-asset" type="audio" audioNodeInteraction={{ onSelect: vi.fn() }}/></div>));
    expect(host.querySelector('audio')).toBe(audio);
    expect(audio.currentTime).toBe(2);
    expect(assetURL).toHaveBeenCalledOnce();
    await act(async () => host.querySelector('.audio-node-media')!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, button: 0 })));
    expect(move).toHaveBeenCalledOnce();
  });
  it.each([[1,52],[.5,104],[2,52]] as const)('缩放%s仍给原生控件保留独立区域', async (scale, bottom) => {
    const select = vi.fn();
    await act(async () => root.render(<AssetView id="video-asset" type="video" videoNodeInteraction={{ scale, height: 236, onSelect: select }}/>));
    expect(host.querySelector('video')?.controls).toBe(true);
    expect(host.querySelector<HTMLButtonElement>('.video-node-drag-surface')?.style.bottom).toBe(`${bottom}px`);
    expect(host.querySelector<HTMLButtonElement>('.video-node-drag-surface')?.style.height).toBe(`calc(100% - ${bottom}px)`);
    const video = host.querySelector('video')!;
    video.currentTime = 3;
    await act(async () => root.render(<AssetView id="video-asset" type="video" videoNodeInteraction={{ scale, height: 236, onSelect: vi.fn() }}/>));
    expect(host.querySelector('video')).toBe(video);
    expect(video.currentTime).toBe(3);
    expect(assetURL).toHaveBeenCalledOnce();
  });

  it('极小节点不遮挡播放器，素材库仍保持独立播放器行为', async () => {
    await act(async () => root.render(<AssetView id="video-asset" type="video" videoNodeInteraction={{ scale: .25, height: 236, onSelect: vi.fn() }}/>));
    expect(host.querySelector('.video-node-drag-surface')).toBeNull();
    expect(host.querySelector('video')?.controls).toBe(true);
    await act(async () => root.render(<AssetView id="video-asset" type="video"/>));
    expect(host.querySelector('.video-node-media')).toBeNull();
    expect(host.querySelector('video')?.controls).toBe(true);
  });

  it('播放器Esc仍传给浏览器，画布不能截断原生全屏退出', async () => {
    await act(async () => root.render(<AssetView id="video-asset" type="video"/>));
    const listener = vi.fn();
    window.addEventListener('keydown', listener);
    try {
      const escape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
      host.querySelector('video')!.dispatchEvent(escape);
      expect(listener).toHaveBeenCalledOnce();
      expect(escape.defaultPrevented).toBe(false);
    } finally { window.removeEventListener('keydown', listener); }
  });
});
