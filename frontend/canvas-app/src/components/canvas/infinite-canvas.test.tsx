import { act, createRef } from 'react';
import { createPortal } from 'react-dom';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { InfiniteCanvas } from './infinite-canvas';

let host: HTMLDivElement;
let portal: HTMLDivElement;
let root: Root;
let frames: Map<number, FrameRequestCallback>;
let onViewportChange: ReturnType<typeof vi.fn>;
let container: ReturnType<typeof createRef<HTMLDivElement>>;

function pointer(type: string, target: EventTarget, x = 10, buttons = type === 'pointerup' ? 0 : 1) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, buttons, clientX: x, clientY: 20 });
  Object.defineProperties(event, { pointerId: { value: 1 }, pointerType: { value: 'mouse' } });
  target.dispatchEvent(event);
}
const flush = () => { const current = [...frames.values()]; frames.clear(); current.forEach(callback => callback(16)); };
beforeEach(() => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement('div'); portal = document.createElement('div'); document.body.append(host, portal); root = createRoot(host);
  container = createRef<HTMLDivElement>(); frames = new Map(); onViewportChange = vi.fn(); let next = 0;
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation(callback => { frames.set(++next, callback); return next; });
  vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(id => { frames.delete(id); });
  act(() => root.render(<InfiniteCanvas containerRef={container} viewport={{ x: 0, y: 0, k: 1 }} tool="pan" onViewportChange={onViewportChange}>
    <input aria-label="节点输入"/>
    <video controls/><audio controls/><button type="button">播放操作</button>
    {createPortal(<div data-testid="settings"><input aria-label="设置输入"/></div>, portal)}
  </InfiniteCanvas>));
});
afterEach(() => { act(() => root.unmount()); host.remove(); portal.remove(); vi.restoreAllMocks(); });

describe('无限画布平移边界', () => {
  it.each(['video','audio','button'])('%s聚焦时空格保留原生操作，不进入临时平移', tag => {
    const target = host.querySelector(tag)!;
    const down = new KeyboardEvent('keydown', { key: ' ', code: 'Space', bubbles: true, cancelable: true });
    const up = new KeyboardEvent('keyup', { key: ' ', code: 'Space', bubbles: true, cancelable: true });
    act(() => target.dispatchEvent(down));
    expect(down.defaultPrevented).toBe(false);
    expect(container.current?.style.cursor).toBe('grab');
    act(() => target.dispatchEvent(up));
    expect(up.defaultPrevented).toBe(false);
    expect(onViewportChange).not.toHaveBeenCalled();
  });
  it('从 Portal 设置背景或输入框拖入画布不启动平移', () => {
    for (const target of [portal.querySelector('div')!, portal.querySelector('input')!, host.querySelector('input')!]) {
      act(() => { pointer('pointerdown', target); pointer('pointermove', container.current!, 300); flush(); pointer('pointerup', container.current!); });
    }
    expect(onViewportChange).not.toHaveBeenCalled();
    expect(document.body.style.cursor).not.toBe('grabbing');
  });

  it('画布上开始拖动，在弹层松手后视口不再跟随鼠标', () => {
    portal.addEventListener('pointerup', event => event.stopPropagation());
    act(() => { pointer('pointerdown', container.current!); pointer('pointermove', window, 110); pointer('pointerup', portal); });
    expect(onViewportChange).toHaveBeenCalledExactlyOnceWith({ x: 100, y: 0, k: 1 });
    act(() => { pointer('pointermove', container.current!, 700, 0); flush(); });
    expect(onViewportChange).toHaveBeenCalledOnce();
    expect(document.body.style.cursor).not.toBe('grabbing');
  });
});
