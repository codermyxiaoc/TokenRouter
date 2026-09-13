import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    get,
    post
  }
}))

import {
  consumeCodexInviteReset,
  getAdvancedSchedulerScore,
  previewAdvancedSchedulerScore,
  queryBatchUpstreamUsage,
  queryUpstreamUsage,
  syncFromCrs
} from '@/api/admin/accounts'

describe('admin accounts API', () => {
  beforeEach(() => {
	get.mockReset()
    post.mockReset()
  })

  it('loads an overview or a single advanced scheduler score group through the dedicated endpoint', async () => {
    const response = { groups: [] }
    get.mockResolvedValue({ data: response })

    await expect(getAdvancedSchedulerScore(42)).resolves.toEqual(response)
    expect(get).toHaveBeenLastCalledWith('/admin/accounts/42/advanced-scheduler-score', { params: undefined })

    await expect(getAdvancedSchedulerScore(42, 9)).resolves.toEqual(response)
    expect(get).toHaveBeenLastCalledWith('/admin/accounts/42/advanced-scheduler-score', { params: { group_id: 9 } })
  })

  it('sends only the safe score-preview context fields', async () => {
    const payload = {
      group_id: 9,
      requested_model: 'gpt-5',
      sticky_account_id: 42,
      previous_response_account_id: 43
    }
    post.mockResolvedValue({ data: { detail: {} } })

    await previewAdvancedSchedulerScore(42, payload)

    expect(post).toHaveBeenCalledWith('/admin/accounts/42/advanced-scheduler-score/preview', payload)
  })

  it('uses a dedicated 180 second timeout for CRS synchronization', async () => {
    const payload = {
      base_url: 'https://crs.example.com',
      username: 'admin',
      password: 'secret',
      sync_proxies: true,
      selected_account_ids: ['crs-1']
    }
    const response = {
      created: 1,
      updated: 0,
      skipped: 0,
      failed: 0,
      items: []
    }
    post.mockResolvedValue({ data: response })

    const result = await syncFromCrs(payload)

    expect(post).toHaveBeenCalledWith('/admin/accounts/sync/crs', payload, {
      timeout: 180_000
    })
    expect(result).toEqual(response)
  })

  it('Codex 重置有明细选择时发送 credit_id', async () => {
    const response = {
      code: 'reset',
      credit_id: 'credit-1',
      redeem_request_id: 'redeem-1',
      windows_reset: 1
    }
    post.mockResolvedValue({ data: response })

    const result = await consumeCodexInviteReset(42, ' credit-1 ')

    expect(post).toHaveBeenCalledWith('/admin/accounts/42/codex/invite-reset/consume', {
      credit_id: 'credit-1'
    })
    expect(result).toEqual(response)
  })

  it('Codex 重置没有明细选择时省略 credit_id', async () => {
    const response = {
      code: 'reset',
      redeem_request_id: 'redeem-2',
      windows_reset: 1
    }
    post.mockResolvedValue({ data: response })

    const result = await consumeCodexInviteReset(42)

    expect(post).toHaveBeenCalledWith('/admin/accounts/42/codex/invite-reset/consume', {})
    expect(result).toEqual(response)
  })

  it('使用固定的 API Key 上游用量查询端点和批量请求体', async () => {
    const single = { account_id: 42, adapter: 'sub2api', observed_at: '2026-08-20T00:00:00Z' }
    const batch = { usage: { '42': single }, errors: {} }
    post.mockResolvedValueOnce({ data: single }).mockResolvedValueOnce({ data: batch })

    await expect(queryUpstreamUsage(42)).resolves.toEqual(single)
    expect(post).toHaveBeenNthCalledWith(1, '/admin/accounts/42/upstream-usage/query', undefined, {
      timeout: 65_000
    })
    await expect(queryBatchUpstreamUsage([42, 43])).resolves.toEqual(batch)
    expect(post).toHaveBeenNthCalledWith(2, '/admin/accounts/upstream-usage/query/batch', {
      account_ids: [42, 43]
    }, {
      timeout: 65_000
    })
  })
})
