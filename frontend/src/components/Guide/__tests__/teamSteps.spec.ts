import { describe, expect, it } from 'vitest'

import { getTeamSteps } from '../steps'

const translate = (key: string) => key

describe('getTeamSteps', () => {
  it('为所有者展示成员、邀请、设置和成员统计步骤', () => {
    const steps = getTeamSteps(translate, true)

    expect(steps.map((step) => step.element)).toEqual([
      undefined,
      '[data-tour="team-members"]',
      '[data-tour="team-invitations"]',
      '[data-tour="team-settings-tab"]',
      '[data-tour="sidebar-my-keys"]',
      '[data-tour="keys-scope-switch"]',
      '[data-tour="keys-create-btn"]',
      '[data-tour="sidebar-usage"]',
      '[data-tour="team-member-usage-charts"]',
    ])
    expect(steps.at(-1)?.route).toEqual({ path: '/usage' })
  })

  it('为普通成员隐藏所有者操作并展示个人限额与请求明细', () => {
    const steps = getTeamSteps(translate, false)

    expect(steps.map((step) => step.element)).toContain('[data-tour="team-limit-progress"]')
    expect(steps.map((step) => step.element)).toContain('[data-tour="team-usage-records"]')
    expect(steps.map((step) => step.element)).not.toContain('[data-tour="team-invitations"]')
    expect(steps.map((step) => step.element)).not.toContain('[data-tour="team-settings-tab"]')
  })

  it('通过路由步骤自动进入团队密钥作用域', () => {
    const steps = getTeamSteps(translate, true)
    const scopeStep = steps.find((step) => step.element === '[data-tour="keys-scope-switch"]')

    expect(scopeStep?.route).toEqual({ path: '/keys', query: { scope: 'team' } })
    expect(scopeStep?.popover?.showButtons).toBeUndefined()
  })

  it('无团队时展示创建入口和后续功能概览', () => {
    const steps = getTeamSteps(translate, false, false)

    expect(steps.map((step) => step.element)).toEqual([
      undefined,
      '[data-tour="team-create-form"]',
      '[data-tour="sidebar-my-keys"]',
      '[data-tour="sidebar-usage"]',
    ])
    expect(steps.every((step) => step.route?.path === '/team')).toBe(true)
  })
})
