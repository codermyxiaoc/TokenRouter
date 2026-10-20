import { useEffect, useState, type ReactNode } from 'react';
import { Modal } from 'antd';
import type { CanvasNodeData } from '@/types/canvas';

// 详情只展示服务端提供的安全原因；查看提示不查询、重提任务，也不推断退款或终态。
export function GenerationStatusDetails({ node, compact = false }: { node: CanvasNodeData; compact?: boolean }) {
  const [open, setOpen] = useState(false);
  const reconciling = !!node.metadata?.generation_request || (node.metadata?.status === 'loading' && node.metadata.generation_phase === 'reconciliation');
  const failed = node.metadata?.status === 'error';
  const visible = failed || reconciling;
  useEffect(() => { if (!visible) setOpen(false); }, [visible]);
  if (!visible) return null;
  const title = reconciling ? '任务核对详情' : '生成失败详情';
  const message = node.metadata?.errorDetails || (reconciling ? '任务结果正在核对，请勿重复生成' : '生成失败，请联系管理员核对任务');
  return <>
    <button type="button" className={compact ? 'canvas-generation-details-button' : 'node-error'} title="查看完整任务说明"
      aria-label={title} onPointerDown={event => event.stopPropagation()} onMouseDown={event => event.stopPropagation()}
      onDoubleClick={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()}
      onClick={event => { event.stopPropagation(); setOpen(true); }}>
      {compact ? '查看详情' : message}
    </button>
    <Modal title={title} open={open} onCancel={() => setOpen(false)} centered destroyOnHidden
      modalRender={(dialog: ReactNode) => <div onPointerDown={event => event.stopPropagation()} onMouseDown={event => event.stopPropagation()} onDoubleClick={event => event.stopPropagation()}>{dialog}</div>}
      transitionName="" maskTransitionName="" footer={<button type="button" aria-label="关闭任务详情" onClick={() => setOpen(false)}>关闭</button>}>
      <div className="generation-status-details" data-canvas-no-zoom onPointerDown={event => event.stopPropagation()}>
        <p style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{message}</p>
        {reconciling && <p>页面会继续更新任务状态。请勿重复生成；是否扣费或释放预扣以服务端最终结算为准。</p>}
      </div>
    </Modal>
  </>;
}
