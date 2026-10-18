import { describe, expect, it } from 'vitest';
import type { CanvasDocument } from './types';
import type { CanvasNodeData } from '@/types/canvas';
import { compileConfigPrompt, configInputs, configInputCounts, remapConfigReferences } from './config-composer';

const node = (id: string, type: string, metadata: CanvasNodeData['metadata'] = {}): CanvasNodeData => ({ id, type, title: id, position: { x: 0, y: 0 }, width: 340, height: 240, metadata });
const fixture = (): CanvasDocument => ({ nodes: [node('cfg','config'),node('text-a','text',{content:'甲'}),node('image-a','image',{asset_id:'a'}),node('text-b','text',{content:'乙',groupId:'group'}),node('image-b','image',{asset_id:'b',groupId:'group'}),node('group','group'),node('video','video'),node('audio','audio')], connections: ['image-a','text-a','group','video','audio'].map((id,index)=>({id:String(index),fromNodeId:id,toNodeId:'cfg'})), viewport:{x:0,y:0,k:1} });

describe('生成配置的纯数据引用合同',()=>{
  it('按连线顺序编号而非nodes顺序，组展开去重并统计真实类型',()=>{
    const doc=fixture();doc.connections.push({id:'duplicate',fromNodeId:'image-b',toNodeId:'cfg'});
    expect(configInputs(doc,'cfg').map(input=>input.label)).toEqual(['图片1','文本1','组1','视频1','音频1','图片2']);
    expect(configInputCounts(configInputs(doc,'cfg'))).toEqual({image:2,text:2,video:1,audio:1});
    doc.connections.unshift(doc.connections.pop()!);
    expect(configInputs(doc,'cfg')[0]).toMatchObject({label:'图片1',node:{id:'image-b'}});
  });
  it('空组装自动连接文本，非空组装只选择显式引用并按连接顺序编号媒体',()=>{
    const doc=fixture(),cfg=doc.nodes[0];cfg.metadata={prompt:'基础'};
    expect(compileConfigPrompt(doc,cfg).prompt).toBe('基础\n\n甲\n\n乙');
    cfg.metadata={composerContent:'将 @[node:group] 与 @[node:image-a] 结合，再读 @[node:text-a]'};
    const result=compileConfigPrompt(doc,cfg);
    expect(result.inputs.map(input=>input.id)).toEqual(['image-a','text-a','text-b','image-b']);
    expect(result.prompt).toBe('将 【文本2】、【图片2】 与 【图片1】 结合，再读 【文本1】\n\n【文本1】\n甲\n\n【文本2】\n乙');
    cfg.metadata.composerContent='只用这段话，不引用素材';
    expect(compileConfigPrompt(doc,cfg)).toEqual({prompt:cfg.metadata.composerContent,inputs:[]});
  });
  it('断开、删除、外部或其它画布节点不能通过原ID读取；无匹配引用不会发起新请求',()=>{
    const doc=fixture(),cfg=doc.nodes[0];cfg.metadata={composerContent:'@[node:foreign]'};
    expect(()=>compileConfigPrompt(doc,cfg)).toThrow('未连接');
    cfg.metadata.composerContent='@[node:image-a]';doc.connections=doc.connections.filter(edge=>edge.fromNodeId!=='image-a');
    expect(()=>compileConfigPrompt(doc,cfg)).toThrow('未连接');
    doc.connections.push({id:'stale',fromNodeId:'deleted',toNodeId:'cfg'});
    expect(configInputs(doc,'cfg').some(input=>input.node.id==='deleted')).toBe(false);
  });
  it('重复提及不会重复添加素材/文本，组循环不会递归失控',()=>{
    const doc=fixture(),cfg=doc.nodes[0];cfg.metadata={composerContent:'@[node:text-a] @[node:text-a] @[node:group]'};
    const result=compileConfigPrompt(doc,cfg);expect(result.prompt.match(/\n甲/g)).toHaveLength(1);
    doc.nodes.find(item=>item.id==='group')!.metadata={groupId:'group'};
    expect(configInputCounts(configInputs(doc,'cfg')).image).toBe(2);
  });
  it('复制仅重映射复制集合内token，不替换普通文本或重新引入外部引用',()=>{
    expect(remapConfigReferences('literal image-a @[node:image-a] @[node:foreign]',new Map([['image-a','new-a']]))).toBe('literal image-a @[node:new-a] @[node:foreign]');
  });
});
