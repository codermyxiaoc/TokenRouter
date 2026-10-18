import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { X } from 'lucide-react';
import { canvasThemes } from '@/lib/canvas-theme';
import { useThemeStore } from '@/stores/use-theme-store';
import { normalizeCustomVideoResolution, resolveGenerationSettings, videoResolutionPresets } from '@/platform/generation-settings';
import type { GenerationKind, Model } from '@/platform/types';
import type { CanvasNodeData, CanvasNodeMetadata } from '@/types/canvas';
import './generation-settings.css';

export { getImageSizes } from '@/platform/generation-settings';

type GenerationSettingsProps = {
  open: boolean;
  onClose: () => void;
  anchor: HTMLElement | null;
  kind: GenerationKind;
  node: CanvasNodeData;
  model?: Model;
  onChange: (patch: Partial<CanvasNodeMetadata>) => void;
};

type PopupPosition = { left: number; top: number; width: number; maxHeight: number };
const qualityLabels: Record<string, string> = { auto: '自动', low: '低', medium: '中', high: '高', xhigh: '极高', max: '最高' };
const backgroundLabels: Record<string, string> = { auto: '自动', opaque: '不透明', transparent: '透明' };
const imageEdges: Record<string, number> = { '512': 512, '1K': 1024, '2K': 2048, '4K': 4096 };
const labelFor = (value: string) => value === 'auto' || value === 'adaptive' ? '自动' : value;

