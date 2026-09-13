import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountBulkActionsBar from '../AccountBulkActionsBar.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

describe('AccountBulkActionsBar', () => {
  it('allows selecting all results before any row is selected', async () => {
    const wrapper = mount(AccountBulkActionsBar, {
      props: {
        selectedIds: [],
        totalResults: 45,
        selectingAll: false,
        allResultsSelected: false
      }
    })

    const button = wrapper.findAll('button').find((item) =>
      item.text().includes('admin.accounts.bulkActions.selectAllResults')
    )

    expect(button).toBeDefined()
    await button!.trigger('click')
    expect(wrapper.emitted('select-all-results')).toHaveLength(1)
  })

  it('emits refresh-token for selected accounts', async () => {
    const wrapper = mount(AccountBulkActionsBar, {
      props: {
        selectedIds: [44],
        totalResults: 45,
        selectingAll: false,
        allResultsSelected: false
      }
    })

    await wrapper.findAll('button').find((button) => button.text() === 'admin.accounts.bulkActions.refreshToken')?.trigger('click')

    expect(wrapper.emitted('refresh-token')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('admin.accounts.bulkActions.probeUpstreamBilling')
  })
})
