import type {CanvasDocument,Execution} from './types';
import {CanvasNodeType, type CanvasNodeData} from '@/types/canvas';

const types=new Set(['image','video','audio','text','config','group']);
const safeMetadata=new Set(['content','composerContent','prompt','status','errorDetails','fontSize','generationMode','generationType','model','size','quality','background','count','seconds','asset_id','asset_ids','execution_id','execution_ids','generation_key','generation_target_id','max_output_tokens','generation_request','generation_phase','expires_at','media_type','operation','image_size','aspect_ratio','resolution','voice','audio_format','speed','endpoint','inputOrder','mask_asset_id','naturalWidth','naturalHeight','mimeType','bytes','groupId']);
const textReconciliationCodes=new Set(['CREATIVE_TEXT_SUBMISSION_UNCONFIRMED','CREATIVE_TEXT_SETTLEMENT_PENDING']);
const audioReconciliationCodes=new Set(['CREATIVE_AUDIO_SUBMISSION_UNCONFIRMED','CREATIVE_AUDIO_SETTLEMENT_PENDING','CREATIVE_AUDIO_RELEASE_PENDING']);
const videoReconciliationCodes=new Set(['CREATIVE_VIDEO_SUBMISSION_UNCONFIRMED','CREATIVE_VIDEO_SETTLEMENT_PENDING','CREATIVE_VIDEO_RELEASE_PENDING','CREATIVE_VIDEO_RECONCILIATION']);
const imageReconciliationCodes=new Set(['CREATIVE_IMAGE_SUBMISSION_UNCONFIRMED','CREATIVE_IMAGE_SETTLEMENT_PENDING','CREATIVE_IMAGE_RELEASE_PENDING','CREATIVE_IMAGE_RECONCILIATION']);
const forbidden=/^(?:__proto__|prototype|constructor|script|api[_-]?key|token|authorization|base[_-]?url|html|on\w+|url|src|storageKey|videoTaskId)$/i;
function finite(value:unknown,min:number,max:number) {return typeof value==='number' && Number.isFinite(value) && value>=min && value<=max;}
function assertSafe(value:unknown,key='') {
  if(forbidden.test(key)) throw new Error('画布包含不可导入的凭证、脚本或外部资源字段');
  if(typeof value==='string' && /^(?:data:|blob:|javascript:)/i.test(value)) throw new Error('画布资源必须通过素材附件导入');
  if(Array.isArray(value)) value.forEach(item=>assertSafe(item));
  else if(value && typeof value==='object') for(const [name,child] of Object.entries(value)) assertSafe(child,name);
}
// 导入只接受数据，不接受原画布可执行插件/渠道配置；未知字段明确拒绝。
export function validateDocument(raw:unknown):CanvasDocument {
  if(!raw || typeof raw!=='object') throw new Error('画布文件格式错误');
  assertSafe(raw);
  const d=raw as CanvasDocument;
  const topKeys=new Set(['nodes','connections','viewport','backgroundMode']);
  if(Object.keys(d as object).some(key=>!topKeys.has(key))) throw new Error('画布包含不支持的顶层字段');
  if(!Array.isArray(d.nodes)||!Array.isArray(d.connections)||!d.viewport)throw new Error('画布缺少节点、连线或视口');
  if(!finite(d.viewport.x,-1e8,1e8)||!finite(d.viewport.y,-1e8,1e8)||!finite(d.viewport.k,.05,5))throw new Error('画布视口无效');
  const ids=new Set<string>();
  for(const n of d.nodes){
    if(!n || typeof n.id!=='string'||!n.id||ids.has(n.id)||!types.has(n.type)||typeof n.title!=='string')throw new Error('画布节点无效或重复');
    ids.add(n.id);
    if(Object.keys(n as object).some(key=>!['id','type','title','position','width','height','metadata'].includes(key))) throw new Error('画布节点包含不支持的字段');
    if(!n.position||!finite(n.position.x,-1e8,1e8)||!finite(n.position.y,-1e8,1e8)||!finite(n.width,40,100000)||!finite(n.height,40,100000))throw new Error('画布节点尺寸无效');
    // 音频参数必须保持可序列化标量，导入不会开放原插件的声音脚本。
    if(n.metadata){
      if(n.metadata.composerContent!==undefined&&typeof n.metadata.composerContent!=='string')throw new Error('组装提示词必须是纯文本');
      if(n.metadata.generation_target_id!==undefined&&(typeof n.metadata.generation_target_id!=='string'||n.metadata.generation_target_id.length>128))throw new Error('生成结果关联格式错误');
      if(n.metadata.max_output_tokens!==undefined&&(!Number.isInteger(n.metadata.max_output_tokens)||!finite(n.metadata.max_output_tokens,1,4096)))throw new Error('文本输出上限格式错误');
      for(const key of ['voice','audio_format'] as const)if(n.metadata[key]!==undefined&&typeof n.metadata[key]!=='string')throw new Error('语音参数格式错误');
      if(n.metadata.speed!==undefined&&!finite(n.metadata.speed,.7,1.5))throw new Error('语音参数格式错误');
    }
    if(n.metadata)for(const key of Object.keys(n.metadata))if(!safeMetadata.has(key))throw new Error(`不支持的节点字段：${key}`);
  }
  const connections=new Set<string>();
  for(const edge of d.connections){if(!edge||typeof edge.id!=='string'||connections.has(edge.id)||!ids.has(edge.fromNodeId)||!ids.has(edge.toNodeId)||edge.fromNodeId===edge.toNodeId)throw new Error('画布连线无效');connections.add(edge.id);}
  return structuredClone(d);
}
export function validateExecutionOwner(execution:Execution,canvasId:string,nodeId?:string) {return execution.canvas_id===canvasId&&(!nodeId||execution.node_id===nodeId);}

