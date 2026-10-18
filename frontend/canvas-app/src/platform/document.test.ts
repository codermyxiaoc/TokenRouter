import {describe,expect,it} from 'vitest';
import {mergeExecution,mergeExecutions,clearExecutionMetadata,restoreHistoryDocument,validateDocument,protectedGenerationNodeIds} from './document';
import type {CanvasDocument,Execution} from './types';
import type {CanvasNodeData} from '@/types/canvas';

const doc:CanvasDocument={nodes:[{id:'n1',type:'text',title:'提示词',position:{x:0,y:0},width:120,height:80,metadata:{content:'hello'}}],connections:[],viewport:{x:0,y:0,k:1},backgroundMode:'lines'};
describe('云端画布安全文档',()=>{
 it('配置新结果在途或提交未知时撤销不能丢弃意图，终态后仍允许正常移除',()=>{
   const request={key:'frozen',kind:'text' as const,params:{group_id:1,model:'text',prompt:'原文',operation:'generate',max_output_tokens:1024},count:1};
   const source={...doc.nodes[0],id:'source',type:'config',metadata:{generation_target_id:'result'}};
   const result={...doc.nodes[0],id:'result',type:'text',metadata:{generation_request:request,status:'error' as const}};
   const current={...doc,nodes:[source,result],connections:[{id:'result-edge',fromNodeId:'source',toNodeId:'result'}]};
   const empty={...doc,nodes:[]};
   expect(protectedGenerationNodeIds(current)).toEqual(new Set(['source','result']));
   const restored=restoreHistoryDocument(empty,current);expect(restored.nodes).toHaveLength(2);expect(restored.connections).toHaveLength(1);expect(restored.nodes[1].metadata?.generation_request).toEqual(request);
   current.nodes[1]={...result,metadata:{execution_id:'done',status:'success',content:'结果'} as never};
   expect(protectedGenerationNodeIds(current).size).toBe(0);expect(restoreHistoryDocument(empty,current).nodes).toHaveLength(0);
 });
 it('文本执行只将本站同一节点的output_text作为纯文本写回，不影响资产或其他归属',()=>{
   const original={...doc,nodes:[{...doc.nodes[0],metadata:{execution_id:'text-e',status:'loading' as const}}]};
   const execution:Execution={id:'text-e',canvas_id:'c1',node_id:'n1',kind:'text',status:'succeeded',task_id:'t1',assets:[],output_text:'<script>纯文本</script>',created_at:'now',updated_at:'now'};
   const next=mergeExecution(original,execution,'c1');expect(next.nodes[0].metadata?.content).toBe('<script>纯文本</script>');expect(next.nodes[0].metadata?.status).toBe('success');
   expect(mergeExecution(original,{...execution,canvas_id:'other'},'c1')).toBe(original);
 });
 it('音频参数可安全保存和复制，但拒绝对象、无限语速和旧插件声音指令',()=>{
   const metadata={voice:'eve',audio_format:'wav',speed:.85,prompt:'你好',execution_id:'audio-e',status:'loading' as const};
   const audio={...doc,nodes:[{...doc.nodes[0],type:'audio',metadata}]};
   expect(validateDocument(audio)).toEqual(audio);
   expect(clearExecutionMetadata(audio.nodes)[0].metadata).toMatchObject({voice:'eve',audio_format:'wav',speed:.85,prompt:'你好'});
   for(const patch of [{voice:{}},{audio_format:['wav']},{speed:Infinity},{speed:.6},{audioInstructions:'custom'}])expect(()=>validateDocument({...audio,nodes:[{...audio.nodes[0],metadata:{...metadata,...patch} as never}]})).toThrow();
 });
 it.each(['CREATIVE_AUDIO_SUBMISSION_UNCONFIRMED','CREATIVE_AUDIO_SETTLEMENT_PENDING','CREATIVE_AUDIO_RELEASE_PENDING'])('音频 %s 恢复同一个任务，隔离其他画布及旧任务',code=>{
   const original:CanvasDocument={...doc,nodes:[{...doc.nodes[0],type:'audio',metadata:{execution_id:'audio-e',status:'loading',voice:'eve',audio_format:'mp3',speed:1}}]};
   const execution:Execution={id:'audio-e',canvas_id:'c1',node_id:'n1',kind:'audio',status:'running',task_id:'same-run',assets:[],error_code:code,error_message:'音频结果核对中',created_at:'now',updated_at:'now'};
   expect(mergeExecution(original,{...execution,canvas_id:'other'},'c1')).toBe(original);
   expect(mergeExecution(original,{...execution,id:'old-e'},'c1')).toBe(original);
   const next=mergeExecution(original,execution,'c1');
   expect(next.nodes[0].metadata).toMatchObject({status:'loading',generation_phase:'reconciliation',voice:'eve',audio_format:'mp3',speed:1});
   expect(mergeExecution(next,{...execution,status:'failed'},'c1').nodes[0].metadata).toMatchObject({status:'error',generation_phase:undefined});
 });
 it('拒绝脚本、外链和缺失节点引用',()=>{
   expect(()=>validateDocument({...doc,nodes:[{...doc.nodes[0],metadata:{content:'x',script:'alert(1)' as never}}]})).toThrow();
   expect(()=>validateDocument({...doc,connections:[{id:'e',fromNodeId:'n1',toNodeId:'missing'}]})).toThrow();
   expect(()=>validateDocument({...doc,viewport:{x:0,y:0,k:9}})).toThrow();
 });
 it('接受受控媒体资产字段并保持布局',()=>{
   expect(validateDocument({...doc,nodes:[{...doc.nodes[0],type:'image',metadata:{asset_id:'00000000-0000-0000-0000-000000000001',expires_at:'2030-01-01T00:00:00Z'}}]}).nodes[0].metadata?.asset_id).toBe('00000000-0000-0000-0000-000000000001');
 });
 it('只为同画布执行写回成功输出，多张保存在当前节点',()=>{
   const execution:Execution={id:'e1',canvas_id:'c1',node_id:'n1',kind:'image',status:'succeeded',task_id:'t',assets:[{id:'a1',media_type:'image',mime_type:'image/png',byte_size:3,expires_at:'2030-01-01T00:00:00Z',created_at:'now'},{id:'a2',media_type:'image',mime_type:'image/png',byte_size:3,expires_at:'2030-01-01T00:00:00Z',created_at:'now'}],created_at:'now',updated_at:'now'};
   const original:CanvasDocument={...doc,nodes:[{...doc.nodes[0],type:'image',metadata:{execution_id:'e1',status:'loading'}} as CanvasNodeData]};
   const next=mergeExecution(original,execution,'c1');expect(next.nodes).toHaveLength(1);expect(next.nodes[0].metadata?.asset_id).toBe('a1');expect(next.nodes[0].metadata?.asset_ids).toEqual(['a1','a2']);expect(mergeExecution(next,execution,'c1')).toBe(next);expect(mergeExecution(original,execution,'other')).toBe(original);
 });
 it.each(['CREATIVE_VIDEO_SUBMISSION_UNCONFIRMED','CREATIVE_VIDEO_SETTLEMENT_PENDING','CREATIVE_VIDEO_RELEASE_PENDING','CREATIVE_VIDEO_RECONCILIATION'])('视频非终态 %s 保留防重与媒体，导出复制清除核对展示字段',code=>{
   const original:CanvasDocument={...doc,nodes:[{...doc.nodes[0],type:'video',metadata:{execution_id:'e1',status:'loading',asset_id:'00000000-0000-0000-0000-000000000001'}}]};
   const execution:Execution={id:'e1',canvas_id:'c1',node_id:'n1',kind:'video',status:'running',task_id:'vid_same',assets:[],error_code:code,error_message:'上游受理结果待核对，请勿重复生成；预扣尚未结算',created_at:'now',updated_at:'now'};
   const next=mergeExecution(original,execution,'c1');
   expect(next.nodes[0].metadata).toMatchObject({status:'loading',execution_id:'e1',generation_phase:'reconciliation',asset_id:original.nodes[0].metadata!.asset_id,errorDetails:execution.error_message});
   expect(validateDocument(next)).toEqual(next);
   expect(clearExecutionMetadata(next.nodes)[0].metadata?.generation_phase).toBeUndefined();
   expect(restoreHistoryDocument(original,next).nodes[0].metadata?.generation_phase).toBe('reconciliation');
   const resumed=mergeExecution(next,{...execution,error_code:undefined,error_message:undefined},'c1');
   expect(resumed.nodes[0].metadata?.generation_phase).toBeUndefined();expect(resumed.nodes[0].metadata?.errorDetails).toBeUndefined();expect(resumed.nodes[0].metadata?.status).toBe('loading');
   expect(mergeExecution(next,{...execution,status:'failed'},'c1').nodes[0].metadata?.generation_phase).toBeUndefined();
   expect(mergeExecution(original,{...execution,canvas_id:'foreign'},'c1')).toBe(original);
 });
});

