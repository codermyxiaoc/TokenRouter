import { describe, expect, it } from 'vitest';
import { connectionEndpoints, findConnectionTarget } from './connection-drag';
import type { CanvasNodeData } from '@/types/canvas';

const node = (id: string, x: number, y = 0): CanvasNodeData => ({ id, type: 'image', title: id, position: { x, y }, width: 100, height: 80 });
const source = node('source', 0), target = node('target', 300);

describe('画布连线命中与方向', () => {
  it('支持在目标主体松手，反向拖动保持上游到下游方向', () => {
    const handle = { nodeId: 'source', handleType: 'source' as const };
    expect(findConnectionTarget([source, target], [], handle, { x: 350, y: 20 }, 1)?.id).toBe('target');
    expect(connectionEndpoints({ nodeId: 'target', handleType: 'target' }, 'source')).toEqual({ fromNodeId: 'source', toNodeId: 'target' });
  });
  it('命中容差按屏幕像素计算，缩放后仍容易命中', () => {
    const handle = { nodeId: 'source', handleType: 'source' as const };
    expect(findConnectionTarget([source, target], [], handle, { x: 260, y: 40 }, 0.5)?.id).toBe('target');
    expect(findConnectionTarget([source, target], [], handle, { x: 260, y: 40 }, 1)).toBeUndefined();
  });
  it('忽略自身、分组和重复有向连线', () => {
    const handle = { nodeId: 'source', handleType: 'source' as const };
    expect(findConnectionTarget([source, target], [], handle, { x: 30, y: 40 }, 1)).toBeUndefined();
    expect(findConnectionTarget([source, { ...target, type: 'group' }], [], handle, { x: 340, y: 40 }, 1)).toBeUndefined();
    expect(findConnectionTarget([source, target], [{ id: 'existing', fromNodeId: 'source', toNodeId: 'target' }], handle, { x: 340, y: 40 }, 1)).toBeUndefined();
  });
  it('节点主体优先于近邻端口，重叠节点按后添加顺序选择', () => {
    const handle = { nodeId: 'source', handleType: 'source' as const };
    const overlap = node('overlap', 310);
    expect(findConnectionTarget([source, target, overlap], [], handle, { x: 305, y: 40 }, 1)?.id).toBe('target');
    expect(findConnectionTarget([source, target, overlap], [], handle, { x: 340, y: 40 }, 1)?.id).toBe('overlap');
  });
});
