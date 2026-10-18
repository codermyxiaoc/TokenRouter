import { useEffect, useState } from 'react';
import { Image, MessageSquare, Video, Music, Settings2, Play, LoaderCircle, Clock3 } from 'lucide-react';
import { Choice } from './Choice';
import { GenerationSettings } from './GenerationSettings';
import { configInputCounts, type ConfigInput } from '@/platform/config-composer';
import { generationSettingsSummary } from '@/platform/generation-settings';
import { mediaType, modelId, type GenerationKind, type Model } from '@/platform/types';
import type { CanvasNodeData, CanvasNodeMetadata } from '@/types/canvas';
import './config-node.css';

const modes = [{ value: 'image', label: '生图', icon: Image }, { value: 'text', label: '文本', icon: MessageSquare }, { value: 'video', label: '视频', icon: Video }, { value: 'audio', label: '音频', icon: Music }] as const;
export const configKind = (node: CanvasNodeData): GenerationKind => modes.some(mode => mode.value === node.metadata?.generationMode) ? node.metadata!.generationMode as GenerationKind : 'image';
const clearModelParameters = { model: undefined, image_size: undefined, aspect_ratio: undefined, quality: undefined, background: undefined, count: undefined, seconds: undefined, resolution: undefined, voice: undefined, audio_format: undefined, speed: undefined, max_output_tokens: undefined };

// 结构与 Infinite Canvas 的配置节点一致，模型和能力只取本站目录，不携带浏览器渠道配置。
export function ConfigNodePanel({ node, inputs, models, selected, busy, pending, running, reconciling, composerOpen, onChange, onComposerToggle, onGenerate }: {
  node: CanvasNodeData; inputs: ConfigInput[]; models: Model[]; selected: boolean; busy: boolean; pending: boolean; running: boolean; reconciling: boolean; composerOpen: boolean;
  onChange: (patch: Partial<CanvasNodeMetadata>) => void; onComposerToggle: () => void; onGenerate: () => void;
}) {
  const kind = configKind(node), counts = configInputCounts(inputs);
  const available = models.filter(model => mediaType(model) === kind), model = available.find(item => modelId(item) === node.metadata?.model);
  const [settingsOpen, setSettingsOpen] = useState(false), [anchor, setAnchor] = useState<HTMLButtonElement | null>(null);
  useEffect(() => { if (!selected) setSettingsOpen(false); }, [selected]);
  const hasPrompt = !!(node.metadata?.composerContent?.trim() || node.metadata?.prompt?.trim() || counts.text);
  const label = pending ? '核对提交' : running ? reconciling ? '任务核对中…' : '生成中…' : '开始生成';
  return <div className="config-node-panel" onWheel={event => event.stopPropagation()}>
    <header className="config-node-header"><strong>生成配置</strong><div className="config-mode-tabs" role="tablist" aria-label="生成类型" onPointerDown={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()}>
      {modes.map(({ value, label, icon: Icon }) => <button key={value} type="button" role="tab" aria-selected={kind === value} disabled={busy || running || pending} onClick={() => { setSettingsOpen(false); onChange({ ...clearModelParameters, generationMode: value }); }}><Icon size={14}/><span>{label}</span></button>)}
    </div></header>
    <div className="config-input-summary"><span>提示词 <b>{counts.text} 条</b></span><span>参考图 <b>{counts.image} 张</b></span><span>参考视频 <b>{counts.video} 个</b></span><span>参考音频 <b>{counts.audio} 个</b></span><button type="button" aria-expanded={composerOpen} onPointerDown={event => event.stopPropagation()} onClick={onComposerToggle}><Settings2 size={14}/>组装提示词</button></div>
    <div className="config-model-settings" onPointerDown={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()}>
      <Choice compact label="配置节点模型" value={node.metadata?.model || ''} options={available.map(item => ({ value: modelId(item), label: `${item.display_name || item.model}（${item.group_name}）` }))} onChange={value => { setSettingsOpen(false); onChange({ ...clearModelParameters, model: value }); }}/>
      <button ref={setAnchor} className="config-settings-summary" type="button" aria-label="打开配置节点设置" aria-expanded={settingsOpen} onClick={() => setSettingsOpen(value => !value)}><Settings2 size={15}/><span>{generationSettingsSummary(model, node.metadata, kind)}</span></button>
    </div>
    <button className="primary config-generate" type="button" aria-label={label} disabled={busy || (running && !pending) || (!pending && (!model || !hasPrompt))} onPointerDown={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()} onClick={onGenerate}>{reconciling ? <Clock3 size={16}/> : running || busy ? <LoaderCircle size={16} className="animate-spin"/> : <Play size={16}/>}<span>{!available.length && !pending && !running ? '管理员尚未开放此类模型' : label}</span></button>
    <GenerationSettings open={settingsOpen} onClose={() => setSettingsOpen(false)} anchor={anchor} kind={kind} node={node} model={model} onChange={onChange}/>
  </div>;
}
