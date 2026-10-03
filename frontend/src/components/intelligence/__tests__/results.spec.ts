import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import IntelligenceResultBar from '../IntelligenceResultBar.vue'
import { groupTests, intelligencePreviewURL, recentArtifacts, recentRuns, resultColor, resultMarkdown, resultStatus } from '../results'
import { runFixture, testFixture } from './fixtures'

vi.mock('vue-i18n', async (importOriginal) => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))

describe('检测结果展示契约', () => {
  it('明确未通过为红，执行异常与未知为黄，运行中保持灰色', () => {
    expect(resultColor(runFixture())).toContain('emerald')
    expect(resultColor(runFixture({ verdict: 'failed' }))).toContain('rose')
    expect(resultColor(runFixture({ status: 'error', verdict: 'failed' }))).toContain('amber')
    expect(resultStatus(runFixture({ status: 'running', verdict: 'failed' }))).toBe('running')
    expect(resultStatus(runFixture({ verdict: 'pending' }))).toBe('unknown')
  })

  it('只保留最近60条，状态条从旧到新，作品从新到旧且最多10个', () => {
    const runs = Array.from({ length: 70 }, (_, index) => runFixture({ id: `run-${index}`, has_artifact: true, created_at: new Date(Date.UTC(2026, 9, 3, 0, index)).toISOString() })).reverse()
    expect(recentRuns(runs)).toHaveLength(60)
    expect(recentRuns(runs)[0].id).toBe('run-10')
    expect(recentRuns(runs)[59].id).toBe('run-69')
    expect(recentArtifacts(runs)).toHaveLength(10)
    expect(recentArtifacts(runs)[0].id).toBe('run-69')
    expect(recentArtifacts(runs)[9].id).toBe('run-60')
  })

  it('同组多个模型和同模型多个类型分层分组，不用分组名合并不同分组', () => {
    const grouped = groupTests([testFixture(), testFixture({ id: 2, benchmark: 'drawing' }), testFixture({ id: 3, model: 'model-b' }), testFixture({ id: 4, group_id: 2 })])
    expect(grouped).toHaveLength(2)
    expect(grouped[0].models).toHaveLength(2)
    expect(grouped[0].models[0].tests).toHaveLength(2)
  })

  it('点击竖条返回对应run，没有详情时不能点击', async () => {
    const first = runFixture()
    const second = runFixture({ id: 'run-2', has_detail: false })
    const wrapper = mount(IntelligenceResultBar, { props: { runs: [first, second] } })
    await wrapper.get('[data-run-id="run-1"]').trigger('click')
    expect(wrapper.emitted('select')?.[0]).toEqual([first])
    expect(wrapper.get('[data-run-id="run-2"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('模型Markdown清理脚本、危险链接与远程图片', () => {
    const rendered = resultMarkdown('**通过** <script>alert(1)</script><img src="https://evil.example/track"><a href="javascript:alert(2)">坏链接</a><iframe src="/admin"></iframe>')
    expect(rendered).toContain('<strong>通过</strong>')
    expect(rendered).not.toMatch(/<script|<img|<iframe|javascript:/)
  })

  it.each(['https://evil.example/a', '//evil.example/a', '/api/v1/admin/settings', '/api/v1/intelligence-tests/preview-content/a.b?x=1', '/api/v1/intelligence-tests/preview-content/a.b#x', 'javascript:alert(1)'])('拒绝非签名相对预览路径 %s', value => {
    expect(intelligencePreviewURL(value)).toBe('')
  })

  it('允许本站签名预览路径', () => {
    const url = '/api/v1/intelligence-tests/preview-content/eyJhbGciOi.a-_b123'
    expect(intelligencePreviewURL(url)).toBe(url)
  })
})
