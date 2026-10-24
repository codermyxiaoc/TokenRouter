import type { ChanshuiReport, ChanshuiRun } from '@/api/admin/chanshui'

export const isActiveRun = (run?: ChanshuiRun | null) => !!run && ['queued', 'submitting', 'running'].includes(run.status)

export function runLabelKey(run: ChanshuiRun) {
  if (run.status !== 'completed') return `chanshui.statuses.${run.status}`
  if (run.verdict === 'adulterated') return 'chanshui.verdicts.adulterated'
  // 缺失评分不显示通过，执行异常也不能冒充模型掺水。
  if (!run.score_complete || !Number.isFinite(run.score)) return 'chanshui.incomplete'
  return `chanshui.verdicts.${run.verdict}`
}

export function runColor(run: ChanshuiRun) {
  if (isActiveRun(run)) return 'bg-blue-400'
  if (run.status === 'error' || run.status === 'unknown') return 'bg-gray-400'
  if (run.verdict === 'adulterated') return 'bg-red-500'
  return run.score_complete && Number.isFinite(run.score) && run.verdict === 'clean' ? 'bg-emerald-500' : 'bg-amber-400'
}

export function recentRuns(runs: ChanshuiRun[]) {
  // 先去重再截取最近 60 次，避免轮询与即时入队摘要同时出现时重复占位。
  return [...new Map(runs.map(run => [run.id, run])).values()]
    .sort((a, b) => (Date.parse(a.created_at) - Date.parse(b.created_at)) || a.id.localeCompare(b.id))
    .slice(-60)
}

export function hasPartScore(part: NonNullable<ChanshuiReport['parts']>[number], report: ChanshuiReport) {
  if (!part.known || !part.applicable) return false
  // 远端可能把未执行的题目折算为零分；必须先确认对应探针完成，不能据 known 单独显示成绩。
  const probes = report.probes || []
  if (part.key === 'iq') {
    const questions = probes.filter(probe => /^Q[1-8]$/.test(probe.id))
    return questions.length === 8 && new Set(questions.map(probe => probe.id)).size === 8 && questions.every(probe => probe.status === 'done')
  }
  return probes.filter(probe => probe.id === part.key).every(probe => probe.status === 'done')
}
