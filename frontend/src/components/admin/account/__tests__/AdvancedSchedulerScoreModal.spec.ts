import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AdvancedSchedulerScoreModal from '../AdvancedSchedulerScoreModal.vue'

const { getAdvancedSchedulerScore, previewAdvancedSchedulerScore } = vi.hoisted(() => ({
  getAdvancedSchedulerScore: vi.fn(),
  previewAdvancedSchedulerScore: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getAdvancedSchedulerScore,
      previewAdvancedSchedulerScore
    }
  }
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() })
}))

vi.mock('@/utils/format', () => ({
  formatDateTime: (value: string) => value
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const groups = [
  { id: 11, name: 'first-group', platform: 'gemini', eligible: true, final_score: 2.5, status: 'eligible' },
  { id: 12, name: 'second-group', platform: 'gemini', eligible: true, final_score: 1.5, status: 'eligible' }
]

function detailFor(groupID: number) {
  return {
    account: { id: 7, name: 'account', platform: 'gemini', type: 'oauth', status: 'active' },
    generated_at: '2026-08-11T00:00:00Z',
    calculation_version: 'v1',
    groups,
    detail: {
      group: groups.find(group => group.id === groupID),
      context: { baseline: true },
      eligible: true,
      candidate_pool: {
        total_candidates: 2,
        eligible_candidates: 2,
        excluded_candidates: 0,
        exclusion_reasons: {},
        top_k: 2,
        normalization_ranges: { priority_min: 1, priority_max: 2, max_waiting_count: 1 },
        candidates: [
          { id: 7, name: 'account', platform: 'gemini', priority: 1, final_score: 2.5, rank: 1, in_top_k: true, selection_weight: 2, selection_probability: 0.67 },
          { id: 8, name: 'other', platform: 'gemini', priority: 2, final_score: 1.5, rank: 2, in_top_k: true, selection_weight: 1, selection_probability: 0.33 }
        ]
      },
      score: {
        base_score: 2.5,
        sticky_bonus: 0,
        final_score: 2.5,
        rank: 1,
        in_top_k: true,
        selection_weight: 2,
        selection_probability: 0.67,
        selection_mode: 'top_k_weighted',
        formula: '1.0000×1.0000 + 1.0000×0.5000 = 2.5000'
      },
      metrics: [
        {
          key: 'priority', raw_value: '1', normalization: '1', normalized_value: 1, weight: 1,
          weighted_contribution: 1, available: true, neutral: false, source: 'account.priority'
        }
      ],
      effective_settings: [{ key: 'lb_top_k', value: '2', source: 'group_override' }],
      policy_signals: []
    }
  }
}

function mountModal() {
  return mount(AdvancedSchedulerScoreModal, {
    props: {
      show: true,
      account: {
        id: 7,
        name: 'account',
        platform: 'gemini',
        type: 'oauth'
      } as any
    },
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /></div>' },
        Select: { props: ['modelValue'], template: '<button type="button">select</button>' },
        Icon: true
      }
    }
  })
}

describe('AdvancedSchedulerScoreModal', () => {
  beforeEach(() => {
    getAdvancedSchedulerScore.mockReset()
    previewAdvancedSchedulerScore.mockReset()
    getAdvancedSchedulerScore.mockResolvedValueOnce({
      account: { id: 7, name: 'account', platform: 'gemini', type: 'oauth', status: 'active' },
      generated_at: '2026-08-11T00:00:00Z',
      calculation_version: 'v1',
      groups
    })
    getAdvancedSchedulerScore.mockResolvedValueOnce(detailFor(11))
    getAdvancedSchedulerScore.mockResolvedValueOnce(detailFor(12))
    previewAdvancedSchedulerScore.mockResolvedValue(detailFor(12))
  })

  it('loads only the overview and active group detail, then fetches another tab on demand', async () => {
    const wrapper = mountModal()
    await flushPromises()

    expect(getAdvancedSchedulerScore).toHaveBeenNthCalledWith(1, 7)
    expect(getAdvancedSchedulerScore).toHaveBeenNthCalledWith(2, 7, 11)
    expect(wrapper.text()).toContain('1.0000×1.0000 + 1.0000×0.5000 = 2.5000')
    expect(wrapper.text()).not.toContain('admin.accounts.advancedSchedulerScore.policySignals')
    expect(wrapper.get('[data-testid="advanced-scheduler-effective-settings"]').classes()).toContain('sm:grid-cols-2')

    const secondTab = wrapper.findAll('button').find(button => button.text().includes('second-group'))
    expect(secondTab).toBeDefined()
    await secondTab!.trigger('click')
    await flushPromises()

    expect(getAdvancedSchedulerScore).toHaveBeenNthCalledWith(3, 7, 12)
  })

  it('uses the preview endpoint when a model scenario is recalculated', async () => {
    const wrapper = mountModal()
    await flushPromises()

    const scenarioToggle = wrapper.findAll('button').find(button => button.text().includes('admin.accounts.advancedSchedulerScore.scenario'))
    expect(scenarioToggle).toBeDefined()
    await scenarioToggle!.trigger('click')
    const input = wrapper.find('input')
    await input.setValue('gpt-5')
    const recalculate = wrapper.findAll('button').find(button => button.text().includes('admin.accounts.advancedSchedulerScore.recalculate'))
    expect(recalculate).toBeDefined()
    await recalculate!.trigger('click')
    await flushPromises()

    expect(previewAdvancedSchedulerScore).toHaveBeenCalledWith(7, {
      group_id: 11,
      requested_model: 'gpt-5'
    })
  })
})