const output=(id:string)=>({id,media_type:'image' as const,mime_type:'image/png',byte_size:3,expires_at:'2030-01-01T00:00:00Z',created_at:'now'});
const child=(index:number,status:Execution['status']='succeeded',overrides:Partial<Execution>={}):Execution=>({id:`e${index}`,canvas_id:'c1',node_id:'n1',kind:'image',status,task_id:`t${index}`,batch_id:'batch1',batch_index:index,batch_size:2,batch_output_count:2,assets:status==='succeeded'?[output(`a${index}`)]:[],created_at:'now',updated_at:'now',...overrides});
const batchDoc=():CanvasDocument=>({...doc,nodes:[{...doc.nodes[0],type:'image',metadata:{generation_key:'batch1',generation_request:{key:'batch1',count:2,kind:'image',params:{group_id:1,model:'m',prompt:'p',operation:'generate'}},status:'loading'}}]});

describe('当前节点的批次结果恢复',()=>{
 it('乱序和重复轮询按批次序号聚合，不增节点或重复图片',()=>{
   const initial=batchDoc();const next=mergeExecutions(initial,[child(1),child(0)],'c1');
   expect(next.nodes).toHaveLength(1);expect(next.connections).toHaveLength(0);
   expect(next.nodes[0].metadata).toMatchObject({asset_ids:['a0','a1'],status:'success',execution_ids:['e0','e1']});
   expect(next.nodes[0].metadata?.generation_request).toBeUndefined();
   expect(mergeExecutions(next,[child(0),child(1)],'c1')).toBe(next);
 });
 it('部分成功保留可用图片，失败原因与余额不足真实信息可见',()=>{
   const next=mergeExecutions(batchDoc(),[child(0),child(1,'failed',{error_message:'余额不足，请充值后重试'})],'c1');
   expect(next.nodes[0].metadata).toMatchObject({asset_ids:['a0'],status:'error',errorDetails:'已完成 1 张，部分任务失败：余额不足，请充值后重试'});
 });
 it('分页缺失或仍在排队不会提前显示完成，刷新可恢复响应丢失的批次',()=>{
   const partial=mergeExecutions(batchDoc(),[child(0)],'c1');expect(partial.nodes[0].metadata?.status).toBe('loading');
   expect(mergeExecutions(partial,[child(0),child(1,'running')],'c1').nodes[0].metadata?.status).toBe('loading');
   expect(mergeExecutions(JSON.parse(JSON.stringify(batchDoc())),[child(0),child(1)],'c1').nodes[0].metadata?.status).toBe('success');
 });
 it.each(['CREATIVE_IMAGE_SUBMISSION_UNCONFIRMED','CREATIVE_IMAGE_SETTLEMENT_PENDING','CREATIVE_IMAGE_RELEASE_PENDING','CREATIVE_IMAGE_RECONCILIATION'])('批次子任务 %s 保留结果与防重状态，恢复后清除核对展示',code=>{
   const pending=child(0,'running',{error_code:code,error_message:'图片请求结果正在核对，请勿重复生成；费用以最终结算为准'});
   const next=mergeExecutions(batchDoc(),[pending,child(1,'succeeded')],'c1');
   expect(next.nodes[0].metadata).toMatchObject({status:'loading',generation_phase:'reconciliation',asset_ids:['a1']});
   expect(next.nodes[0].metadata?.errorDetails).toContain('正在核对');
   expect(next.nodes[0].metadata?.generation_request).toBeUndefined();
   expect(protectedGenerationNodeIds(next)).toEqual(new Set(['n1']));
   expect(mergeExecutions(next,[child(0,'running'),child(1)],'c1').nodes[0].metadata).toMatchObject({status:'loading',generation_phase:undefined,errorDetails:undefined});
   expect(mergeExecutions(next,[child(0),child(1)],'c1').nodes[0].metadata).toMatchObject({status:'success',generation_phase:undefined,errorDetails:undefined,asset_ids:['a0','a1']});
   const failed=mergeExecutions(next,[child(0,'failed',{error_message:'预扣已释放'}),child(1)],'c1');
   expect(failed.nodes[0].metadata).toMatchObject({status:'error',generation_phase:undefined,asset_ids:['a1'],errorDetails:'已完成 1 张，部分任务失败：预扣已释放'});
 });
 it('旧批次、另一个画布和旧单任务不能覆盖新生成意图',()=>{
   const current=batchDoc();expect(mergeExecutions(current,[child(0,'succeeded',{batch_id:'old'}),child(1,'succeeded',{canvas_id:'other'}),child(2,'succeeded',{batch_id:undefined})],'c1')).toBe(current);
 });
 it('切换主图后轮询保留选择；删除节点不复活，复制导出不保留执行意图',()=>{
   const next=mergeExecutions(batchDoc(),[child(0),child(1)],'c1');next.nodes[0].metadata!.asset_id='a1';
   expect(mergeExecutions(next,[child(0),child(1)],'c1').nodes[0].metadata?.asset_id).toBe('a1');
   const empty={...next,nodes:[]};expect(mergeExecutions(empty,[child(0),child(1)],'c1')).toBe(empty);
   const copied=clearExecutionMetadata(next.nodes)[0].metadata!;expect(copied.asset_ids).toEqual(['a0','a1']);expect(copied.generation_key).toBeUndefined();expect(copied.execution_ids).toBeUndefined();expect(clearExecutionMetadata(batchDoc().nodes)[0].metadata?.generation_request).toBeUndefined();
 });
 it('撤销重做保留执行绑定和成果，不能借旧快照清除防重键',()=>{
   const active=batchDoc();const restored=restoreHistoryDocument(doc,active);
   expect(restored.nodes[0].metadata?.generation_key).toBe('batch1');expect(restored.nodes[0].metadata?.generation_request?.key).toBe('batch1');
   const done=mergeExecutions(active,[child(0),child(1)],'c1');expect(restoreHistoryDocument(doc,done).nodes[0].metadata?.asset_ids).toEqual(['a0','a1']);
   const manual={...doc,nodes:[{...doc.nodes[0],metadata:{asset_id:'manual'}}]};expect(restoreHistoryDocument(active,manual).nodes[0].metadata?.generation_request).toBeUndefined();
 });
 it('手工替换和复制的节点不接受历史单任务或批次回填',()=>{
   const manual={...doc,nodes:[{...doc.nodes[0],type:'image',metadata:{asset_id:'manual'}}]};
   expect(mergeExecutions(manual,[child(0,'succeeded',{batch_id:undefined}),child(1)],'c1')).toBe(manual);
 });
});
