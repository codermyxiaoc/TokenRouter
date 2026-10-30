import type {CanvasDocument,CanvasRecord} from './types';

// 节点默认尺寸，与编辑器新建节点（Editor makeNode）一致。
const defaultSize:Record<string,{width:number;height:number}>={
  image:{width:340,height:240},config:{width:340,height:240},video:{width:420,height:236},
  audio:{width:320,height:100},text:{width:320,height:200},group:{width:640,height:420},
};
const finite=(value:unknown):value is number=>typeof value==='number'&&Number.isFinite(value);

// 服务端接口允许只含 id/type 的精简节点或缺少 viewport/connections 的文档；编辑器按坐标、尺寸与视口渲染，缺失时打开即白屏。
// 载入时为缺少或非法的 position/width/height/title、viewport 与 connections 补默认值，只修正展示所需字段，不改动其它内容；
// 补齐后的文档在用户下次编辑保存时一并写回。
export function normalizeCanvasDocument(document:CanvasDocument):CanvasDocument {
  if(!document||typeof document!=='object')return document;
  let changed=false;
  const sourceNodes=Array.isArray(document.nodes)?document.nodes:[];
  if(sourceNodes!==document.nodes)changed=true;
  const nodes=sourceNodes.map((node,index)=>{
    if(!node||typeof node!=='object')return node;
    const size=defaultSize[node.type]||defaultSize.image;
    const position=node.position&&finite(node.position.x)&&finite(node.position.y)?node.position:{x:index*40,y:index*40};
    const width=finite(node.width)&&node.width>0?node.width:size.width;
    const height=finite(node.height)&&node.height>0?node.height:size.height;
    // 缺标题时用节点类型兜底，避免下载文件名等处显示 undefined。
    const title=typeof node.title==='string'&&node.title.trim()?node.title:(node.type||'node');
    if(position===node.position&&width===node.width&&height===node.height&&title===node.title)return node;
    changed=true;
    return {...node,position,width,height,title};
  });
  const view=document.viewport as Partial<CanvasDocument['viewport']>|undefined;
  const viewport=view&&finite(view.x)&&finite(view.y)&&finite(view.k)&&view.k>0?document.viewport:{x:0,y:0,k:1};
  const connections=Array.isArray(document.connections)?document.connections:[];
  if(viewport!==document.viewport||connections!==document.connections)changed=true;
  return changed?{...document,nodes,connections,viewport}:document;
}

export function normalizeCanvasRecord(record:CanvasRecord):CanvasRecord {
  if(!record||!record.document)return record;
  const document=normalizeCanvasDocument(record.document);
  return document===record.document?record:{...record,document};
}
