export const DEFAULT_LATENCY_BUCKET_BOUNDARIES = [100, 200, 500, 1000, 2000] as const
export const MAX_LATENCY_BUCKET_BOUNDARY_MS = 86_400_000
export const LATENCY_BUCKET_STORAGE_KEY = 'ops-latency-bucket-boundaries'

export function defaultLatencyBucketBoundaries(): number[] {
  return [...DEFAULT_LATENCY_BUCKET_BOUNDARIES]
}

export function normalizeLatencyBucketBoundaries(values: readonly unknown[]): number[] | null {
  if (values.length !== DEFAULT_LATENCY_BUCKET_BOUNDARIES.length) return null

  const normalized = values.map((value) => Number(value))
  for (let index = 0; index < normalized.length; index += 1) {
    const boundary = normalized[index]
    if (!Number.isInteger(boundary) || boundary <= 0 || boundary > MAX_LATENCY_BUCKET_BOUNDARY_MS) {
      return null
    }
    if (index > 0 && boundary <= normalized[index - 1]) {
      return null
    }
  }
  return normalized
}

// URL 中必须是五个逗号分隔的十进制整数，任何非法值都交由调用方回退默认配置。
export function parseLatencyBucketBoundaries(raw: string): number[] | null {
  if (!raw.trim()) return null
  const parts = raw.split(',').map((value) => value.trim())
  if (parts.some((value) => !/^\d+$/.test(value))) return null
  return normalizeLatencyBucketBoundaries(parts)
}

export function serializeLatencyBucketBoundaries(boundaries: readonly number[]): string {
  return boundaries.join(',')
}

export function areLatencyBucketBoundariesEqual(left: readonly number[], right: readonly number[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index])
}

export function areDefaultLatencyBucketBoundaries(boundaries: readonly number[]): boolean {
  return areLatencyBucketBoundariesEqual(boundaries, DEFAULT_LATENCY_BUCKET_BOUNDARIES)
}
