import { useEffect, useState, type ReactNode } from 'react';
import { Modal } from 'antd';
import { BookOpen } from 'lucide-react';
import { mediaType, modelId, type Model } from '@/platform/types';
import './model-usage-notes.css';

// 说明来自当前有权限的模型目录，不写入提示词、画布参数或生成请求。
export function videoModelUsageNotes(model?: Model): string {
  return model && mediaType(model) === 'video' && typeof model.usage_notes === 'string' ? model.usage_notes.trim() : '';
}

// 低矮窗口只保留单行摘要，和布局预留共用同一个断点。
export function modelUsageNotesFootprint(viewportHeight: number): number { return viewportHeight < 600 ? 44 : 92; }

// 正文始终按纯文本呈现；详情弹窗独立于画布缩放，不扩大生成面板或触发画布快捷键。
export function ModelUsageNotes({ model, compact = false, condensed = false }: { model?: Model; compact?: boolean; condensed?: boolean }) {
  const notes = videoModelUsageNotes(model);
  const identity = model ? modelId(model) : '';
  const [open, setOpen] = useState(false);
  useEffect(() => { setOpen(false); }, [identity, notes]);
  if (!notes || !model) return null;
  const stop = (event: { stopPropagation: () => void }) => event.stopPropagation();
  const showDetails = (event: { stopPropagation: () => void }) => { stop(event); setOpen(true); };
  const trigger = <button type="button" className={compact ? 'model-usage-notes-compact' : 'model-usage-notes-link'} aria-label="查看视频模型使用说明" title="查看视频模型使用说明" onClick={showDetails}>
    {compact && <BookOpen size={14}/>}<span>{compact ? '使用说明' : '详情'}</span>
  </button>;
  return <>
    <aside className={compact ? 'model-usage-notes-action' : `model-usage-notes${condensed ? ' condensed' : ''}`} aria-label="视频模型使用说明" data-canvas-no-zoom
      onPointerDown={stop} onMouseDown={stop} onDoubleClick={stop} onKeyDown={stop} onWheel={stop}>
      {compact ? trigger : <><div className="model-usage-notes-heading"><span><BookOpen size={14}/>使用说明</span>{trigger}</div><p>{notes}</p></>}
    </aside>
    <Modal title="视频模型使用说明" open={open} onCancel={() => setOpen(false)} width={640} centered destroyOnHidden transitionName="" maskTransitionName=""
      modalRender={(dialog: ReactNode) => <div onPointerDown={stop} onMouseDown={stop} onDoubleClick={stop} onWheel={stop}>{dialog}</div>}
      footer={<button type="button" aria-label="关闭使用说明" onClick={() => setOpen(false)}>关闭</button>}>
      <div className="model-usage-notes-details" data-canvas-no-zoom>
        <strong>{model.display_name || model.model}（{model.group_name}）</strong>
        <p>{notes}</p>
      </div>
    </Modal>
  </>;
}
