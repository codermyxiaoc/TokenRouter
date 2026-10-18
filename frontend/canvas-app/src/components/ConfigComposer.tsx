import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react';
import { createPortal } from 'react-dom';
import { FileText, Image, Video, Music, Group, X } from 'lucide-react';
import { configReferencePattern, type ConfigInput } from '@/platform/config-composer';
import { ReferenceImagePreview } from './ReferenceImagePreview';
import { Select, SelectTrigger, SelectContent, SelectItem } from './ui/select';
import './config-node.css';
import type { GenerationKind } from '@/platform/types';

const iconFor = (type: string) => type === 'image' ? Image : type === 'video' ? Video : type === 'audio' ? Music : type === 'group' ? Group : FileText;

// 组装编辑器只序列化纯文字与节点 ID；剪贴板 HTML 永不写入 DOM 或文档。
export function serializeConfigEditor(element: HTMLElement): string {
  const visit = (nodes: NodeListOf<ChildNode>): string => [...nodes].map(node => {
    if (node.nodeType === Node.TEXT_NODE) return node.textContent || '';
    if (!(node instanceof HTMLElement)) return '';
    if (node.dataset.referenceNodeId) return `@[node:${node.dataset.referenceNodeId}]`;
    if (node.tagName === 'BR') return '\n';
    return visit(node.childNodes) + (node.tagName === 'DIV' || node.tagName === 'P' ? '\n' : '');
  }).join('');
  return visit(element.childNodes).replace(/\uFEFF/g, '');
}

