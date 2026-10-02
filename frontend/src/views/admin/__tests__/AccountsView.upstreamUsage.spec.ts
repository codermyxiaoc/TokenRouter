import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  getBatchTodayStats,
  getById,
  queryUpstreamUsage,
  queryBatchUpstreamUsage,
  refreshCredentials,
  getAllProxies,
  getAllGroups,
  showError,
  showSuccess,
  showWarning
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getById: vi.fn(),
  queryUpstreamUsage: vi.fn(),
  queryBatchUpstreamUsage: vi.fn(),
  refreshCredentials: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      getById,
      queryUpstreamUsage,
      queryBatchUpstreamUsage,
      refreshCredentials,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: { getAll: getAllProxies },
    groups: { getAllIncludingInactive: getAllGroups }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showInfo: vi.fn(),
    showWarning
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    token: 'test-token',
    user: { id: 42 }
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

const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-row-id="row.id">
        <span data-test="account-name">{{ row.name }}</span>
        <slot name="cell-select" :row="row" />
        <slot name="cell-usage" :row="row" />
      </div>
    </div>
  `
}

const AccountUsageCellStub = defineComponent({
  props: ['account', 'todayStats', 'upstreamUsage', 'upstreamUsageError', 'upstreamUsageLoading', 'requestUpstreamUsage'],
  emits: ['account-updated'],
  template: `
    <div :data-usage-account="account.id">
      <span data-test="upstream-remaining">{{ upstreamUsage?.balance?.remaining ?? '' }}</span>
      <button data-test="query-upstream" @click="requestUpstreamUsage(account, { force: true })">query</button>
      <button data-test="account-updated" @click="$emit('account-updated', account)">updated</button>
    </div>
  `
})

const AccountBulkActionsBarStub = {
  props: ['selectedIds', 'upstreamUsageLoading'],
  emits: ['query-upstream-usage'],
  template: '<button data-test="query-upstream-batch" :disabled="upstreamUsageLoading" @click="$emit(\'query-upstream-usage\')">batch</button>'
}

const account = (id: number, type: 'apikey' | 'oauth' = 'apikey') => ({
  id,
  name: `account-${id}`,
  platform: 'openai',
  type,
  credentials: type === 'apikey' ? { base_url: 'https://gateway.example.test/v1' } : {},
  extra: type === 'apikey' ? { upstream_usage_query: { enabled: true, adapter: 'sub2api' } } : {},
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  status: 'active',
  schedulable: true,
  created_at: '2026-08-20T00:00:00Z',
  updated_at: '2026-08-20T00:00:00Z'
})

const result = (id: number) => ({
  account_id: id,
  adapter: 'sub2api',
  observed_at: '2026-08-20T01:00:00Z',
  provider: 'sub2api',
  mode: 'balance',
  unit: 'USD',
  balance: { remaining: 12.5 }
})

const mountView = () => mount(AccountsView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
      DataTable: DataTableStub,
      Pagination: true,
      ConfirmDialog: true,
      AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
      AccountTableFilters: true,
      AccountBulkActionsBar: AccountBulkActionsBarStub,
      AccountActionMenu: true,
      ImportDataModal: true,
      ReAuthAccountModal: true,
      AccountTestModal: true,
      AccountStatsModal: true,
      ScheduledTestsPanel: true,
      SyncFromCrsModal: true,
      TempUnschedStatusModal: true,
      ErrorPassthroughRulesModal: true,
      TLSFingerprintProfilesModal: true,
      TLSFingerprintRoutersModal: true,
      CreateAccountModal: true,
      EditAccountModal: true,
      BulkEditAccountModal: true,
      PlatformTypeBadge: true,
      AccountCapacityCell: true,
      AccountStatusIndicator: true,
      AccountTodayStatsCell: true,
      AccountGroupsCell: true,
      AccountUsageCell: AccountUsageCellStub,
      HelpTooltip: true,
      Icon: true
    }
  }
})

describe('admin AccountsView upstream usage', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    vi.clearAllMocks()
    listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getById.mockRejectedValue(new Error('unexpected getById call'))
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
  })

  it('部分刷新成功显示警告并更新账号，仍清理旧上游用量缓存', async () => {
    const original = account(1)
    listAccounts.mockResolvedValue({ items: [original], total: 1, page: 1, page_size: 20, pages: 1 })
    queryUpstreamUsage.mockResolvedValue(result(1))
    refreshCredentials.mockResolvedValue({ account: { ...original, name: 'updated-account' }, warning: 'missing_project_id_temporary', message: 'project id pending' })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="query-upstream"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="upstream-remaining"]').text()).toBe('12.5')

    wrapper.findComponent({ name: 'AccountActionMenu' }).vm.$emit('refresh-token', original)
    await flushPromises()

    expect(refreshCredentials).toHaveBeenCalledWith(1)
    expect(showWarning).toHaveBeenCalledWith('project id pending')
    expect(wrapper.get('[data-test="account-name"]').text()).toBe('updated-account')
    expect(wrapper.get('[data-test="upstream-remaining"]').text()).toBe('')
    expect(sessionStorage.length).toBe(0)
    expect(showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('只在手动查询时访问上游，并在五分钟会话缓存中恢复成功结果', async () => {
    listAccounts.mockResolvedValue({ items: [account(1)], total: 1, page: 1, page_size: 20, pages: 1 })
    queryUpstreamUsage.mockResolvedValue(result(1))

    const first = mountView()
    await flushPromises()
    expect(queryUpstreamUsage).not.toHaveBeenCalled()

    await first.get('[data-test="query-upstream"]').trigger('click')
    await flushPromises()
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(1)
    expect(first.get('[data-test="upstream-remaining"]').text()).toBe('12.5')
    expect(sessionStorage.length).toBe(1)
    first.unmount()

    const second = mountView()
    await flushPromises()
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(1)
    expect(second.get('[data-test="upstream-remaining"]').text()).toBe('12.5')

    // 行内按钮是强制刷新入口，必须绕过已有缓存。
    await second.get('[data-test="query-upstream"]').trigger('click')
    await flushPromises()
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(2)

    await second.get('[data-test="account-updated"]').trigger('click')
    await flushPromises()
    expect(sessionStorage.length).toBe(0)
    expect(second.get('[data-test="upstream-remaining"]').text()).toBe('')
  })

  it('混合批量选择只查询 API Key 账号，不把 OAuth 账号计为失败', async () => {
    listAccounts.mockResolvedValue({ items: [account(1), account(2, 'oauth')], total: 2, page: 1, page_size: 20, pages: 1 })
    queryBatchUpstreamUsage.mockResolvedValue({ usage: { '1': result(1) }, errors: {} })

    const wrapper = mountView()
    await flushPromises()
    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    expect(checkboxes).toHaveLength(2)
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)
    await wrapper.get('[data-test="query-upstream-batch"]').trigger('click')
    await flushPromises()

    expect(queryBatchUpstreamUsage).toHaveBeenCalledWith([1])
    expect(showSuccess).toHaveBeenCalledWith('admin.accounts.upstreamUsage.success')
    expect(showError).not.toHaveBeenCalled()
  })

  // 本站统计覆盖全部账号；单条和批量上游查询采用相同的模式与开关限制。
  it('CN 和 OpenCode 的本站统计不受上游资格影响，关闭或不支持的账号均不查询', async () => {
    const modes = [
      ['opencode_go', 'zen', 'sub2api'], ['opencode_go', 'go', 'opencode_go'],
      ['minimax', 'payg', 'sub2api'], ['minimax', 'coding', 'minimax_coding'],
      ['zhipu', 'payg', 'sub2api'], ['zhipu', 'coding', 'zhipu_coding'],
      ['kimi', 'payg', 'kimi_balance'], ['deepseek', 'payg', 'deepseek_balance'],
    ] as const
    const accounts = [
      ...modes.map(([platform, mode], index) => ({ ...account(index + 1), platform, credentials: { account_mode: mode } })),
      { ...account(9), extra: { upstream_usage_query: { enabled: false, adapter: 'sub2api' } } },
      account(10, 'oauth'),
    ]
    const ids = accounts.map(item => item.id)
    const supportedIDs = [1, 2, 3, 4, 5, 6, 7, 8]
    const response = (id: number) => ({ ...result(id), adapter: modes[id - 1][2], provider: modes[id - 1][0] })
    listAccounts.mockResolvedValue({ items: accounts, total: accounts.length, page: 1, page_size: 20, pages: 1 })
    getBatchTodayStats.mockResolvedValue({ stats: Object.fromEntries(ids.map(id => [String(id), {
      requests: 42, tokens: 200, cost: 2, standard_cost: 2, user_cost: 3,
    }])) })
    queryUpstreamUsage.mockImplementation(async (id: number) => response(id))
    queryBatchUpstreamUsage.mockImplementation(async (queryIDs: number[]) => ({
      usage: Object.fromEntries(queryIDs.map(id => [String(id), response(id)])), errors: {},
    }))
    const wrapper = mountView()
    await flushPromises()
    expect(getBatchTodayStats).toHaveBeenCalledWith(ids)
    for (const cell of wrapper.findAllComponents(AccountUsageCellStub)) {
      expect(cell.props('todayStats')).toMatchObject({ requests: 42, cost: 2, user_cost: 3 })
    }
    expect(queryUpstreamUsage).not.toHaveBeenCalled()
    expect(queryBatchUpstreamUsage).not.toHaveBeenCalled()
    for (const id of ids) {
      await wrapper.get(`[data-usage-account="${id}"] [data-test="query-upstream"]`).trigger('click')
      await flushPromises()
    }
    expect(queryUpstreamUsage.mock.calls.map(([id]) => id)).toEqual(supportedIDs)
    for (const checkbox of wrapper.findAll('input[type="checkbox"]')) await checkbox.setValue(true)
    await wrapper.get('[data-test="query-upstream-batch"]').trigger('click')
    await flushPromises()
    expect(queryBatchUpstreamUsage).toHaveBeenCalledTimes(1)
    expect(queryBatchUpstreamUsage).toHaveBeenCalledWith(supportedIDs)
    expect(showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it.each([
    ['opencode_go', 'zen', 'sub2api', 'sub2api'],
    ['opencode_go', 'zen', 'new_api', 'new_api'],
    ['opencode_go', 'zen', 'zivv', 'zivv'],
    ['opencode_go', 'zen', 'opencode_go', 'sub2api'],
    ['minimax', 'payg', 'sub2api', 'sub2api'],
    ['minimax', 'payg', 'new_api', 'new_api'],
    ['minimax', 'payg', 'zivv', 'zivv'],
    ['zhipu', 'payg', 'sub2api', 'sub2api'],
    ['zhipu', 'payg', 'new_api', 'new_api'],
    ['zhipu', 'payg', 'zivv', 'zivv'],
  ])('%s %s 的 %s 配置接受通用结果并恢复会话缓存', async (platform, mode, configured, adapter) => {
    const item = { ...account(1), platform, credentials: { account_mode: mode },
      extra: { upstream_usage_query: { enabled: true, adapter: configured } } }
    const usage = { ...result(1), adapter, provider: adapter }
    listAccounts.mockResolvedValue({ items: [item], total: 1, page: 1, page_size: 20, pages: 1 })
    queryUpstreamUsage.mockResolvedValue(usage)
    queryBatchUpstreamUsage.mockResolvedValue({ usage: { '1': usage }, errors: {} })

    const first = mountView()
    await flushPromises()
    expect(queryUpstreamUsage).not.toHaveBeenCalled()
    await first.get('[data-test="query-upstream"]').trigger('click')
    await flushPromises()
    expect(first.get('[data-test="upstream-remaining"]').text()).toBe('12.5')
    for (const checkbox of first.findAll('input[type="checkbox"]')) await checkbox.setValue(true)
    await first.get('[data-test="query-upstream-batch"]').trigger('click')
    await flushPromises()
    expect(queryBatchUpstreamUsage).toHaveBeenCalledWith([1])
    expect(showError).not.toHaveBeenCalled()
    expect(sessionStorage.length).toBe(1)
    first.unmount()

    // 复访页面只恢复匹配适配器的结果，不能误报无效响应或自动访问上游。
    const second = mountView()
    await flushPromises()
    expect(second.get('[data-test="upstream-remaining"]').text()).toBe('12.5')
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(1)
    second.unmount()
  })

  it('清除超过五分钟的成功缓存，不因列表加载自动补查上游', async () => {
    listAccounts.mockResolvedValue({ items: [account(1)], total: 1, page: 1, page_size: 20, pages: 1 })
    queryUpstreamUsage.mockResolvedValue(result(1))

    const first = mountView()
    await flushPromises()
    await first.get('[data-test="query-upstream"]').trigger('click')
    await flushPromises()
    const cacheKey = sessionStorage.key(0)
    expect(cacheKey).toBeTruthy()
    sessionStorage.setItem(cacheKey!, JSON.stringify({
      data: result(1),
      ts: Date.now() - 5 * 60 * 1000 - 1
    }))
    first.unmount()

    const second = mountView()
    await flushPromises()
    expect(second.get('[data-test="upstream-remaining"]').text()).toBe('')
    expect(sessionStorage.length).toBe(0)
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(1)
  })

  it('把 API Key 批量请求按 100 个账号分块并并发执行', async () => {
    const accounts = Array.from({ length: 205 }, (_, index) => account(index + 1))
    listAccounts.mockResolvedValue({ items: accounts, total: 205, page: 1, page_size: 500, pages: 1 })
    const releases: Array<() => void> = []
    queryBatchUpstreamUsage.mockImplementation((ids: number[]) => new Promise((resolve) => {
      releases.push(() => resolve({
        usage: Object.fromEntries(ids.map(id => [String(id), result(id)])),
        errors: {}
      }))
    }))

    const wrapper = mountView()
    await flushPromises()
    for (const checkbox of wrapper.findAll('input[type="checkbox"]')) {
      await checkbox.setValue(true)
    }
    await wrapper.get('[data-test="query-upstream-batch"]').trigger('click')
    await flushPromises()

    // 三个请求在首个请求完成前都已启动，证明批次不是串行执行。
    expect(queryBatchUpstreamUsage).toHaveBeenCalledTimes(3)
    expect(queryBatchUpstreamUsage).toHaveBeenNthCalledWith(1, Array.from({ length: 100 }, (_, index) => index + 1))
    expect(queryBatchUpstreamUsage).toHaveBeenNthCalledWith(2, Array.from({ length: 100 }, (_, index) => index + 101))
    expect(queryBatchUpstreamUsage).toHaveBeenNthCalledWith(3, [201, 202, 203, 204, 205])
    releases.forEach(release => release())
    await flushPromises()
  })
})
