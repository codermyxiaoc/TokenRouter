// @vitest-environment jsdom
import { act, createRef } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CanvasConnectionLayer, type CanvasConnectionController } from './canvas-connection-layer';
import type { CanvasConnection, CanvasNodeData } from '@/types/canvas';

const nodes: CanvasNodeData[] = [
  { id: 'a', type: 'image', title: 'A', position: { x: 20, y: 20 }, width: 100, height: 80 },
  { id: 'b', type: 'image', title: 'B', position: { x: 300, y: 20 }, width: 100, height: 80 },
];
let host: HTMLDivElement;
let root: Root;
let controller: ReturnType<typeof createRef<CanvasConnectionController>>;
let frames: FrameRequestCallback[];
let onConnect: ReturnType<typeof vi.fn>;
let onDropEmpty: ReturnType<typeof vi.fn>;
let parentRenders: number;

function Harness({ connections = [] }: { connections?: CanvasConnection[] }) {
  parentRenders++;
  return <>
    <button data-testid="a-source" className="handle" onPointerDown={event => controller.current?.begin(event, 'a', 'source')}>a source</button>
    <button data-testid="b-target" className="handle" onPointerDown={event => controller.current?.begin(event, 'b', 'target')}>b target</button>
    <CanvasConnectionLayer ref={controller} containerRef={{ current: host }} nodes={nodes} connections={connections} viewport={{ x: 0, y: 0, k: 1 }} selected={new Set()} selectedEdge="" onSelect={() => {}} onConnect={onConnect} onDropEmpty={onDropEmpty}/>
  </>;
}

// jsdom 不提供原生 PointerEvent，这里仅补齐测试所需的指针字段。
function pointer(type: string, x: number, y: number, target: EventTarget = window, id = 1) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, buttons: type === 'pointerup' ? 0 : 1, clientX: x, clientY: y });
  Object.defineProperty(event, 'pointerId', { value: id });
  target.dispatchEvent(event);
}
const start = (id = 'a-source', x = 120, y = 60) => pointer('pointerdown', x, y, host.querySelector(`[data-testid="${id}"]`)!);

beforeEach(() => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement('div'); document.body.append(host); root = createRoot(host);
  controller = createRef<CanvasConnectionController>(); frames = []; onConnect = vi.fn(); onDropEmpty = vi.fn(); parentRenders = 0;
  vi.spyOn(host, 'getBoundingClientRect').mockReturnValue({ left: 0, top: 0, right: 800, bottom: 600, width: 800, height: 600, x: 0, y: 0, toJSON() {} });
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation(callback => { frames.push(callback); return frames.length; });
  vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {});
  act(() => root.render(<Harness/>));
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.restoreAllMocks(); });

