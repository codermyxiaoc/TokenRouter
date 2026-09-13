import { describe, expect, it, vi } from 'vitest'
import { defineComponent, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import OpsLatencyChart from '../OpsLatencyChart.vue'

vi.mock('chart.js', () => ({
  Chart: { register: vi.fn() },
  BarElement: {},
  CategoryScale: {},
  Legend: {},
  LinearScale: {},
  Tooltip: {},
}))

vi.mock('vue-chartjs', async () => {
  const { defineComponent } = await import('vue')
  return {
    Bar: defineComponent({
      name: 'BarChartStub',
      props: ['data', 'options'],
      template: '<div class="bar-chart-stub" />',
    }),
  }
})

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    show: { type: Boolean, default: false },
    title: { type: String, default: '' },
  },
  emits: ['close'],
  template: '<div v-if="show" class="dialog-stub"><slot /><slot name="footer" /></div>',
})

function mountChart() {
  return mount(OpsLatencyChart, {
    props: {
      latencyData: null,
      loading: false,
      bucketBoundaries: [100, 200, 500, 1000, 2000],
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        EmptyState: true,
        HelpTooltip: true,
        Icon: true,
      },
    },
  })
}

describe('OpsLatencyChart', () => {
  it('校验并应用五个自定义时长边界', async () => {
    const wrapper = mountChart()
    await wrapper.get('[data-test="latency-bucket-settings"]').trigger('click')

    const values = ['1000', '2000', '5000', '10000', '20000']
    for (let index = 0; index < values.length; index += 1) {
      await wrapper.get(`[data-test="latency-boundary-${index}"]`).setValue(values[index])
    }

    await wrapper.get('[data-test="latency-boundary-apply"]').trigger('click')
    expect(wrapper.emitted('update:bucketBoundaries')).toEqual([[[1000, 2000, 5000, 10000, 20000]]])
    expect(wrapper.find('.dialog-stub').exists()).toBe(false)
  })

  it('阻止非法边界并支持恢复默认值', async () => {
    const wrapper = mountChart()
    await wrapper.get('[data-test="latency-bucket-settings"]').trigger('click')
    await wrapper.get('[data-test="latency-boundary-2"]').setValue('200')

    const applyButton = wrapper.get<HTMLButtonElement>('[data-test="latency-boundary-apply"]')
    expect(applyButton.element.disabled).toBe(true)
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)

    await wrapper.get('[data-test="latency-boundary-reset"]').trigger('click')
    await nextTick()
    const inputValues = wrapper.findAll<HTMLInputElement>('input').map((input) => input.element.value)
    expect(inputValues).toEqual(['100', '200', '500', '1000', '2000'])

    await wrapper.get('[data-test="latency-boundary-apply"]').trigger('click')
    expect(wrapper.emitted('update:bucketBoundaries')).toEqual([[[100, 200, 500, 1000, 2000]]])
  })
})
