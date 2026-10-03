import DOMPurify from 'dompurify'
import { marked } from 'marked'
import type { IntelligenceRun, IntelligenceTest } from '@/api/intelligence'

export type IntelligenceDisplayStatus = 'passed' | 'incorrect' | 'error' | 'unknown' | 'running' | 'queued'

// 未结束或供应商异常不能误标为模型答错，红色只表示明确未通过。
export function resultStatus(run: IntelligenceRun): IntelligenceDisplayStatus {
  if (run.status === 'queued') return 'queued'
  if (run.status === 'submitting' || run.status === 'running') return 'running'
  if (run.status === 'error' || run.verdict === 'error') return 'error'
  if (run.status === 'unknown' || run.verdict === 'unknown') return 'unknown'
  if (run.verdict === 'passed') return 'passed'
  if (run.verdict === 'failed') return 'incorrect'
  return 'unknown'
}

export function resultColor(run: IntelligenceRun): string {
  const status = resultStatus(run)
  if (status === 'passed') return 'bg-emerald-500'
  if (status === 'incorrect') return 'bg-rose-500'
  if (status === 'error' || status === 'unknown') return 'bg-amber-400'
  return 'bg-gray-300 dark:bg-dark-600'
}

export function recentRuns(runs: IntelligenceRun[], count = 60): IntelligenceRun[] {
  return [...runs].sort((a, b) => a.created_at.localeCompare(b.created_at) || a.id.localeCompare(b.id)).slice(-count)
}

export function recentArtifacts(runs: IntelligenceRun[]): IntelligenceRun[] {
  return recentRuns(runs.filter(run => run.has_artifact || !!run.html), 10).reverse()
}

export function groupTests(tests: IntelligenceTest[]) {
  const groups = new Map<number, { id: number; name: string; models: Map<string, IntelligenceTest[]> }>()
  for (const test of tests) {
    if (!groups.has(test.group_id)) groups.set(test.group_id, { id: test.group_id, name: test.group_name, models: new Map() })
    const group = groups.get(test.group_id)!
    if (!group.models.has(test.model)) group.models.set(test.model, [])
    group.models.get(test.model)!.push(test)
  }
  return [...groups.values()].map(group => ({ ...group, models: [...group.models].map(([name, tests]) => ({ name, tests })) }))
}

export function resultMarkdown(content?: string): string {
  if (!content) return ''
  // 模型回答只显示安全 Markdown，不允许远程图片请求或嵌入式主动内容。
  return DOMPurify.sanitize(marked.parse(content, { async: false, gfm: true, breaks: true }) as string, {
    FORBID_TAGS: ['img', 'svg', 'math', 'iframe', 'form', 'input', 'button', 'style'],
    FORBID_ATTR: ['style'],
  })
}

// iframe 仅可加载本模块签名内容路由，禁止把接口响应中的任意 URL 当成页面执行。
export function intelligencePreviewURL(value: string): string {
  return /^\/api\/v1\/intelligence-tests\/preview-content\/[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(value) ? value : ''
}