export function GenerationSettings({ open, onClose, anchor, kind, node, model, onChange }: GenerationSettingsProps) {
  const theme = canvasThemes[useThemeStore(state => state.theme)];
  const panelRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  const [position, setPosition] = useState<PopupPosition | null>(null);
  const ownerDocument = anchor?.ownerDocument;
  const {
    imageSizes, resolutions, aspectRatios: ratios, qualities, backgroundOptions: backgrounds, durations,
    imageSize = '', resolution = '', aspectRatio: ratio = '', quality = '', background = '', duration: seconds, outputCount, outputCounts, allowCustomResolution,
    maxOutputTokens, maxOutputTokensLimit, voices, audioFormats, voice = '', audioFormat = '', speed, speedMin, speedMax, maxInputChars,
  } = resolveGenerationSettings(model, node.metadata, kind);
  const dimensions = kind === 'audio' || kind === 'text' ? undefined : deriveDimensions(kind, kind === 'image' ? imageSize : resolution, ratio);
  const title = kind === 'text' ? '文本设置' : kind === 'audio' ? '音频设置' : kind === 'image' ? '图像设置' : '视频设置';

  useLayoutEffect(() => {
    if (!open || !anchor || !ownerDocument) {
      setPosition(null);
      return;
    }
    const view = ownerDocument.defaultView;
    if (!view) return;
    let frame = 0;
    let disposed = false;
    // 画布平移使用 CSS transform，普通 resize/scroll 监听不足以追踪锚点。
    const syncPosition = () => {
      if (disposed || !panelRef.current) return;
      if (!anchor.isConnected) {
        closeRef.current();
        return;
      }
      const viewport = view.visualViewport;
      const viewportWidth = viewport?.width || view.innerWidth;
      const viewportHeight = viewport?.height || view.innerHeight;
      const viewportLeft = viewport?.offsetLeft || 0;
      const viewportTop = viewport?.offsetTop || 0;
      const margin = Math.min(12, viewportWidth / 4, viewportHeight / 4);
      const gap = 8;
      const rect = anchor.getBoundingClientRect();
      const width = Math.max(1, Math.min(356, viewportWidth - margin * 2));
      const above = Math.max(0, rect.top - viewportTop - gap - margin);
      const below = Math.max(0, viewportTop + viewportHeight - rect.bottom - gap - margin);
      const placeAbove = above >= 260 || above >= below;
      // 小视口不沿用上游的最小 260px 高度，保证弹层始终可滚动且不越界。
      const available = Math.max(1, Math.min(viewportHeight - margin * 2, placeAbove ? above : below));
      const height = Math.min(panelRef.current.getBoundingClientRect().height, available);
      const left = Math.max(viewportLeft + margin, Math.min(viewportLeft + viewportWidth - width - margin, rect.left));
      const candidateTop = placeAbove ? rect.top - gap - height : rect.bottom + gap;
      const top = Math.max(viewportTop + margin, Math.min(viewportTop + viewportHeight - height - margin, candidateTop));
      const next = { left, top, width, maxHeight: available };
      setPosition(previous => previous && Object.keys(next).every(key => Math.abs(previous[key as keyof PopupPosition] - next[key as keyof PopupPosition]) < 0.5) ? previous : next);
    };
    const followAnchor = () => {
      syncPosition();
      if (!disposed) frame = view.requestAnimationFrame(followAnchor);
    };
    syncPosition();
    frame = view.requestAnimationFrame(followAnchor);
    view.addEventListener('resize', syncPosition);
    view.addEventListener('scroll', syncPosition, true);
    viewportEvents(view.visualViewport, 'addEventListener', syncPosition);
    return () => {
      disposed = true;
      view.cancelAnimationFrame(frame);
      view.removeEventListener('resize', syncPosition);
      view.removeEventListener('scroll', syncPosition, true);
      viewportEvents(view.visualViewport, 'removeEventListener', syncPosition);
    };
  }, [open, anchor, ownerDocument]);

  useEffect(() => {
    if (!open || !anchor || !ownerDocument) return;
    const closeOutside = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (target && (anchor.contains(target) || panelRef.current?.contains(target))) return;
      closeRef.current();
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      event.stopPropagation();
      closeRef.current();
      anchor.focus();
    };
    ownerDocument.addEventListener('pointerdown', closeOutside, true);
    ownerDocument.addEventListener('keydown', closeOnEscape, true);
    return () => {
      ownerDocument.removeEventListener('pointerdown', closeOutside, true);
      ownerDocument.removeEventListener('keydown', closeOnEscape, true);
    };
  }, [open, anchor, ownerDocument]);

  if (!open || !ownerDocument) return null;
  const style: CSSProperties & Record<`--generation-settings-${string}`, string> = {
    ...(position || { left: 12, top: 12, width: 356, maxHeight: 'calc(100vh - 24px)' }),
    visibility: position ? 'visible' : 'hidden',
    '--generation-settings-panel': theme.toolbar.panel,
    '--generation-settings-fill': theme.node.fill,
    '--generation-settings-stroke': theme.node.stroke,
    '--generation-settings-text': theme.node.text,
    '--generation-settings-muted': theme.node.muted,
    '--generation-settings-focus': theme.canvas.selectionStroke,
  };
  return createPortal(
    <div ref={panelRef} role="dialog" aria-label={title} className="generation-settings-popover" style={style} data-canvas-no-zoom onPointerDown={event => event.stopPropagation()} onMouseDown={event => event.stopPropagation()} onClick={event => event.stopPropagation()}>
      <div className="generation-settings-heading"><h2>{title}</h2><button type="button" aria-label="关闭设置" onClick={onClose}><X size={16}/></button></div>
      {!model && <p className="generation-settings-hint">请先选择管理员开放的模型。</p>}
      {kind === 'text' && <SettingGroup title="最大输出 Token"><TextOutputControl value={maxOutputTokens} max={maxOutputTokensLimit} onChange={value => onChange({ max_output_tokens: value })}/><p className="generation-settings-hint">生成一个文本结果，按实际 Token 用量结算。</p></SettingGroup>}
      {kind === 'audio' && <>
        <SettingGroup title="声音"><Options values={voices} selected={voice} columns={3} labels={Object.fromEntries(voices.map(value => [value, value[0].toUpperCase() + value.slice(1)]))} onChange={value => onChange({ voice: value })}/></SettingGroup>
        <SettingGroup title="格式"><Options values={audioFormats} selected={audioFormat} columns={2} labels={Object.fromEntries(audioFormats.map(value => [value, value.toUpperCase()]))} onChange={value => onChange({ audio_format: value })}/></SettingGroup>
        <SettingGroup title="语速"><AudioSpeedControl key={`${model?.group_id}:${model?.model}`} value={speed} min={speedMin} max={speedMax} onChange={value => onChange({ speed: value })}/></SettingGroup>
        <p className="generation-settings-hint">自动识别文本语言，仅支持文字转语音。{maxInputChars ? `每次最多 ${maxInputChars.toLocaleString()} 个字符，按实际提交文本字符计费。` : ''}</p>
      </>}
      {kind === 'image' && <SettingGroup title="质量"><Options values={qualities} selected={quality} columns={3} labels={qualityLabels} onChange={value => onChange({ quality: value })} empty="此模型未开放质量设置。"/></SettingGroup>}
      {kind === 'video' && <SettingGroup title="清晰度"><VideoResolutionControl key={`${model?.group_id}:${model?.model}`} value={resolution} allowed={resolutions} allowCustom={allowCustomResolution} onChange={value => onChange({ resolution: value })}/></SettingGroup>}
      {(kind === 'image' || kind === 'video') && <SettingGroup title="尺寸"><div className="generation-settings-dimensions"><Dimension label="W" value={dimensions?.width}/><span aria-hidden="true">↔</span><Dimension label="H" value={dimensions?.height}/></div><p className="generation-settings-hint">根据档位与比例估算，实际尺寸由模型决定。</p></SettingGroup>}
      {kind === 'image' && <SettingGroup title="分辨率"><Options values={imageSizes} selected={imageSize} columns={4} onChange={value => onChange({ image_size: value })} empty="此模型没有可用的尺寸档位。"/></SettingGroup>}
      {(kind === 'image' || kind === 'video') && <SettingGroup title={kind === 'image' ? '宽高比' : '比例'}>{ratios.length ? <div className="generation-settings-options generation-settings-ratios">{ratios.map(value => <button key={value} type="button" aria-pressed={ratio === value} className="generation-settings-ratio" onClick={() => onChange({ aspect_ratio: value })}><AspectIcon value={value}/><span>{labelFor(value)}</span></button>)}</div> : <p className="generation-settings-hint">此模型未开放比例设置。</p>}</SettingGroup>}
      {kind === 'video' && <SettingGroup title="秒数"><DurationControl value={seconds} allowed={durations} onChange={value => onChange({ seconds: String(value) })}/></SettingGroup>}
      {kind === 'image' && <>
        {!!backgrounds.length && <SettingGroup title="背景"><Options values={backgrounds} selected={background} columns={3} labels={backgroundLabels} onChange={value => onChange({ background: value })}/></SettingGroup>}
        <SettingGroup title="生成张数"><Options values={outputCounts.map(String)} selected={outputCount ? String(outputCount) : ''} columns={4} labels={Object.fromEntries(outputCounts.map(value => [value, `${value} 张`]))} onChange={value => onChange({ count: Number(value) })} empty="此模型没有可用的生成张数。"/>{!!outputCounts.length && <p className="generation-settings-hint">{outputCounts.length === 1 ? `当前模型每次固定生成 ${outputCounts[0]} 张，按实际成功张数计费。` : '按所选张数预扣，生成完成后按实际成功张数计费。'}</p>}</SettingGroup>
      </>}
      {kind === 'video' && allowCustomResolution && <p className="generation-settings-hint">参数将按所选协议提交；模型支持范围与实际输出由上游决定，费用按实际提交的分辨率和计费规则计算。</p>}
    </div>,
    ownerDocument.body,
  );
}

