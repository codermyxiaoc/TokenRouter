import type { CanvasNodeData } from '@/types/canvas';
import type { CanvasDocument } from './types';

export type ConfigInput = { node: CanvasNodeData; label: string; resources: CanvasNodeData[] };
export const configReferencePattern = /@\[node:([^\]\r\n]+)\]/g;
const names: Record<string, string> = { image: '图片', text: '文本', video: '视频', audio: '音频', group: '组' };
const isResource = (node: CanvasNodeData) => ['image', 'text', 'video', 'audio'].includes(node.type);

// 只按当前画布入边顺序解析引用；组展开有循环保护，同一素材不会因重复连接被重复提交。
export function configInputs(document: CanvasDocument, nodeId: string): ConfigInput[] {
  const byId = new Map(document.nodes.map(node => [node.id, node]));
  const seen = new Set<string>(), counts: Record<string, number> = {};
  const expand = (node: CanvasNodeData, ancestors = new Set<string>()): CanvasNodeData[] => {
    if (isResource(node)) return [node];
    if (node.type !== 'group' || ancestors.has(node.id)) return [];
    const visited = new Set(ancestors).add(node.id);
    return document.nodes.filter(child => child.metadata?.groupId === node.id).flatMap(child => expand(child, visited));
  };
  return document.connections.filter(edge => edge.toNodeId === nodeId).flatMap(edge => {
    const node = byId.get(edge.fromNodeId);
    if (!node || seen.has(node.id) || (!isResource(node) && node.type !== 'group')) return [];
    seen.add(node.id);
    counts[node.type] = (counts[node.type] || 0) + 1;
    return [{ node, label: `${names[node.type]}${counts[node.type]}`, resources: expand(node) }];
  });
}

export function flattenConfigInputs(inputs: ConfigInput[]): CanvasNodeData[] {
  return [...new Map(inputs.flatMap(input => input.resources).map(node => [node.id, node])).values()];
}

export function configInputCounts(inputs: ConfigInput[]) {
  const nodes = flattenConfigInputs(inputs);
  return { text: nodes.filter(node => node.type === 'text').length, image: nodes.filter(node => node.type === 'image').length, video: nodes.filter(node => node.type === 'video').length, audio: nodes.filter(node => node.type === 'audio').length };
}

// 引用仅存节点 ID 的纯文本标记；组装时校验仍有连接，绝不从其它节点或外部 URL 补数据。
export function compileConfigPrompt(document: CanvasDocument, source: CanvasNodeData): { prompt: string; inputs: CanvasNodeData[] } {
  const inputs = configInputs(document, source.id);
  const composer = source.metadata?.composerContent;
  if (!composer?.trim()) {
    const resources = flattenConfigInputs(inputs);
    return { prompt: [source.metadata?.prompt || '', ...resources.filter(node => node.type === 'text').map(node => node.metadata?.content || '')].filter(Boolean).join('\n\n').trim(), inputs: resources };
  }
  const byId = new Map(inputs.map(input => [input.node.id, input]));
  const selected = new Set<string>();
  for (const match of composer.matchAll(configReferencePattern)) {
    const input = byId.get(match[1]);
    if (!input) throw new Error('组装提示词包含已删除或未连接的引用，请重新选择 @ 引用');
    input.resources.forEach(node => selected.add(node.id));
  }
  const resources = flattenConfigInputs(inputs).filter(node => selected.has(node.id));
  const counts: Record<string, number> = {}, labels = new Map<string, string>();
  resources.forEach(node => { counts[node.type] = (counts[node.type] || 0) + 1; labels.set(node.id, `${names[node.type]}${counts[node.type]}`); });
  // 编号按连接顺序而非输入标记顺序，确保发送的素材数组与提示词中的图片编号一致。
  let prompt = composer.replace(configReferencePattern, (_token, id: string) => byId.get(id)!.resources.map(node => `【${labels.get(node.id)}】`).filter((value, index, all) => all.indexOf(value) === index).join('、'));
  const texts = resources.filter(node => node.type === 'text').map(node => `【${labels.get(node.id)}】\n${node.metadata?.content || ''}`);
  if (texts.length) prompt = `${prompt.trim()}\n\n${texts.join('\n\n')}`;
  return { prompt: prompt.trim(), inputs: resources };
}

// 复制时只重写本次复制集合的节点身份；未复制的引用保持失效，不能悄悄连回原节点。
export function remapConfigReferences(value: string | undefined, ids: Map<string, string>): string | undefined {
  return value?.replace(configReferencePattern, (token, id: string) => ids.has(id) ? `@[node:${ids.get(id)}]` : token);
}
