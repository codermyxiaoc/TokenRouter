// @vitest-environment jsdom
import {act} from 'react';
import {createRoot,type Root} from 'react-dom/client';
import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest';
import {Library} from './Library';
import {ApiError,createCanvas,deleteCanvas,listCanvases,saveCanvas} from '@/platform/api';
import {archiveManifest,importCanvasAssets} from '@/platform/archive';
import {blankDocument,type CanvasRecord} from '@/platform/types';

const {navigate}=vi.hoisted(()=>({navigate:vi.fn()}));
vi.mock('react-router-dom',()=>({useNavigate:()=>navigate}));
vi.mock('@/platform/api',async importOriginal=>{
  const actual=await importOriginal<typeof import('@/platform/api')>();
  return {...actual,createCanvas:vi.fn(),deleteCanvas:vi.fn(),listCanvases:vi.fn(),saveCanvas:vi.fn(),getCanvas:vi.fn()};
});
vi.mock('@/platform/archive',()=>({archiveManifest:vi.fn(),exportCanvas:vi.fn(),importCanvasAssets:vi.fn()}));

let host:HTMLDivElement;
let root:Root;
let records:CanvasRecord[];
const canvas=(id:string,title=id):CanvasRecord=>({id,title,document:blankDocument(),revision:1,created_at:'2026-10-18T00:00:00Z',updated_at:'2026-10-18T00:00:00Z'});
const buttons=()=>[...host.querySelectorAll<HTMLButtonElement>('button')];
const button=(text:string)=>buttons().find(item=>item.textContent?.startsWith(text))!;
const checkbox=(title:string)=>host.querySelector<HTMLInputElement>('[aria-label="选择'+title+'"]')!;
const alert=()=>host.querySelector('[role="alert"]')?.textContent||'';

async function click(element:HTMLElement){await act(async()=>{element.click();});}
async function mount(){await act(async()=>{root.render(<Library/>);});}
async function importZip(){
  const file=new File(['mock-zip'],'canvas.zip',{type:'application/zip'});
  Object.defineProperty(file,'arrayBuffer',{value:async()=>new ArrayBuffer(1)});
  const input=host.querySelector<HTMLInputElement>('input[type="file"]')!;
  Object.defineProperty(input,'files',{configurable:true,value:[file]});
  await act(async()=>{input.dispatchEvent(new Event('change',{bubbles:true}));});
}

beforeEach(()=>{
  (globalThis as typeof globalThis&{IS_REACT_ACT_ENVIRONMENT:boolean}).IS_REACT_ACT_ENVIRONMENT=true;
  vi.resetAllMocks();
  records=[canvas('a','画布 A'),canvas('b','画布 B')];
  vi.mocked(listCanvases).mockImplementation(async(page=1)=>({items:records.slice((page-1)*30,page*30),total:records.length,page,page_size:30}));
  vi.mocked(archiveManifest).mockReturnValue({document:blankDocument(),title:'导入画布',assets:{},files:{}});
  vi.mocked(createCanvas).mockImplementation(async title=>{
    const created=canvas('imported',title);
    records=[created,...records];
    return created;
  });
  vi.mocked(importCanvasAssets).mockResolvedValue(blankDocument());
  vi.mocked(saveCanvas).mockImplementation(async record=>record);
  vi.spyOn(window,'confirm').mockReturnValue(true);
  host=document.createElement('div');
  document.body.append(host);
  root=createRoot(host);
});