function viewportEvents(viewport: VisualViewport | null, method: 'addEventListener' | 'removeEventListener', listener: () => void) {
  viewport?.[method]('resize', listener);
  viewport?.[method]('scroll', listener);
}

function SettingGroup({ title, children }: { title: string; children: ReactNode }) {
  return <section className="generation-settings-group"><h3>{title}</h3>{children}</section>;
}

function Options({ values, selected, columns, labels = {}, onChange, empty }: { values: string[]; selected: string; columns: number; labels?: Record<string, string>; onChange: (value: string) => void; empty?: string }) {
  if (!values.length) return <p className="generation-settings-hint">{empty || '此模型未开放此设置。'}</p>;
  return <div className="generation-settings-options" style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}>{values.map(value => <button key={value} type="button" aria-pressed={selected === value} className="generation-settings-pill" onClick={() => onChange(value)}>{labels[value] || labelFor(value)}</button>)}</div>;
}

function AspectIcon({ value }: { value: string }) {
  const ratio = parseRatio(value);
  if (!ratio) return null;
  const longSide = Math.max(ratio.width, ratio.height);
  return <span className="generation-settings-ratio-icon" aria-hidden="true"><span style={{ width: Math.max(10, ratio.width / longSide * 24), height: Math.max(10, ratio.height / longSide * 24) }}/></span>;
}

