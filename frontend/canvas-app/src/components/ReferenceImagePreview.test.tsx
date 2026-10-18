// @vitest-environment jsdom
import { act, type ComponentProps } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, assetBlob, assetURL } from '@/platform/api';
import { clearSession } from '@/platform/session';
import { ReferenceImagePreview } from './ReferenceImagePreview';

vi.mock('@/platform/api', async original => ({ ...await original<typeof import('@/platform/api')>(), assetBlob: vi.fn(), assetURL: vi.fn() }));
let host: HTMLDivElement;
let root: Root;
const props = { assetId: 'reference-a', title: '参考图片 A', index: 0, expiresAt: '2099-01-01T00:00:00Z' };
const render = async (patch: Partial<ComponentProps<typeof ReferenceImagePreview>> = {}) => { await act(async () => root.render(<ReferenceImagePreview {...props} {...patch}/>)); };
const click = async (element: HTMLElement) => { await act(async () => element.click()); };
const trigger = () => host.querySelector<HTMLButtonElement>('.reference-image-trigger')!;
const closeButton = () => document.querySelector<HTMLButtonElement>('[aria-label="关闭参考预览"]')!;

beforeEach(() => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.clearAllMocks();
  vi.mocked(assetURL).mockResolvedValue('blob:thumbnail-cache');
  vi.mocked(assetBlob).mockResolvedValue(new Blob(['private-image'], { type: 'image/png' }));
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} unobserve() {} });
  vi.stubGlobal('matchMedia', () => ({ matches: false, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => setTimeout(() => callback(0), 0));
  vi.stubGlobal('cancelAnimationFrame', clearTimeout);
  const computedStyle = window.getComputedStyle.bind(window);
  vi.spyOn(window, 'getComputedStyle').mockImplementation(element => computedStyle(element));
  // jsdom 不提供对象 URL，保留 URL 构造器并单独替代浏览器资源生命周期接口。
  const BrowserURL = URL;
  vi.stubGlobal('URL', class extends BrowserURL {
    static createObjectURL = vi.fn(() => 'blob:private-preview');
    static revokeObjectURL = vi.fn();
  });
  host = document.createElement('div'); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); clearSession(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe('参考图私有预览', () => {
  it('真实弹层在画布容器外打开，每次读取私有内容，关闭撤销专用URL且不动缩略图缓存', async () => {
    await render();
    expect(assetBlob).not.toHaveBeenCalled();
    await click(trigger());
    const dialog = document.querySelector('[role="dialog"]')!;
    expect(dialog).not.toBeNull();
    expect(host.contains(dialog)).toBe(false);
    expect(dialog.querySelector('img')?.getAttribute('src')).toBe('blob:private-preview');
    expect(assetBlob).toHaveBeenCalledWith('reference-a');
    await click(closeButton());
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:private-preview');
    expect(URL.revokeObjectURL).not.toHaveBeenCalledWith('blob:thumbnail-cache');
    await click(trigger());
    expect(assetBlob).toHaveBeenCalledTimes(2);
  });

  it('Esc关闭真实弹层，缩略图指针和按键不冒泡到画布', async () => {
    const pointer = vi.fn(); const keyboard = vi.fn();
    window.addEventListener('pointerdown', pointer); window.addEventListener('keydown', keyboard);
    await render();
    trigger().dispatchEvent(new Event('pointerdown', { bubbles: true }));
    trigger().dispatchEvent(new KeyboardEvent('keydown', { key: 'Delete', bubbles: true }));
    expect(pointer).not.toHaveBeenCalled(); expect(keyboard).not.toHaveBeenCalled();
    window.removeEventListener('pointerdown', pointer); window.removeEventListener('keydown', keyboard);
    await click(trigger());
    await act(async () => closeButton().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', keyCode: 27, bubbles: true })));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it.each([[410, '参考图已过期'], [404, '不存在或没有访问权限'], [403, '不存在或没有访问权限'], [401, '登录会话已失效']] as const)('已有缩略图也不绕过服务端 %s 校验', async (status, message) => {
    vi.mocked(assetBlob).mockRejectedValue(new ApiError(status, 'private-secret-path'));
    await render(); await click(trigger());
    const dialog = document.querySelector('[role="dialog"]')!;
    expect(dialog.textContent).toContain(message);
    expect(dialog.textContent).not.toContain('private-secret-path');
    expect(dialog.querySelector('img')).toBeNull();
    expect(URL.createObjectURL).not.toHaveBeenCalled();
  });

  it('已知过期或无素材的节点不发内容读取', async () => {
    await render({ expiresAt: '2000-01-01T00:00:00Z' }); await click(trigger());
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('参考图已过期');
    expect(assetBlob).not.toHaveBeenCalled();
    await render({ assetId: undefined });
    expect(trigger().disabled).toBe(true);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it.each(['close', 'session', 'replace'] as const)('异步返回前%s不会回填旧图片', async action => {
    let resolve!: (blob: Blob) => void;
    vi.mocked(assetBlob).mockReturnValue(new Promise(done => { resolve = done; }));
    await render(); await click(trigger());
    if (action === 'close') await click(closeButton());
    if (action === 'session') await act(async () => clearSession());
    if (action === 'replace') await render({ assetId: 'reference-b', title: '参考图片 B' });
    await act(async () => resolve(new Blob(['late-private-image'], { type: 'image/png' })));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(URL.createObjectURL).not.toHaveBeenCalled();
  });

  it('退出会话或卸载时撤销已打开的预览', async () => {
    await render(); await click(trigger());
    await act(async () => clearSession());
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:private-preview');
  });
});
