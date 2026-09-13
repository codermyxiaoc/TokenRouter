import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import ChannelsView from '../ChannelsView.vue'

const { listChannels, getGroups, getWebSearchEmulationConfig } = vi.hoisted(() => ({
  listChannels: vi.fn(),
  getGroups: vi.fn(),
  getWebSearchEmulationConfig: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channels: {
      list: listChannels,
      create: vi.fn(),
      update: vi.fn(),
      remove: vi.fn(),
      syncPricingModels: vi.fn(),
      getModelDefaultPricing: vi.fn()
    },
    groups: {
      getAll: getGroups
    },
    settings: {
      getWebSearchEmulationConfig
    },
    accounts: {
      list: vi.fn().mockResolvedValue({ items: [], total: 0 }),
      getById: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
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

const BaseDialogStub = defineComponent({
  props: {
    show: {
      type: Boolean,
      default: false
    }
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const SelectStub = defineComponent({
  props: {
    modelValue: {
      type: [String, Number],
      default: ''
    },
    options: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue'],
  template: `
    <div>
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        :data-option="option.value"
        @click="$emit('update:modelValue', option.value)"
      >
        {{ option.label }}
      </button>
    </div>
  `
})

function mountView() {
  return mount(ChannelsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: true,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: SelectStub,
        Icon: true,
        PlatformIcon: true,
        Toggle: true,
        PricingEntryCard: true
      }
    }
  })
}

describe('ChannelsView model routing copy', () => {
  beforeEach(() => {
    listChannels.mockReset()
    getGroups.mockReset()
    getWebSearchEmulationConfig.mockReset()
    listChannels.mockResolvedValue({ items: [], total: 0 })
    getGroups.mockResolvedValue([])
    getWebSearchEmulationConfig.mockResolvedValue({ enabled: false, providers: [] })
  })

  it('updates the restriction stage hint for all three pricing bases', async () => {
    const wrapper = mountView()
    await flushPromises()

    const createButton = wrapper.findAll('button').find(button => button.text().includes('admin.channels.createChannel'))
    expect(createButton).toBeTruthy()
    await createButton!.trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="billing-model-source-hint"]').text()).toBe('admin.channels.form.billingModelSourceHintChannelMapped')

    await wrapper.get('[data-option="requested"]').trigger('click')
    expect(wrapper.get('[data-testid="billing-model-source-hint"]').text()).toBe('admin.channels.form.billingModelSourceHintRequested')

    await wrapper.get('[data-option="upstream"]').trigger('click')
    expect(wrapper.get('[data-testid="billing-model-source-hint"]').text()).toBe('admin.channels.form.billingModelSourceHintUpstream')
  })

  it('shows the complete channel and account mapping chain', async () => {
    const wrapper = mountView()
    await flushPromises()
    const createButton = wrapper.findAll('button').find(button => button.text().includes('admin.channels.createChannel'))
    await createButton!.trigger('click')
    await flushPromises()

    const anthropicToggle = wrapper
      .findAll('label')
      .find(label => label.text().includes('admin.groups.platforms.anthropic'))
    expect(anthropicToggle).toBeTruthy()
    await anthropicToggle!.get('input[type="checkbox"]').trigger('change')

    const anthropicTab = wrapper
      .findAll('button')
      .find(button => button.text().includes('admin.groups.platforms.anthropic'))
    expect(anthropicTab).toBeTruthy()
    await anthropicTab!.trigger('click')

    expect(wrapper.get('[data-testid="channel-model-mapping-hint"]').text()).toBe('admin.channels.form.modelMappingHint')
  })
})
