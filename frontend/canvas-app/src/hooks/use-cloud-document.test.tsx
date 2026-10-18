// @vitest-environment jsdom
import {act} from 'react';
import {createRoot,type Root} from 'react-dom/client';
import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest';
import {useCloudDocument} from './use-cloud-document';
import {ApiError,getCanvas,saveCanvas} from '@/platform/api';
import {blankDocument,type CanvasRecord} from '@/platform/types';

vi.mock('@/platform/api',()=>({
  ApiError:class ApiError extends Error {constructor(public status:number,message:string){super(message);}},
  getCanvas:vi.fn(),saveCanvas:vi.fn(),
}));

const get=vi.mocked(getCanvas),save=vi.mocked(saveCanvas);
const record=(id='a',revision=1,document=blankDocument()):CanvasRecord=>({id,revision,document,title:id,created_at:'',updated_at:''});
function deferred<T>(){let resolve!:(value:T)=>void;let reject!:(reason:unknown)=>void;const promise=new Promise<T>((ok,fail)=>{resolve=ok;reject=fail;});return{promise,resolve,reject};}
let host:HTMLDivElement;let root:Root;let cloud:ReturnType<typeof useCloudDocument>;let mounted:boolean;
function Harness({id}:{id:string}){cloud=useCloudDocument(id);return null;}
const render=async(id='a')=>{await act(async()=>root.render(<Harness id={id}/>));};
const edit=(x:number)=>act(()=>cloud.update(old=>({...old,viewport:{...old.viewport,x}})));

beforeEach(()=>{
  (globalThis as typeof globalThis & {IS_REACT_ACT_ENVIRONMENT:boolean}).IS_REACT_ACT_ENVIRONMENT=true;
  vi.useFakeTimers();get.mockReset();save.mockReset();get.mockResolvedValue(record());
  host=document.createElement('div');document.body.append(host);root=createRoot(host);mounted=true;
});
afterEach(()=>{if(mounted)act(()=>root.unmount());host.remove();vi.useRealTimers();});

