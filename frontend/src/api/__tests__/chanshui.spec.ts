import { beforeEach, describe, expect, it, vi } from 'vitest'
import { chanshuiAPI, type ChanshuiConfigInput } from '../admin/chanshui'

const { get, post, put, remove } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), remove: vi.fn() }))
vi.mock('../client', () => ({ apiClient: { get, post, put, delete: remove } }))

describe('管理员掺水检测 API', () => {
  beforeEach(() => { for (const mock of [get, post, put, remove]) mock.mockReset().mockResolvedValue({ data: {} }) })
  it('所有读取都使用管理员端点并传递取消信号', async () => {
    const signal = new AbortController().signal
    await chanshuiAPI.configs(signal); await chanshuiAPI.targets(signal); await chanshuiAPI.history(12, signal); await chanshuiAPI.detail('run/path', signal)
    expect(get.mock.calls).toEqual([
      ['/admin/chanshui-tests', { signal }], ['/admin/chanshui-tests/targets', { signal }],
      ['/admin/chanshui-tests/12/runs', { signal }], ['/admin/chanshui-tests/runs/run%2Fpath', { signal }],
    ])
  })
  it('保存账号配置、手动运行和删除不会引入站内 Key 或组路由', async () => {
    const input: ChanshuiConfigInput = { account_id: 12, model: 'model', protocol: 'auto', sections: ['fingerprint'], timeout_seconds: 360, enabled: true, schedule_enabled: false, interval_minutes: 60, disable_on_adulteration: false, auto_recover: false, minimum_score: null }
    await chanshuiAPI.save(input); await chanshuiAPI.save(input, 1); await chanshuiAPI.run(1); await chanshuiAPI.remove(1)
    expect(post.mock.calls).toEqual([['/admin/chanshui-tests', input], ['/admin/chanshui-tests/1/run']])
    expect(put).toHaveBeenCalledWith('/admin/chanshui-tests/1', input)
    expect(remove).toHaveBeenCalledWith('/admin/chanshui-tests/1')
  })
})