function Dimension({ label, value }: { label: string; value?: number }) {
  return <label className="generation-settings-dimension"><span>{label}</span><input aria-label={label === 'W' ? '预计宽度' : '预计高度'} value={value || '—'} readOnly tabIndex={-1}/></label>;
}

function AudioSpeedControl({ value, min, max, onChange }: { value?: number; min?: number; max?: number; onChange: (value: number) => void }) {
  const [draft, setDraft] = useState(value === undefined ? '' : String(value));
  const [error, setError] = useState('');
  useEffect(() => { setDraft(value === undefined ? '' : String(value)); setError(''); }, [value, min, max]);
  if (min === undefined || max === undefined || min > max) return <p className="generation-settings-hint">此模型未配置可用语速。</p>;
  const presets = [.75, 1, 1.25, 1.5].filter(item => item >= min && item <= max);
  const commit = () => {
    const next = Number(draft);
    // 允许连续语速，拒绝空白、无穷值和越界，不静默截断用户输入。
    if (!draft.trim() || !Number.isFinite(next) || next < min || next > max) {
      setDraft(value === undefined ? '' : String(value)); setError(`语速须在 ${min}–${max} 倍之间`); return;
    }
    setError('');
    if (next !== value) onChange(next);
  };
  return <><Options values={presets.map(String)} selected={String(value)} columns={4} labels={Object.fromEntries(presets.map(item => [item, `${item}x`]))} onChange={item => { setError(''); onChange(Number(item)); }}/>
    <label className="generation-settings-audio-speed"><input type="number" aria-label="自定义音频语速" min={min} max={max} step="any" value={draft} aria-invalid={!!error} onChange={event => setDraft(event.currentTarget.value)} onBlur={commit} onKeyDown={event => { if (event.key === 'Enter') event.currentTarget.blur(); }}/><span>x</span></label>
    {error && <p role="status" className="generation-settings-error">{error}</p>}</>;
}

function VideoResolutionControl({ value, allowed, allowCustom, onChange }: { value: string; allowed: string[]; allowCustom: boolean; onChange: (value: string) => void }) {
  const customSelected = !videoResolutionPresets.includes(value) && !!normalizeCustomVideoResolution(value);
  const [draft, setDraft] = useState(customSelected ? value.replace(/p$/i, '') : '');
  const [error, setError] = useState('');
  useEffect(() => { setDraft(customSelected ? value.replace(/p$/i, '') : ''); setError(''); }, [value, allowed.join(','), allowCustom]);
  const canEnterCustom = allowCustom || allowed.some(item => !videoResolutionPresets.includes(item) && !!normalizeCustomVideoResolution(item));
  const extraTiers = allowed.filter(item => !videoResolutionPresets.includes(item) && !normalizeCustomVideoResolution(item));
  const commit = () => {
    const next = normalizeCustomVideoResolution(draft);
    if (!next || (!allowCustom && !allowed.includes(next))) {
      setDraft(customSelected ? value.replace(/p$/i, '') : '');
      setError(allowCustom ? '请输入 1–65536 的整数分辨率，例如 768。' : `可用分辨率：${allowed.join('、')}`);
      return;
    }
    setError('');
    if (next !== value) onChange(next);
  };
  return <><div className="generation-settings-options generation-settings-resolution">
    {videoResolutionPresets.map(item => <button key={item} type="button" className="generation-settings-pill" aria-pressed={value === item} disabled={!allowCustom && !allowed.includes(item)} onClick={() => onChange(item)}>{item}</button>)}
    <label className="generation-settings-custom-resolution" data-selected={customSelected} title="自定义分辨率">
      <input aria-label="自定义视频分辨率" type="number" min={1} max={65536} step={1} placeholder="自定义" disabled={!canEnterCustom} value={draft} aria-invalid={!!error} onChange={event => setDraft(event.currentTarget.value)} onBlur={commit} onKeyDown={event => { if (event.key === 'Enter') event.currentTarget.blur(); }}/><span>p</span>
    </label>
  </div>{extraTiers.length > 0 && <div className="generation-settings-extra-resolutions"><Options values={extraTiers} selected={value} columns={4} onChange={onChange}/></div>}{error && <p role="status" className="generation-settings-error">{error}</p>}</>;
}

