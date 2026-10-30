import type { CanvasNodeData } from '@/types/canvas';

export type PromptReference = { node: CanvasNodeData; label: string };

const names: Record<string, string> = { image: '图片', text: '文本', video: '视频', audio: '音频' };

// 标签按传入顺序分类型编号；调用方必须传入与提交素材相同顺序的列表，保证“图片1”就是发送的第一张参考图。
export function promptReferences(nodes: CanvasNodeData[]): PromptReference[] {
  const counts: Record<string, number> = {};
  return nodes.filter(node => names[node.type]).map(node => {
    counts[node.type] = (counts[node.type] || 0) + 1;
    return { node, label: `${names[node.type]}${counts[node.type]}` };
  });
}

// 删除键位于完整标签旁时整体删除，避免留下“图片”这类半截标签；标签按长度降序匹配，“图片10”优先于“图片1”。
export function deleteAdjacentLabel(value: string, caret: number, direction: 'backward' | 'forward', labels: string[]): { value: string; caret: number } | null {
  const ordered = [...labels].sort((a, b) => b.length - a.length);
  if (direction === 'backward') {
    // 光标后紧跟数字时说明位于更长的编号中间（如“图片10”的“1”之后），不能当作完整标签删除。
    if (/[0-9]/.test(value[caret] ?? '')) return null;
    const before = value.slice(0, caret);
    for (const label of ordered) {
      const match = new RegExp(`(^|\\s)(${escapeRegExp(label)})\\s?$`).exec(before);
      if (match) {
        const start = match.index + match[1].length;
        return { value: value.slice(0, start) + value.slice(caret), caret: start };
      }
    }
    return null;
  }
  if (caret > 0 && !/\s/.test(value[caret - 1])) return null;
  const after = value.slice(caret);
  for (const label of ordered) {
    const match = new RegExp(`^(${escapeRegExp(label)})(?=\\s|$)\\s?`).exec(after);
    if (match) return { value: value.slice(0, caret) + value.slice(caret + match[0].length), caret };
  }
  return null;
}

// 只把独立成词的标签视为引用，避免“图片10”被拆成“图片1”加“0”。
export function splitPromptLabels(value: string, labels: string[]): { text: string; label: boolean }[] {
  if (!labels.length || !value) return [{ text: value, label: false }];
  const ordered = [...labels].sort((a, b) => b.length - a.length);
  const pattern = new RegExp(`(${ordered.map(escapeRegExp).join('|')})(?![0-9])`, 'g');
  const parts: { text: string; label: boolean }[] = [];
  let end = 0;
  for (const match of value.matchAll(pattern)) {
    if (match.index! > end) parts.push({ text: value.slice(end, match.index), label: false });
    parts.push({ text: match[0], label: true });
    end = match.index! + match[0].length;
  }
  if (end < value.length) parts.push({ text: value.slice(end), label: false });
  return parts;
}

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
