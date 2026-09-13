// buildOpsErrorTimeParams 统一错误列表与详情弹窗的时间范围参数。
export function buildOpsErrorTimeParams(
  timeRange: string,
  customStartTime?: string | null,
  customEndTime?: string | null
): Record<string, string> {
  if (timeRange === 'custom' && customStartTime && customEndTime) {
    return { start_time: customStartTime, end_time: customEndTime }
  }

  return { time_range: timeRange === 'custom' ? '1h' : timeRange }
}