function DurationControl({ value, allowed, onChange }: { value?: number; allowed: number[]; onChange: (value: number) => void }) {
  const [draft, setDraft] = useState(value ? String(value) : '');
  const [error, setError] = useState('');
  useEffect(() => { setDraft(value ? String(value) : ''); setError(''); }, [value, allowed.join(',')]);
  if (!allowed.length) return <p className="generation-settings-hint">此模型未配置可用时长。</p>;
  const commit = () => {
    const next = Number(draft);
    // 输入框和滑块只可提交服务端白名单中的秒数，离散时长不能被当成连续区间。
    if (!draft.trim() || !allowed.includes(next)) {
      setDraft(value ? String(value) : '');
      setError(`可用时长：${allowed.join('、')} 秒`);
      return;
    }
    setError('');
    if (next !== value) onChange(next);
  };
  return <><div className="generation-settings-duration"><input aria-label="视频时长滑块" type="range" min={0} max={Math.max(0, allowed.length - 1)} step={1} disabled={allowed.length === 1} value={Math.max(0, allowed.indexOf(value!))} onChange={event => { const next = allowed[Number(event.currentTarget.value)]; if (next !== undefined) { setError(''); onChange(next); } }}/><input aria-label="视频时长秒数" type="number" min={allowed[0]} max={allowed[allowed.length - 1]} step={1} value={draft} disabled={allowed.length === 1} aria-invalid={!!error} onChange={event => setDraft(event.currentTarget.value)} onBlur={commit} onKeyDown={event => { if (event.key === 'Enter') event.currentTarget.blur(); }}/><span>s</span></div>{error && <p role="status" className="generation-settings-error">{error}</p>}{allowed.length === 1 && <p className="generation-settings-hint">当前模型固定为 {allowed[0]} 秒。</p>}</>;
}

function parseRatio(value: string): { width: number; height: number } | null {
  const match = /^(\d+(?:\.\d+)?):(\d+(?:\.\d+)?)$/.exec(value);
  if (!match) return null;
  const width = Number(match[1]), height = Number(match[2]);
  return width > 0 && height > 0 && Number.isFinite(width / height) ? { width, height } : null;
}

// 尺寸仅用于展示，不向后端添加自由尺寸或改写分辨率参数。
function deriveDimensions(kind: 'image' | 'video', resolution: string, ratioValue: string): { width: number; height: number } | undefined {
  const ratio = parseRatio(ratioValue);
  if (!ratio) return;
  const edge = kind === 'image' ? imageEdges[resolution.toUpperCase()] : /^\d+p$/i.test(resolution) ? Number(resolution.slice(0, -1)) : 0;
  if (!edge) return;
  const divisor = kind === 'image' ? Math.max(ratio.width, ratio.height) : Math.min(ratio.width, ratio.height);
  return { width: Math.round(edge * ratio.width / divisor), height: Math.round(edge * ratio.height / divisor) };
}

// 文本只开放后端明确支持的输出上限，不发送通用但可能不兼容的推理/温度字段。
function TextOutputControl({ value, max, onChange }: { value?: number; max?: number; onChange: (value: number) => void }) {
  const [draft, setDraft] = useState(String(value ?? ''));
  const [error, setError] = useState('');
  useEffect(() => { setDraft(String(value ?? '')); setError(''); }, [value, max]);
  const commit = () => { const next = Number(draft); if (!max || !draft.trim() || !Number.isInteger(next) || next < 1 || next > max) { setDraft(String(value ?? '')); setError(`请输入 1–${max || '—'} 的整数`); return; } setError(''); if (next !== value) onChange(next); };
  return <><label className="generation-settings-audio-speed"><input type="number" aria-label="文本最大输出Token" min={1} max={max} step={1} value={draft} disabled={!max} aria-invalid={!!error} onChange={event => setDraft(event.currentTarget.value)} onBlur={commit} onKeyDown={event => { if (event.key === 'Enter') event.currentTarget.blur(); }}/></label>{error && <p role="status" className="generation-settings-error">{error}</p>}</>;
}