// 同节点接收任务结果，轮询只更新素材引用，不创建新节点或触发生成。
export function mergeExecution(document:CanvasDocument,execution:Execution,canvasId:string):CanvasDocument {
  if(!validateExecutionOwner(execution,canvasId)) return document;
  const target=document.nodes.find(n=>n.id===execution.node_id);
  if(!target) return document;
  if(target.metadata?.generation_key || target.metadata?.generation_request)return document;
  // 手工替换/复制已清除任务绑定，不能再被历史轮询覆盖。
  if(target.metadata?.execution_id!==execution.id)return document;
  const next=structuredClone(document);const node=next.nodes.find(n=>n.id===target.id)!;
  const reconciling=execution.status==='running'&&(execution.kind==='image'?imageReconciliationCodes.has(execution.error_code||''):execution.kind==='video'?videoReconciliationCodes.has(execution.error_code||''):execution.kind==='audio'?audioReconciliationCodes.has(execution.error_code||''):execution.kind==='text'&&textReconciliationCodes.has(execution.error_code||''));
  node.metadata={...node.metadata,execution_id:execution.id,status:execution.status==='succeeded'?'success':['failed','result_lost'].includes(execution.status)?'error':'loading',generation_phase:reconciling?'reconciliation':undefined,errorDetails:execution.error_message||undefined};
  if(execution.status==='succeeded'){if(execution.kind==='text'&&typeof execution.output_text==='string')node.metadata={...node.metadata,content:execution.output_text};else applyOutputAssets(node,execution.assets);}
  return JSON.stringify(next)===JSON.stringify(document)?document:next;
}

function applyOutputAssets(node:CanvasNodeData,assets:Execution['assets']) {
  if(!assets.length)return;
  const primary=assets.find(a=>a.id===node.metadata?.asset_id)||assets[0];
  node.metadata={...node.metadata,asset_ids:assets.map(a=>a.id),asset_id:primary.id,expires_at:primary.expires_at,mimeType:primary.mime_type,bytes:primary.byte_size};
}

