import type {CanvasNodeData, ViewportTransform} from '@/types/canvas';

// 延续 Infinite Canvas 的整体聚焦算法：工具条、节点、生成面板一同避开底部工具栏。
export function focusNodeLayout(node: Pick<CanvasNodeData,'position'|'width'|'height'>, size: {width:number;height:number}, below=0): ViewportTransform {
  const above=76;
  const usable=Math.max(100,size.height-100);
  const k=Math.max(.05,Math.min(1,size.width*.6/node.width,size.height*.6/node.height,(usable*.92-above-below)/node.height));
  const height=above+node.height*k+below;
  const slack=usable-height;
  const top=Math.max(0,slack*(below>0&&slack>0?.75:.5));
  return {x:size.width/2-(node.position.x+node.width/2)*k,y:top+above-node.position.y*k,k};
}

// 小视口只缩短提示词区域，操作栏保持固定高度且始终可见。
export function composerPromptHeight(height:number,extra=0){return Math.max(40,Math.min(160,height-540-extra));}
export function composerFootprint(height:number,extra=0){return composerPromptHeight(height,extra)+170+extra;}
