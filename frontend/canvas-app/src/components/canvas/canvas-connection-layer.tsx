import { forwardRef, memo, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type RefObject } from 'react';
import { ActiveConnectionPath, ConnectionPath } from './canvas-connections';
import { connectionEndpoints, findConnectionTarget, isNearAnyNode } from '@/lib/canvas/connection-drag';
import { canvasThemes } from '@/lib/canvas-theme';
import { useThemeStore } from '@/stores/use-theme-store';
import type { CanvasConnection, CanvasNodeData, ConnectionHandle, Position, ViewportTransform } from '@/types/canvas';

export type CanvasConnectionController = {
  begin: (event: ReactPointerEvent, nodeId: string, handleType: 'source' | 'target') => void;
  cancel: () => void;
};

type Props = {
  containerRef: RefObject<HTMLDivElement | null>;
  nodes: CanvasNodeData[];
  connections: CanvasConnection[];
  viewport: ViewportTransform;
  selected: Set<string>;
  selectedEdge: string;
  onSelect: (id: string) => void;
  onConnect: (from: string, to: string) => void;
  /** 拖线松手在空白处：由编辑器弹出“新建并连接”菜单；point 为画布坐标，client 为屏幕坐标。 */
  onDropEmpty?: (handle: ConnectionHandle, point: Position, client: Position) => void;
};

type Drag = {
  handle: ConnectionHandle;
  start: Position;
  client: Position;
  pointerId: number;
  moved: boolean;
  armed: boolean;
  capture?: Element;
};
type Preview = { handle: ConnectionHandle; point: Position; target?: CanvasNodeData };

// 连线预览独立更新，鼠标移动不重渲染整个编辑器，也不触发云端文档保存。
export const CanvasConnectionLayer = forwardRef<CanvasConnectionController, Props>(function CanvasConnectionLayer(props, ref) {
  const latest = useRef(props);
  latest.current = props;
  const drag = useRef<Drag | null>(null);
  const frame = useRef<number | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const theme = canvasThemes[useThemeStore(state => state.theme)];
  const nodeById = useMemo(() => new Map(props.nodes.map(node => [node.id, node])), [props.nodes]);

  const clearFrame = useCallback(() => {
    if (frame.current !== null) cancelAnimationFrame(frame.current);
    frame.current = null;
  }, []);
  const cancel = useCallback(() => {
    clearFrame();
    const current = drag.current;
    drag.current = null;
    if (current?.capture?.hasPointerCapture?.(current.pointerId)) current.capture.releasePointerCapture(current.pointerId);
    setPreview(null);
  }, [clearFrame]);

  const calculate = useCallback((current: Drag): Preview | null => {
    const { containerRef, nodes, connections, viewport } = latest.current;
    const rect = containerRef.current?.getBoundingClientRect();
    if (!rect || !nodes.some(node => node.id === current.handle.nodeId)) return null;
    const point = { x: (current.client.x - rect.left - viewport.x) / viewport.k, y: (current.client.y - rect.top - viewport.y) / viewport.k };
    const insideCanvas = current.client.x >= rect.left && current.client.x <= rect.right && current.client.y >= rect.top && current.client.y <= rect.bottom;
    return { handle: current.handle, point, target: insideCanvas ? findConnectionTarget(nodes, connections, current.handle, point, viewport.k) : undefined };
  }, []);

  const finish = useCallback((current: Drag, target: CanvasNodeData) => {
    const edge = connectionEndpoints(current.handle, target.id);
    cancel();
    latest.current.onConnect(edge.fromNodeId, edge.toNodeId);
  }, [cancel]);

  useImperativeHandle(ref, () => ({ cancel, begin(event, nodeId, handleType) {
    if (event.button !== 0) return;
    event.preventDefault();
    event.stopPropagation();
    const existing = drag.current;
    if (existing?.armed && nodeId !== existing.handle.nodeId) {
      // 点击另一个端点也可完成连线，方向仍由最初选择的端点决定。
      const node = latest.current.nodes.find(item => item.id === nodeId);
      const edge = connectionEndpoints(existing.handle, nodeId);
      if (node && node.type !== 'group' && !latest.current.connections.some(item => item.fromNodeId === edge.fromNodeId && item.toNodeId === edge.toNodeId)) finish(existing, node);
      else cancel();
      return;
    }
    cancel();
    const node = latest.current.nodes.find(item => item.id === nodeId);
    if (!node || node.type === 'group') return;
    const point = { x: event.clientX, y: event.clientY };
    const current: Drag = { handle: { nodeId, handleType }, start: point, client: point, pointerId: event.pointerId, moved: false, armed: false, capture: event.currentTarget };
    drag.current = current;
    event.currentTarget.setPointerCapture?.(event.pointerId);
    setPreview(calculate(current));
  } }), [calculate, cancel, finish]);

  useEffect(() => {
    const move = (event: PointerEvent) => {
      const current = drag.current;
      if (!current || (!current.armed && current.pointerId !== event.pointerId)) return;
      // 真正拖线时补查鼠标按键；两次点击端点的待连接模式则允许松键移动预览。
      if (!current.armed && event.pointerType !== 'touch' && (event.buttons & 1) === 0) { cancel(); return; }
      current.client = { x: event.clientX, y: event.clientY };
      if (Math.hypot(event.clientX - current.start.x, event.clientY - current.start.y) > 4) current.moved = true;
      if (frame.current !== null) return;
      frame.current = requestAnimationFrame(() => {
        frame.current = null;
        const active = drag.current;
        if (active) setPreview(calculate(active));
      });
    };
    const up = (event: PointerEvent) => {
      const current = drag.current;
      if (!current || (!current.armed && current.pointerId !== event.pointerId)) return;
      current.client = { x: event.clientX, y: event.clientY };
      current.moved ||= Math.hypot(event.clientX - current.start.x, event.clientY - current.start.y) > 4;
      // 松手时同步命中检测，不能依赖尚未执行的最后一帧，避免快拖漏连。
      const next = calculate(current);
      if (next?.target) { finish(current, next.target); return; }
      // 真正拖出的线落在画布空白处时交给编辑器新建节点；点击端点进入的待连接模式不弹菜单。
      const rect = latest.current.containerRef.current?.getBoundingClientRect();
      const insideCanvas = !!rect && current.client.x >= rect.left && current.client.x <= rect.right && current.client.y >= rect.top && current.client.y <= rect.bottom;
      if (next && current.moved && !current.armed && insideCanvas && latest.current.onDropEmpty && !isNearAnyNode(latest.current.nodes, next.point, latest.current.viewport.k)) {
        const { handle } = current, client = current.client;
        cancel();
        latest.current.onDropEmpty(handle, next.point, client);
        return;
      }
      if (!current.moved && !current.armed) {
        current.armed = true;
        if (current.capture?.hasPointerCapture?.(current.pointerId)) current.capture.releasePointerCapture(current.pointerId);
        return;
      }
      cancel();
    };
    const down = (event: PointerEvent) => {
      const current = drag.current;
      if (!current?.armed || event.button !== 0) return;
      const target = event.target instanceof Element ? event.target : null;
      if (target?.closest('.handle')) return;
      // 二次点击空白或工具栏退出连线，避免后续操作误连。
      cancel();
    };
    const key = (event: KeyboardEvent) => { if (event.key === 'Escape') cancel(); };
    const captureLost = (event: PointerEvent) => {
      const current = drag.current;
      if (current && !current.armed && current.pointerId === event.pointerId) cancel();
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up, true);
    window.addEventListener('pointerdown', down, true);
    window.addEventListener('pointercancel', cancel, true);
    window.addEventListener('lostpointercapture', captureLost, true);
    window.addEventListener('blur', cancel);
    window.addEventListener('keydown', key);
    return () => {
      clearFrame();
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up, true);
      window.removeEventListener('pointerdown', down, true);
      window.removeEventListener('pointercancel', cancel, true);
      window.removeEventListener('lostpointercapture', captureLost, true);
      window.removeEventListener('blur', cancel);
      window.removeEventListener('keydown', key);
    };
  }, [calculate, cancel, clearFrame, finish]);

  return <svg className="connections" style={{ overflow: 'visible', pointerEvents: 'none' }}>
    <SavedConnections nodes={props.nodes} connections={props.connections} selected={props.selected} selectedEdge={props.selectedEdge} onSelect={props.onSelect}/>
    {preview?.target && <ConnectionTargetHint target={preview.target} input={preview.handle.handleType === 'source'} zoom={props.viewport.k} stroke={theme.node.activeStroke}/>}
    {preview && <ActiveConnectionPath node={nodeById.get(preview.handle.nodeId)} handle={preview.handle} mouseWorld={preview.point} target={preview.target}/>}
  </svg>;
});

