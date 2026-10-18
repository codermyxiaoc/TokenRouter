import {getSession,getEpoch,getSessionSignal,tellParent,onSessionClear} from './session';
import {blankDocument,type Asset,type CanvasRecord,type CanvasDocument,type Page,type Execution,type ExecutionBatch,type ExecuteParams,type GenerationKind,type Model} from './types';
export class ApiError extends Error { constructor(public status:number, message:string, public code='') {super(message);} }
const base = '/api/v1/creative';
const urls = new Map<string,string>();
const loading = new Map<string,Promise<string>>();
onSessionClear(() => {urls.forEach(url => URL.revokeObjectURL(url));urls.clear();loading.clear();});

// 服务端是归属和结算的权威；用户切换后丢弃旧请求返回，杜绝跨会话回填。
export async function api<T>(path:string, init:RequestInit = {}, binary=false):Promise<T> {
  const current=getSession(); const requestEpoch=getEpoch();
  if (!current) throw new ApiError(401,'请重新登录');
  const headers = new Headers(init.headers); headers.set('Authorization',`Bearer ${current.token}`);
  if (init.body && !(init.body instanceof FormData)) headers.set('Content-Type','application/json');
  const response=await fetch(base+path,{...init,headers,credentials:'same-origin',signal:init.signal ? AbortSignal.any([init.signal,getSessionSignal()]) : getSessionSignal()});
  if (requestEpoch!==getEpoch() || getSession()?.userId!==current.userId) throw new DOMException('会话已变更','AbortError');
  if (response.status===401) tellParent('expired');
  if (!response.ok) {const detail=await response.json().catch(()=>({})); throw new ApiError(response.status,detail.message || detail.error?.message || `请求失败 (${response.status})`, detail.reason || detail.code || '');}
  if (binary) return await response.blob() as T;
  if (response.status===204) return undefined as T;
  const envelope=await response.json();
  if (envelope.code !== undefined && envelope.code !== 0 && envelope.code !== '0') throw new ApiError(response.status,envelope.message || '请求失败',String(envelope.code));
  return (envelope.data === undefined ? envelope : envelope.data) as T;
}
export const listCanvases = (page=1) => api<Page<CanvasRecord>>(`/canvases?page=${page}&page_size=30`);
export const getCanvas = (id:string) => api<CanvasRecord>(`/canvases/${encodeURIComponent(id)}`);
export const createCanvas = (title:string, document:CanvasDocument=blankDocument()) => api<CanvasRecord>('/canvases',{method:'POST',body:JSON.stringify({title,document})});
export const saveCanvas = (canvas:CanvasRecord, document:CanvasDocument, title=canvas.title) => api<CanvasRecord>(`/canvases/${encodeURIComponent(canvas.id)}`,{method:'PUT',body:JSON.stringify({title,document,revision:canvas.revision})});
export const deleteCanvas = async (id:string) => {const current=await getCanvas(id);return api<void>(`/canvases/${encodeURIComponent(id)}?revision=${current.revision}`,{method:'DELETE'});};
export const listAssets = (canvasId:string,page=1) => api<Page<Asset>>(`/assets?canvas_id=${encodeURIComponent(canvasId)}&page=${page}&page_size=50`);
export const assetMetadata = (id:string) => api<Asset>(`/assets/${encodeURIComponent(id)}`);
export const assetBlob = (id:string) => api<Blob>(`/assets/${encodeURIComponent(id)}/content`,{},true);
export const isAssetExpired = (asset:Pick<Asset,'expires_at'>,now=Date.now()) => !!asset.expires_at && (!Number.isFinite(Date.parse(asset.expires_at)) || Date.parse(asset.expires_at)<=now);
export async function assetURL(id:string) {
  if(urls.has(id)) return urls.get(id)!;
  if(loading.has(id)) return loading.get(id)!;
  const epoch=getEpoch();
  const task=assetBlob(id).then(blob=>{if(epoch!==getEpoch())throw new DOMException('会话已变更','AbortError'); const url=URL.createObjectURL(blob); urls.set(id,url);return url;}).finally(()=>loading.delete(id));
  loading.set(id,task);return task;
}
export async function uploadAsset(file:Blob,canvasId:string,name='reference.png') {const data=new FormData();data.append('file',file,name);data.append('canvas_id',canvasId);return api<Asset>('/assets',{method:'POST',body:data});}
export const listModels = () => api<Model[]>('/models');
// 批次标签由服务端写入 params，读取时统一展开供画布聚合，客户端不把标签回传到模型参数。
function executionRecord(value:Execution & {params?:{batch_id?:string;batch_index?:number;batch_size?:number;batch_output_count?:number}}):Execution {
  return {...value,batch_id:value.params?.batch_id,batch_index:value.params?.batch_index,batch_size:value.params?.batch_size,batch_output_count:value.params?.batch_output_count};
}
export const listExecutions = async (canvasId:string,page=1) => {const result=await api<Page<Execution>>(`/executions?canvas_id=${encodeURIComponent(canvasId)}&page=${page}&page_size=100`);return {...result,items:result.items.map(executionRecord)};};
export const getExecution = async (id:string) => executionRecord(await api<Execution>(`/executions/${encodeURIComponent(id)}`));
export const createExecution = (canvasId:string,nodeId:string,kind:GenerationKind,params:ExecuteParams,key:string) => api<Execution>('/executions',{method:'POST',headers:{'Idempotency-Key':key},body:JSON.stringify({canvas_id:canvasId,node_id:nodeId,kind,params})});
export const createImageBatch = async (canvasId:string,nodeId:string,params:ExecuteParams,count:number,key:string) => {const result=await api<ExecutionBatch>('/execution-batches',{method:'POST',headers:{'Idempotency-Key':key},body:JSON.stringify({canvas_id:canvasId,node_id:nodeId,kind:'image',count,params})});return {...result,executions:result.executions.map(executionRecord)};};
