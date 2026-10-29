import { forwardRef, useMemo, useRef, useState, type TextareaHTMLAttributes } from 'react';
import { createPortal } from 'react-dom';
import { FileText, Music, Video } from 'lucide-react';
import { AssetView } from './AssetView';
import { deleteAdjacentLabel, splitPromptLabels, type PromptReference } from '@/platform/prompt-references';
import './prompt-mention.css';

type Props = Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, 'value' | 'onChange'> & {
  value: string;
  references: PromptReference[];
  onChange: (value: string) => void;
};
type Mention = { start: number; query: string };

// 提示词中的 @ 只插入纯文本标签（如“图片1”），不保存节点 ID 或 HTML；标签编号与提交的参考素材顺序一致。
export const PromptMentionTextarea = forwardRef<HTMLTextAreaElement, Props>(function PromptMentionTextarea({ value, references, onChange, className, style, onKeyDown, onBlur, onScroll, ...props }, forwardedRef) {
  const textareaRef = useRef<HTMLTextAreaElement | null>(null), overlayRef = useRef<HTMLDivElement | null>(null);
  const composing = useRef(false);
  const [mention, setMention] = useState<Mention | null>(null), [activeIndex, setActiveIndex] = useState(0);
  const labels = useMemo(() => references.map(item => item.label), [references]);
  const candidates = useMemo(() => {
    if (!mention) return [];
    const query = mention.query.trim().toLowerCase();
    return query ? references.filter(item => `${item.label} ${item.node.title} ${item.node.metadata?.content || ''}`.toLowerCase().includes(query)) : references;
  }, [mention, references]);

  const closeMention = () => { setMention(null); setActiveIndex(0); };
  const syncMention = (next: string, cursor: number) => {
    // 只有 @ 位于行首或空白之后才弹出菜单，邮箱等普通文字不会误触发。
    const match = /(^|\s)@([^\s@]*)$/.exec(next.slice(0, cursor));
    if (!match || !references.length) { closeMention(); return; }
    setMention({ start: cursor - match[2].length - 1, query: match[2] }); setActiveIndex(0);
  };
  const updateValue = (next: string, caret: number) => {
    onChange(next);
    requestAnimationFrame(() => { textareaRef.current?.focus(); textareaRef.current?.setSelectionRange(caret, caret); });
  };
  const insertReference = (reference: PromptReference) => {
    if (!mention) return;
    const end = textareaRef.current?.selectionStart ?? value.length;
    const text = `${reference.label} `;
    closeMention();
    updateValue(`${value.slice(0, mention.start)}${text}${value.slice(end)}`, mention.start + text.length);
  };
  const syncOverlayScroll = () => {
    if (overlayRef.current && textareaRef.current) { overlayRef.current.scrollTop = textareaRef.current.scrollTop; overlayRef.current.scrollLeft = textareaRef.current.scrollLeft; }
  };
  const highlight = labels.length > 0 && !!value;

  return <div className="prompt-mention">
    <textarea {...props} ref={node => { textareaRef.current = node; if (typeof forwardedRef === 'function') forwardedRef(node); else if (forwardedRef) forwardedRef.current = node; }}
      value={value} className={`${className || ''}${highlight ? ' prompt-mention-input' : ''}`} style={style}
      onChange={event => { onChange(event.target.value); if (!composing.current) syncMention(event.target.value, event.target.selectionStart); requestAnimationFrame(syncOverlayScroll); }}
      onCompositionStart={() => { composing.current = true; }}
      onCompositionEnd={event => { composing.current = false; syncMention(event.currentTarget.value, event.currentTarget.selectionStart); }}
      onScroll={event => { syncOverlayScroll(); onScroll?.(event); }}
      onBlur={event => { window.setTimeout(closeMention, 120); onBlur?.(event); }}
      onKeyDown={event => {
        if (composing.current || event.nativeEvent.isComposing) { onKeyDown?.(event); return; }
        if (mention && candidates.length) {
          if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); setActiveIndex(index => (index + (event.key === 'ArrowDown' ? 1 : candidates.length - 1)) % candidates.length); return; }
          if (event.key === 'Enter' || event.key === 'Tab') { event.preventDefault(); insertReference(candidates[Math.min(activeIndex, candidates.length - 1)]); return; }
          if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); closeMention(); return; }
        }
        if ((event.key === 'Backspace' || event.key === 'Delete') && !mention) {
          const element = event.currentTarget;
          if (element.selectionStart === element.selectionEnd) {
            const result = deleteAdjacentLabel(value, element.selectionStart, event.key === 'Backspace' ? 'backward' : 'forward', labels);
            if (result) { event.preventDefault(); updateValue(result.value, result.caret); return; }
          }
        }
        onKeyDown?.(event);
      }}/>
    {/* 高亮层放在输入框之后，按类名查询输入框时不会先命中高亮层；层级由样式控制。 */}
    {highlight && <div ref={overlayRef} aria-hidden="true" className={`${className || ''} prompt-mention-overlay`} style={style}>
      {splitPromptLabels(value, labels).map((part, index) => part.label ? <mark key={index} className="prompt-mention-label">{part.text}</mark> : <span key={index}>{part.text}</span>)}{'\n'}
    </div>}
    {mention && candidates.length > 0 && textareaRef.current && <MentionMenu textarea={textareaRef.current} caretIndex={mention.start} references={candidates} activeIndex={Math.min(activeIndex, candidates.length - 1)} onSelect={insertReference}/>}
  </div>;
});

