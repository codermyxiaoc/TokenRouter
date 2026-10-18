import {useEffect,useState,type PointerEvent} from 'react';
import {Image,Video,Music,LoaderCircle} from 'lucide-react';
import {assetURL,ApiError} from '@/platform/api';
import './canvas-video-media.css';
type VideoNodeInteraction={scale:number;height:number;onSelect:(event:Pick<PointerEvent,'shiftKey'>)=>void};
type AudioNodeInteraction=Pick<VideoNodeInteraction,'onSelect'>;
export function AssetView({id,type='image',expired=false,onURL,videoNodeInteraction,audioNodeInteraction}:{id?:string;type?:string;expired?:boolean;onURL?:(url:string)=>void;videoNodeInteraction?:VideoNodeInteraction;audioNodeInteraction?:AudioNodeInteraction}){
  const [url,setURL]=useState('');const [error,setError]=useState('');
  useEffect(()=>{let live=true;setURL('');setError('');if(id&&!expired)void assetURL(id).then(value=>{if(live){setURL(value);onURL?.(value);}}).catch(e=>{if(live)setError(e instanceof ApiError&&e.status===410?'素材已过期':e.message);});return()=>{live=false;};},[id,expired]);
  if(expired||error)return <div className="media-placeholder"><span>{expired?'素材已过期':error}</span></div>;
  if(!id)return <div className="media-placeholder"><span className="placeholder-icon">{type==='video'?<Video size={24}/>:type==='audio'?<Music size={24}/>:<Image size={24}/>}</span><span className="placeholder-label">空{type==='video'?'视频':type==='audio'?'音频':'图片'}节点</span></div>;
  if(!url)return <div className="media-placeholder"><LoaderCircle className="animate-spin"/></div>;
  if(type==='video'){
    // 原生控件继续接收自己的指针与键盘事件；只在外层捕获阶段选中，不取消默认播放行为。
    // 键盘由画布快捷键入口识别媒体后跳过，不在播放器截断 Esc 等浏览器原生操作。
    const video=<video src={url} controls preload="metadata" data-canvas-no-zoom onPointerDown={e=>e.stopPropagation()} onFocus={()=>videoNodeInteraction?.onSelect({shiftKey:false})} className="media-content"/>;
    if(!videoNodeInteraction)return video;
    const scale=Math.max(.05,videoNodeInteraction.scale);
    // 同时保留原生控制条逻辑高度和至少52个屏幕像素，缩小或放大都不覆盖播放控件。
    const controlsSpace=Math.max(52,52/scale);
    const hasDragSurface=videoNodeInteraction.height-controlsSpace>=24/scale;
    return <div className="video-node-media" onPointerDownCapture={event=>{if(event.button===0)videoNodeInteraction.onSelect(event);}}>
      {video}
      {hasDragSurface&&<button type="button" className="video-node-drag-surface" aria-label="拖动视频画面" title="拖动视频画面移动节点；底部可控制播放" style={{bottom:controlsSpace,height:`calc(100% - ${controlsSpace}px)`}} onKeyDown={e=>e.stopPropagation()} onKeyUp={e=>e.stopPropagation()} onClick={event=>{event.stopPropagation();videoNodeInteraction.onSelect(event);}}/>}
    </div>;
  }
  if(type==='audio'){
    // 播放器捕获阶段只选中；图标与留白负责拖动，原生播放/进度/音量继续自行处理事件。
    const audio=<audio src={url} controls preload="metadata" data-canvas-no-zoom onPointerDown={e=>e.stopPropagation()} onFocus={()=>audioNodeInteraction?.onSelect({shiftKey:false})}/>;
    if(!audioNodeInteraction)return audio;
    return <div className="audio-node-media" onPointerDownCapture={event=>{if(event.button===0)audioNodeInteraction.onSelect(event);}}><Music size={24} aria-hidden="true"/>{audio}</div>;
  }
  return <img src={url} draggable={false} alt="画布素材" className="media-content"/>;
}
