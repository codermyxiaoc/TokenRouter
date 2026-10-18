import {useCallback,useEffect,useRef,useState} from 'react';
import {ApiError,getCanvas,saveCanvas} from '@/platform/api';
import type {CanvasDocument,CanvasRecord} from '@/platform/types';

// 每次载入使用独立代号，旧画布的读写响应不能覆盖当前草稿。
export function useCloudDocument(id:string){
  const [record,setRecord]=useState<CanvasRecord|null>(null);const [document,setDocument]=useState<CanvasDocument|null>(null);
  const [error,setError]=useState('');const [saving,setSaving]=useState(false);const [conflict,setConflict]=useState(false);
  const current=useRef<CanvasRecord|null>(null);const latest=useRef<CanvasDocument|null>(null);const blocked=useRef(false);const live=useRef(true);
  const routeId=useRef(id);routeId.current=id;
  const session=useRef({id,generation:0,ready:false});
  const running=useRef<{generation:number;promise:Promise<void>}|null>(null);
  const isActive=useCallback((generation:number,canvasId:string)=>live.current&&session.current.generation===generation&&session.current.id===canvasId&&routeId.current===canvasId,[]);
  const stale=()=>new DOMException('画布已切换或重新载入，请在当前画布重新操作','AbortError');

  const load=useCallback(async()=>{
    if(!live.current||routeId.current!==id)throw stale();
    const generation=session.current.generation+1;
    session.current={id,generation,ready:false};running.current=null;setSaving(false);
    try{
      const value=await getCanvas(id);
      if(!isActive(generation,id))throw stale();
      if(value.id!==id)throw new Error('服务端返回的画布归属不一致');
      current.current=value;latest.current=value.document;session.current.ready=true;blocked.current=false;
      setConflict(false);setError('');setRecord(value);setDocument(value.document);
    }catch(e){if(isActive(generation,id))setError((e as Error).message);throw e;}
  },[id,isActive]);
  useEffect(()=>{live.current=true;void load().catch(()=>{});return()=>{live.current=false;session.current.ready=false;session.current.generation++;};},[load]);
  const flush=useCallback(async()=>{
    const {generation,id:canvasId,ready}=session.current;
    // 生成之前必须明确确认画布已持久化；未载入或卸载不能当作保存成功。
    if(!ready||!isActive(generation,canvasId)||!current.current||!latest.current||current.current.id!==canvasId)throw new Error('画布尚未载入或已关闭，无法保存');
    if(blocked.current)throw new Error('保存冲突，请先导出本地草稿或重新载入');
    if(running.current?.generation===generation)return running.current.promise;
    if(latest.current===current.current.document)return;
    const entry={generation,promise:Promise.resolve()};running.current=entry;
    const work=async()=>{
      setSaving(true);
      try{
        // 共享同一次保存循环，期间新增修改按服务器最新 revision 顺序追加。
        while(current.current&&latest.current&&latest.current!==current.current.document){
          if(!isActive(generation,canvasId)||!session.current.ready)throw stale();
          const pending=latest.current;const base=current.current;
          const saved=await saveCanvas(base,pending);
          if(!isActive(generation,canvasId)||!session.current.ready)throw stale();
          if(saved.id!==canvasId)throw new Error('服务端返回的画布归属不一致');
          current.current={...saved,document:pending};setRecord(current.current);setError('');
        }
      }catch(e){
        if(!isActive(generation,canvasId))throw stale();
        setError((e as Error).message);
        if(e instanceof ApiError&&e.status===409){blocked.current=true;setConflict(true);}
        throw e;
      }finally{if(running.current===entry){running.current=null;if(isActive(generation,canvasId))setSaving(false);}}
    };
    entry.promise=work();return entry.promise;
  },[isActive]);
  const update=useCallback((value:CanvasDocument|((old:CanvasDocument)=>CanvasDocument))=>{
    const old=latest.current;if(!old||!session.current.ready||!isActive(session.current.generation,session.current.id))return;
    const next=typeof value==='function'?value(old):value;latest.current=next;setDocument(next);
  },[isActive]);
  useEffect(()=>{if(!document||blocked.current)return;const timer=setTimeout(()=>void flush().catch(()=>{}),600);return()=>clearTimeout(timer);},[document,flush]);
  useEffect(()=>{const before=(e:BeforeUnloadEvent)=>{if(latest.current&&latest.current!==current.current?.document){e.preventDefault();e.returnValue='';}};window.addEventListener('beforeunload',before);return()=>window.removeEventListener('beforeunload',before);},[]);
  // 路由已改变但副作用尚未运行时，也不能展示或操作上一张画布。
  return {record:record?.id===id?record:null,document:record?.id===id?document:null,update,flush,error,saving,conflict,reload:load};
}
