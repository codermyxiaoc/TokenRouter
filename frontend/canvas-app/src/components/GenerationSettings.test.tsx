// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GenerationSettings, getImageSizes } from './GenerationSettings';
import type { Model } from '@/platform/types';
import type { CanvasNodeData } from '@/types/canvas';

const node: CanvasNodeData = { id: 'node-1', type: 'image', title: '图片', position: { x: 0, y: 0 }, width: 320, height: 240, metadata: {} };
const model: Model = { group_id: 1, group_name: '测试', model: 'image-model', operations: ['generate'], image_sizes: ['2K'], aspect_ratios: ['1:1', '16:9'], qualities: ['low', 'high'], background_options: ['auto', 'transparent'], max_reference_images: 1, output_count: 4, price_1k: 0, price_2k: 1, price_4k: 0 };
let host: HTMLDivElement;
let anchor: HTMLButtonElement;
let root: Root;
let frame: FrameRequestCallback | undefined;
const rect = (left: number, top: number, width: number, height: number) => ({ left, top, width, height, right: left + width, bottom: top + height, x: left, y: top, toJSON: () => ({}) });

beforeEach(() => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement('div');
  anchor = document.createElement('button');
  document.body.append(host, anchor);
  root = createRoot(host);
  Object.defineProperty(window, 'innerWidth', { value: 1200, configurable: true });
  Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true });
  vi.spyOn(anchor, 'getBoundingClientRect').mockReturnValue(rect(500, 640, 120, 36));
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation(callback => { frame = callback; return 1; });
  vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {});
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  anchor.remove();
  frame = undefined;
  vi.restoreAllMocks();
});

const button = (text: string) => [...document.querySelectorAll<HTMLButtonElement>('.generation-settings-popover button')].find(item => item.textContent === text)!;
const setNumber = (input: HTMLInputElement, value: string) => {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
  act(() => input.dispatchEvent(new FocusEvent('focusout', { bubbles: true })));
};

