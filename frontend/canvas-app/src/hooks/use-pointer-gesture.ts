import { useCallback, useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react';

type EndReason = 'release' | 'cancel';
type Options<T> = { onMove: (event: PointerEvent, data: T) => void; onEnd?: (data: T, reason: EndReason) => void; scope?: string };
type Gesture<T> = { pointerId: number; buttonMask: number; capture: Element; data: T };

// 所有画布拖动共用捕获阶段的收尾，弹层拦截冒泡或浏览器漏发松手事件时也不能粘住鼠标。
export function usePointerGesture<T>(options: Options<T>) {
  const latest = useRef(options); latest.current = options;
  const gesture = useRef<Gesture<T> | null>(null);
  const frame = useRef<number | null>(null);
  const pending = useRef<PointerEvent | null>(null);
  const [active, setActive] = useState(false);
  const end = useCallback((reason: EndReason, notify = true) => {
    const current = gesture.current;
    if (!current) return;
    const last = pending.current;
    if (frame.current !== null) cancelAnimationFrame(frame.current);
    frame.current = null; pending.current = null; gesture.current = null;
    // 正常松手同步提交最后一帧；取消时丢弃待执行帧，避免结束后仍改变视口或节点。
    if (reason === 'release' && last) latest.current.onMove(last, current.data);
    try { if (current.capture.hasPointerCapture?.(current.pointerId)) current.capture.releasePointerCapture(current.pointerId); } catch { /* 元素已移除时仍必须清理手势。 */ }
    if (notify) { setActive(false); latest.current.onEnd?.(current.data, reason); }
  }, []);
  const cancel = useCallback(() => end('cancel'), [end]);
  const begin = useCallback((event: ReactPointerEvent, data: T) => {
    if (event.button !== 0 && event.button !== 1) return;
    end('cancel');
    gesture.current = { pointerId: event.pointerId, buttonMask: event.button === 1 ? 4 : 1, capture: event.currentTarget, data };
    try { event.currentTarget.setPointerCapture?.(event.pointerId); } catch { /* 捕获不可用时由窗口级监听保证收尾。 */ }
    setActive(true);
  }, [end]);

  useEffect(() => {
    // 切换画布会清理旧捕获，同时复位展示状态；不能把旧画布的拖动标记带到新画布。
    setActive(false);
    const move = (event: PointerEvent) => {
      const current = gesture.current;
      if (!current || current.pointerId !== event.pointerId) return;
      // 鼠标在窗口外松开后可能没有 pointerup，返回时的 buttons 才是实际按键状态。
      if (event.pointerType !== 'touch' && (event.buttons & current.buttonMask) === 0) { end('cancel'); return; }
      pending.current = event;
      if (frame.current !== null) return;
      frame.current = requestAnimationFrame(() => {
        frame.current = null;
        const next = pending.current; pending.current = null;
        if (gesture.current === current && next) latest.current.onMove(next, current.data);
      });
    };
    const release = (event: PointerEvent) => { if (gesture.current?.pointerId === event.pointerId) end('release'); };
    const interrupted = (event: PointerEvent) => { if (gesture.current?.pointerId === event.pointerId) end('cancel'); };
    const key = (event: KeyboardEvent) => { if (event.key === 'Escape') end('cancel'); };
    const hidden = () => { if (document.visibilityState === 'hidden') end('cancel'); };
    window.addEventListener('pointermove', move, true);
    window.addEventListener('pointerup', release, true);
    window.addEventListener('pointercancel', interrupted, true);
    window.addEventListener('lostpointercapture', interrupted, true);
    window.addEventListener('blur', cancel);
    window.addEventListener('keydown', key, true);
    document.addEventListener('visibilitychange', hidden);
    return () => {
      end('cancel', false);
      window.removeEventListener('pointermove', move, true);
      window.removeEventListener('pointerup', release, true);
      window.removeEventListener('pointercancel', interrupted, true);
      window.removeEventListener('lostpointercapture', interrupted, true);
      window.removeEventListener('blur', cancel);
      window.removeEventListener('keydown', key, true);
      document.removeEventListener('visibilitychange', hidden);
    };
  }, [cancel, end, options.scope]);
  return { begin, cancel, active };
}
