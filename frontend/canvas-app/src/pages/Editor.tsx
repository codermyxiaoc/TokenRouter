import {useCallback,useEffect,useLayoutEffect,useMemo,useRef,useState} from 'react';
import {Modal} from 'antd';
import {useNavigate,useParams} from 'react-router-dom';
import {ArrowLeft,Hand,MousePointer2,Type,Image as ImageIcon,Video,Music,Settings2,Group,Upload,Trash2,Undo2,Redo2,Play,Copy,Download,Scissors,Brush,Search,Moon,Sun,PanelLeftClose,PanelLeftOpen,LoaderCircle,Clock3,Link2,X,Info,Pencil,BookOpen,Maximize2,Minimize2,Focus,LayoutGrid,ListChecks,Eye,ArrowUp,Palette} from 'lucide-react';
import {InfiniteCanvas} from '@/components/canvas/infinite-canvas';
import {CanvasConnectionLayer,type CanvasConnectionController} from '@/components/canvas/canvas-connection-layer';
import {ConnectionCreateMenu,type ConnectionCreateType,type PendingConnectionCreate} from '@/components/canvas/canvas-connection-create-menu';
import {CanvasGenerationLoading} from '@/components/canvas/canvas-generation-loading';
import {Minimap} from '@/components/canvas/canvas-mini-map';
import {CanvasZoomControls} from '@/components/canvas/canvas-zoom-controls';
import {CanvasNodeCropDialog} from '@/components/canvas/canvas-node-crop-dialog';
import {CanvasNodeMaskEditDialog} from '@/components/canvas/canvas-node-mask-edit-dialog';
import {Choice} from '@/components/Choice';
import {ConfigNodePanel,configKind} from '@/components/ConfigNodePanel';
import {ConfigComposer} from '@/components/ConfigComposer';
import {configInputs,compileConfigPrompt,remapConfigReferences} from '@/platform/config-composer';
import {GenerationSettings} from '@/components/GenerationSettings';
import {GenerationStatusDetails} from '@/components/GenerationStatusDetails';
import {PromptMentionTextarea} from '@/components/PromptMentionTextarea';
import {promptReferences} from '@/platform/prompt-references';
import {ModelUsageNotes,videoModelUsageNotes,modelUsageNotesFootprint} from '@/components/ModelUsageNotes';
import {resolveGenerationSettings} from '@/platform/generation-settings';
import {referenceImageSizeWarning} from '@/platform/reference-image-limit';
import {mediaUploadAccept,validateUploadFiles} from '@/platform/upload-validation';
import {composerFootprint,composerPromptHeight,focusNodeLayout} from '@/lib/canvas/editor-layout';
import {AssetView} from '@/components/AssetView';
import {ReferenceImagePreview} from '@/components/ReferenceImagePreview';
import {ImageResults} from '@/components/ImageResults';
import {canvasThemes} from '@/lib/canvas-theme';
import {cropDataUrl} from '@/lib/canvas/canvas-image-data';
import {randomId} from '@/lib/utils';
import {useThemeStore} from '@/stores/use-theme-store';
import {useCloudDocument} from '@/hooks/use-cloud-document';
import {usePointerGesture} from '@/hooks/use-pointer-gesture';
import {api,ApiError,assetBlob,assetMetadata,assetURL,createExecution,createImageBatch,isAssetExpired,listAssets,listExecutions,listModels,uploadAsset} from '@/platform/api';
import {download,exportCanvas} from '@/platform/archive';
import {clearExecutionMetadata,mergeExecution,mergeExecutions,restoreHistoryDocument,selectedReferences,protectedGenerationNodeIds} from '@/platform/document';
import {modelId,mediaType,type Asset,type CanvasDocument,type ExecuteParams,type Execution,type GenerationKind,type GenerationRequest,type Model} from '@/platform/types';
import {CanvasNodeType,type CanvasNodeData,type CanvasNodeMetadata,type Position} from '@/types/canvas';

const names:Record<string,string>={image:'图片',video:'视频',text:'文本',config:'生成配置',audio:'音频',group:'分组'};
const iconFor=(type:string)=>type==='image'?ImageIcon:type==='video'?Video:type==='audio'?Music:type==='config'?Settings2:type==='group'?Group:Type;
// 节点类型优先于配置元数据，避免旧生成模式把音频误当成图片。
const generationKind=(node?:CanvasNodeData):GenerationKind=>node?.type==='config'?configKind(node):node?.type==='text'?'text':node?.type==='audio'?'audio':node?.type==='video'?'video':'image';
const terminal=new Set(['succeeded','failed','result_lost']);
const snapshot=(doc:CanvasDocument)=>structuredClone(doc);
type Gesture={kind:'move'|'resize'|'select';x:number;y:number;start:CanvasDocument;ids:Set<string>;nodeId?:string;deferMoveHistory?:boolean;moved?:boolean};

// 视频参考图角色：只有一张图且没有参考视频时作为首帧；多图或带参考视频时全部作为参考图，避免首帧与参考素材混用被协议拒绝。
export function videoImageRole(images:number,videos:number):'first_frame'|'reference_image'{return images===1&&videos===0?'first_frame':'reference_image';}