describe('云端画布保存与生成前置条件',()=>{
  it('并发 flush 共享保存循环，期间的新修改按最新 revision 串行持久化',async()=>{
    await render();edit(10);
    const first=deferred<CanvasRecord>(),second=deferred<CanvasRecord>();save.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    let one!:Promise<void>,two!:Promise<void>,three!:Promise<void>;
    act(()=>{one=cloud.flush();two=cloud.flush();});expect(save).toHaveBeenCalledTimes(1);
    edit(20);act(()=>{three=cloud.flush();});expect(save).toHaveBeenCalledTimes(1);
    await act(async()=>first.resolve(record('a',2,save.mock.calls[0][1])));
    expect(save).toHaveBeenCalledTimes(2);expect(save.mock.calls[1][0].revision).toBe(2);expect(save.mock.calls[1][1].viewport.x).toBe(20);
    await act(async()=>{second.resolve(record('a',3,save.mock.calls[1][1]));await Promise.all([one,two,three]);});
    expect(cloud.record?.revision).toBe(3);expect(cloud.document?.viewport.x).toBe(20);expect(cloud.saving).toBe(false);
    await act(async()=>cloud.flush());expect(save).toHaveBeenCalledTimes(2);
  });
  it('保存失败保留草稿，await flush 的生成入口不能继续执行',async()=>{
    await render();edit(10);save.mockRejectedValueOnce(new Error('网络异常'));
    const create=vi.fn();let failure:unknown;
    await act(async()=>{await cloud.flush().then(create).catch(error=>{failure=error;});});
    expect((failure as Error).message).toBe('网络异常');expect(create).not.toHaveBeenCalled();expect(cloud.document?.viewport.x).toBe(10);expect(cloud.record?.revision).toBe(1);
    save.mockImplementationOnce(async(base,document)=>record(base.id,base.revision+1,document));
    await act(async()=>cloud.flush());expect(cloud.record?.revision).toBe(2);expect(cloud.error).toBe('');
  });
  it('409 冲突停止写入并保留本地草稿，重新载入后才可恢复',async()=>{
    await render();edit(10);save.mockRejectedValueOnce(new ApiError(409,'revision conflict'));
    await act(async()=>{await cloud.flush().catch(()=>{});});expect(cloud.conflict).toBe(true);
    edit(20);await expect(cloud.flush()).rejects.toThrow('保存冲突');expect(save).toHaveBeenCalledTimes(1);expect(cloud.document?.viewport.x).toBe(20);
    get.mockResolvedValueOnce(record('a',7));await act(async()=>cloud.reload());expect(cloud.conflict).toBe(false);expect(cloud.record?.revision).toBe(7);
  });
  it('切换画布后旧 GET 返回不得覆盖新画布',async()=>{
    const old=deferred<CanvasRecord>();get.mockReturnValueOnce(old.promise).mockResolvedValueOnce(record('b',2));
    await render('a');await render('b');expect(cloud.record?.id).toBe('b');
    await act(async()=>old.resolve(record('a')));expect(cloud.record?.id).toBe('b');expect(cloud.record?.revision).toBe(2);
  });
  it('切换画布时旧 SAVE 不能回写新画布，等待中的生成流程必须中止',async()=>{
    await render();edit(10);const pending=deferred<CanvasRecord>();save.mockReturnValueOnce(pending.promise);
    const create=vi.fn();let operation!:Promise<unknown>;
    act(()=>{operation=cloud.flush().then(create).catch(error=>error);});
    get.mockResolvedValueOnce(record('b',4));await render('b');edit(20);
    await act(async()=>pending.resolve(record('a',2)));
    expect((await operation as Error).name).toBe('AbortError');expect(create).not.toHaveBeenCalled();expect(cloud.record?.id).toBe('b');expect(cloud.document?.viewport.x).toBe(20);expect(save).toHaveBeenCalledTimes(1);
  });
  it('重新载入时旧保存失效，不能覆盖新 revision 或错误取消新保存状态',async()=>{
    await render();edit(10);const oldSave=deferred<CanvasRecord>();save.mockReturnValueOnce(oldSave.promise);let oldOperation!:Promise<unknown>;
    act(()=>{oldOperation=cloud.flush().catch(error=>error);});
    get.mockResolvedValueOnce(record('a',8));await act(async()=>cloud.reload());edit(30);
    const newSave=deferred<CanvasRecord>();save.mockReturnValueOnce(newSave.promise);let newOperation!:Promise<void>;
    act(()=>{newOperation=cloud.flush();});expect(cloud.saving).toBe(true);
    await act(async()=>oldSave.resolve(record('a',2)));expect((await oldOperation as Error).name).toBe('AbortError');expect(cloud.saving).toBe(true);expect(cloud.record?.revision).toBe(8);
    await act(async()=>{newSave.resolve(record('a',9,save.mock.calls[1][1]));await newOperation;});expect(cloud.record?.revision).toBe(9);expect(cloud.document?.viewport.x).toBe(30);
  });
  it('尚未加载和卸载都拒绝 flush，卸载前在途保存不再授权后续生成',async()=>{
    const loading=deferred<CanvasRecord>();get.mockReturnValueOnce(loading.promise);await render();await expect(cloud.flush()).rejects.toThrow('尚未载入');
    await act(async()=>loading.resolve(record()));edit(10);
    const pending=deferred<CanvasRecord>();save.mockReturnValueOnce(pending.promise);const create=vi.fn();let operation!:Promise<unknown>;
    act(()=>{operation=cloud.flush().then(create).catch(error=>error);});act(()=>root.unmount());mounted=false;
    pending.resolve(record('a',2));expect((await operation as Error).name).toBe('AbortError');expect(create).not.toHaveBeenCalled();await expect(cloud.flush()).rejects.toThrow('已关闭');
  });
  it('保存响应归属不符时明确失败，草稿不被当作持久化成功',async()=>{
    await render();edit(10);save.mockResolvedValueOnce(record('another',99));
    await act(async()=>{await expect(cloud.flush()).rejects.toThrow('归属不一致');});
    expect(cloud.record?.id).toBe('a');expect(cloud.record?.revision).toBe(1);expect(cloud.document?.viewport.x).toBe(10);
  });
  it('只在存在未保存草稿时阻止退出，初次加载和保存成功都不误报',async()=>{
    const loading=deferred<CanvasRecord>();get.mockReturnValueOnce(loading.promise);await render();
    const before=()=>{const event=new Event('beforeunload',{cancelable:true});window.dispatchEvent(event);return event.defaultPrevented;};
    expect(before()).toBe(false);await act(async()=>loading.resolve(record()));expect(before()).toBe(false);
    edit(10);expect(before()).toBe(true);save.mockImplementationOnce(async(base,document)=>record(base.id,base.revision+1,document));
    await act(async()=>cloud.flush());expect(before()).toBe(false);
  });
});