describe('画布生成设置的能力边界与弹层交互', () => {
  it('音频只展示 Grok 声音、两种浏览器格式和合法连续语速', () => {
    const onChange = vi.fn();
    const audioModel: Model = { ...model, media_type: 'audio', model: 'grok-voice-tts', audio_capabilities: { voices: ['eve','ara','rex','sal','leo'], formats: ['mp3','wav'], speed_min: .7, speed_max: 1.5, default_voice: 'eve', default_format: 'mp3', default_speed: 1, max_input_chars: 15000 } };
    act(() => root.render(<GenerationSettings open anchor={anchor} node={{ ...node, type: 'audio' }} model={audioModel} kind="audio" onClose={() => {}} onChange={onChange}/>));
    expect(document.querySelector('[role="dialog"]')?.getAttribute('aria-label')).toBe('音频设置');
    expect(button('Eve').getAttribute('aria-pressed')).toBe('true');
    expect(button('MP3').getAttribute('aria-pressed')).toBe('true');
    for (const absent of ['Alloy','Opus','PCM','4 张','2K','16:9']) expect(button(absent)).toBeUndefined();
    expect(document.querySelector('[aria-label="预计宽度"]')).toBeNull();
    expect(document.querySelector('.generation-settings-popover')?.textContent).not.toContain('声音指令');
    act(() => button('Ara').click());
    expect(onChange).toHaveBeenLastCalledWith({ voice: 'ara' });
    act(() => button('WAV').click());
    expect(onChange).toHaveBeenLastCalledWith({ audio_format: 'wav' });
    const speed = document.querySelector<HTMLInputElement>('[aria-label="自定义音频语速"]')!;
    setNumber(speed, '.88');
    expect(onChange).toHaveBeenLastCalledWith({ speed: .88 });
    setNumber(speed, '1.51');
    expect(onChange).toHaveBeenCalledTimes(3);
    expect(document.querySelector('[role="status"]')?.textContent).toContain('0.7–1.5');
    act(() => button('1.25x').click());
    expect(onChange).toHaveBeenLastCalledWith({ speed: 1.25 });
  });
  it('优先使用管理员白名单，不因零价字段扩展可选尺寸，旧目录固定输出张数', () => {
    expect(getImageSizes(model)).toEqual(['2K']);
    expect(getImageSizes({ ...model, image_sizes: [] })).toEqual([]);
    const onChange = vi.fn();
    act(() => root.render(<GenerationSettings open anchor={anchor} node={node} model={model} kind="image" onClose={() => {}} onChange={onChange}/>));
    expect(button('1K')).toBeUndefined();
    expect(button('4K')).toBeUndefined();
    expect(button('2K').getAttribute('aria-pressed')).toBe('true');
    expect(button('4 张').getAttribute('aria-pressed')).toBe('true');
    expect(button('1 张')).toBeUndefined();
    expect(button('8 张')).toBeUndefined();
    expect(document.querySelector('[aria-label="预计宽度"]')?.getAttribute('readonly')).not.toBeNull();
    act(() => button('高').click());
    expect(onChange).toHaveBeenLastCalledWith({ quality: 'high' });
    act(() => button('16:9').click());
    expect(onChange).toHaveBeenLastCalledWith({ aspect_ratio: '16:9' });
    act(() => button('透明').click());
    expect(onChange).toHaveBeenLastCalledWith({ background: 'transparent' });
    expect(onChange.mock.calls.every(([patch]) => !('count' in patch) && !('size' in patch))).toBe(true);
  });

  it('只开放目录允许的生成张数，选中后回传数值且不改写尺寸', () => {
    const onChange = vi.fn();
    act(() => root.render(<GenerationSettings open anchor={anchor} node={{ ...node, metadata: { count: 2 } }} model={{ ...model, output_count: 1, output_counts: [1, 2, 3, 4] }} kind="image" onClose={() => {}} onChange={onChange}/>));
    expect(button('2 张').getAttribute('aria-pressed')).toBe('true');
    expect(button('5 张')).toBeUndefined();
    act(() => button('3 张').click());
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ count: 3 });
    expect(document.querySelector('.generation-settings-popover')?.textContent).toContain('按实际成功张数计费');
  });

  it('切换到固定四张模型时清除旧多张选中态，显式空名单不开放数量', () => {
    const onChange = vi.fn();
    act(() => root.render(<GenerationSettings open anchor={anchor} node={{ ...node, metadata: { count: 2 } }} model={model} kind="image" onClose={() => {}} onChange={onChange}/>));
    expect(button('4 张').getAttribute('aria-pressed')).toBe('true');
    expect(button('2 张')).toBeUndefined();
    act(() => root.render(<GenerationSettings open anchor={anchor} node={node} model={{ ...model, output_counts: [] }} kind="image" onClose={() => {}} onChange={onChange}/>));
    expect(button('4 张')).toBeUndefined();
    expect(document.querySelector('.generation-settings-popover')?.textContent).toContain('没有可用的生成张数');
    expect(onChange).not.toHaveBeenCalled();
  });

  it('MJ 八张选项按总张数回传，八张以外没有重复放大选项', () => {
    const onChange = vi.fn();
    act(() => root.render(<GenerationSettings open anchor={anchor} node={node} model={{ ...model, model: 'MJ-v7', output_counts: [4, 8] }} kind="image" onClose={() => {}} onChange={onChange}/>));
    expect(button('4 张').getAttribute('aria-pressed')).toBe('true');
    expect(button('2 张')).toBeUndefined();
    expect(button('32 张')).toBeUndefined();
    act(() => button('8 张').click());
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ count: 8 });
  });

  it('视频数字输入拒绝区间内但未开放的时长，允许的离散值保持原值提交', () => {
    const onChange = vi.fn();
    const videoModel = { ...model, media_type: 'video' as const, video_capabilities: { durations: [5, 10], resolutions: ['768p'], aspect_ratios: ['16:9'] } };
    act(() => root.render(<GenerationSettings open anchor={anchor} node={{ ...node, type: 'video', metadata: { seconds: '5' } }} model={videoModel} kind="video" onClose={() => {}} onChange={onChange}/>));
    const seconds = document.querySelector<HTMLInputElement>('[aria-label="视频时长秒数"]')!;
    setNumber(seconds, '7');
    expect(onChange).not.toHaveBeenCalled();
    expect(seconds.value).toBe('5');
    expect(document.querySelector('[role="status"]')?.textContent).toContain('5、10');
    setNumber(seconds, '10');
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ seconds: '10' });
    expect(document.querySelector<HTMLInputElement>('[aria-label="视频时长滑块"]')?.max).toBe('1');
  });

  it('视频展示三个预设加第四个自定义分辨率，并保留原生 2k 档位', () => {
    const onChange = vi.fn();
    const videoModel = { ...model, media_type: 'video' as const, video_capabilities: { durations: Array.from({ length: 60 }, (_, index) => index + 1), resolutions: ['480p', '720p', '1080p', '2k'], aspect_ratios: ['1:1', '3:4', '4:3', '16:9', '9:16', '21:9', 'adaptive'], allow_custom_resolution: true, allow_custom_aspect_ratio: true, default_duration: 4, default_resolution: '720p', default_aspect_ratio: '16:9' } };
    act(() => root.render(<GenerationSettings open anchor={anchor} node={{ ...node, type: 'video' }} model={videoModel} kind="video" onClose={() => {}} onChange={onChange}/>));
    expect([...document.querySelector('.generation-settings-resolution')!.children]).toHaveLength(4);
    expect(button('480p').disabled).toBe(false);
    expect(button('720p').getAttribute('aria-pressed')).toBe('true');
    expect(button('1080p').disabled).toBe(false);
    expect(button('2k')).toBeDefined();
    expect(button('自动')).toBeDefined();
    expect(button('3:4')).toBeDefined();
    const input = document.querySelector<HTMLInputElement>('[aria-label="自定义视频分辨率"]')!;
    setNumber(input, '768');
    expect(onChange).toHaveBeenLastCalledWith({ resolution: '768p' });
    setNumber(input, '65537');
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[role="status"]')?.textContent).toContain('1–65536');
    expect(document.querySelector('[aria-label="自定义视频比例"]')).toBeNull();
    act(() => button('4:3').click());
    expect(onChange).toHaveBeenLastCalledWith({ aspect_ratio: '4:3' });
    const seconds = document.querySelector<HTMLInputElement>('[aria-label="视频时长秒数"]')!;
    expect(seconds.value).toBe('4');
    setNumber(seconds, '60');
    expect(onChange).toHaveBeenLastCalledWith({ seconds: '60' });
    setNumber(seconds, '61');
    expect(onChange).toHaveBeenCalledTimes(3);
  });

  it('已有自定义分辨率回显并驱动尺寸估算，切换模型不会沿用过时输入', () => {
    const onChange = vi.fn();
    const videoModel = { ...model, media_type: 'video' as const, video_capabilities: { durations: [5], resolutions: ['480p', '720p', '1080p'], aspect_ratios: ['16:9', '4:3'], allow_custom_resolution: true } };
    act(() => root.render(<GenerationSettings open anchor={anchor} node={{ ...node, metadata: { resolution: '768p', aspect_ratio: '4:3' } }} model={videoModel} kind="video" onClose={() => {}} onChange={onChange}/>));
    expect(document.querySelector<HTMLInputElement>('[aria-label="自定义视频分辨率"]')?.value).toBe('768');
    expect(document.querySelector<HTMLInputElement>('[aria-label="预计宽度"]')?.value).toBe('1024');
    expect(document.querySelector<HTMLInputElement>('[aria-label="预计高度"]')?.value).toBe('768');
    expect(document.querySelector('[aria-label="自定义视频比例"]')).toBeNull();
    act(() => root.render(<GenerationSettings open anchor={anchor} node={node} model={{ ...videoModel, model: 'other' }} kind="video" onClose={() => {}} onChange={onChange}/>));
    expect(document.querySelector<HTMLInputElement>('[aria-label="自定义视频分辨率"]')?.value).toBe('');
    expect(onChange).not.toHaveBeenCalled();
  });

  it('旧目录未声明自由输入时不能用自定义输入绕过现有白名单', () => {
    const onChange = vi.fn();
    const videoModel = { ...model, video_capabilities: { durations: [5], resolutions: ['768p'], aspect_ratios: ['16:9'] } };
    act(() => root.render(<GenerationSettings open anchor={anchor} node={{ ...node, type: 'video' }} model={videoModel} kind="video" onClose={() => {}} onChange={onChange}/>));
    expect(button('480p').disabled).toBe(true);
    const input = document.querySelector<HTMLInputElement>('[aria-label="自定义视频分辨率"]')!;
    expect(input.value).toBe('768');
    setNumber(input, '800');
    expect(onChange).not.toHaveBeenCalled();
    expect(input.value).toBe('768');
    expect(document.querySelector('[aria-label="自定义视频比例"]')).toBeNull();
  });

  it('弹层脱离画布容器，跟随锚点并限制在小视口内', () => {
    Object.defineProperty(window, 'innerWidth', { value: 240, configurable: true });
    Object.defineProperty(window, 'innerHeight', { value: 200, configurable: true });
    vi.mocked(anchor.getBoundingClientRect).mockReturnValue(rect(-100, 90, 120, 36));
    act(() => root.render(<GenerationSettings open anchor={anchor} node={node} model={model} kind="image" onClose={() => {}} onChange={() => {}}/>));
    const panel = document.querySelector<HTMLDivElement>('.generation-settings-popover')!;
    expect(panel.parentElement).toBe(document.body);
    expect(host.contains(panel)).toBe(false);
    expect(parseFloat(panel.style.width)).toBe(216);
    expect(parseFloat(panel.style.left)).toBe(12);
    expect(parseFloat(panel.style.maxHeight)).toBeLessThan(176);
    vi.mocked(anchor.getBoundingClientRect).mockReturnValue(rect(180, 170, 40, 20));
    act(() => frame?.(16));
    expect(parseFloat(panel.style.left) + parseFloat(panel.style.width)).toBeLessThanOrEqual(228);
    expect(parseFloat(panel.style.maxHeight)).toBeLessThanOrEqual(176);
  });

  it('内部点击保留弹层，外部点击和 Escape 关闭', () => {
    const onClose = vi.fn();
    act(() => root.render(<GenerationSettings open anchor={anchor} node={node} model={model} kind="image" onClose={onClose} onChange={() => {}}/>));
    act(() => button('2K').dispatchEvent(new Event('pointerdown', { bubbles: true })));
    expect(onClose).not.toHaveBeenCalled();
    act(() => document.body.dispatchEvent(new Event('pointerdown', { bubbles: true })));
    expect(onClose).toHaveBeenCalledTimes(1);
    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
    expect(onClose).toHaveBeenCalledTimes(2);
    expect(document.activeElement).toBe(anchor);
  });
});
