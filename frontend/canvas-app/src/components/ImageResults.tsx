import { useState, type CSSProperties } from 'react';
import { ChevronRight, Copy, Download, Star } from 'lucide-react';
import { canvasThemes } from '@/lib/canvas-theme';
import { isAssetExpired } from '@/platform/api';
import { useThemeStore } from '@/stores/use-theme-store';
import type { CanvasNodeData } from '@/types/canvas';
import { AssetView } from './AssetView';
import './image-results.css';

type ImageResultsProps = {
  node: CanvasNodeData;
  onSelect: (assetId: string) => void;
  onDownload: (assetId: string) => void;
  onExtract?: (assetId: string) => void;
};

// 批量结果属于同一个画布节点；展开仅改变展示，不创建节点或提交生成请求。
export function ImageResults({ node, onSelect, onDownload, onExtract }: ImageResultsProps) {
  const theme = canvasThemes[useThemeStore(state => state.theme)];
  const [expandedNode, setExpandedNode] = useState<string | null>(null);
  const storedIds = Array.isArray(node.metadata?.asset_ids) ? node.metadata.asset_ids : [];
  const ids = [...new Set(storedIds.filter(id => typeof id === 'string' && id.length > 0))];
  if (!ids.length && node.metadata?.asset_id) ids.push(node.metadata.asset_id);
  const primaryId = node.metadata?.asset_id && ids.includes(node.metadata.asset_id) ? node.metadata.asset_id : ids[0];
  const expanded = ids.length > 1 && expandedNode === node.id;
  const primaryExpired = primaryId === node.metadata?.asset_id && isAssetExpired({ expires_at: node.metadata?.expires_at || '' });
  const style: CSSProperties & Record<`--image-results-${string}`, string> = {
    '--image-results-panel': theme.node.panel,
    '--image-results-fill': theme.node.fill,
    '--image-results-stroke': theme.node.stroke,
    '--image-results-text': theme.node.text,
    '--image-results-toolbar': theme.toolbar.panel,
    '--image-results-active': theme.node.activeStroke,
    // 折叠结果完全重合，不再绘制偏移的装饰纸张；展开时才允许结果卡向外铺开。
    overflow: expanded ? 'visible' : 'hidden',
  };
  const columns = Math.min(ids.length, 4);
  const rows = Math.ceil(ids.length / Math.max(columns, 1));
  const rootSlot = (rows - 1) * columns;

  return <div className="image-results" style={style} data-expanded={expanded || undefined}>
    {expanded && ids.filter(id => id !== primaryId).map((id, index) => {
      // 与原画布一致：主图保留在末行第一列，其余结果向上、向右展开。
      const slot = index >= rootSlot ? index + 1 : index;
      const x = slot % columns * (node.width + 18);
      const y = (Math.floor(slot / columns) - rows + 1) * (node.height + 18);
      const imageNumber = ids.indexOf(id) + 1;
      const cardStyle: CSSProperties & Record<`--image-results-${string}`, string> = {
        left: x, top: y, width: node.width, height: node.height,
        '--image-results-from-x': `${-x}px`, '--image-results-from-y': `${-y}px`,
        animationDelay: `${index * 35}ms`,
      };
      return <div key={id} className="image-results-card" style={cardStyle} role="group" aria-label={`第 ${imageNumber} 张图片`} onPointerDown={event => event.stopPropagation()} onMouseDown={event => event.stopPropagation()} onDoubleClick={event => event.stopPropagation()}>
        <AssetView id={id}/>
        <div className="image-results-card-actions" data-canvas-no-zoom onKeyDown={event => event.stopPropagation()}>
          <button type="button" aria-label={`下载第 ${imageNumber} 张图片`} title="下载" onClick={event => { event.stopPropagation(); onDownload(id); }}><Download size={12}/><span>下载</span></button>
          {onExtract && <button type="button" aria-label={`提取第 ${imageNumber} 张图片到新节点`} title="提取到新节点" onClick={event => { event.stopPropagation(); onExtract(id); }}><Copy size={12}/><span>提取</span></button>}
          <button type="button" aria-label={`将第 ${imageNumber} 张图片设为主图`} title="设为主图" onClick={event => { event.stopPropagation(); onSelect(id); }}><Star size={12}/><span>设为主图</span></button>
        </div>
      </div>;
    })}
    <div className="image-results-primary"><AssetView id={primaryId} expired={primaryExpired}/></div>
    {primaryId && !primaryExpired && <div className="image-results-primary-actions" data-canvas-no-zoom onPointerDown={event => event.stopPropagation()} onMouseDown={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()}>
      <button type="button" aria-label="下载主图" title="下载主图" onClick={event => { event.stopPropagation(); onDownload(primaryId); }}><Download size={12}/><span>下载</span></button>
      {onExtract && <button type="button" aria-label="提取主图到新节点" title="提取到新节点" onClick={event => { event.stopPropagation(); onExtract(primaryId); }}><Copy size={12}/><span>提取</span></button>}
    </div>}
    {ids.length > 1 && <button type="button" className="image-results-toggle" data-canvas-no-zoom aria-label={`${expanded ? '收起' : '展开'} ${ids.length} 张图片`} aria-expanded={expanded} onPointerDown={event => event.stopPropagation()} onMouseDown={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()} onClick={event => { event.stopPropagation(); setExpandedNode(expanded ? null : node.id); }}><span>{ids.length} 张</span><ChevronRight size={14}/></button>}
  </div>;
}