// 按持久批次键聚合独立账务任务；旧批次、其他用户画布的回包不能覆盖当前节点。
export function mergeExecutions(document:CanvasDocument,executions:Execution[],canvasId:string):CanvasDocument {
  let next=document;
  for(const execution of executions)if(!execution.batch_id)next=mergeExecution(next,execution,canvasId);
  const nodes=next.nodes.map(target=>{
    const key=target.metadata?.generation_key;
    if(!key)return target;
    const batch=executions.filter(e=>validateExecutionOwner(e,canvasId,target.id)&&e.batch_id===key).sort((a,b)=>(a.batch_index||0)-(b.batch_index||0));
    if(!batch.length)return target;
    const expected=Math.max(...batch.map(e=>e.batch_size||1));
    const settled=batch.length===expected&&batch.every(e=>['succeeded','failed','result_lost'].includes(e.status));
    const failures=batch.filter(e=>e.status==='failed'||e.status==='result_lost');
    // 图片通常走批次接口；任一子任务待核对时保留非终态，不能被聚合误画成普通生成或失败。
    const reconciliation=batch.filter(e=>e.status==='running'&&imageReconciliationCodes.has(e.error_code||''));
    const assets=[...new Map(batch.filter(e=>e.status==='succeeded').flatMap(e=>e.assets||[]).map(a=>[a.id,a])).values()];
    const failureMessage=failures.map(e=>e.error_message||'图片生成失败').filter((value,index,list)=>list.indexOf(value)===index).join('；');
    const reconciliationMessage=reconciliation.map(e=>e.error_message||'图片任务结果正在核对，请勿重复生成').filter((value,index,list)=>list.indexOf(value)===index).join('；');
    const message=[failureMessage?(assets.length?`已完成 ${assets.length} 张，部分任务失败：${failureMessage}`:failureMessage):'',reconciliationMessage].filter(Boolean).join('；');
    const node={...target,metadata:{...target.metadata,execution_id:batch[0].id,execution_ids:batch.map(e=>e.id),generation_request:undefined,status:settled?(failures.length?'error':'success'):'loading',generation_phase:reconciliation.length?'reconciliation':undefined,errorDetails:message||undefined}} as CanvasNodeData;
    applyOutputAssets(node,assets);
    return JSON.stringify(node)===JSON.stringify(target)?target:node;
  });
  return nodes.every((n,i)=>n===next.nodes[i])?next:{...next,nodes};
}
export function selectedReferences(document:CanvasDocument,nodeId:string) {
  const ids=document.connections.filter(c=>c.toNodeId===nodeId).map(c=>c.fromNodeId);
  const input=document.nodes.filter(n=>ids.includes(n.id));
  const expanded=input.flatMap(n=>n.type===CanvasNodeType.Group?document.nodes.filter(child=>child.metadata?.groupId===n.id):[n]);
  return [...new Map(expanded.map(n=>[n.id,n])).values()];
}
export function clearExecutionMetadata(nodes:CanvasNodeData[]) {
  return nodes.map(node=>({...node,metadata:node.metadata?{...node.metadata,execution_id:undefined,execution_ids:undefined,generation_key:undefined,generation_target_id:undefined,generation_request:undefined,generation_phase:undefined,status:node.metadata.asset_id?'success' as const:'idle' as const,errorDetails:undefined}:undefined}));
}

// 只保护在途任务与响应未知的意图；终态记录不妨碍正常删除、复制或编辑撤销。
export function protectedGenerationNodeIds(document:CanvasDocument):Set<string> {
  const ids=new Set(document.nodes.filter(node=>node.metadata?.generation_request||(node.metadata?.status==='loading'&&(node.metadata.execution_id||node.metadata.generation_key))).map(node=>node.id));
  for(const node of document.nodes)if(node.type==='config'&&node.metadata?.generation_target_id&&ids.has(node.metadata.generation_target_id))ids.add(node.id);
  return ids;
}

// 撤销只恢复编辑内容，不能撤销已经受理的收费任务或抹掉响应未知的幂等键。
export function restoreHistoryDocument(history:CanvasDocument,current:CanvasDocument):CanvasDocument {
  const protectedIds=protectedGenerationNodeIds(current);
  const nodes=history.nodes.map(node=>{
    const live=current.nodes.find(n=>n.id===node.id);
    if(!live)return node;
    const metadata=live.metadata;
    if(!metadata?.generation_key&&!metadata?.generation_request&&!metadata?.execution_id&&!metadata?.generation_target_id)return clearExecutionMetadata([node])[0];
    return {...node,metadata:{...node.metadata,generation_target_id:metadata.generation_target_id,generation_key:metadata.generation_key,generation_request:metadata.generation_request,generation_phase:metadata.generation_phase,execution_id:metadata.execution_id,execution_ids:metadata.execution_ids,status:metadata.status,errorDetails:metadata.errorDetails,asset_id:metadata.asset_id,asset_ids:metadata.asset_ids,expires_at:metadata.expires_at,mimeType:metadata.mimeType,bytes:metadata.bytes}};
  });
  // 生成配置的新结果可能尚未出现在历史快照，撤销不能删除它唯一的持久提交意图。
  for(const node of current.nodes)if(protectedIds.has(node.id)&&!nodes.some(item=>item.id===node.id))nodes.push(node);
  const nodeIds=new Set(nodes.map(node=>node.id));
  const connections=[...history.connections.filter(edge=>nodeIds.has(edge.fromNodeId)&&nodeIds.has(edge.toNodeId))];
  for(const edge of current.connections)if(protectedIds.has(edge.toNodeId)&&current.nodes.find(node=>node.id===edge.fromNodeId)?.metadata?.generation_target_id===edge.toNodeId&&nodeIds.has(edge.fromNodeId)&&nodeIds.has(edge.toNodeId)&&!connections.some(item=>item.id===edge.id))connections.push(edge);
  return {...history,nodes,connections};
}
