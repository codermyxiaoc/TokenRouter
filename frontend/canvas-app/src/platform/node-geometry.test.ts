import { afterEach, describe, expect, it, vi } from 'vitest';
import { getCanvas } from './api';
import { normalizeCanvasDocument } from './node-geometry';
import { clearSession, setSession } from './session';
import type { CanvasDocument } from './types';

afterEach(() => { clearSession(); vi.unstubAllGlobals(); });

describe('载入画布时补齐节点几何信息', () => {
  // 接口允许只含 id/type 的精简节点；载入后必须有合法坐标和尺寸，编辑器才不会读取 position.x 崩溃。
  it('缺少或非法的 position、width、height 补默认值，其它字段不变', () => {
    const doc = { nodes: [
      { id: 'a', type: 'image', title: 'A', metadata: { prompt: 'keep' } },
      { id: 'b', type: 'video', title: 'B', position: { x: 'bad' }, width: -1, height: Number.NaN },
      { id: 'c', type: 'text', title: 'C', position: { x: 5, y: 6 }, width: 300, height: 120 },
    ], connections: [], viewport: { x: 0, y: 0, k: 1 } } as unknown as CanvasDocument;
    const out = normalizeCanvasDocument(doc);
    expect(out.nodes[0]).toMatchObject({ position: { x: 0, y: 0 }, width: 340, height: 240, metadata: { prompt: 'keep' } });
    expect(out.nodes[1]).toMatchObject({ position: { x: 40, y: 40 }, width: 420, height: 236 });
    expect(out.nodes[2]).toBe(doc.nodes[2]);
    // 已合法的文档原样返回，不会触发无谓的保存。
    expect(normalizeCanvasDocument({ ...doc, nodes: [doc.nodes[2]] }).nodes[0]).toBe(doc.nodes[2]);
  });

  // 报告反例：节点字段完整但整个文档没有 viewport/connections 时，编辑器报 viewport 未定义并白屏。
  it('缺少 viewport、connections 或 title 时补默认值', () => {
    const doc = { nodes: [
      { id: 'a', type: 'image', position: { x: 1, y: 2 }, width: 340, height: 240 },
      { id: 'b', type: 'text', title: '   ', position: { x: 3, y: 4 }, width: 320, height: 200 },
    ] } as unknown as CanvasDocument;
    const out = normalizeCanvasDocument(doc);
    expect(out.viewport).toEqual({ x: 0, y: 0, k: 1 });
    expect(out.connections).toEqual([]);
    expect(out.nodes[0]).toMatchObject({ title: 'image' });
    expect(out.nodes[1]).toMatchObject({ title: 'text' });
    // 非法视口同样补默认值；已合法的视口与原对象保持不变，不触发无谓保存。
    expect(normalizeCanvasDocument({ nodes: [], connections: [], viewport: { x: 0, y: 0, k: 0 } } as unknown as CanvasDocument).viewport).toEqual({ x: 0, y: 0, k: 1 });
    const valid = { nodes: [], connections: [], viewport: { x: 1, y: 2, k: 1.5 } } as unknown as CanvasDocument;
    expect(normalizeCanvasDocument(valid)).toBe(valid);
  });

  it('getCanvas 返回的记录已补齐', async () => {
    setSession({ userId: 1, token: 'unit-test-token', locale: 'zh-CN', theme: 'light' });
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: { id: 'c1', title: 't', revision: 1, created_at: '', updated_at: '',
      document: { nodes: [{ id: 'n', type: 'config', title: 'x' }], connections: [], viewport: { x: 0, y: 0, k: 1 } } } }), { status: 200 })));
    const record = await getCanvas('c1');
    expect(record.document.nodes[0]).toMatchObject({ position: { x: 0, y: 0 }, width: 340, height: 240 });
  });
});
