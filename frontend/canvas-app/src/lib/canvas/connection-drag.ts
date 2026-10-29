import type { CanvasConnection, CanvasNodeData, ConnectionHandle, Position } from '@/types/canvas';

// 命中范围使用屏幕像素，缩小画布后仍能轻松把连线放到节点上。
const HANDLE_RADIUS = 24;
const NODE_PADDING = 10;

export function connectionEndpoints(handle: ConnectionHandle, targetId: string) {
  return handle.handleType === 'source'
    ? { fromNodeId: handle.nodeId, toNodeId: targetId }
    : { fromNodeId: targetId, toNodeId: handle.nodeId };
}

export function findConnectionTarget(
  nodes: CanvasNodeData[], connections: CanvasConnection[], handle: ConnectionHandle,
  point: Position, zoom: number,
): CanvasNodeData | undefined {
  const radius = HANDLE_RADIUS / Math.max(zoom, 0.05);
  const padding = NODE_PADDING / Math.max(zoom, 0.05);
  let best: CanvasNodeData | undefined;
  let priority = Infinity;
  // 与节点层叠顺序一致；同一优先级保留最后添加的可用节点。
  for (let index = nodes.length - 1; index >= 0; index--) {
    const node = nodes[index];
    if (node.id === handle.nodeId || node.type === 'group') continue;
    const edge = connectionEndpoints(handle, node.id);
    if (connections.some(item => item.fromNodeId === edge.fromNodeId && item.toNodeId === edge.toNodeId)) continue;
    const { x, y } = node.position;
    const anchorX = handle.handleType === 'source' ? x : x + node.width;
    const anchorY = y + node.height / 2;
    const inside = point.x >= x && point.x <= x + node.width && point.y >= y && point.y <= y + node.height;
    const handleHit = (point.x - anchorX) ** 2 + (point.y - anchorY) ** 2 <= radius ** 2;
    const expanded = point.x >= x - padding && point.x <= x + node.width + padding && point.y >= y - padding && point.y <= y + node.height + padding;
    const nextPriority = inside ? 0 : handleHit ? 1 : expanded ? 2 : Infinity;
    if (nextPriority < priority) { best = node; priority = nextPriority; }
  }
  return best;
}

// 松手位置靠近任意节点（含已连接或不可连的节点）时不弹出新建菜单，避免把“没连上”误当作“要新建”。
export function isNearAnyNode(nodes: CanvasNodeData[], point: Position, zoom: number): boolean {
  const padding = NODE_PADDING / Math.max(zoom, 0.05);
  return nodes.some(node => node.type !== 'group' && point.x >= node.position.x - padding && point.x <= node.position.x + node.width + padding && point.y >= node.position.y - padding && point.y <= node.position.y + node.height + padding);
}

// 预览与最终路径共用曲线，吸附完成时不突然改变弯曲形状。
export function connectionCurve(start: Position, end: Position) {
  const curvature = Math.max(Math.abs(end.x - start.x) * 0.5, 50);
  return `M ${start.x} ${start.y} C ${start.x + curvature} ${start.y}, ${end.x - curvature} ${end.y}, ${end.x} ${end.y}`;
}