// 已有连线不随活动预览刷新，节点查找改为 Map，避免每条线重复扫描整个画布。
const SavedConnections = memo(function SavedConnections({ nodes, connections, selected, selectedEdge, onSelect }: Pick<Props, 'nodes' | 'connections' | 'selected' | 'selectedEdge' | 'onSelect'>) {
  const nodeById = useMemo(() => new Map(nodes.map(node => [node.id, node])), [nodes]);
  return connections.map(edge => {
    const from = nodeById.get(edge.fromNodeId), to = nodeById.get(edge.toNodeId);
    return from && to ? <ConnectionPath key={edge.id} connection={edge} from={from} to={to} active={selectedEdge === edge.id || selected.has(from.id) || selected.has(to.id)} onSelect={() => onSelect(edge.id)}/> : null;
  });
});

// 吸附目标使用实线高亮，并在节点上方标明“连接到”哪个节点与连接方向；尺寸按缩放倒数换算，始终保持屏幕大小。
function ConnectionTargetHint({ target, input, zoom, stroke }: { target: CanvasNodeData; input: boolean; zoom: number; stroke: string }) {
  const k = Math.max(zoom, 0.05), pad = 4 / k, fontSize = 12 / k;
  const text = `${input ? '连接到' : '输入来自'}：${target.title || '未命名节点'}`;
  const width = (Array.from(text).length * 12 + 20) / k, height = 24 / k;
  return <g data-connection-target={target.id} pointerEvents="none">
    <rect x={target.position.x - pad} y={target.position.y - pad} width={target.width + pad * 2} height={target.height + pad * 2} rx={24} fill={stroke} fillOpacity={0.06} stroke={stroke} strokeWidth={2.5 / k}/>
    <g transform={`translate(${target.position.x} ${target.position.y - pad - height - 6 / k})`}>
      <rect width={width} height={height} rx={height / 2} fill={stroke}/>
      <text x={10 / k} y={height / 2} dominantBaseline="central" fontSize={fontSize} fill="#fff" fontWeight={600}>{text}</text>
    </g>
  </g>;
}
