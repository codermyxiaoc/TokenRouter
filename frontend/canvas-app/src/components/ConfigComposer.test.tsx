// @vitest-environment jsdom
import { act, useState } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { ConfigComposer, serializeConfigEditor } from './ConfigComposer';
import type { ConfigInput } from '@/platform/config-composer';

vi.mock('./ReferenceImagePreview',()=>({ReferenceImagePreview:()=> <span>私有参考图</span>}));
let host:HTMLDivElement,root:Root;
const inputs:ConfigInput[]=['image','text','group'].map((type,index)=>({node:{id:`ref-${index}`,type,title:`标题${index}`,position:{x:0,y:0},width:340,height:240,metadata:{}},label:`${['图片','文本','组'][index]}1`,resources:[]}));
beforeEach(()=>{(globalThis as typeof globalThis & {IS_REACT_ACT_ENVIRONMENT:boolean}).IS_REACT_ACT_ENVIRONMENT=true;host=document.createElement('div');document.body.append(host);root=createRoot(host);});
afterEach(()=>{act(()=>root.unmount());host.remove();vi.restoreAllMocks();});
const editor=()=>host.querySelector<HTMLDivElement>('[role=textbox]')!;
const caretEnd=()=>{editor().focus();const range=document.createRange();range.selectNodeContents(editor());range.collapse(false);window.getSelection()?.removeAllRanges();window.getSelection()?.addRange(range);};
const mount=(onChange=vi.fn(),initial='')=>{function Harness(){const [value,setValue]=useState(initial);return <ConfigComposer value={value} inputs={inputs} onChange={next=>{onChange(next);setValue(next);}} onClose={()=>{}} onDisconnect={()=>{}} onAddText={()=>{}} onUploadImage={()=>{}}/>;}act(()=>root.render(<Harness/>));return onChange;};
const typeText=(value:string)=>act(()=>{editor().textContent=value;caretEnd();editor().dispatchEvent(new Event('input',{bubbles:true}));});

describe('生成配置组装编辑器',()=>{
  it('空参考区仍显示标签和加号，菜单分别触发新增文本或上传图片',()=>{
    const add=vi.fn(),upload=vi.fn();
    const scroll=HTMLElement.prototype.scrollIntoView;
    HTMLElement.prototype.scrollIntoView=vi.fn();
    try{
      act(()=>root.render(<ConfigComposer value="" inputs={[]} onChange={()=>{}} onClose={()=>{}} onDisconnect={()=>{}} onAddText={add} onUploadImage={upload}/>));
      expect(host.textContent).toContain('参考内容');
      const trigger=host.querySelector<HTMLButtonElement>('[aria-label="添加参考内容"]')!;
      expect(trigger.textContent).toBe('+');
      for(const [label,callback] of [['新增文本并连接',add],['上传参考图片',upload]] as const){
        act(()=>trigger.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true,cancelable:true})));
        const option=[...document.querySelectorAll<HTMLElement>('[role=option]')].find(item=>item.textContent===label)!;
        expect(option).toBeDefined();
        act(()=>option.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true})));
        expect(callback).toHaveBeenCalledOnce();
      }
      expect(host.querySelector('select')).toBeNull();
    }finally{HTMLElement.prototype.scrollIntoView=scroll;}
  });
  it('输入@列出当前连接，并插入纯节点ID标记而非HTML',()=>{
    const onChange=mount();typeText('使用 @');
    const options=[...document.querySelectorAll<HTMLButtonElement>('[role=option]')];expect(options).toHaveLength(3);
    expect(options.map(option=>option.textContent)).toEqual(expect.arrayContaining([expect.stringContaining('图片1'),expect.stringContaining('文本1'),expect.stringContaining('组1')]));
    act(()=>options[0].click());
    expect(onChange).toHaveBeenLastCalledWith('使用 @[node:ref-0] ');
    expect(serializeConfigEditor(editor())).toBe('使用 @[node:ref-0] ');
    expect(editor().querySelector('.config-reference-chip')?.textContent).toBe('图片1');
    expect(document.querySelector('[role=listbox]')).toBeNull();
  });
  it('连接顺序改变会更新chip编号，断开引用显示失效但不静默删除原token',()=>{
    const onChange=vi.fn();const render=(references:ConfigInput[])=>act(()=>root.render(<ConfigComposer value="@[node:ref-0]" inputs={references} onChange={onChange} onClose={()=>{}} onDisconnect={()=>{}} onAddText={()=>{}} onUploadImage={()=>{}}/>));
    render(inputs);expect(editor().textContent).toBe('图片1');
    render([{...inputs[0],label:'图片2'},...inputs.slice(1)]);expect(editor().textContent).toBe('图片2');
    render(inputs.slice(1));expect(editor().textContent).toBe('失效引用');expect(serializeConfigEditor(editor())).toBe('@[node:ref-0]');expect(onChange).not.toHaveBeenCalled();
  });
  it('粘贴只取text/plain，HTML和外链字符串不会执行或保存成节点属性',()=>{
    const onChange=mount();const text='<img src="https://example.invalid/x" onerror="alert(1)">';
    const paste=new Event('paste',{bubbles:true,cancelable:true});const getData=vi.fn((kind:string)=>kind==='text/plain'?text:'<script>bad()</script>');Object.defineProperty(paste,'clipboardData',{value:{getData}});
    act(()=>{caretEnd();editor().dispatchEvent(paste);});
    expect(paste.defaultPrevented).toBe(true);expect(getData).toHaveBeenCalledExactlyOnceWith('text/plain');expect(onChange).toHaveBeenLastCalledWith(text);expect(editor().querySelector('img,script')).toBeNull();
  });
  it('中文输入期间不捕获确认键，普通Enter换行而不触发外部生成/画布快捷键',()=>{
    mount();const keyboard=vi.fn();document.addEventListener('keydown',keyboard);
    try{act(()=>{editor().dispatchEvent(new CompositionEvent('compositionstart',{bubbles:true}));editor().textContent='@';caretEnd();editor().dispatchEvent(new Event('input',{bubbles:true}));});expect(document.querySelector('[role=listbox]')).toBeNull();
      act(()=>editor().dispatchEvent(new CompositionEvent('compositionend',{bubbles:true})));expect(document.querySelector('[role=listbox]')).not.toBeNull();
      act(()=>editor().dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true})));
      typeText('第一行');act(()=>editor().dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true})));
      expect(serializeConfigEditor(editor())).toBe('第一行\n');expect(keyboard).not.toHaveBeenCalled();
    }finally{document.removeEventListener('keydown',keyboard);}
  });
});
