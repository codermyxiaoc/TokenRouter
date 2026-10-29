import { useEffect, useRef } from 'react';
import { FileText, Image as ImageIcon, Music, Settings2, Video, X } from 'lucide-react';
import type { ConnectionHandle, Position } from '@/types/canvas';
import './connection-create-menu.css';

export type ConnectionCreateType = 'text' | 'image' | 'video' | 'audio' | 'config';
export type PendingConnectionCreate = { handle: ConnectionHandle; point: Position; client: Position };

const options: { type: ConnectionCreateType; title: string; description?: string; Icon: typeof FileText }[] = [
  { type: 'text', title: '文本', description: '提示词、脚本或参考文字', Icon: FileText },
  { type: 'image', title: '图片', Icon: ImageIcon },
  { type: 'video', title: '视频', Icon: Video },
  { type: 'audio', title: '音频', Icon: Music },
  { type: 'config', title: '生成配置', description: '组合多个输入后生成新结果', Icon: Settings2 },
];

// 拖线落在空白处时选择要新建并连接的节点；只创建节点和连线，不触发生成或扣费。
// 从输入端拖出时新节点作为来源，生成配置没有输出端，因此不提供该选项。
export function ConnectionCreateMenu({ pending, onCreate, onClose }: { pending: PendingConnectionCreate; onCreate: (type: ConnectionCreateType) => void; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null), closeRef = useRef(onClose);
  closeRef.current = onClose;
  const available = options.filter(option => pending.handle.handleType === 'source' || option.type !== 'config');
  useEffect(() => {
    ref.current?.querySelector<HTMLButtonElement>('button[data-create-type]')?.focus();
    // 点击菜单外、Esc 或滚轮缩放画布时关闭；监听只注册一次，最新回调通过 ref 读取。
    const close = () => closeRef.current();
    const down = (event: PointerEvent) => { if (!(event.target instanceof Node && ref.current?.contains(event.target))) close(); };
    const key = (event: KeyboardEvent) => { if (event.key === 'Escape') { event.preventDefault(); close(); } };
    window.addEventListener('pointerdown', down, true);
    window.addEventListener('keydown', key, true);
    window.addEventListener('wheel', close, { passive: true });
    return () => { window.removeEventListener('pointerdown', down, true); window.removeEventListener('keydown', key, true); window.removeEventListener('wheel', close); };
  }, []);
  const view = typeof window === 'undefined' ? { innerWidth: 1024, innerHeight: 768 } : window;
  const width = 240, height = available.length * 50 + 48;
  const left = Math.max(8, Math.min(pending.client.x + 8, view.innerWidth - width - 8));
  const top = Math.max(8, Math.min(pending.client.y + 8, view.innerHeight - height - 8));
  return <div ref={ref} role="menu" aria-label={pending.handle.handleType === 'source' ? '新建节点并连接到它' : '新建节点作为输入'} className="connection-create-menu" data-canvas-no-zoom style={{ left, top, width }}
    onPointerDown={event => event.stopPropagation()} onMouseDown={event => event.stopPropagation()} onWheel={event => event.stopPropagation()}>
    <header><span>{pending.handle.handleType === 'source' ? '新建并连接到' : '新建作为输入'}</span><button type="button" aria-label="关闭新建菜单" onClick={onClose}><X size={14}/></button></header>
    {available.map(({ type, title, description, Icon }) => <button key={type} type="button" role="menuitem" data-create-type={type} onClick={() => onCreate(type)}>
      <span className="connection-create-icon"><Icon size={16}/></span>
      <span className="connection-create-copy"><strong>{title}</strong>{description && <small>{description}</small>}</span>
    </button>)}
  </div>;
}
