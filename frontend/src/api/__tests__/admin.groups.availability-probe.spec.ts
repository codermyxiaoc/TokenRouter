import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { GroupAvailabilityProbeConfig } from '@/types'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { testAvailabilityProbe } from '@/api/admin/groups'

describe('管理员分组单次探测 API', () => {
  beforeEach(() => post.mockReset())

  it.each([[undefined, 60000], [5, 35000], [120, 150000]])('探测超时 %s 秒时为响应预留额外时间', async (timeout, expected) => {
    const config: GroupAvailabilityProbeConfig = {
      enabled: true, model_id: 'kimi-k3', protocol: 'chat_completions', timeout_seconds: timeout,
    }
    const result = { group_id: 54, status: 'failed', success: false, error_message: 'upstream error' }
    post.mockResolvedValue({ data: result })
    await expect(testAvailabilityProbe(54, config)).resolves.toEqual(result)
    expect(post).toHaveBeenCalledWith('/admin/groups/54/availability-probe/test',
      { availability_probe_config: config }, { timeout: expected })
  })
})
