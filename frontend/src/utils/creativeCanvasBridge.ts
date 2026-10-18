/** 只接收本站当前画布窗口的控制消息，不接受其他标签页或嵌套窗口。 */
export function isCanvasBridgeMessage(
  event: Pick<MessageEvent, 'origin' | 'source' | 'data'>,
  origin: string,
  expectedWindow: Window | null,
): boolean {
  return expectedWindow !== null && event.origin === origin && event.source === expectedWindow &&
    event.data !== null && typeof event.data === 'object' &&
    event.data.namespace === 'tokenrouter-canvas' &&
    ['ready', 'expired'].includes(event.data.type)
}