describe('独立连线拖动层', () => {
  it('合并高频移动为一帧，预览不刷新父编辑器或提交连线', () => {
    act(() => start());
    const renders = parentRenders;
    act(() => { for (let x = 150; x < 350; x++) pointer('pointermove', x, 60); });
    expect(frames).toHaveLength(1); expect(onConnect).not.toHaveBeenCalled();
    act(() => frames.shift()!(16));
    expect(parentRenders).toBe(renders);
    expect(host.querySelector('[data-connection-target="b"]')).not.toBeNull();
    expect(host.querySelector('[data-active-connection]')?.getAttribute('d')).toContain('300 60');
  });
  it('快速拖动在最后一帧前松手仍只提交一次，预览立即消失', () => {
    act(() => { start(); pointer('pointermove', 340, 60); pointer('pointerup', 340, 60); pointer('pointerup', 340, 60); });
    expect(onConnect).toHaveBeenCalledExactlyOnceWith('a', 'b');
    expect(host.querySelector('[data-active-connection]')).toBeNull();
    act(() => frames.forEach(frame => frame(16)));
    expect(host.querySelector('[data-active-connection]')).toBeNull();
  });
  it('从输入反向拖动到上游节点保持连线方向', () => {
    act(() => { start('b-target', 300, 60); pointer('pointermove', 50, 40); pointer('pointerup', 50, 40); });
    expect(onConnect).toHaveBeenCalledExactlyOnceWith('a', 'b');
  });
  it('保留两次点击端点连接，点击空白后不再悬挂预览', () => {
    act(() => { start(); pointer('pointerup', 120, 60); });
    expect(host.querySelector('[data-active-connection]')).not.toBeNull();
    act(() => start('b-target', 300, 60));
    expect(onConnect).toHaveBeenCalledExactlyOnceWith('a', 'b');
    act(() => { start(); pointer('pointerup', 120, 60); pointer('pointerdown', 600, 500, host); });
    expect(host.querySelector('[data-active-connection]')).toBeNull();
  });
  it('空白释放、取消指针、失焦和 Escape 都清除拖线且不提交', () => {
    for (const end of ['empty', 'pointercancel', 'blur', 'Escape']) {
      act(() => {
        start(); pointer('pointermove', 600, 500);
        if (end === 'empty') pointer('pointerup', 600, 500);
        else if (end === 'Escape') window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
        else window.dispatchEvent(new Event(end));
      });
      expect(host.querySelector('[data-active-connection]')).toBeNull();
    }
    expect(onConnect).not.toHaveBeenCalled();
  });
  it('重复连线和拖回自身不会提交，其他指针不干扰当前拖动', () => {
    act(() => root.render(<Harness connections={[{ id: 'edge', fromNodeId: 'a', toNodeId: 'b' }]}/>));
    act(() => { start(); pointer('pointermove', 340, 60); pointer('pointerup', 340, 60); });
    expect(onConnect).not.toHaveBeenCalled();
    act(() => { start(); pointer('pointermove', 50, 40); pointer('pointerup', 50, 40); });
    expect(onConnect).not.toHaveBeenCalled();
    act(() => { start(); pointer('pointerup', 340, 60, window, 2); });
    expect(host.querySelector('[data-active-connection]')).not.toBeNull();
  });
  it('漏收松手或意外丢失捕获时退出拖线，同时保留两次点击连接', () => {
    act(() => {
      start();
      const releasedMove = new MouseEvent('pointermove', { bubbles: true, buttons: 0, clientX: 340, clientY: 60 });
      Object.defineProperty(releasedMove, 'pointerId', { value: 1 });
      window.dispatchEvent(releasedMove);
    });
    expect(host.querySelector('[data-active-connection]')).toBeNull();
    act(() => { start(); pointer('lostpointercapture', 120, 60); });
    expect(host.querySelector('[data-active-connection]')).toBeNull();
    act(() => { start(); pointer('pointerup', 120, 60); pointer('lostpointercapture', 120, 60); });
    expect(host.querySelector('[data-active-connection]')).not.toBeNull();
    act(() => start('b-target', 300, 60));
    expect(onConnect).toHaveBeenCalledExactlyOnceWith('a', 'b');
  });
  // 吸附目标显示“连接到”提示；拖出的线落在空白处交给编辑器弹出新建菜单，靠近节点或点击端点模式不弹。
  it('吸附时标明连接目标，拖到空白处松手请求新建并连接', () => {
    act(() => { start(); pointer('pointermove', 340, 60); frames.shift()!(16); });
    expect(host.querySelector('[data-connection-target="b"]')?.textContent).toBe('连接到：B');
    act(() => { pointer('pointermove', 600, 500); pointer('pointerup', 600, 500); });
    expect(onConnect).not.toHaveBeenCalled();
    expect(onDropEmpty).toHaveBeenCalledExactlyOnceWith({ nodeId: 'a', handleType: 'source' }, { x: 600, y: 500 }, { x: 600, y: 500 });
    expect(host.querySelector('[data-active-connection]')).toBeNull();
    act(() => { start('b-target', 300, 60); pointer('pointermove', 50, 40); frames.splice(0).forEach(frame => frame(16)); });
    expect(host.querySelector('[data-connection-target="a"]')?.textContent).toBe('输入来自：A');
    act(() => { pointer('pointerup', 50, 40); });
    // 靠近节点但不可连（已存在同向连线）时直接取消，不弹新建菜单。
    act(() => root.render(<Harness connections={[{ id: 'edge', fromNodeId: 'a', toNodeId: 'b' }]}/>));
    act(() => { start(); pointer('pointermove', 405, 60); pointer('pointerup', 405, 60); });
    act(() => { start(); pointer('pointerup', 120, 60); pointer('pointerdown', 600, 500, host); });
    expect(onDropEmpty).toHaveBeenCalledTimes(1);
  });
});
