import {zipSync,unzipSync,strToU8,strFromU8} from 'fflate';
import {assetBlob,uploadAsset} from './api';
import {clearExecutionMetadata,validateDocument} from './document';
import type {CanvasRecord} from './types';

export function download(blob:Blob,name:string){const url=URL.createObjectURL(blob);const a=document.createElement('a');a.href=url;a.download=name;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}
export async function exportCanvas(canvas:CanvasRecord){
  const doc=validateDocument(canvas.document);doc.nodes=clearExecutionMetadata(doc.nodes);
  const files:Record<string,Uint8Array>={};const assets:Record<string,{path:string;mime:string}>={};
  const ids=new Set(doc.nodes.flatMap(n=>[n.metadata?.asset_id,n.metadata?.mask_asset_id,...n.metadata?.asset_ids||[]].filter((id):id is string=>!!id)));
  for(const id of ids){try{const blob=await assetBlob(id);const path=`assets/${Object.keys(assets).length}.bin`;files[path]=new Uint8Array(await blob.arrayBuffer());assets[id]={path,mime:blob.type};}catch{ /* 过期素材保留占位，不伪造已成功导出。 */ }}
  files['canvas.json']=strToU8(JSON.stringify({format:'tokenrouter-canvas',version:1,title:canvas.title,document:doc,assets}));
  download(new Blob([zipSync(files,{level:0}) as Uint8Array<ArrayBuffer>],{type:'application/zip'}),`${canvas.title.replace(/[\\/:*?"<>|]/g,'_')}.zip`);
}
// ZIP 限制先检中央目录声明的展开体积，防止压缩炸弹在解析前耗尽内存。
export function archiveManifest(bytes:Uint8Array){
  const maxBytes=256*1024*1024; if(bytes.byteLength>maxBytes)throw new Error('画布压缩包超过 256 MiB');
  let expanded=0;const view=new DataView(bytes.buffer,bytes.byteOffset,bytes.byteLength);
  for(let i=0;i+46<=bytes.length;i++){if(view.getUint32(i,true)!==0x02014b50)continue;expanded+=view.getUint32(i+24,true);if(expanded>maxBytes)throw new Error('画布解压后超过 256 MiB');i+=45+view.getUint16(i+28,true)+view.getUint16(i+30,true)+view.getUint16(i+32,true);}
  const files=unzipSync(bytes,{filter:file=>file.originalSize<=maxBytes});
  if(!files['canvas.json'])throw new Error('不是本站画布导出文件');
  const manifest=JSON.parse(strFromU8(files['canvas.json']));
  if(manifest.format!=='tokenrouter-canvas'||manifest.version!==1)throw new Error('不支持的画布版本');
  return {document:validateDocument(manifest.document),title:String(manifest.title||'导入画布'),assets:manifest.assets as Record<string,{path:string;mime:string}>,files};
}
export async function importCanvasAssets(raw:ReturnType<typeof archiveManifest>,canvasId:string){
  const doc=raw.document;const remap=new Map<string,string>();
  for(const n of doc.nodes){for(const id of [n.metadata?.asset_id,n.metadata?.mask_asset_id,...n.metadata?.asset_ids||[]]){
    if(!id||remap.has(id))continue;const item=raw.assets?.[id];
    if(!item||typeof item.path!=='string'||!/^assets\/\d+\.bin$/.test(item.path)||!raw.files[item.path])continue;
    const asset=await uploadAsset(new Blob([raw.files[item.path] as Uint8Array<ArrayBuffer>],{type:item.mime}),canvasId,'import');remap.set(id,asset.id);
  }}
  doc.nodes=clearExecutionMetadata(doc.nodes).map(n=>({...n,metadata:n.metadata?{...n.metadata,asset_id:remap.get(n.metadata.asset_id||''),mask_asset_id:remap.get(n.metadata.mask_asset_id||''),asset_ids:n.metadata.asset_ids?.flatMap(id=>remap.has(id)?[remap.get(id)!]:[]),expires_at:undefined}:undefined}));
  return doc;
}
