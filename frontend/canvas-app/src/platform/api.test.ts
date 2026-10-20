import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { listExecutions } from './api';
import { clearSession, setSession } from './session';

const successfulPage = () => new Response(JSON.stringify({ data: { items: [], total: 0, page: 1, page_size: 100 } }), { status: 200 });
const waitForAbort = (signal: AbortSignal) => new Promise<never>((_resolve, reject) => {
  if (signal.aborted) reject(signal.reason);
  else signal.addEventListener('abort', () => reject(signal.reason), { once: true });
});

beforeEach(() => {
  vi.useFakeTimers();
  setSession({ userId: 1, token: 'unit-test-token', locale: 'zh-CN', theme: 'light' });
  vi.stubGlobal('fetch', vi.fn());
});
afterEach(() => { clearSession(); vi.useRealTimers(); vi.unstubAllGlobals(); });

describe('画布任务状态只读查询', () => {
  it('悬挂查询到期终止，下一轮只 GET 原任务，不创建生成请求', async () => {
    const fetchMock = vi.mocked(fetch);
    fetchMock.mockImplementationOnce((_url, init) => waitForAbort(init!.signal!)).mockResolvedValueOnce(successfulPage());
    const result = listExecutions('canvas-test').catch(error => error);
    await vi.advanceTimersByTimeAsync(19_999);
    expect(fetchMock.mock.calls[0][1]?.signal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(await result).toMatchObject({ name: 'TimeoutError', message: '任务状态查询超时' });
    expect(await listExecutions('canvas-test')).toMatchObject({ items: [], total: 0 });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    for (const [url, init] of fetchMock.mock.calls) {
      expect(url).toBe('/api/v1/creative/executions?canvas_id=canvas-test&page=1&page_size=100');
      expect(init?.method).toBeUndefined();
      expect(init?.body).toBeUndefined();
    }
    expect(vi.getTimerCount()).toBe(0);
  });

  it('响应头返回后正文悬挂也会终止，避免解析阶段永久持有轮询锁', async () => {
    vi.mocked(fetch).mockImplementation(async (_url, init) => Object.assign(successfulPage(), {
      json: () => waitForAbort(init!.signal!),
    }));
    const result = listExecutions('canvas-test').catch(error => error);
    await vi.advanceTimersByTimeAsync(20_000);
    expect(await result).toMatchObject({ name: 'TimeoutError' });
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each(['离开画布', '退出会话'])('%s 立即终止未结束的查询并清理超时计时器', async source => {
    vi.mocked(fetch).mockImplementation((_url, init) => waitForAbort(init!.signal!));
    const controller = new AbortController();
    const result = listExecutions('canvas-test', 1, controller.signal).catch(error => error);
    if (source === '离开画布') controller.abort();
    else clearSession();
    expect(await result).toMatchObject({ name: 'AbortError' });
    expect(vi.getTimerCount()).toBe(0);
  });
});
