import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import OpsErrorLogTable from '../OpsErrorLogTable.vue'
import DataTable from '@/components/common/DataTable.vue'
import zhLocale from '@/i18n/locales/zh'
import enLocale from '@/i18n/locales/en'
import type { OpsErrorLog } from '@/api/admin/ops'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const TooltipStub = { template: '<div><slot /></div>' }
const PaginationStub = { template: '<div class="pagination-stub" />' }

function mountTable(row: Partial<OpsErrorLog>) {
  const base = {
    id: 1,
    created_at: '2026-06-05T23:59:50Z',
    phase: 'upstream',
    type: '',
    error_owner: 'provider',
    error_source: 'upstream_http',
    severity: 'error',
    status_code: 529,
    platform: 'anthropic',
    model: 'claude-opus-4-8',
    resolved: false,
    client_request_id: '',
    request_id: 'req-1',
    message: 'boom',
    user_email: '',
    account_name: '',
    group_name: '',
    ...row,
  } as OpsErrorLog

  return mount(OpsErrorLogTable, {
    props: { rows: [base], total: 1, loading: false, page: 1, pageSize: 20 },
    global: { stubs: { 'el-tooltip': TooltipStub, Pagination: PaginationStub } },
  })
}

describe('OpsErrorLogTable user/api-key/account columns', () => {
  it('展示关联请求实际扣费套餐，而非失败尝试的分组名称', () => {
    const wrapper = mountTable({
      group_name: '失败分组',
      billing_subscriptions: [
        { subscription_id: 9, plan_name: '最终结算套餐', amount_usd: 0.1 },
        { subscription_id: 10, amount_usd: 0.2 },
      ],
    })
    expect(wrapper.text()).toContain('admin.usage.billingSubscriptions')
    expect(wrapper.get('[data-testid="billing-subscriptions"]').text()).toContain('最终结算套餐')
    expect(wrapper.get('[data-testid="billing-subscriptions"]').text()).toContain('#10')
    expect(wrapper.get('[data-testid="billing-subscriptions"]').text()).not.toContain('失败分组')
  })

  it('仅详情模式优先展示时间与响应内容，并保留列显隐选择', async () => {
    const wrapper = mountTable({})
    const keys = () => wrapper.findComponent(DataTable).props('columns').map((column: { key: string }) => column.key)
    const original = keys()
    expect(original.slice(0, 2)).not.toEqual(['created_at', 'message'])
    await wrapper.setProps({ summaryFirst: true })
    expect(keys().slice(0, 2)).toEqual(['created_at', 'message'])
    expect(keys().slice(2)).toEqual(original.filter((key: string) => key !== 'created_at' && key !== 'message'))
    await wrapper.setProps({ visibleColumnKeys: ['user', 'message'] })
    expect(keys()).toEqual(['message', 'user'])
    await wrapper.setProps({ summaryFirst: false })
    expect(keys()).toEqual(['user', 'message'])
  })
  // 回归:上游错误行(phase=upstream, owner=provider)以前在单一「用户」列里只显示账号、
  // 丢失用户;现在用户/API Key/账号各占独立列,三者同时可见。
  it('renders user, api key and account in separate columns for an upstream row', () => {
    const wrapper = mountTable({
      user_id: 2,
      user_email: 'alice@test.com',
      api_key_id: 5,
      api_key_name: 'my-key',
      account_id: 9,
      account_name: 'acct-A',
    })

    const text = wrapper.text()
    expect(text).toContain('alice@test.com') // 用户列(上游行也显示用户)
    expect(text).toContain('my-key') // API Key 列
    expect(text).toContain('acct-A') // 账号列
  })

  it('shows the deleted badge for a soft-deleted api key', () => {
    const wrapper = mountTable({
      api_key_id: 5,
      api_key_name: 'old-key',
      api_key_deleted: true,
    })

    expect(wrapper.text()).toContain('old-key')
    expect(wrapper.text()).toContain('admin.ops.errorLog.keyDeletedBadge')
  })

  // 恢复请求仍展示上游 503，同时明确标注最终 HTTP 200，便于排查失败分组。
  it('keeps the upstream failure visible with the recovered final result', () => {
    const wrapper = mountTable({
      status_code: 503,
      client_status_code: 200,
      recovered_upstream: true,
      group_id: 2,
      group_name: '失败分组',
      recovered_group_id: 3,
      recovered_group_name: '恢复分组',
    })

    expect(wrapper.text()).toContain('503')
    expect(wrapper.text()).toContain('usage.errors.recovered')
    expect(wrapper.text()).toContain('usage.errors.finalStatus 200')
    expect(wrapper.text()).toContain('失败分组')
    expect(wrapper.text()).toContain('usage.errors.recoveredTo')
    expect(wrapper.text()).toContain('恢复分组')
  })

  // 历史恢复记录可能没有目标快照，禁止把失败分组当作成功目标。
  it('does not infer a recovery target from the failed group', () => {
    const wrapper = mountTable({ recovered_upstream: true, group_id: 2, group_name: '失败分组' })

    expect(wrapper.text()).toContain('usage.errors.recovered')
    expect(wrapper.text()).not.toContain('usage.errors.recoveredTo')
  })

  // 流式响应可能在 HTTP 200 后失败，只接受后端明确提供的恢复标记。
  it('does not label an HTTP 200 stream failure as recovered', () => {
    const wrapper = mountTable({
      status_code: 503,
      client_status_code: 200,
      recovered_upstream: false,
      recovered_group_name: '不应显示的目标',
    })

    expect(wrapper.text()).toContain('503')
    expect(wrapper.text()).not.toContain('usage.errors.recovered')
    expect(wrapper.text()).not.toContain('usage.errors.finalStatus')
    expect(wrapper.text()).not.toContain('不应显示的目标')
  })
})

// 防回归:组件用 admin.ops.errorLog.* 命名空间。若 i18n 键写错命名空间(如误放到
// errorDetail),真实 vue-i18n 会回退返回 key 本身 → 界面显示原始路径字符串。
// 这里用真实 locale 校验键确实可解析(返回译文而非 key)。
// 防回归:组件用 admin.ops.errorLog.* 命名空间。若键写错命名空间(如误放到
// errorDetail),界面会显示原始路径字符串而非译文。vitest 的 vue-i18n 为 runtime-only
// (无消息编译器,t() 对任何键都回退返回 key),故直接校验 locale 对象的命名空间含这些键。
describe('OpsErrorLogTable i18n keys exist in the errorLog namespace', () => {
  const locales: Record<string, any> = { zh: zhLocale, en: enLocale }
  for (const [name, msgs] of Object.entries(locales)) {
    it(`has apiKey & keyDeletedBadge for ${name}`, () => {
      const errorLog = msgs?.admin?.ops?.errorLog
      expect(errorLog?.apiKey).toBeTruthy()
      expect(errorLog?.keyDeletedBadge).toBeTruthy()
      expect(msgs?.usage?.errors?.recovered).toBeTruthy()
      expect(msgs?.usage?.errors?.recoveredHint).toBeTruthy()
      expect(msgs?.usage?.errors?.finalStatus).toBeTruthy()
      expect(msgs?.usage?.errors?.recoveredTo).toBeTruthy()
      expect(msgs?.usage?.errors?.finalFailed).toBeTruthy()
    })
  }
})
