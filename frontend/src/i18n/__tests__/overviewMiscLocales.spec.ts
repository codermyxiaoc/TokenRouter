import { describe, expect, it } from 'vitest'

import zh from '../locales/zh'

describe('overview and payment locale keys', () => {
  it('exposes the missing dashboard copy at the runtime paths', () => {
    expect(zh.admin.dashboard).toMatchObject({
      newUsersToday: '今日新增用户',
      active: '活跃',
      ok: '正常',
      err: '错误',
      create: '创建',
      userUsageTrend: '用户使用趋势（Top 12）'
    })
  })

  it('keeps Claude Max simulation copy outside model routing', () => {
    expect(zh.admin.groups.claudeMaxSimulation).toMatchObject({
      title: 'Claude Max 用量模拟',
      enabled: '已启用（模拟 1h 缓存）',
      disabled: '已禁用'
    })
    expect(zh.admin.groups.modelRouting).not.toHaveProperty('claudeMaxSimulation')
  })

  it('exposes the user refund copy at the runtime path', () => {
    expect(zh.payment.admin.allowUserRefund).toBe('允许用户退款')
  })
})