afterEach(()=>{
  act(()=>root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe('画布库云端操作失败恢复',()=>{
  it('整批结束后刷新，移除成功卡片，仅保留失败选择并允许单独重试',async()=>{
    let completeB:()=>void=()=>{};
    vi.mocked(deleteCanvas).mockImplementation(id=>id==='a'
      ?Promise.reject(new ApiError(409,'画布已被其他窗口修改','CANVAS_REVISION_CONFLICT'))
      :new Promise<void>(resolve=>{completeB=()=>{records=records.filter(item=>item.id!==id);resolve();};}));
    await mount();
    await click(checkbox('画布 A'));
    await click(checkbox('画布 B'));
    await click(button('删除所选'));
    // 已失败的首个请求不能让操作提前结束，另外一个删除仍在执行。
    expect(listCanvases).toHaveBeenCalledTimes(1);
    expect(button('删除所选').disabled).toBe(true);
    await act(async()=>{completeB();});
    expect(listCanvases).toHaveBeenCalledTimes(2);
    expect(checkbox('画布 A').checked).toBe(true);
    expect(checkbox('画布 B')).toBeNull();
    expect(button('删除所选').textContent).toBe('删除所选（1）');
    expect(alert()).toContain('删除失败 1 个画布');

    vi.mocked(deleteCanvas).mockImplementation(async id=>{records=records.filter(item=>item.id!==id);});
    await click(button('删除所选'));
    expect(vi.mocked(deleteCanvas).mock.calls.map(([id])=>id)).toEqual(['a','b','a']);
    expect(host.querySelectorAll('.library-card')).toHaveLength(0);
    expect(alert()).toBe('');
  });

  it('只将明确 CANVAS_NOT_FOUND 视为已删除，普通路由 404 仍显示失败',async()=>{
    vi.mocked(deleteCanvas).mockImplementation(async id=>{
      if(id==='a'){
        records=records.filter(item=>item.id!==id);
        throw new ApiError(404,'画布或素材不存在','CANVAS_NOT_FOUND');
      }
      throw new ApiError(404,'接口不存在');
    });
    await mount();
    await click(checkbox('画布 A'));
    await click(checkbox('画布 B'));
    await click(button('删除所选'));
    expect(checkbox('画布 A')).toBeNull();
    expect(checkbox('画布 B').checked).toBe(true);
    expect(alert()).toContain('删除失败 1 个画布');
    expect(listCanvases).toHaveBeenCalledTimes(2);
  });

  it('批删后的列表刷新失败也不重新展示已确认删除的卡片',async()=>{
    vi.mocked(deleteCanvas).mockImplementation(async id=>{
      if(id==='a'){records=records.filter(item=>item.id!==id);return;}
      throw new ApiError(503,'暂时不可用');
    });
    await mount();
    vi.mocked(listCanvases).mockRejectedValueOnce(new Error('网络断开'));
    await click(checkbox('画布 A'));
    await click(checkbox('画布 B'));
    await click(button('删除所选'));
    expect(checkbox('画布 A')).toBeNull();
    expect(checkbox('画布 B').checked).toBe(true);
    expect(alert()).toContain('删除失败 1 个画布');
    expect(alert()).toContain('列表刷新失败：网络断开');
  });

  it('删除后末页消失会回到上一页，并保留跨页失败选择和错误提示',async()=>{
    records=Array.from({length:31},(_,index)=>canvas(String(index+1),'画布 '+(index+1)));
    vi.mocked(deleteCanvas).mockImplementation(async id=>{
      if(id==='1')throw new ApiError(409,'其他窗口已修改','CANVAS_REVISION_CONFLICT');
      records=records.filter(item=>item.id!==id);
    });
    await mount();
    await click(checkbox('画布 1'));
    await click(button('下一页'));
    await click(checkbox('画布 31'));
    await click(button('删除所选'));
    expect(checkbox('画布 31')).toBeNull();
    expect(checkbox('画布 1').checked).toBe(true);
    expect(host.querySelectorAll('.library-card')).toHaveLength(30);
    expect(alert()).toContain('删除失败 1 个画布');
    expect(vi.mocked(listCanvases).mock.calls.map(([page])=>page)).toEqual([1,2,2,1]);
  });

  it.each(['upload','save'])('导入 %s 阶段失败保留新画布和素材，刷新列表并持续显示错误',async stage=>{
    if(stage==='upload')vi.mocked(importCanvasAssets).mockRejectedValueOnce(new Error('素材上传失败'));
    else vi.mocked(saveCanvas).mockRejectedValueOnce(new Error('保存冲突'));
    await mount();
    await importZip();
    expect(createCanvas).toHaveBeenCalledExactlyOnceWith('导入画布');
    expect(listCanvases).toHaveBeenCalledTimes(2);
    expect(checkbox('导入画布')).not.toBeNull();
    expect(alert()).toContain('导入未完成');
    expect(alert()).toContain(stage==='upload'?'素材上传失败':'保存冲突');
    expect(alert()).toContain('已上传素材已保留');
    expect(deleteCanvas).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
  });

  it('导入后刷新也失败时保留原始导入错误，并提示用户刷新查看保留内容',async()=>{
    vi.mocked(importCanvasAssets).mockRejectedValueOnce(new Error('素材上传中断'));
    await mount();
    vi.mocked(listCanvases).mockRejectedValueOnce(new Error('列表网络断开'));
    await importZip();
    expect(alert()).toContain('素材上传中断');
    expect(alert()).toContain('列表刷新失败，请刷新页面后查看：列表网络断开');
    expect(records.some(item=>item.id==='imported')).toBe(true);
    expect(deleteCanvas).not.toHaveBeenCalled();
  });

  it('第二页导入失败后回到第一页展示已创建画布，分页加载不会清除错误',async()=>{
    records=Array.from({length:31},(_,index)=>canvas(String(index+1),'画布 '+(index+1)));
    vi.mocked(importCanvasAssets).mockRejectedValueOnce(new Error('部分素材未上传成功'));
    await mount();
    await click(button('下一页'));
    await importZip();
    expect(checkbox('导入画布')).not.toBeNull();
    expect(checkbox('画布 31')).toBeNull();
    expect(host.querySelector('.library-pagination')?.textContent).toContain('1 / 2');
    expect(alert()).toContain('部分素材未上传成功');
    expect(alert()).toContain('已上传素材已保留');
    expect(deleteCanvas).not.toHaveBeenCalled();
  });

  it('压缩包解析失败不会创建画布或发起额外列表请求',async()=>{
    vi.mocked(archiveManifest).mockImplementationOnce(()=>{throw new Error('不支持的画布版本');});
    await mount();
    await importZip();
    expect(alert()).toContain('不支持的画布版本');
    expect(createCanvas).not.toHaveBeenCalled();
    expect(importCanvasAssets).not.toHaveBeenCalled();
    expect(listCanvases).toHaveBeenCalledTimes(1);
  });
});

describe('素材保存期提示',()=>{
  // 重新 mount 复用同一个 root 会保留旧的组件状态，回退场景必须换一个全新 root。
  async function freshRoot(){
    act(()=>root.unmount());
    host.remove();
    host=document.createElement('div');
    document.body.append(host);
    root=createRoot(host);
    await mount();
    await act(async()=>{await Promise.resolve();});
  }

  it('按系统设置的小时数展示保存期，读取失败时回退七天',async()=>{
    const originalFetch=globalThis.fetch;
    try{
      globalThis.fetch=vi.fn().mockResolvedValue({ok:true,json:async()=>({code:0,data:{creative_canvas_asset_retention_hours:72}})} as unknown as Response);
      await freshRoot();
      expect(host.textContent).toContain('保留 3 天');
      globalThis.fetch=vi.fn().mockRejectedValue(new Error('offline'));
      await freshRoot();
      expect(host.textContent).toContain('保留 7 天');
    }finally{globalThis.fetch=originalFetch;}
  });
});