export function Editor(){
 const {id=''}=useParams();const canvasIdRef=useRef(id);canvasIdRef.current=id;const navigate=useNavigate();const cloud=useCloudDocument(id);const doc=cloud.document;
 const [models,setModels]=useState<Model[]>([]);const [assets,setAssets]=useState<Asset[]>([]);const [assetPage,setAssetPage]=useState(1);const [assetTotal,setAssetTotal]=useState(0);const [capabilities,setCapabilities]=useState<{max_asset_bytes?:number;max_prompt_chars?:number}>({});
 const [selected,setSelected]=useState<Set<string>>(new Set());const [selectedEdge,setSelectedEdge]=useState('');const [tool,setTool]=useState<'pan'|'select'>('pan');const [sideTab,setSideTab]=useState<'canvas'|'assets'|'prompts'>('canvas');const [sidebar,setSidebar]=useState(true);const [search,setSearch]=useState('');const [miniMap,setMiniMap]=useState(false);const [error,setError]=useState('');
 // 提交前校验失败只显示在点击生成的面板内，用户无需到页面顶部寻找原因；已提交的失败写入结果节点。
 const [generationError,setGenerationError]=useState<{nodeId:string;message:string}|null>(null);const [busy,setBusy]=useState(false);
 const connectionController=useRef<CanvasConnectionController>(null);const [box,setBox]=useState<{from:Position;to:Position}|null>(null);const [imageTool,setImageTool]=useState<{kind:'crop'|'mask';node:CanvasNodeData;url:string}|null>(null);const [settingsOpen,setSettingsOpen]=useState(false);
 const [,historyRender]=useState(0);const history=useRef<{past:CanvasDocument[];future:CanvasDocument[]}>({past:[],future:[]});const copied=useRef<CanvasDocument|null>(null);const container=useRef<HTMLDivElement>(null);const uploadInput=useRef<HTMLInputElement>(null);const documentRef=useRef(doc);documentRef.current=doc;
 const submitLocks=useRef(new Set<string>());const activeExecutions=useRef(new Map<string,Execution>());const themeName=useThemeStore(s=>s.theme);const setTheme=useThemeStore(s=>s.setTheme);const theme=canvasThemes[themeName];
 const [configComposerId,setConfigComposerId]=useState('');
 const [executionSyncError,setExecutionSyncError]=useState('');
 const [settingsAnchor,setSettingsAnchor]=useState<HTMLButtonElement|null>(null);
 const [expandedPrompt,setExpandedPrompt]=useState(false);const [nodeFilter,setNodeFilter]=useState('all');const [gridList,setGridList]=useState(false);const [infoOpen,setInfoOpen]=useState(false);
 const [viewSize,setViewSize]=useState({width:1200,height:800});
 const uploadTarget=useRef<{nodeId:string;mode:'reference'|'replace'}|null>(null);
 useEffect(()=>{if(!container.current)return;const observer=new ResizeObserver(entries=>{const rect=entries[0].contentRect;setViewSize({width:rect.width,height:rect.height});});observer.observe(container.current);return()=>observer.disconnect();},[!!doc]);
 const report=(e:unknown)=>{if((e as Error)?.name!=='AbortError')setError((e as Error).message||'操作失败');};
 const update=cloud.update;
 const remember=useCallback((old:CanvasDocument)=>{history.current.past=[...history.current.past.slice(-49),snapshot(old)];history.current.future=[];historyRender(n=>n+1);},[]);
 const change=useCallback((fn:(old:CanvasDocument)=>CanvasDocument)=>update(old=>{remember(old);return fn(old);}),[update,remember]);
 const patchNode=useCallback((nodeId:string,metadata:Partial<CanvasNodeMetadata>)=>change(old=>({...old,nodes:old.nodes.map(n=>n.id===nodeId?{...n,metadata:{...n.metadata,...metadata}}:n)})),[change]);
 const viewport=useCallback((value:CanvasDocument['viewport'])=>update(old=>({...old,viewport:value})),[update]);
 const world=useCallback((x:number,y:number)=>{const current=documentRef.current;const rect=container.current?.getBoundingClientRect();if(!current||!rect)return{x:0,y:0};return{x:(x-rect.left-current.viewport.x)/current.viewport.k,y:(y-rect.top-current.viewport.y)/current.viewport.k};},[]);
 const center=()=>{const rect=container.current?.getBoundingClientRect();return rect?world(rect.left+rect.width/2,rect.top+rect.height/2):{x:100,y:100};};
 // 节点采用上游尺寸，并将附属面板一起纳入聚焦范围。
 const makeNode=(type:string,position=center()):CanvasNodeData=>({id:randomId(),type,title:names[type],position,width:type==='group'?640:type==='image'||type==='config'?340:type==='video'?420:320,height:type==='image'?240:type==='video'?236:type==='audio'?100:type==='text'?200:type==='group'?420:240,metadata:type==='config'?{generationMode:'image'}:{}});
 // 新提交的大小提醒只影响编辑区，不能改变正在核对或运行的历史任务。
 const referenceWarning=(node:CanvasNodeData)=>node.metadata?.generation_request||node.metadata?.status==='loading'||!(node.type==='video'||node.metadata?.generationMode==='video')?undefined:referenceImageSizeWarning(models.find(m=>modelId(m)===node.metadata?.model),documentRef.current?selectedReferences(documentRef.current,node.id):[],assets);
 const panelExtra=(node:CanvasNodeData)=> (node.type==='config'?60:0)+(generationKind(node)==='audio'?24:0)+(models.some(m=>mediaType(m)===generationKind(node))?0:24)+(referenceWarning(node)?48:0)+(generationKind(node)==='video'&&videoModelUsageNotes(models.find(m=>modelId(m)===node.metadata?.model))?modelUsageNotesFootprint(viewSize.height):0);
 const addNode=(type:string,position?:Position)=>{const node=makeNode(type,position);const withPanel=['image','video','audio','text'].includes(type);change(old=>({...old,nodes:[...old.nodes,node],viewport:!position?focusNodeLayout(node,viewSize,withPanel?composerFootprint(viewSize.height,panelExtra(node)):0):old.viewport}));setSettingsOpen(false);setSelected(new Set([node.id]));};
 const removeSelected=useCallback(()=>{const current=documentRef.current;if(current){const protectedIds=protectedGenerationNodeIds(current);if(current.nodes.some(n=>protectedIds.has(n.id)&&(selected.has(n.id)||!!n.metadata?.groupId&&selected.has(n.metadata.groupId)))){setError('任务尚未结束或提交待核对，请等待处理完成后再删除');return;}}change(old=>{const ids=new Set(selected);old.nodes.forEach(n=>{if(n.metadata?.groupId&&selected.has(n.metadata.groupId))ids.add(n.id);});return{...old,nodes:old.nodes.filter(n=>!ids.has(n.id)),connections:old.connections.filter(e=>e.id!==selectedEdge&&!ids.has(e.fromNodeId)&&!ids.has(e.toNodeId))};});setSelected(new Set());setSelectedEdge('');},[change,selected,selectedEdge]);
 const undo=useCallback(()=>{const current=documentRef.current;const prev=history.current.past.pop();if(current&&prev){history.current.future.push(snapshot(current));update(restoreHistoryDocument(prev,current));historyRender(n=>n+1);}},[update]);
 const redo=useCallback(()=>{const current=documentRef.current;const next=history.current.future.pop();if(current&&next){history.current.past.push(snapshot(current));update(restoreHistoryDocument(next,current));historyRender(n=>n+1);}},[update]);
 // 复制组时包含其子节点，随后统一重映射组归属、内部连线和组装提示词引用。
 const copy=useCallback(()=>{const current=documentRef.current;if(!current)return;const ids=new Set(selected);for(let changed=true;changed;){changed=false;for(const node of current.nodes)if(node.metadata?.groupId&&ids.has(node.metadata.groupId)&&!ids.has(node.id)){ids.add(node.id);changed=true;}}copied.current={...current,nodes:clearExecutionMetadata(current.nodes.filter(n=>ids.has(n.id))),connections:current.connections.filter(e=>ids.has(e.fromNodeId)&&ids.has(e.toNodeId))};},[selected]);
 const paste=useCallback(()=>{const saved=copied.current;if(!saved)return;const ids=new Map(saved.nodes.map(n=>[n.id,randomId()]));const nodes=saved.nodes.map(n=>({...structuredClone(n),id:ids.get(n.id)!,position:{x:n.position.x+40,y:n.position.y+40},metadata:{...n.metadata,groupId:ids.get(n.metadata?.groupId||''),composerContent:remapConfigReferences(n.metadata?.composerContent,ids)}}));change(old=>({...old,nodes:[...old.nodes,...nodes],connections:[...old.connections,...saved.connections.map(e=>({...e,id:randomId(),fromNodeId:ids.get(e.fromNodeId)!,toNodeId:ids.get(e.toNodeId)!}))]}));setSelected(new Set(nodes.map(n=>n.id)));},[change]);
 const group=useCallback(()=>{const current=documentRef.current;if(!current||!selected.size)return;const chosen=current.nodes.filter(n=>selected.has(n.id));const left=Math.min(...chosen.map(n=>n.position.x))-30,top=Math.min(...chosen.map(n=>n.position.y))-50;const node:CanvasNodeData={id:randomId(),type:CanvasNodeType.Group,title:'分组',position:{x:left,y:top},width:Math.max(...chosen.map(n=>n.position.x+n.width))-left+30,height:Math.max(...chosen.map(n=>n.position.y+n.height))-top+30,metadata:{}};change(old=>({...old,nodes:[node,...old.nodes.map(n=>selected.has(n.id)?{...n,metadata:{...n.metadata,groupId:node.id}}:n)]}));setSelected(new Set([node.id]));},[change,selected]);
 const focus=(node?:CanvasNodeData)=>{const current=documentRef.current;if(!current)return;const target=node||current.nodes.find(n=>selected.has(n.id));if(target){const panel=container.current?.querySelector<HTMLElement>(`[data-node-id="${target.id}"] .node-panel`);const below=target.type==='config'&&configComposerId===target.id?304:['image','video','audio','text'].includes(target.type)?Math.max(panel?.getBoundingClientRect().height||0,composerFootprint(viewSize.height,panelExtra(target))):0;viewport(focusNodeLayout(target,viewSize,below));}else viewport({x:0,y:0,k:1});};
 useEffect(()=>{const handler=(e:KeyboardEvent)=>{const target=e.target as Element;if(e.defaultPrevented||target.closest('input,textarea,video,audio,[contenteditable="true"],[role="dialog"],[role="listbox"],[role="option"],[role="combobox"],[role="slider"]'))return;const mod=e.ctrlKey||e.metaKey;if(mod&&e.key.toLowerCase()==='z'){e.preventDefault();e.shiftKey?redo():undo();}if(mod&&e.key.toLowerCase()==='y'){e.preventDefault();redo();}if(mod&&e.key.toLowerCase()==='c'){e.preventDefault();copy();}if(mod&&e.key.toLowerCase()==='v'){e.preventDefault();paste();}if(mod&&e.key.toLowerCase()==='a'){e.preventDefault();setSelected(new Set(documentRef.current?.nodes.map(n=>n.id)));}if(mod&&e.key.toLowerCase()==='g'){e.preventDefault();if(e.shiftKey)change(old=>({...old,nodes:old.nodes.map(n=>n.metadata?.groupId&&selected.has(n.metadata.groupId)?{...n,metadata:{...n.metadata,groupId:undefined}}:n)}));else group();}if(e.key==='Delete'||e.key==='Backspace'){e.preventDefault();removeSelected();}if(e.key==='Escape'){connectionController.current?.cancel();setSelected(new Set());}};window.addEventListener('keydown',handler);return()=>window.removeEventListener('keydown',handler);},[copy,paste,undo,redo,group,removeSelected,change,selected]);
 useEffect(()=>{void Promise.all([listModels(),api<typeof capabilities>('/capabilities')]).then(([m,c])=>{setModels(m);setCapabilities(c);}).catch(report);},[]);
 const refreshAssets=useCallback(async()=>{const result=await listAssets(id,assetPage);setAssets(result.items);setAssetTotal(result.total);},[id,assetPage]);
 useEffect(()=>{void refreshAssets().catch(report);},[refreshAssets]);
 // 收齐分页后聚合同一批次，避免部分页面暂时把未完成任务显示为成功。
 useEffect(()=>{
  if(!doc)return;
  let live=true;let running=false;const controller=new AbortController();
  const refresh=async()=>{
   if(running)return;
   running=true;
   try{
    let page=1,total=Infinity,seen=0;
    while(live&&seen<total){
     const result=await listExecutions(id,page++,controller.signal);
     if(!live)return;
     total=result.total;seen+=result.items.length;
     for(const execution of result.items){
      if(execution.canvas_id!==id)continue;
      const cached=activeExecutions.current.get(execution.id);
      if(!cached||Date.parse(execution.updated_at)>=Date.parse(cached.updated_at))activeExecutions.current.set(execution.id,execution);
     }
     if(!result.items.length)break;
    }
    if(live){setExecutionSyncError('');update(old=>mergeExecutions(old,[...activeExecutions.current.values()],id));void refreshAssets().catch(()=>{});}
   }catch(e){
    // 查询失败不能改写任务/费用状态，后续只继续 GET，不自动重发生成。
    if(live&&((e as Error)?.name!=='AbortError'))setExecutionSyncError((e as Error)?.name==='TimeoutError'?'任务状态查询超时，将自动重试。任务结果尚未确认，请勿重复生成。':'任务状态暂时无法更新，将自动重试。任务结果尚未确认，请勿重复生成。');
   }finally{running=false;}
  };
  void refresh();const timer=setInterval(()=>void refresh(),4000);
  return()=>{live=false;controller.abort();clearInterval(timer);};
 },[id,!!doc,update,refreshAssets]);
 // 移动、缩放和框选共享指针生命周期，设置弹层吞掉松手或切换窗口后也立即停止。
 const nodeGesture=usePointerGesture<Gesture>({scope:id,onMove(e,g){
  const point=world(e.clientX,e.clientY);
  if(g.kind==='select'){const start=world(g.x,g.y);setBox({from:start,to:point});setSelected(new Set(g.start.nodes.filter(n=>n.position.x+n.width>=Math.min(start.x,point.x)&&n.position.x<=Math.max(start.x,point.x)&&n.position.y+n.height>=Math.min(start.y,point.y)&&n.position.y<=Math.max(start.y,point.y)).map(n=>n.id)));return;}
  // 媒体普通点击只选中，越过屏幕像素门槛后才记录历史并移动，避免误动播放器时产生脏文档。
  if(g.deferMoveHistory&&!g.moved){if(Math.hypot(e.clientX-g.x,e.clientY-g.y)<4)return;g.moved=true;remember(g.start);}
  const dx=(e.clientX-g.x)/g.start.viewport.k,dy=(e.clientY-g.y)/g.start.viewport.k;
  update(old=>({...old,nodes:old.nodes.map(n=>{const original=g.start.nodes.find(item=>item.id===n.id);if(!original||!g.ids.has(n.id))return n;if(g.kind==='move')return{...n,position:{x:original.position.x+dx,y:original.position.y+dy}};if(n.id!==g.nodeId)return n;const w=Math.max(n.type==='config'?340:n.type==='audio'?280:80,original.width+dx);const h=n.type==='image'&&n.metadata?.asset_id?Math.max(60,w*original.height/original.width):Math.max(n.type==='config'?240:n.type==='audio'?90:60,original.height+dy);return{...n,width:w,height:h};})}));
 },onEnd(){setBox(null);}});
 const onMoveStart=(event:React.PointerEvent,node:CanvasNodeData,resize=false)=>{if(event.button!==0||!documentRef.current)return;event.stopPropagation();event.preventDefault();const ids=new Set(selected);if(!ids.has(node.id)){if(!event.shiftKey)ids.clear();ids.add(node.id);setSelected(ids);}if(node.type==='group')documentRef.current.nodes.forEach(n=>{if(n.metadata?.groupId===node.id)ids.add(n.id);});const deferMoveHistory=!resize&&['video','audio'].includes(node.type)&&!!node.metadata?.asset_id;nodeGesture.begin(event,{kind:resize?'resize':'move',x:event.clientX,y:event.clientY,start:snapshot(documentRef.current),ids,nodeId:node.id,deferMoveHistory});if(!deferMoveHistory)remember(documentRef.current);};
 // 播放控件只触发选择，不 preventDefault 或捕获指针，以保留拖动进度、音量与全屏操作。
 const selectMediaNode=(event:{shiftKey:boolean},node:CanvasNodeData)=>{setSelectedEdge('');setSelected(previous=>{if(previous.has(node.id))return previous;const next=event.shiftKey?new Set(previous):new Set<string>();next.add(node.id);return next;});};
 // 拖线松手在空白处新建节点并按拖出方向连线：从输出端拖出时新节点接收输入，从输入端拖出时新节点作为来源。
 const [connectionCreate,setConnectionCreate]=useState<PendingConnectionCreate|null>(null);
 const createConnected=(type:ConnectionCreateType)=>{
  const pending=connectionCreate;setConnectionCreate(null);
  if(!pending||(type==='config'&&pending.handle.handleType!=='source'))return;
  const draft=makeNode(type,pending.point);
  const node={...draft,position:{x:pending.handle.handleType==='source'?pending.point.x:pending.point.x-draft.width,y:pending.point.y-draft.height/2}};
  const edge=pending.handle.handleType==='source'?{fromNodeId:pending.handle.nodeId,toNodeId:node.id}:{fromNodeId:node.id,toNodeId:pending.handle.nodeId};
  change(old=>old.nodes.some(n=>n.id===pending.handle.nodeId)?{...old,nodes:[...old.nodes,node],connections:[...old.connections,{id:randomId(),...edge}]}:old);
  setSettingsOpen(false);setSelectedEdge('');setSelected(new Set([node.id]));
 };
 const link=(from:string,to:string)=>{if(from===to)return;change(old=>({...old,connections:old.connections.some(e=>e.fromNodeId===from&&e.toNodeId===to)?old.connections:[...old.connections,{id:randomId(),fromNodeId:from,toNodeId:to}]}));connectionController.current?.cancel();};
 async function insertAsset(asset:Asset,position?:Position){const node=makeNode(asset.media_type,position);node.metadata={asset_id:asset.id,mimeType:asset.mime_type,bytes:asset.byte_size,expires_at:asset.expires_at,status:'success'};change(old=>({...old,nodes:[...old.nodes,node]}));setSelected(new Set([node.id]));}
 const pickUpload=(target?:{nodeId:string;mode:'reference'|'replace'})=>{uploadTarget.current=target||null;uploadInput.current?.click();};
 // TTS 只收文本，参考区的加号创建已连线文本节点，不打开图片上传框。
 const addTextReference=(target:CanvasNodeData)=>{const node=makeNode('text',{x:target.position.x-380,y:target.position.y});change(old=>({...old,nodes:[...old.nodes,node],connections:[...old.connections,{id:randomId(),fromNodeId:node.id,toNodeId:target.id}]}));};
 async function upload(files:File[],position?:Position,intent?:{nodeId:string;mode:'reference'|'replace'}|null){setBusy(true);try{
   if(!files.length)return;
   const initialTarget=intent?documentRef.current?.nodes.find(n=>n.id===intent.nodeId):undefined;
   if(intent&&!initialTarget)throw new Error('目标节点已被删除');
   const originalBinding={generationKey:initialTarget?.metadata?.generation_key,executionId:initialTarget?.metadata?.execution_id,assetId:initialTarget?.metadata?.asset_id};
   // 先检查整批输入，避免第二个文件非法时第一个已经上传并产生半批素材。
   validateUploadFiles(files,capabilities?.max_asset_bytes,intent&&initialTarget?{...intent,targetType:initialTarget.type}:undefined);
   if(intent?.mode==='replace'&&(initialTarget?.metadata?.status==='loading'||initialTarget?.metadata?.generation_request))throw new Error('任务尚未结束或提交待核对，请等待处理完成后再替换素材');
   for(const [index,file] of files.entries()){
   const target=intent?documentRef.current?.nodes.find(n=>n.id===intent.nodeId):undefined;
   // 目标在上传期间被删除时只保留云端素材，不把迟到回包插入当前画布。
   if(intent&&!target)continue;
   const asset=await uploadAsset(file,id,file.name);
   if(canvasIdRef.current!==id)return;
   if(target&&intent?.mode==='reference'){
     const node=makeNode(asset.media_type,{x:target.position.x-380,y:target.position.y+index*270});node.metadata={asset_id:asset.id,bytes:asset.byte_size,expires_at:asset.expires_at,mimeType:asset.mime_type,status:'success'};
     // 上传期间可删除目标，迟到的素材保留在云端素材库，不创建悬空连线。
     change(old=>old.nodes.some(n=>n.id===target.id)?{...old,nodes:[...old.nodes,node],connections:[...old.connections,{id:randomId(),fromNodeId:node.id,toNodeId:target.id}]}:old);if(documentRef.current?.nodes.some(n=>n.id===target.id))setSelected(new Set([target.id]));
   }else if(target&&intent?.mode==='replace'&&index===0){
     if(asset.media_type!==target.type)throw new Error('上传的素材类型与当前节点不一致');
     // 文件读取期间任务可能已被恢复或更新；迟到上传不能清除新任务或未知提交的幂等绑定。
     const latest=documentRef.current?.nodes.find(n=>n.id===target.id);
     if(!latest)continue;
     if(latest.metadata?.status==='loading'||latest.metadata?.generation_request||latest.metadata?.generation_key!==originalBinding.generationKey||latest.metadata?.execution_id!==originalBinding.executionId||latest.metadata?.asset_id!==originalBinding.assetId)throw new Error('节点任务或素材已更新，上传文件已保留在素材库，请确认后再替换');
     patchNode(target.id,{asset_id:asset.id,asset_ids:[asset.id],bytes:asset.byte_size,expires_at:asset.expires_at,mimeType:asset.mime_type,status:'success',execution_id:undefined,execution_ids:undefined,generation_key:undefined,generation_request:undefined,generation_phase:undefined,errorDetails:undefined});
   }else{await insertAsset(asset,position);if(position)position={x:position.x+40,y:position.y+40};}
 }await refreshAssets();}catch(e){report(e);}finally{setBusy(false);}}
 const downloadImage=(assetId:string)=>void assetBlob(assetId).then(blob=>download(blob,`image.${blob.type.split('/')[1]||'png'}`)).catch(report);
 const selectImage=(nodeId:string,assetId:string)=>{
   const generationKey=documentRef.current?.nodes.find(n=>n.id===nodeId)?.metadata?.generation_key;
   void assetMetadata(assetId).then(asset=>update(old=>{
     const live=old.nodes.find(n=>n.id===nodeId);
     // 切主图的慢响应不能写入后续新批次，也不能复活已经删除的节点。
     if(canvasIdRef.current!==id||!live||live.metadata?.generation_key!==generationKey||!live.metadata?.asset_ids?.includes(assetId))return old;
     remember(old);return {...old,nodes:old.nodes.map(n=>n.id===nodeId?{...n,metadata:{...n.metadata,asset_id:asset.id,expires_at:asset.expires_at,mimeType:asset.mime_type,bytes:asset.byte_size}}:n)};
   })).catch(report);
 };
 async function openImageTool(kind:'crop'|'mask',node:CanvasNodeData){try{const url=await assetURL(node.metadata?.asset_id||'');setImageTool({kind,node,url});}catch(e){report(e);}}
 async function execute(source:CanvasNodeData,mask?:{assetId:string;prompt:string}){
   if(submitLocks.current.has(source.id))return;submitLocks.current.add(source.id);setBusy(true);setError('');setGenerationError(null);
   let targetId=source.id;let request:GenerationRequest|undefined;let saved=false;
   try{
    const current=documentRef.current;if(!current)throw new Error('画布尚未载入');
    const latest=current.nodes.find(n=>n.id===source.id);if(!latest)throw new Error('节点已被删除');
    if(source.type==='config'&&latest.metadata?.generation_target_id){
      const result=current.nodes.find(node=>node.id===latest.metadata?.generation_target_id);
      if(result?.metadata?.generation_request){targetId=result.id;request=result.metadata.generation_request;await submitSavedGeneration(targetId,request);return;}
      if(result?.metadata?.status==='loading')throw new Error('当前配置的任务仍在处理中，请等待结果');
    }
    if(latest.metadata?.generation_request){
      request=latest.metadata.generation_request;
      // 网络结果未知时只重放已经保存的请求，编辑面板不会改变原幂等参数。
      await submitSavedGeneration(targetId,request);return;
    }
    if(latest.metadata?.status==='loading')throw new Error('当前节点的任务仍在处理中，请等待结果');
    const kind=generationKind(source);
    const selectedModel=models.find(m=>modelId(m)===source.metadata?.model&&mediaType(m)===kind);
    if(!selectedModel)throw new Error(`请选择管理员开放的${names[kind]}模型`);
    const assembled=source.type==='config'?compileConfigPrompt(current,source):undefined;
    const inputs=assembled?.inputs||selectedReferences(current,source.id);const imageInputs=inputs.filter(n=>n.type==='image'&&n.metadata?.asset_id);
    if(inputs.some(n=>['image','video','audio'].includes(n.type)&&!n.metadata?.asset_id))throw new Error('引用素材尚未生成或已被移除，请先补齐素材');
    // 覆盖当前节点时，上一次生成结果不自动成为下一次参考图；局部重绘明确引用主图。
    if(source.metadata?.asset_id&&mask)imageInputs.unshift(source);
    // 已保存的提交意图在上方优先恢复；新请求才检查单图大小，不压缩或删除素材。
    const sizeWarning=kind==='video'?referenceImageSizeWarning(selectedModel,imageInputs,assets):undefined;
    if(sizeWarning)throw new Error(sizeWarning);
    const prompt=assembled?.prompt??[mask?.prompt||source.metadata?.prompt||source.metadata?.content||'',...inputs.filter(n=>n.type==='text').map(n=>n.metadata?.content||'')].filter(Boolean).join('\n\n').trim();
    if(!prompt)throw new Error('请填写提示词或连接文本节点');
    const inputLimit=kind==='audio'?selectedModel.audio_capabilities?.max_input_chars:kind==='text'?selectedModel.text_capabilities?.max_input_chars:capabilities.max_prompt_chars;
    if(inputLimit&&Array.from(prompt).length>inputLimit)throw new Error(kind==='audio'?`朗读文本最多 ${inputLimit.toLocaleString()} 个字符（包含参考文本）`:'提示词超过服务器允许长度');
    const videoCaps=selectedModel.video_capabilities as {durations:number[];resolutions:string[];aspect_ratios:string[];max_reference_images:number;max_reference_videos?:number}|undefined;
    if((kind==='audio'||kind==='text')&&inputs.some(n=>['image','video','audio'].includes(n.type)))throw new Error(kind==='audio'?'语音生成只支持文本参考，请移除图片、视频或音频连线':'当前文本模型只支持文本参考，请移除图片、视频或音频引用');
    const maxReferences=kind==='video'?(videoCaps?.max_reference_images||0):selectedModel.max_reference_images;
    if(imageInputs.length>maxReferences)throw new Error(`当前模型最多支持 ${maxReferences} 张参考图`);
    if(imageInputs.some(n=>n.metadata?.expires_at&&Date.parse(n.metadata.expires_at)<=Date.now()))throw new Error('参考图已过期，请重新上传');
    // 参考视频数量来自服务端目录（协议能力与管理员配置的较小值）；音频参考尚无协议承载，图片生成也不接受视频。
    const videoInputs=inputs.filter(n=>n.type==='video'&&n.metadata?.asset_id);
    if(inputs.some(n=>n.type==='audio'&&n.metadata?.asset_id))throw new Error('创作台暂不支持音频参考素材，请移除音频连线');
    if(videoInputs.length&&kind!=='video')throw new Error('图片生成不支持视频参考素材，请移除视频连线');
    const maxVideos=kind==='video'?(videoCaps?.max_reference_videos||0):0;
    if(videoInputs.length>maxVideos)throw new Error(maxVideos?`当前模型最多支持 ${maxVideos} 个参考视频`:'当前模型的协议不支持参考视频，请移除视频连线或换用支持参考视频的模型');
    if(videoInputs.some(n=>n.metadata?.expires_at&&Date.parse(n.metadata.expires_at)<=Date.now()))throw new Error('参考视频已过期，请重新上传');
    const operation=mask?'inpaint':imageInputs.length&&kind==='image'?'edit':'generate';
    if(!selectedModel.operations.includes(operation))throw new Error('此模型未开放所需的生成能力');
    const params:ExecuteParams={group_id:selectedModel.group_id,model:selectedModel.model,operation,prompt,references:[...imageInputs.map(n=>({asset_id:n.metadata!.asset_id!,...(kind==='video'?{role:videoImageRole(imageInputs.length,videoInputs.length)}:{})})),...videoInputs.map(n=>({asset_id:n.metadata!.asset_id!,role:'reference_video'}))]};
    // 显示与提交统一按当前模型能力取值，导入或旧节点不能发送已停用的参数。
    const settings=resolveGenerationSettings(selectedModel,source.metadata,kind);
    if(kind==='image'){
      if(!settings.imageSize)throw new Error('当前模型没有可用的尺寸档位');
      params.image_size=settings.imageSize;params.aspect_ratio=settings.aspectRatio;params.quality=settings.quality;params.background=settings.background;
      if(mask)params.mask_asset_id=mask.assetId;
    }else if(kind==='text'){
      if(!settings.maxOutputTokens)throw new Error('当前模型没有可用的文本输出上限');
      params.max_output_tokens=settings.maxOutputTokens;delete params.references;
    }else if(kind==='audio'){
      if(!settings.voice||!settings.audioFormat||settings.speed===undefined)throw new Error('当前模型没有可用的语音参数');
      params.voice=settings.voice;params.audio_format=settings.audioFormat;params.speed=settings.speed;
      delete params.references;
    }else{
      if(!settings.duration)throw new Error('当前模型没有可用的视频时长');
      params.endpoint=selectedModel.video_endpoint;params.duration=settings.duration;params.resolution=settings.resolution;params.aspect_ratio=settings.aspectRatio;
    }
    if(kind==='image'&&!settings.outputCount)throw new Error('当前模型没有可用的生成张数');
    const prepared:GenerationRequest={key:`canvas-${randomId()}`,kind,params,count:kind==='image'?settings.outputCount!:1};request=prepared;
    const resultIndex=current.connections.filter(edge=>edge.fromNodeId===source.id).length;
    const target=source.type==='config'?makeNode(kind,{x:source.position.x+source.width+100,y:source.position.y+resultIndex*280}):latest;
    targetId=target.id;
    // 音频文本参考只在本次请求中拼接，不能写回输入框导致下次重复朗读。
    const metadata={...(source.type==='config'?source.metadata:{}),...target.metadata,composerContent:undefined,generation_target_id:undefined,execution_id:undefined,execution_ids:undefined,generation_key:kind==='image'?prepared.key:undefined,generation_request:prepared,generation_phase:undefined,status:'loading' as const,errorDetails:undefined,prompt:kind==='audio'&&source.type!=='config'?(source.metadata?.prompt||source.metadata?.content||''):prompt,model:modelId(selectedModel),...(kind==='audio'?{voice:settings.voice,audio_format:settings.audioFormat,speed:settings.speed}:{})};
    update(old=>({...old,nodes:source.type==='config'?[...old.nodes.map(n=>n.id===source.id?{...n,metadata:{...n.metadata,generation_target_id:targetId}}:n),{...target,metadata}]:old.nodes.map(n=>n.id===targetId?{...n,metadata}:n),connections:source.type==='config'?[...old.connections,{id:randomId(),fromNodeId:source.id,toNodeId:targetId}]:old.connections}));
    setSelected(new Set([targetId]));
    await cloud.flush();saved=true;
    await submitSavedGeneration(targetId,prepared);
   }catch(e){
    // 尚未形成提交请求的错误（模型、参数、参考图校验等）贴在当前面板；局部重绘弹窗中仍同时使用顶部提示。
    if(!request&&(e as Error)?.name!=='AbortError'){setGenerationError({nodeId:source.id,message:(e as Error).message||'生成失败'});if(mask)report(e);}
    else report(e);
    // 保存失败不会请求上游；已提交但响应未知时保留键，供按钮核对及刷新恢复。
    if(request&&!saved)update(old=>({...old,nodes:old.nodes.map(n=>n.id===targetId&&n.metadata?.generation_request?.key===request?.key?{...n,metadata:{...n.metadata,status:'error',errorDetails:'提交尚未确认，可点击核对提交；不会创建重复任务'}}:n)}));
   }finally{submitLocks.current.delete(source.id);setBusy(false);}
 }

 async function submitSavedGeneration(nodeId:string,request:GenerationRequest){
   // 必须保存成功才提交，重放同键仍交由服务端事务去重。
   await cloud.flush();
   try{
     if(request.kind==='image'){
       const batch=await createImageBatch(id,nodeId,request.params,request.count,request.key);
       if(canvasIdRef.current!==id)return;
       if(batch.batch_id!==request.key||!batch.executions.length||batch.executions.some(e=>e.canvas_id!==id||e.node_id!==nodeId||e.batch_id!==request.key))throw new Error('服务端返回的任务归属不一致');
       batch.executions.forEach(e=>activeExecutions.current.set(e.id,e));
       update(old=>mergeExecutions(old,[...activeExecutions.current.values()],id));
     }else{
       const execution=await createExecution(id,nodeId,request.kind,request.params,request.key);
       if(canvasIdRef.current!==id)return;
       if(execution.canvas_id!==id||execution.node_id!==nodeId||execution.kind!==request.kind)throw new Error('服务端返回的任务归属不一致');
       activeExecutions.current.set(execution.id,execution);
       update(old=>mergeExecution({...old,nodes:old.nodes.map(n=>n.id===nodeId?{...n,metadata:{...n.metadata,generation_request:undefined,execution_id:execution.id}}:n)},execution,id));
     }
   }catch(e){
     // 确定被拒绝的请求可重新填写；超时、断网和服务端异常保留原键等待核对。
     // 只有本站容量准入的稳定 429 明确表示尚未受理；普通限流或上游 429 仍保留原键核对。
     const rejected=e instanceof ApiError&&([400,403,404,413,422].includes(e.status)||(e.status===429&&e.code==='CANVAS_EXECUTION_LIMIT'));
     update(old=>({...old,nodes:old.nodes.map(n=>n.id===nodeId&&n.metadata?.generation_request?.key===request.key?{...n,metadata:{...n.metadata,status:'error',generation_key:rejected?undefined:n.metadata.generation_key,generation_request:rejected?undefined:request,errorDetails:rejected?(e as Error).message:`${(e as Error).message?`${(e as Error).message}。`:''}提交结果待确认，请点击核对提交；不会重复生成或扣费`}}:n)}));
     throw e;
   }
 }
 const active=doc?.nodes.find(n=>selected.size===1&&selected.has(n.id));const kind=generationKind(active);const chosenModel=models.find(m=>modelId(m)===active?.metadata?.model&&mediaType(m)===kind);
 const available=models.filter(m=>mediaType(m)===kind);const modelOptions=available.map(m=>({value:modelId(m),label:`${m.display_name||m.model}（${m.group_name}）`}));
 const videoCaps=chosenModel?.video_capabilities as {durations:number[];resolutions:string[];aspect_ratios:string[]}|undefined;
 const canGenerate=!!active&&['image','video','audio','text'].includes(active.type);
 const referenceNodes=active&&doc?selectedReferences(doc,active.id).filter(n=>kind==='audio'||kind==='text'?['text','image','video','audio'].includes(n.type):n.type==='image'||n.type==='video'):[];
 // @ 标签沿用提交时的素材顺序编号：“图片1”就是请求中的第一张参考图；语音会逐字朗读输入，不提供 @ 标签。
 const mentionReferences=kind==='audio'?[]:promptReferences(referenceNodes);
 const sizeWarning=active?referenceWarning(active):undefined;
 const effectiveSettings=resolveGenerationSettings(chosenModel,active?.metadata,kind);
 const qualityLabels:Record<string,string>={auto:'自动',low:'低',medium:'中',high:'高',hd:'高清',standard:'标准'};
 const quality=effectiveSettings.quality||'auto';
 // 自动比例的协议值只用于提交，摘要继续展示用户选择的中文含义。
 const ratioLabel=['adaptive','auto'].includes(effectiveSettings.aspectRatio||'')?'自动':effectiveSettings.aspectRatio||'自动';
 const summary=kind==='text'?`最多 ${effectiveSettings.maxOutputTokens||'—'} Token`:kind==='audio'?`${effectiveSettings.voice?effectiveSettings.voice[0].toUpperCase()+effectiveSettings.voice.slice(1):'声音'} · ${effectiveSettings.audioFormat?.toUpperCase()||'格式'} · ${effectiveSettings.speed??'—'}x`:kind==='video'?`${effectiveSettings.resolution||'自动'} · ${ratioLabel} · ${effectiveSettings.duration||'—'}s`:`${qualityLabels[quality]||quality} · ${effectiveSettings.imageSize||'自动'} · ${ratioLabel} · ${effectiveSettings.outputCount||1} 张`;
 // 只在切换节点时关闭弹层，缩放和平移不触发生成或更改模型参数。
 useLayoutEffect(()=>{setSettingsOpen(false);setInfoOpen(false);setExpandedPrompt(false);},[active?.id]);
 const visible=useMemo(()=>{if(!doc)return[];const {x,y,k}=doc.viewport;return doc.nodes.filter(n=>n.position.x+n.width>-x/k-300&&n.position.x<(viewSize.width-x)/k+300&&n.position.y+n.height>-y/k-300&&n.position.y<(viewSize.height-y)/k+300);},[doc,viewSize]);
 if(!doc||!cloud.record)return <div className="empty">{cloud.error||<LoaderCircle className="animate-spin"/>}</div>;
 return <div className="editor" style={{background:theme.canvas.background,color:theme.node.text}}>
  {sidebar&&<aside className="side-panel" style={{background:theme.node.panel,borderColor:theme.node.stroke}}><div className="side-tabs">{(['canvas','assets','prompts'] as const).map(tab=><button key={tab} className={sideTab===tab?'active':''} onClick={()=>setSideTab(tab)}>{tab==='canvas'?'画布':tab==='assets'?'素材':'提示词库'}</button>)}</div><div className="side-subheading"><span>{sideTab==='canvas'?`画布元素 ${doc.nodes.length}`:sideTab==='assets'?'个人素材 · 保留 7 天':'提示词库'}</span>{sideTab==='canvas'&&<><button title="选择所有节点" onClick={()=>setSelected(new Set(doc.nodes.map(n=>n.id)))}><ListChecks size={14}/><span>选择</span></button><Choice compact label="筛选节点类型" value={nodeFilter} options={[{value:'all',label:'全部'},...Object.entries(names).map(([value,label])=>({value,label}))]} onChange={setNodeFilter}/><button title="切换列表布局" onClick={()=>setGridList(v=>!v)}><LayoutGrid size={14}/></button></>}</div><label className="search"><Search size={15}/><input placeholder="搜索节点" value={search} onChange={e=>setSearch(e.target.value)}/></label>
   <div className={`side-list ${gridList?'grid-list':''}`}>{sideTab==='assets'?<>{assets.filter(a=>!search||a.media_type.includes(search)).map(asset=><button key={asset.id} className="asset-card" disabled={isAssetExpired(asset)} onClick={()=>void insertAsset(asset)}><AssetView id={asset.id} type={asset.media_type} expired={isAssetExpired(asset)}/><span>{asset.media_type} · {isAssetExpired(asset)?'已过期':new Date(asset.expires_at).toLocaleDateString()}</span></button>)}{assetTotal>50&&<div className="pagination"><button disabled={assetPage===1} onClick={()=>setAssetPage(p=>p-1)}>上一页</button><span>{assetPage}</span><button disabled={assetPage*50>=assetTotal} onClick={()=>setAssetPage(p=>p+1)}>下一页</button></div>}</>:doc.nodes.filter(n=>(sideTab!=='prompts'||n.type==='text'||!!n.metadata?.prompt)&&(nodeFilter==='all'||sideTab!=='canvas'||n.type===nodeFilter)&&`${n.title} ${n.metadata?.content||''} ${n.metadata?.prompt||''}`.toLowerCase().includes(search.toLowerCase())).map(node=>{const Icon=iconFor(node.type);return <button key={node.id} className={`node-list-item ${selected.has(node.id)?'selected':''}`} onClick={()=>{setSelected(new Set([node.id]));setSettingsOpen(false);focus(node);}}><div className="node-list-preview">{node.type==='image'&&node.metadata?.asset_id?<AssetView id={node.metadata.asset_id} type="image"/>:<Icon size={18}/>}</div><div className="node-list-copy"><strong>{node.title}</strong><small>{names[node.type]}</small></div><span className={`node-status ${node.metadata?.status||'idle'}`}/><Eye size={13} className="node-visibility"/>{sideTab==='prompts'&&<span className="text-preview">{node.metadata?.content||node.metadata?.prompt}</span>}</button>;})}</div><button className="flat" onClick={()=>sideTab==='assets'?pickUpload():addNode('text')}>{sideTab==='assets'?'+ 上传素材':'+ 添加文本'}</button></aside>}
  <section className="canvas-main"><header className="canvas-top"><button title="画布库" onClick={()=>void cloud.flush().then(()=>navigate('/')).catch(report)}><ArrowLeft size={18}/></button><button title="侧栏" onClick={()=>setSidebar(s=>!s)}>{sidebar?<PanelLeftClose size={18}/>:<PanelLeftOpen size={18}/>}</button><strong>{cloud.record.title}</strong><span className="save-state">{cloud.conflict?'保存冲突':cloud.saving?'保存中…':cloud.error?'保存失败':'已保存'}</span><span className="grow"/><button title="导出画布" onClick={()=>void exportCanvas({...cloud.record!,document:doc}).catch(report)}><Download size={18}/></button><button title="切换主题" onClick={()=>setTheme(themeName==='dark'?'light':'dark')}>{themeName==='dark'?<Sun size={18}/>:<Moon size={18}/>}</button></header>
   {(error||cloud.error||executionSyncError)&&<div className="editor-alert" role="alert">{error||cloud.error||executionSyncError}{cloud.conflict&&<><button onClick={()=>void exportCanvas({...cloud.record!,document:doc}).catch(report)}>导出本地草稿</button><button onClick={()=>{if(confirm('重新载入会放弃未保存修改，是否继续？'))void cloud.reload().catch(report);}}>重新载入</button></>}<button aria-label="关闭错误" onClick={()=>{setError('');setExecutionSyncError('');}}><X size={16}/></button></div>}
   <div className="canvas-stage">
    <InfiniteCanvas containerRef={container} viewport={doc.viewport} tool={tool} backgroundMode={doc.backgroundMode} onViewportChange={viewport} onCanvasDeselect={()=>{setSelected(new Set());setSelectedEdge('');connectionController.current?.cancel();}} onCanvasMouseDown={e=>nodeGesture.begin(e,{kind:'select',x:e.clientX,y:e.clientY,start:snapshot(doc),ids:new Set()})} onCanvasDoubleClick={e=>addNode('text',world(e.clientX,e.clientY))} onContextMenu={e=>e.preventDefault()} onDrop={e=>{e.preventDefault();void upload([...e.dataTransfer.files],world(e.clientX,e.clientY));}}>
     <CanvasConnectionLayer ref={connectionController} containerRef={container} nodes={doc.nodes} connections={doc.connections} viewport={doc.viewport} selected={selected} selectedEdge={selectedEdge} onSelect={edgeId=>{setSelectedEdge(edgeId);setSelected(new Set());}} onConnect={link} onDropEmpty={(handle,point,client)=>setConnectionCreate({handle,point,client})}/>
     {visible.map(node=>{const Icon=iconFor(node.type);const isSelected=selected.has(node.id);const expired=!!node.metadata?.expires_at&&Date.parse(node.metadata.expires_at)<=Date.now();return <div key={node.id} data-node-id={node.id} className={`canvas-node ${node.type==='group'?'group-node':''} ${isSelected?'selected':''}`} style={{left:node.position.x,top:node.position.y,width:node.width,height:node.height,background:node.type==='group'?'transparent':theme.node.panel,borderColor:isSelected?'#3b9cff':theme.node.stroke,zIndex:isSelected?20:node.type==='group'?0:1}} onPointerDownCapture={event=>{if(node.type==='config'&&event.button===0)selectMediaNode(event,node);}} onPointerDown={e=>onMoveStart(e,node)} onDoubleClick={e=>{e.stopPropagation();setSelected(new Set([node.id]));}}>
      {node.type!=='config'&&<div className={`node-heading${['video','audio'].includes(node.type)?' video-node-heading':''}`} title={['video','audio'].includes(node.type)?`拖动标题移动${names[node.type]}节点`:undefined}><span>{node.title}</span></div>}
      {node.type==='text'?<textarea className="text-node-content" value={node.metadata?.content||''} placeholder="输入文本或提示词…" onPointerDown={e=>e.stopPropagation()} onChange={e=>patchNode(node.id,{content:e.target.value})}/>:node.type==='image'?<ImageResults node={node} onSelect={assetId=>selectImage(node.id,assetId)} onDownload={downloadImage} onExtract={assetId=>void assetMetadata(assetId).then(asset=>insertAsset(asset,{x:node.position.x+node.width+48,y:node.position.y})).catch(report)}/>:['video','audio'].includes(node.type)?<AssetView id={node.metadata?.asset_id} type={node.type} expired={expired} videoNodeInteraction={node.type==='video'?{scale:doc.viewport.k,height:node.height,onSelect:event=>selectMediaNode(event,node)}:undefined} audioNodeInteraction={node.type==='audio'?{onSelect:event=>selectMediaNode(event,node)}:undefined}/>:node.type==='config'?<ConfigNodePanel node={node} inputs={configInputs(doc,node.id)} models={models} selected={isSelected} busy={busy||cloud.conflict} pending={!!doc.nodes.find(n=>n.id===node.metadata?.generation_target_id)?.metadata?.generation_request} running={doc.nodes.find(n=>n.id===node.metadata?.generation_target_id)?.metadata?.status==='loading'} reconciling={doc.nodes.find(n=>n.id===node.metadata?.generation_target_id)?.metadata?.generation_phase==='reconciliation'} composerOpen={configComposerId===node.id} onChange={patch=>patchNode(node.id,patch)} onComposerToggle={()=>{setSelected(new Set([node.id]));setConfigComposerId(current=>current===node.id?'':node.id);if(configComposerId!==node.id)viewport(focusNodeLayout(node,viewSize,304));}} onGenerate={()=>void execute(node)}/>:null}
      {node.type==='config'&&generationError?.nodeId===node.id&&<p className="panel-error config-node-error" role="alert" data-canvas-no-zoom onPointerDown={e=>e.stopPropagation()} style={{transform:`scale(${1/doc.viewport.k})`}}><span>{generationError.message}</span><button type="button" aria-label="关闭错误提示" onClick={()=>setGenerationError(null)}><X size={12}/></button></p>}
      {node.type==='config'&&isSelected&&configComposerId===node.id&&<div className="node-panel" style={{width:Math.min(600,viewSize.width-24),transform:`translateX(-50%) scale(${1/doc.viewport.k})`}}><ConfigComposer kind={configKind(node)} onAddText={()=>addTextReference(node)} onUploadImage={()=>pickUpload({nodeId:node.id,mode:'reference'})} value={node.metadata?.composerContent??node.metadata?.prompt??''} inputs={configInputs(doc,node.id)} onChange={value=>patchNode(node.id,{composerContent:value})} onClose={()=>setConfigComposerId('')} onDisconnect={from=>change(old=>({...old,connections:old.connections.filter(edge=>!(edge.toNodeId===node.id&&edge.fromNodeId===from))}))}/></div>}
      <CanvasGenerationLoading node={node}/>
      {node.metadata?.status==='error'&&<GenerationStatusDetails node={node}/>}
      <button className="handle target" style={{touchAction:'none'}} title="连接输入" onPointerDown={e=>connectionController.current?.begin(e,node.id,'target')}/>
      {node.type!=='config'&&<button className="handle source" style={{touchAction:'none'}} title="拖动或点击后选择输入连接点" onPointerDown={e=>connectionController.current?.begin(e,node.id,'source')}/>}
      {isSelected&&<button className="resize-handle" aria-label="缩放节点" onPointerDown={e=>onMoveStart(e,node,true)}/>}
       {isSelected&&active?.id===node.id&&canGenerate&&<div className="node-panel" data-canvas-no-zoom onPointerDown={e=>e.stopPropagation()} style={{width:Math.min(600,viewSize.width-24),transform:`translateX(-50%) scale(${1/doc.viewport.k})`}}>
       <div className={`generation-panel composer-panel ${expandedPrompt?'expanded':''}`}>
        <div className="reference-section"><span className="composer-label">参考内容</span><div className="reference-row">
         <button className="reference-add" type="button" title={kind==='audio'||kind==='text'?'添加参考文本':'添加参考素材'} onClick={()=>kind==='audio'||kind==='text'?addTextReference(active):pickUpload({nodeId:active.id,mode:'reference'})}>+</button>
         {referenceNodes.map((reference,index)=>reference.type==='text'?<button type="button" className="reference-thumb reference-text" title={reference.metadata?.content||'编辑参考文本'} key={reference.id} onClick={()=>{setSelected(new Set([reference.id]));focus(reference);}}><Type size={18}/><span>{reference.metadata?.content?.slice(0,20)||'参考文本'}</span></button>:reference.type==='image'?<ReferenceImagePreview key={reference.id} assetId={reference.metadata?.asset_id} title={reference.title} index={index} expiresAt={reference.metadata?.expires_at}/>:<div className="reference-thumb" key={reference.id}><AssetView id={reference.metadata?.asset_id} type={reference.type} expired={isAssetExpired({expires_at:reference.metadata?.expires_at||''})}/></div>)}
        </div></div>
        {kind==='audio'&&<p className="audio-reference-hint">输入要朗读的文本，也可连接文本节点；不支持图片或音频参考。</p>}
        {sizeWarning&&<p className="reference-size-warning" id="reference-size-warning" role="alert">{sizeWarning}</p>}
        <PromptMentionTextarea style={{height:composerPromptHeight(viewSize.height,panelExtra(active))}} className="composer-prompt" aria-label="提示词" placeholder={(kind==='text'?'输入文本生成要求':kind==='audio'?'输入要朗读的文本':'描述要生成的'+names[kind]+'内容')+(mentionReferences.length?'，键入 @ 引用参考内容':'')} value={active.metadata?.prompt||''} references={mentionReferences} onChange={value=>patchNode(active.id,{prompt:value})}/>
        <div className="composer-footer">
         <div className="composer-controls">
         <button className="composer-icon" title={expandedPrompt?'收起提示词':'展开提示词'} onClick={()=>setExpandedPrompt(v=>!v)}>{expandedPrompt?<Minimize2 size={16}/>:<Maximize2 size={16}/>}</button>
         <button className="composer-icon" title="提示词库" onClick={()=>{setSidebar(true);setSideTab('prompts');}}><BookOpen size={16}/></button>
         <Choice compact label="选择模型" value={active.metadata?.model||''} options={modelOptions} onChange={v=>patchNode(active.id,{model:v,image_size:undefined,aspect_ratio:undefined,quality:undefined,seconds:undefined,resolution:undefined,voice:undefined,audio_format:undefined,speed:undefined,max_output_tokens:undefined})}/>
         <button ref={setSettingsAnchor} className={`summary-pill ${kind}`} type="button" aria-expanded={settingsOpen} onClick={()=>setSettingsOpen(v=>!v)} aria-label="打开生成设置" title={summary}><Settings2 size={16}/><span>{summary}</span></button>
         </div>
         <button className="primary generate-icon" disabled={busy||cloud.conflict||!!sizeWarning||(!chosenModel&&!active.metadata?.generation_request)||(active.metadata?.status==='loading'&&!active.metadata?.generation_request)} aria-describedby={sizeWarning?'reference-size-warning':undefined} title={active.metadata?.generation_request?'核对提交':active.metadata?.status==='loading'?(active.metadata.generation_phase==='reconciliation'?'任务核对中…':'生成中…'):`生成${names[kind]}`} aria-label={active.metadata?.generation_request?'核对提交':active.metadata?.status==='loading'?(active.metadata.generation_phase==='reconciliation'?'任务核对中…':'生成中…'):`生成${names[kind]}`} onClick={()=>void execute(active)}>{active.metadata?.status==='loading'&&active.metadata.generation_phase==='reconciliation'?<Clock3 size={18}/>:busy||active.metadata?.status==='loading'?<LoaderCircle className="animate-spin" size={18}/>:<ArrowUp size={18}/>}</button>
        </div>
        {generationError?.nodeId===active.id&&<p className="panel-error" role="alert"><span>{generationError.message}</span><button type="button" aria-label="关闭错误提示" onClick={()=>setGenerationError(null)}><X size={12}/></button></p>}
        {!available.length&&<p className="muted">管理员尚未开放此类模型。</p>}
        {kind==='video'&&<ModelUsageNotes model={chosenModel} condensed={viewSize.height<600}/>}

       </div>
      </div>}
     </div>;})}
     {box&&<div className="selection-box" style={{left:Math.min(box.from.x,box.to.x),top:Math.min(box.from.y,box.to.y),width:Math.abs(box.to.x-box.from.x),height:Math.abs(box.to.y-box.from.y)}}/>}
    </InfiniteCanvas>
    {connectionCreate&&<ConnectionCreateMenu pending={connectionCreate} onCreate={createConnected} onClose={()=>setConnectionCreate(null)}/>}
    {active&&<div className={`node-actions ${active.type==='image'?'image-actions':''}`} data-canvas-no-zoom style={{left:doc.viewport.x+(active.position.x+active.width/2)*doc.viewport.k,top:doc.viewport.y+active.position.y*doc.viewport.k-14}} onPointerDown={e=>e.stopPropagation()}>
      <button title="信息" onClick={()=>setInfoOpen(v=>!v)}><Info size={16}/><span>信息</span></button>
      <button title="删除节点" onClick={removeSelected}><Trash2 size={16}/><span>删除</span></button>
      {active.metadata?.asset_id&&<button title="下载" onClick={()=>void assetBlob(active.metadata!.asset_id!).then(blob=>download(blob,`${active.title}.${blob.type.split('/')[1]||'bin'}`)).catch(report)}><Download size={16}/><span>下载</span></button>}
      {active.type==='config'&&<button title="生成配置" aria-expanded={configComposerId===active.id} onClick={()=>{setConfigComposerId(current=>current===active.id?'':active.id);if(configComposerId!==active.id)viewport(focusNodeLayout(active,viewSize,304));}}><Settings2 size={16}/><span>生成配置</span></button>}
      {['video','audio'].includes(active.type)&&<button title="编辑提示词" onClick={()=>container.current?.querySelector<HTMLTextAreaElement>('.composer-prompt')?.focus()}><Pencil size={16}/><span>编辑</span></button>}
      {['image','video','audio'].includes(active.type)&&<button title={`上传${names[active.type]}`} onClick={()=>pickUpload({nodeId:active.id,mode:'replace'})}><Upload size={16}/><span>{`上传${names[active.type]}`}</span></button>}
      {active.type==='image'&&active.metadata?.asset_id&&<><button title="裁剪" onClick={()=>void openImageTool('crop',active)}><Scissors size={16}/></button><button title="局部重绘" onClick={()=>void openImageTool('mask',active)}><Brush size={16}/></button></>}
    </div>}
    {active&&infoOpen&&<aside className="node-info" role="dialog" aria-label="节点信息"><header><strong>节点信息</strong><button title="关闭信息" onClick={()=>setInfoOpen(false)}><X size={16}/></button></header><p>{active.title}</p><p>{names[active.type]} · {Math.round(active.width)} × {Math.round(active.height)}</p><p>{active.metadata?.model||'尚未选择模型'}</p>{active.metadata?.expires_at&&<p>素材保留至 {new Date(active.metadata.expires_at).toLocaleString()}</p>}</aside>}
    {canGenerate&&active&&<GenerationSettings open={settingsOpen} onClose={()=>setSettingsOpen(false)} anchor={settingsAnchor} kind={kind} node={active} model={chosenModel} onChange={value=>patchNode(active.id,value)}/>}
    {miniMap&&<Minimap nodes={doc.nodes} viewport={doc.viewport} viewportSize={viewSize} onViewportChange={viewport}/>}<CanvasZoomControls scale={doc.viewport.k} onScaleChange={k=>{const ratio=k/doc.viewport.k;viewport({x:viewSize.width/2-(viewSize.width/2-doc.viewport.x)*ratio,y:viewSize.height/2-(viewSize.height/2-doc.viewport.y)*ratio,k});}} onReset={()=>focus()} isMiniMapOpen={miniMap} onToggleMiniMap={()=>setMiniMap(v=>!v)}/>
    <div className="canvas-dock-wrap"><div className="canvas-dock" style={{background:theme.toolbar.panel,borderColor:theme.toolbar.border}}><button title="切换选择/平移" onClick={()=>setTool(t=>t==='pan'?'select':'pan')}>{tool==='pan'?<Hand size={18}/>:<MousePointer2 size={18}/>}</button><button title="撤销" disabled={!history.current.past.length} onClick={undo}><Undo2 size={18}/></button><button title="重做" disabled={!history.current.future.length} onClick={redo}><Redo2 size={18}/></button><button title="聚焦所选" onClick={()=>focus()}><Focus size={18}/></button><i/>{(['text','image','video','audio','config'] as const).map(type=>{const Icon=iconFor(type);return <button key={type} title={`添加${names[type]}`} onClick={()=>addNode(type)}><Icon size={18}/></button>;})}<button title="分组" disabled={!selected.size} onClick={group}><Group size={18}/></button><button title="上传素材" onClick={()=>pickUpload()}><Upload size={18}/></button><button title="切换网格" onClick={()=>change(old=>({...old,backgroundMode:old.backgroundMode==='dots'?'blank':old.backgroundMode==='blank'?'lines':'dots'}))}><Palette size={18}/></button><i/><button title="删除所选" disabled={!selected.size&&!selectedEdge} onClick={removeSelected}><Trash2 size={18}/></button></div></div>
   </div>
  </section><input type="file" ref={uploadInput} hidden multiple accept={mediaUploadAccept} onChange={e=>{const intent=uploadTarget.current;uploadTarget.current=null;void upload([...e.target.files||[]],undefined,intent);e.target.value='';}}/>
  {active&&<Modal title="提示词" open={expandedPrompt} onCancel={()=>setExpandedPrompt(false)} footer={null} width={720} centered><PromptMentionTextarea className="expanded-prompt-input" aria-label="展开的提示词" value={active.metadata?.prompt||''} references={mentionReferences} onChange={value=>patchNode(active.id,{prompt:value})}/></Modal>}
  {imageTool?.kind==='crop'&&<CanvasNodeCropDialog open dataUrl={imageTool.url} onClose={()=>setImageTool(null)} onConfirm={rect=>{const tool=imageTool;setImageTool(null);void cropDataUrl(tool.url,rect).then(async data=>{const blob=await(await fetch(data)).blob();const asset=await uploadAsset(blob,id,'crop.png');await insertAsset(asset,{x:tool.node.position.x+tool.node.width+40,y:tool.node.position.y});await refreshAssets();}).catch(report);}}/>}
  {imageTool?.kind==='mask'&&<CanvasNodeMaskEditDialog open dataUrl={imageTool.url} onClose={()=>setImageTool(null)} onConfirm={payload=>{const tool=imageTool;setImageTool(null);void fetch(payload.maskDataUrl).then(r=>r.blob()).then(async blob=>{const asset=await uploadAsset(blob,id,'mask.png');patchNode(tool.node.id,{mask_asset_id:asset.id});if(payload.generate)await execute(tool.node,{assetId:asset.id,prompt:payload.prompt});}).catch(report);}}/>}
 </div>;
}
