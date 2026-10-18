import { act, type ReactNode } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { usePointerGesture } from './use-pointer-gesture';

let host: HTMLDivElement;
let root: Root;
let frames: Map<number, FrameRequestCallback>;
let frameId: number;
let moved: ReturnType<typeof vi.fn>;
let ended: ReturnType<typeof vi.fn>;
const capture = new Map<Element, number>();

function Harness({ scope = 'one', children }: { scope?: string; children?: ReactNode }) {
  const drag = usePointerGesture({ scope, onMove: moved, onEnd: ended });
  return <div data-active={drag.active} onPointerDown={event => drag.begin(event, { nodeId: 'current-node' })}>{children}</div>;
}

// jsdom 没有 PointerEvent 和指针捕获，补齐字段以模拟真实按键状态及捕获生命周期。
function pointer(type: string, target: EventTarget = window, options: { x?: number; buttons?: number; button?: number; id?: number } = {}) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, button: options.button ?? 0, buttons: options.buttons ?? (type === 'pointerup' ? 0 : 1), clientX: options.x ?? 10, clientY: 20 });
  Object.defineProperties(event, { pointerId: { value: options.id ?? 1 }, pointerType: { value: 'mouse' } });
  target.dispatchEvent(event);
}
const start = (button = 0) => pointer('pointerdown', host.firstElementChild!, { button });
const flush = () => { const current = [...frames.values()]; frames.clear(); current.forEach(callback => callback(16)); };

beforeEach(() => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  frames = new Map(); frameId = 0; moved = vi.fn(); ended = vi.fn(); capture.clear();
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation(callback => { frames.set(++frameId, callback); return frameId; });
  vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(id => { frames.delete(id); });
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  Element.prototype.setPointerCapture = function(id) { capture.set(this, id); };
  Element.prototype.hasPointerCapture = function(id) { return capture.get(this) === id; };
  Element.prototype.releasePointerCapture = function() { capture.delete(this); };
  host = document.createElement('div'); document.body.append(host); root = createRoot(host);
  act(() => root.render(<Harness/>));
});
afterEach(() => {
  act(() => root.unmount()); host.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals();
  delete (Element.prototype as Partial<Element>).setPointerCapture;
  delete (Element.prototype as Partial<Element>).hasPointerCapture;
  delete (Element.prototype as Partial<Element>).releasePointerCapture;
});

describe('画布指针手势收尾', () => {
  it('高频移动只绘制最后一帧，正常松手立即落下最终位置且释放捕获', () => {
    act(() => { start(); for (let x = 20; x < 120; x++) pointer('pointermove', window, { x }); });
    expect(frames.size).toBe(1); expect(moved).not.toHaveBeenCalled();
    act(() => pointer('pointerup'));
    expect(moved).toHaveBeenCalledTimes(1);
    expect(moved.mock.calls[0][0].clientX).toBe(119);
    expect(ended).toHaveBeenCalledExactlyOnceWith({ nodeId: 'current-node' }, 'release');
    expect(capture.size).toBe(0); expect(frames.size).toBe(0);
    act(() => { pointer('pointermove', window, { x: 400 }); flush(); });
    expect(moved).toHaveBeenCalledTimes(1);
  });

  it('在阻止冒泡的弹层上松手仍可结束手势', () => {
    const overlay = document.createElement('input'); document.body.append(overlay);
    overlay.addEventListener('pointerup', event => event.stopPropagation());
    act(() => { start(); pointer('pointermove'); pointer('pointerup', overlay); });
    expect(ended).toHaveBeenCalledOnce();
    expect(host.firstElementChild?.getAttribute('data-active')).toBe('false');
    act(() => { pointer('pointermove', window, { x: 500 }); flush(); });
    expect(moved).toHaveBeenCalledTimes(1); overlay.remove();
  });

  it.each(['pointercancel', 'lostpointercapture', 'blur', 'Escape', 'hidden', 'buttons'])('遇到 %s 中断时取消待执行帧，后续移动不会再改变节点', reason => {
    act(() => {
      start(); pointer('pointermove');
      if (reason === 'Escape') window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
      else if (reason === 'hidden') { vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange')); }
      else if (reason === 'buttons') pointer('pointermove', window, { buttons: 0 });
      else if (reason === 'blur') window.dispatchEvent(new Event('blur'));
      else pointer(reason);
    });
    expect(ended).toHaveBeenCalledExactlyOnceWith({ nodeId: 'current-node' }, 'cancel');
    expect(capture.size).toBe(0); expect(frames.size).toBe(0);
    act(() => { pointer('pointermove', window, { x: 600 }); flush(); });
    expect(moved).not.toHaveBeenCalled();
  });

  it('鼠标中键平移检查中键状态，不因同时按住左键而粘住', () => {
    act(() => { start(1); pointer('pointermove', window, { buttons: 4 }); flush(); });
    expect(moved).toHaveBeenCalledOnce(); expect(ended).not.toHaveBeenCalled();
    act(() => { pointer('pointermove', window, { buttons: 1 }); pointer('pointermove', window, { buttons: 4 }); flush(); });
    expect(moved).toHaveBeenCalledOnce(); expect(ended).toHaveBeenCalledOnce();
  });

  it('其他指针的松手或取消不会结束当前拖动', () => {
    act(() => { start(); pointer('pointerup', window, { id: 2 }); pointer('pointercancel', window, { id: 2 }); pointer('pointermove'); flush(); });
    expect(ended).not.toHaveBeenCalled(); expect(moved).toHaveBeenCalledOnce();
    act(() => pointer('pointerup'));
    expect(ended).toHaveBeenCalledOnce();
  });

  it('切换画布和卸载均取消旧手势，不回写旧画布', () => {
    act(() => { start(); pointer('pointermove'); root.render(<Harness scope="two"/>); });
    expect(capture.size).toBe(0); expect(frames.size).toBe(0);
    act(() => { pointer('pointermove'); flush(); });
    expect(moved).not.toHaveBeenCalled();
    expect(host.firstElementChild?.getAttribute('data-active')).toBe('false');
  });
});
