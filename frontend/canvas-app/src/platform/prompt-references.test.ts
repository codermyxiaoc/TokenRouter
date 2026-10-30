import { describe, expect, it } from 'vitest';
import { deleteAdjacentLabel, promptReferences, splitPromptLabels } from './prompt-references';
import type { CanvasNodeData } from '@/types/canvas';

const node = (id: string, type: string): CanvasNodeData => ({ id, type, title: id, position: { x: 0, y: 0 }, width: 10, height: 10 });

describe('提示词 @ 引用标签', () => {
  // 编号按传入顺序分类型递增，与提交的参考图顺序一致；分组等非素材节点不参与编号。
  it('按素材顺序分类型编号', () => {
    expect(promptReferences([node('a', 'image'), node('t', 'text'), node('b', 'image'), node('g', 'group'), node('v', 'video')]).map(item => `${item.node.id}:${item.label}`))
      .toEqual(['a:图片1', 't:文本1', 'b:图片2', 'v:视频1']);
  });
  it('删除键整体删除相邻标签，长标签优先匹配', () => {
    const labels = ['图片1', '图片10'];
    expect(deleteAdjacentLabel('参考 图片10 ', 10, 'backward', labels)).toEqual({ value: '参考 ', caret: 3 });
    expect(deleteAdjacentLabel('参考 图片1', 7, 'backward', labels)).toEqual({ value: '参考 ', caret: 3 });
    expect(deleteAdjacentLabel('把 图片1 变亮', 2, 'forward', labels)).toEqual({ value: '把 变亮', caret: 2 });
    expect(deleteAdjacentLabel('普通文字', 4, 'backward', labels)).toBeNull();
    expect(deleteAdjacentLabel('x图片1', 1, 'forward', labels)).toBeNull();
    // 光标位于“图片10”的“1”之后（后面还有数字）时不能误删“图片1”。
    expect(deleteAdjacentLabel('参考 图片10', 6, 'backward', ['图片1'])).toBeNull();
  });
  it('高亮只匹配完整标签，不把“图片10”拆成“图片1”', () => {
    expect(splitPromptLabels('用图片10和图片1', ['图片1'])).toEqual([{ text: '用图片10和', label: false }, { text: '图片1', label: true }]);
    expect(splitPromptLabels('无引用', [])).toEqual([{ text: '无引用', label: false }]);
  });
});
