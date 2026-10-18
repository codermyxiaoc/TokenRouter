import {useCallback,useEffect,useRef,useState} from 'react';
import {useNavigate} from 'react-router-dom';
import {Check,Plus,FileUp,Download,Pencil,Trash2,LoaderCircle,X} from 'lucide-react';
import {ApiError,createCanvas,deleteCanvas,listCanvases,saveCanvas,getCanvas} from '@/platform/api';
import {archiveManifest,exportCanvas,importCanvasAssets} from '@/platform/archive';
import type {CanvasRecord} from '@/platform/types';
import './library.css';

export function Library(){
  const navigate=useNavigate();
  const [items,setItems]=useState<CanvasRecord[]>([]);
  const [page,setPage]=useState(1);
  const [total,setTotal]=useState(0);
  const [error,setError]=useState('');
  const [loading,setLoading]=useState(true);
  const [busy,setBusy]=useState(false);
  const [selected,setSelected]=useState<Set<string>>(new Set());
  const [editingId,setEditingId]=useState<string|null>(null);
  const [editingTitle,setEditingTitle]=useState('');
  const importInput=useRef<HTMLInputElement>(null);
  const actionRunning=useRef(false);

  const refresh=useCallback(async(targetPage=page)=>{
    const data=await listCanvases(targetPage);
    const lastPage=Math.max(1,Math.ceil(data.total/30));
    setTotal(data.total);
    if(targetPage>lastPage){setPage(lastPage);return;}
    setItems(data.items);
    if(targetPage!==page)setPage(targetPage);
  },[page]);

  useEffect(()=>{
    let current=true;
    setLoading(true);
    void listCanvases(page).then(data=>{
      if(!current)return;
      setItems(data.items);
      setTotal(data.total);
    }).catch(e=>{if(current)setError((e as Error).message);})
      .finally(()=>{if(current)setLoading(false);});
    return()=>{current=false;};
  },[page]);

  // 操作在同一时刻只执行一次，避免连点创建或重复提交删除请求。
  async function action(fn:()=>Promise<void>){
    if(actionRunning.current)return;
    actionRunning.current=true;
    setBusy(true);
    setError('');
    try{await fn();}catch(e){setError((e as Error).message);}
    finally{actionRunning.current=false;setBusy(false);}
  }

  async function remove(ids:string[]){
    if(!ids.length||!window.confirm('删除 '+ids.length+' 个画布？已提交的生成任务仍会继续完成。'))return;
    // 等待整批请求结束；明确的资源不存在表示目标已删除，普通 404 仍保留为失败。
    const results=await Promise.allSettled(ids.map(deleteCanvas));
    const failedIds=ids.filter((_,index)=>{
      const result=results[index];
      return result.status==='rejected'&&!(result.reason instanceof ApiError&&result.reason.status===404&&result.reason.code==='CANVAS_NOT_FOUND');
    });
    const failed=new Set(failedIds);
    const removed=new Set(ids.filter(id=>!failed.has(id)));
    setSelected(failed);
    setEditingId(previous=>previous&&removed.has(previous)?null:previous);
    // 即使刷新断网，也先移除已经确认删除的卡片，避免用户再次请求同一资源。
    setItems(previous=>previous.filter(canvas=>!removed.has(canvas.id)));
    let refreshFailure='';
    try{await refresh();}catch(e){refreshFailure='列表刷新失败：'+(e as Error).message;}
    const failure=failedIds.length?'删除失败 '+failedIds.length+' 个画布，已保留所选，可稍后重试。':'';
    if(failure||refreshFailure)throw new Error([failure,refreshFailure].filter(Boolean).join(' '));
  }

  // 先收集私人画布的分页快照，再沿用带 revision 校验的删除接口。
  async function removeAll(){
    const ids=new Set<string>();
    for(let nextPage=1;;nextPage++){
      const data=await listCanvases(nextPage);
      data.items.forEach(canvas=>ids.add(canvas.id));
      if(!data.items.length||nextPage*30>=data.total)break;
    }
    await remove([...ids]);
  }

  function create(){
    void action(async()=>{
      const canvas=await createCanvas('无限画布 '+(total+1));
      navigate('/'+canvas.id);
    });
  }

  function importFile(file?:File){
    if(!file)return;
    void action(async()=>{
      const archive=archiveManifest(new Uint8Array(await file.arrayBuffer()));
      const canvas=await createCanvas(archive.title);
      try{
        const document=await importCanvasAssets(archive,canvas.id);
        await saveCanvas(canvas,document);
      }catch(e){
        // 导入可能已有素材到账，保留画布和素材供用户检查，不进行隐式删除。
        let refreshFailure='';
        try{await refresh(1);}catch(refreshError){refreshFailure=' 列表刷新失败，请刷新页面后查看：'+(refreshError as Error).message;}
        throw new Error('导入未完成：'+(e as Error).message+'。已创建的画布“'+canvas.title+'”及已上传素材已保留。'+refreshFailure);
      }
      navigate('/'+canvas.id);
    });
  }

  function saveTitle(canvas:CanvasRecord){
    const title=editingTitle.trim();
    if(!title)return;
    void action(async()=>{
      const full=await getCanvas(canvas.id);
      await saveCanvas(full,full.document,title);
      setEditingId(null);
      await refresh();
    });
  }

  function toggleSelected(id:string){
    setSelected(previous=>{
      const next=new Set(previous);
      if(next.has(id))next.delete(id);else next.add(id);
      return next;
    });
  }

  const disabled=busy||loading;
  return <main className="library" aria-busy={disabled}>
    <div className="library-container">
      <header className="library-heading">
        <div className="library-heading-copy"><p>画布库</p><h1>无限画布</h1></div>
        <div className="library-actions">
          {selected.size>0&&<button type="button" disabled={disabled} onClick={()=>void action(()=>remove([...selected]))}>删除所选（{selected.size}）</button>}
          {total>0&&<button type="button" disabled={disabled} onClick={()=>void action(removeAll)}>删除全部</button>}
          <button type="button" disabled={disabled} onClick={()=>importInput.current?.click()}><FileUp size={16}/>导入画布</button>
          <button type="button" className="library-primary" disabled={disabled} onClick={create}><Plus size={16}/>新建画布</button>
        </div>
      </header>

      {error&&<div role="alert" className="library-error">{error}</div>}
      {loading?<section className="library-empty library-loading" aria-live="polite"><LoaderCircle className="animate-spin" size={20}/><span>正在加载画布…</span></section>
        :items.length?<div className="library-grid">{items.map(canvas=>{
          const editing=editingId===canvas.id;
          return <article key={canvas.id} className={'library-card'+(selected.has(canvas.id)?' is-selected':'')} onClick={()=>{if(!editing&&!busy)navigate('/'+canvas.id);}}>
            <div className="library-card-heading">
              <input type="checkbox" className="library-checkbox" checked={selected.has(canvas.id)} disabled={busy} aria-label={'选择'+canvas.title} onClick={e=>e.stopPropagation()} onChange={()=>toggleSelected(canvas.id)}/>
              {editing?<input className="library-rename" value={editingTitle} aria-label="画布名称" disabled={busy} autoFocus onClick={e=>e.stopPropagation()} onChange={e=>setEditingTitle(e.target.value)} onKeyDown={e=>{if(e.key==='Enter')saveTitle(canvas);if(e.key==='Escape')setEditingId(null);}}/>
                :<button type="button" className="library-card-title" disabled={busy} onClick={e=>{e.stopPropagation();navigate('/'+canvas.id);}}>
                  <h2 title={canvas.title}>{canvas.title}</h2>
                  <p>{canvas.document?.nodes?.length||0} 个节点 · {canvas.document?.connections?.length||0} 条连线</p>
                </button>}
            </div>
            <footer className="library-card-footer">
              <p>更新于 {new Date(canvas.updated_at).toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'})}</p>
              <div className="library-card-actions" onClick={e=>e.stopPropagation()}>
                {editing?<>
                  <button type="button" title="保存名称" aria-label="保存名称" disabled={busy||!editingTitle.trim()} onClick={()=>saveTitle(canvas)}><Check size={16}/></button>
                  <button type="button" title="取消重命名" aria-label="取消重命名" disabled={busy} onClick={()=>setEditingId(null)}><X size={16}/></button>
                </>:<>
                  <button type="button" title="导出画布与未过期素材" aria-label={'导出'+canvas.title} disabled={busy} onClick={()=>void action(async()=>exportCanvas(await getCanvas(canvas.id)))}><Download size={16}/></button>
                  <button type="button" title="重命名" aria-label={'重命名'+canvas.title} disabled={busy} onClick={()=>{setEditingId(canvas.id);setEditingTitle(canvas.title);}}><Pencil size={16}/></button>
                  <button type="button" title="删除" aria-label={'删除'+canvas.title} disabled={busy} onClick={()=>void action(()=>remove([canvas.id]))}><Trash2 size={16}/></button>
                </>}
              </div>
            </footer>
          </article>;
        })}</div>
          :<section className="library-empty"><h2>还没有画布</h2><p>创建一个画布，开始连接你的想法。</p><button type="button" className="library-primary" disabled={busy} onClick={create}><Plus size={16}/>新建画布</button></section>}

      {total>30&&<nav className="library-pagination" aria-label="画布分页"><button type="button" disabled={disabled||page===1} onClick={()=>setPage(p=>p-1)}>上一页</button><span>{page} / {Math.ceil(total/30)}</span><button type="button" disabled={disabled||page*30>=total} onClick={()=>setPage(p=>p+1)}>下一页</button></nav>}
      <p className="library-retention">画布自动保存到个人账户；上传与生成的素材保留 7 天，到期后画布布局和文字仍然保留。</p>
      {busy&&<div className="library-status" role="status"><LoaderCircle className="animate-spin" size={16}/>正在处理…</div>}
    </div>
    <input ref={importInput} type="file" accept="application/zip,.zip" hidden disabled={disabled} onChange={e=>{const file=e.target.files?.[0];e.target.value='';importFile(file);}}/>
  </main>;
}