export function ConfigComposer({ value, inputs, onChange, onClose, onDisconnect, onAddText, onUploadImage, kind = 'image' }: { value: string; inputs: ConfigInput[]; onChange: (value: string) => void; onClose: () => void; onDisconnect: (id: string) => void; onAddText: () => void; onUploadImage: () => void; kind?: GenerationKind }) {
  const editorRef = useRef<HTMLDivElement>(null), composing = useRef(false), lastLabels = useRef('');
  const [mention, setMention] = useState<string | null>(null), [activeIndex, setActiveIndex] = useState(0);
  const allowImageReference = kind !== 'text';
  const [menuPosition, setMenuPosition] = useState({ left: 0, top: 0, width: 256 });
  const candidates = mention === null ? [] : inputs.filter(input => `${input.label} ${input.node.title} ${input.node.metadata?.content || ''}`.toLowerCase().includes(mention.toLowerCase()));
  const closeMention = () => { setMention(null); setActiveIndex(0); };
  useEffect(() => {
    const editor = editorRef.current;
    if (!editor || composing.current) return;
    const labels = inputs.map(input => `${input.node.id}:${input.label}:${input.node.title}`).join('|');
    if (serializeConfigEditor(editor) === value && labels === lastLabels.current) return;
    lastLabels.current = labels;
    const focused = editor.ownerDocument.activeElement === editor;
    editor.replaceChildren();
    let end = 0;
    for (const token of value.matchAll(configReferencePattern)) {
      editor.append(editor.ownerDocument.createTextNode(value.slice(end, token.index)));
      const input = inputs.find(item => item.node.id === token[1]);
      const chip = editor.ownerDocument.createElement('span');
      chip.contentEditable = 'false'; chip.dataset.referenceNodeId = token[1]; chip.dataset.missing = String(!input); chip.className = 'config-reference-chip';
      chip.textContent = input?.label || '失效引用'; chip.title = input?.node.title || '节点已删除或未连接'; editor.append(chip);
      end = token.index! + token[0].length;
    }
    editor.append(editor.ownerDocument.createTextNode(value.slice(end)));
    if (focused) placeCaretAtEnd(editor);
  }, [value, inputs]);
  useLayoutEffect(() => {
    if (mention === null || !editorRef.current) return;
    const editor = editorRef.current, view = editor.ownerDocument.defaultView;
    if (!view) return;
    const rect = editor.getBoundingClientRect(), width = Math.min(256, view.innerWidth - 24), height = Math.min(224, candidates.length * 48 + 8);
    setMenuPosition({ left: Math.max(12, Math.min(rect.left + 8, view.innerWidth - width - 12)), top: Math.max(12, rect.bottom + height + 6 < view.innerHeight ? rect.bottom + 6 : rect.top - height - 6), width });
  }, [mention, candidates.length]);

  const syncMention = () => {
    const editor = editorRef.current, selection = editor?.ownerDocument.getSelection();
    if (!editor || !selection?.rangeCount) { closeMention(); return; }
    const range = selection.getRangeAt(0).cloneRange();
    if (!editor.contains(range.startContainer)) { closeMention(); return; }
    range.setStart(editor, 0);
    const match = /@([^\s@\[\]]*)$/.exec(range.toString());
    setMention(match ? match[1] : null); setActiveIndex(0);
  };
  const sync = () => { if (editorRef.current) onChange(serializeConfigEditor(editorRef.current)); syncMention(); };
  const insertReference = (input: ConfigInput) => {
    const editor = editorRef.current;
    if (!editor) return;
    const selection = editor.ownerDocument.getSelection();
    let range = selection?.rangeCount ? selection.getRangeAt(0) : null;
    if (!range || !editor.contains(range.startContainer)) { placeCaretAtEnd(editor); range = selection?.getRangeAt(0) || null; }
    if (!range) return;
    // 浏览器粘贴或段尾光标可能停在编辑器元素本身，先定位紧邻的文字节点再移除 @ 查询。
    let boundary = range.startContainer, offset = range.startOffset;
    if (boundary.nodeType !== Node.TEXT_NODE && offset > 0) {
      let previous = boundary.childNodes[offset - 1];
      while (previous?.lastChild && !(previous instanceof HTMLElement && previous.dataset.referenceNodeId)) previous = previous.lastChild;
      if (previous?.nodeType === Node.TEXT_NODE) { boundary = previous; offset = previous.textContent?.length || 0; }
    }
    if (boundary.nodeType === Node.TEXT_NODE) {
      const before = boundary.textContent?.slice(0, offset) || '';
      const match = /@([^\s@\[\]]*)$/.exec(before);
      if (match) range.setStart(boundary, offset - match[0].length);
    }
    range.deleteContents();
    const chip = editor.ownerDocument.createElement('span'); chip.contentEditable = 'false'; chip.dataset.referenceNodeId = input.node.id; chip.className = 'config-reference-chip'; chip.textContent = input.label; chip.title = input.node.title;
    const space = editor.ownerDocument.createTextNode(' '); range.insertNode(space); range.insertNode(chip); range.setStartAfter(space); range.collapse(true); selection?.removeAllRanges(); selection?.addRange(range);
    closeMention(); onChange(serializeConfigEditor(editor)); editor.focus();
  };
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (composing.current || event.nativeEvent.isComposing) return;
    if (mention !== null && candidates.length) {
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); setActiveIndex(index => (index + (event.key === 'ArrowDown' ? 1 : candidates.length - 1)) % candidates.length); return; }
      if (event.key === 'Enter') { event.preventDefault(); insertReference(candidates[Math.min(activeIndex, candidates.length - 1)]); return; }
      if (event.key === 'Escape') { event.preventDefault(); closeMention(); return; }
    }
    // Enter 只插入换行，不执行生成，也不让浏览器创建额外可编辑 HTML 结构。
    if (event.key === 'Enter') { event.preventDefault(); insertPlainText(editorRef.current!, '\n'); sync(); }
  };
  return <div className="config-composer" data-canvas-no-zoom onPointerDown={event => event.stopPropagation()} onMouseDown={event => event.stopPropagation()} onWheel={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()}>
    <header><div><strong>组装提示词</strong><small>使用 @ 引用已连接的图片、文本和组</small></div><button type="button" aria-label="关闭组装提示词" onClick={onClose}><X size={14}/></button></header>
    <span className="composer-label">参考内容</span>
    <div className="config-composer-references">
      {/* 加号复用本站选择组件，两个动作分别建立真实文本连线或上传私有参考图。 */}
      <Select value="" onValueChange={value => value === 'text' ? onAddText() : value === 'image' ? onUploadImage() : undefined}>
        <SelectTrigger hideChevron className="config-reference-add" aria-label="添加参考内容" title="添加参考内容">+</SelectTrigger>
        <SelectContent className="canvas-choice-content config-reference-menu" data-canvas-no-zoom position="popper" align="start" sideOffset={6} collisionPadding={12} onPointerDown={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()}>
          <SelectItem value="text">新增文本并连接</SelectItem>{allowImageReference && <SelectItem value="image">上传参考图片</SelectItem>}
        </SelectContent>
      </Select>
      {inputs.map((input, index) => { const Icon = iconFor(input.node.type); return <div key={input.node.id} className="config-composer-reference" title={input.node.title}>
      {input.node.type === 'image' ? <ReferenceImagePreview assetId={input.node.metadata?.asset_id} title={input.node.title} index={index} expiresAt={input.node.metadata?.expires_at}/> : <Icon size={20}/>}
      <span>{input.label}</span><button type="button" aria-label={`移除${input.label}连线`} onClick={() => onDisconnect(input.node.id)}><X size={12}/></button>
    </div>; })}</div>
    <div ref={editorRef} role="textbox" aria-label="组装提示词内容" aria-multiline="true" contentEditable suppressContentEditableWarning className="config-composer-input" data-placeholder="输入提示词，键入 @ 引用已连接的内容…" onInput={() => { if (!composing.current) sync(); }} onCompositionStart={() => { composing.current = true; }} onCompositionEnd={() => { composing.current = false; sync(); }} onKeyDown={onKeyDown} onKeyUp={event => { if (!['ArrowUp', 'ArrowDown', 'Enter', 'Escape'].includes(event.key) && !composing.current) syncMention(); }} onPaste={event => { event.preventDefault(); insertPlainText(editorRef.current!, event.clipboardData.getData('text/plain')); sync(); }} onDrop={event => event.preventDefault()} onBlur={event => { if (!(event.relatedTarget as Element | null)?.closest('.config-mention-menu')) closeMention(); }}/>
    <p className="config-composer-note">留空时自动组合已连接内容；填写后通过 @ 选择本次引用，编号随连接顺序更新。</p>
    {mention !== null && candidates.length > 0 && editorRef.current && createPortal(<div role="listbox" aria-label="选择引用内容" className="config-mention-menu" style={menuPosition} data-canvas-no-zoom onPointerDown={event => { event.stopPropagation(); event.preventDefault(); }} onMouseDown={event => event.preventDefault()}>{candidates.map((input, index) => { const Icon = iconFor(input.node.type); return <button key={input.node.id} type="button" role="option" aria-selected={index === Math.min(activeIndex, candidates.length - 1)} onClick={() => insertReference(input)}><Icon size={18}/><div><strong>{input.label}</strong><small>{input.node.type === 'group' ? `${input.resources.length} 个节点` : input.node.metadata?.content || input.node.title}</small></div></button>; })}</div>, editorRef.current.ownerDocument.body)}
  </div>;
}

function placeCaretAtEnd(editor: HTMLElement) { const range = editor.ownerDocument.createRange(); range.selectNodeContents(editor); range.collapse(false); const selection = editor.ownerDocument.getSelection(); selection?.removeAllRanges(); selection?.addRange(range); }
function insertPlainText(editor: HTMLElement, text: string) { const selection = editor.ownerDocument.getSelection(); if (!selection?.rangeCount || !editor.contains(selection.getRangeAt(0).startContainer)) placeCaretAtEnd(editor); const range = selection?.getRangeAt(0); if (!range) return; range.deleteContents(); const node = editor.ownerDocument.createTextNode(text); range.insertNode(node); range.setStartAfter(node); range.collapse(true); selection?.removeAllRanges(); selection?.addRange(range); }
