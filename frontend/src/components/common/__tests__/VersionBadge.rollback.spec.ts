import { beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import VersionBadge from '../VersionBadge.vue'

const { app, versions, update, rollback } = vi.hoisted(() => ({
  app: vi.fn(), versions: vi.fn(), update: vi.fn(), rollback: vi.fn()
}))
vi.mock('@/stores', () => ({ useAuthStore: () => ({ isAdmin: true }), useAppStore: app }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/system', () => ({
  getRollbackVersions: versions, performUpdate: update, rollback, restartService: vi.fn()
}))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copied: false, copyToClipboard: vi.fn() }) }))

// 仅模拟版本查询与更新入口，验证部署命令时绝不执行真实更新或回退。
function mountBadge(overrides = {}) {
  app.mockReturnValue(reactive({
    currentVersion: '0.1.278-ct-v2.5', latestVersion: '0.1.278-ct-v2.6',
    hasUpdate: true, versionLoading: false, versionWarning: '', buildType: 'release',
    releaseInfo: null, fetchVersion: vi.fn(), clearVersionCache: vi.fn(), ...overrides
  }))
  return mount(VersionBadge, { global: { stubs: { Icon: true } } })
}

describe('VersionBadge 当前项目更新来源', () => {
  beforeEach(() => vi.clearAllMocks())

  it('展示实际仓库，Docker 更新保留 v 标签且仅重建应用', async () => {
    const wrapper = mountBadge()
    await wrapper.get('button').trigger('click')
    expect(wrapper.get('a[href="https://github.com/codermyxiaoc/TokenRouter/releases"]').text()).toBe('GitHub')
    expect(wrapper.get('a[href="https://hub.docker.com/r/coderxiaoc/tokenrouter/tags"]').text()).toBe('Docker Hub')
    const command = wrapper.get('details code').text()
    expect(command).toContain('image: coderxiaoc/tokenrouter:v0.1.278-ct-v2.6')
    expect(command).toContain('docker compose pull sub2api')
    expect(command).toContain('docker compose up -d --no-deps sub2api')
    expect(command).not.toContain('ghcr.io')
    expect(update).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('回退使用当前脚本，Docker 标签与选中发布一致且不调用在线二进制替换', async () => {
    versions.mockResolvedValue({ versions: [{ version: '0.1.278-ct-v2.4', published_at: '', html_url: '' }] })
    const wrapper = mountBadge({ hasUpdate: false })
    await wrapper.get('button').trigger('click')
    await wrapper.findAll('button').find(button => button.text() === 'version.rollback')!.trigger('click')
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text().includes('v0.1.278-ct-v2.4'))!.trigger('click')
    expect(wrapper.get('code').text()).toContain('https://raw.githubusercontent.com/codermyxiaoc/TokenRouter/main/deploy/install.sh')
    expect(wrapper.get('code').text()).toContain('rollback v0.1.278-ct-v2.4')
    await wrapper.findAll('button').find(button => button.text() === 'version.deployDocker')!.trigger('click')
    expect(wrapper.get('code').text()).toContain('image: coderxiaoc/tokenrouter:v0.1.278-ct-v2.4')
    expect(wrapper.findAll('button').some(button => button.text().includes('version.rollbackConfirm'))).toBe(false)
    expect(rollback).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('检查失败不显示已是最新，错误信息作为文本展示', async () => {
    const wrapper = mountBadge({ hasUpdate: false, versionWarning: '<b>GitHub unavailable</b>' })
    await wrapper.get('button').trigger('click')
    expect(wrapper.text()).toContain('version.checkFailed')
    expect(wrapper.text()).not.toContain('version.upToDate')
    expect(wrapper.get('[role="status"]').text()).toBe('<b>GitHub unavailable</b>')
    expect(wrapper.find('[role="status"] b').exists()).toBe(false)
    wrapper.unmount()
  })
})
