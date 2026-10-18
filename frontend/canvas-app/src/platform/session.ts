export type Session = {userId: number; token: string; locale: 'zh-CN'|'en'; theme: 'dark'|'light'};
let session: Session | null = null;
let epoch = 0;
let controller = new AbortController();
const listeners = new Set<() => void>();
const cleanups = new Set<() => void>();
export const namespace = 'tokenrouter-canvas';

// @project-doc docs/domains/cloud_canvas.md#session_boundary
// 只接受父窗口同源会话，凭证不写 URL、localStorage 或画布文档。
export function isTrustedSessionMessage(event: Pick<MessageEvent, 'origin'|'source'|'data'>, origin: string, parent: Window) {
  return event.origin === origin && event.source === parent && event.data?.namespace === namespace;
}
export function validSession(data: unknown): data is Session {
  const s = data as Session;
  return !!s && Number.isSafeInteger(s.userId) && s.userId > 0 && typeof s.token === 'string' && s.token.length > 0 && ['zh-CN','en'].includes(s.locale) && ['dark','light'].includes(s.theme);
}
export function getSession() { return session; }
export function getEpoch() { return epoch; }
export function getSessionSignal() { return controller.signal; }
export function subscribeSession(fn: () => void) { listeners.add(fn); return () => {listeners.delete(fn);}; }
export function onSessionClear(fn: () => void) { cleanups.add(fn); return () => {cleanups.delete(fn);}; }
export function clearSession() {
  controller.abort(); controller = new AbortController(); session = null; epoch++;
  cleanups.forEach(fn => fn()); listeners.forEach(fn => fn());
}
export function setSession(value: Session) {
  if (session && session.userId !== value.userId) clearSession();
  session = value; listeners.forEach(fn => fn());
}
export function tellParent(type: 'ready'|'expired') {
  if (window.parent !== window) window.parent.postMessage({namespace, type}, window.location.origin);
}
export function startSessionBridge() {
  const receive = (event: MessageEvent) => {
    if (!isTrustedSessionMessage(event, window.location.origin, window.parent)) return;
    if (event.data.type === 'logout') clearSession();
    if (event.data.type === 'session' && validSession(event.data)) setSession(event.data);
  };
  window.addEventListener('message', receive); tellParent('ready');
  return () => {window.removeEventListener('message', receive); clearSession();};
}
