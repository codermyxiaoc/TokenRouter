import { Clock3, LoaderCircle } from 'lucide-react';
import type { CanvasNodeData } from '@/types/canvas';
import './canvas-generation-loading.css';

// 提交、排队和处理阶段已统一映射为 loading；只读此状态，不推断或更改任务生命周期。
export function CanvasGenerationLoading({ node }: { node: CanvasNodeData }) {
  if (!['image', 'video', 'audio', 'text'].includes(node.type) || node.metadata?.status !== 'loading') return null;
  const hasMedia = !!node.metadata.content || !!node.metadata.asset_id || !!node.metadata.asset_ids?.length;
  // 核对是非终态，停转动画但继续轮询，不能向用户暗示生成已失败或预扣已退回。
  const reconciling = ['image', 'video', 'audio', 'text'].includes(node.type) && node.metadata.generation_phase === 'reconciliation';
  return <div className={`canvas-generation-loading${hasMedia ? ' has-media' : ''}`} role="status" aria-label={reconciling ? `${node.type === 'text' ? '文本' : node.type === 'audio' ? '音频' : node.type === 'video' ? '视频' : '图片'}任务核对中` : `${node.type === 'text' ? '文本' : node.type === 'audio' ? '音频' : node.type === 'video' ? '视频' : '图片'}生成中`}>
    <div className="canvas-generation-loading-indicator">
      {reconciling ? <Clock3 size={32} strokeWidth={1.5} aria-hidden="true"/> : <LoaderCircle size={32} strokeWidth={1.5} className="animate-spin" aria-hidden="true"/>}
      <span>{reconciling ? '正在核对' : '生成中'}</span>
      {reconciling && <span className="canvas-generation-status-details">{node.metadata.errorDetails || '任务结果正在核对，请勿重复生成'}</span>}
    </div>
  </div>;
}