function MentionMenu({ textarea, caretIndex, references, activeIndex, onSelect }: { textarea: HTMLTextAreaElement; caretIndex: number; references: PromptReference[]; activeIndex: number; onSelect: (reference: PromptReference) => void }) {
  const view = textarea.ownerDocument.defaultView || window;
  const rect = textarea.getBoundingClientRect();
  // 画布面板按缩放倒数缩放，矩形是屏幕坐标、镜像测量是布局坐标，需要换算后再定位到 @ 所在位置。
  const scale = textarea.offsetWidth ? rect.width / textarea.offsetWidth : 1;
  const computed = view.getComputedStyle(textarea);
  const lineHeight = (parseFloat(computed.lineHeight) || parseFloat(computed.fontSize) * 1.4 || 20) * scale;
  const caret = caretPoint(textarea, caretIndex);
  const width = 256, height = Math.min(224, references.length * 48 + 8), gap = 6;
  const caretLeft = rect.left + (caret.left - textarea.scrollLeft) * scale, caretTop = rect.top + (caret.top - textarea.scrollTop) * scale;
  const left = clamp(caretLeft, 8, view.innerWidth - width - 8);
  const below = caretTop + lineHeight + gap;
  const top = below + height <= view.innerHeight - 8 ? below : clamp(caretTop - gap - height, 8, view.innerHeight - height - 8);
  return createPortal(<div role="listbox" aria-label="选择引用内容" className="config-mention-menu prompt-mention-menu" style={{ left, top, width }} data-canvas-no-zoom
    onPointerDown={event => { event.stopPropagation(); event.preventDefault(); }} onMouseDown={event => event.preventDefault()}>
    {references.map((reference, index) => <button key={reference.node.id} type="button" role="option" aria-selected={index === activeIndex} onClick={() => onSelect(reference)}>
      <ReferenceIcon reference={reference}/>
      <div><strong>{reference.label}</strong><small>{reference.node.type === 'text' ? reference.node.metadata?.content || reference.node.title : reference.node.title}</small></div>
    </button>)}
  </div>, textarea.ownerDocument.body);
}

function ReferenceIcon({ reference }: { reference: PromptReference }) {
  const node = reference.node;
  if (node.type === 'image' && node.metadata?.asset_id) return <span className="prompt-mention-thumb"><AssetView id={node.metadata.asset_id} expired={!!node.metadata.expires_at && Date.parse(node.metadata.expires_at) <= Date.now()}/></span>;
  const Icon = node.type === 'video' ? Video : node.type === 'audio' ? Music : FileText;
  return <span className="prompt-mention-thumb"><Icon size={16}/></span>;
}

function clamp(value: number, min: number, max: number) { return max < min ? min : Math.min(Math.max(value, min), max); }

// 用同样排版的隐藏镜像测量 @ 的布局坐标，菜单贴在光标处而不是输入框边缘。
const mirrorProps = ['boxSizing', 'width', 'paddingTop', 'paddingRight', 'paddingBottom', 'paddingLeft', 'borderTopWidth', 'borderRightWidth', 'borderBottomWidth', 'borderLeftWidth', 'fontStyle', 'fontVariant', 'fontWeight', 'fontSize', 'lineHeight', 'fontFamily', 'textAlign', 'textIndent', 'letterSpacing', 'wordSpacing', 'tabSize'] as const;
function caretPoint(textarea: HTMLTextAreaElement, index: number) {
  const doc = textarea.ownerDocument, computed = (doc.defaultView || window).getComputedStyle(textarea);
  const mirror = doc.createElement('div');
  Object.assign(mirror.style, { position: 'absolute', top: '0', left: '-9999px', visibility: 'hidden', whiteSpace: 'pre-wrap', overflowWrap: 'break-word' } satisfies Partial<CSSStyleDeclaration>);
  for (const prop of mirrorProps) (mirror.style as unknown as Record<string, string>)[prop] = computed[prop];
  mirror.textContent = textarea.value.slice(0, index);
  const marker = doc.createElement('span');
  marker.textContent = textarea.value.slice(index) || '.';
  mirror.appendChild(marker);
  doc.body.appendChild(mirror);
  const point = { left: marker.offsetLeft, top: marker.offsetTop };
  mirror.remove();
  return point;
}

