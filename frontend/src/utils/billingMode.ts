export const BILLING_MODE_TOKEN = 'token'
export const BILLING_MODE_PER_REQUEST = 'per_request'
export const BILLING_MODE_IMAGE = 'image'
export const BILLING_MODE_VIDEO = 'video'
export const BILLING_MODE_VIDEO_TOKEN = 'video_token'
// 独立视频按任务收费与普通请求按次收费分开，避免混淆旧平台规则。
export const BILLING_MODE_VIDEO_PER_REQUEST = 'video_per_request'
// 仅用于历史记录的单位展示，不作为筛选或计费配置值写回后端。
export const BILLING_MODE_VIDEO_SECOND = 'video_second'
export const BILLING_MODE_VIDEO_REQUEST = 'video_request'

// 两端筛选复用渠道定价名称；保留后端枚举，不提交仅用于明细展示的单位值。
export function getBillingModeFilterOptions(t: (key: string) => string) {
  return [
    { value: null, label: t('admin.usage.allBillingModes') },
    { value: BILLING_MODE_TOKEN, label: t('admin.channels.billingMode.token') },
    { value: BILLING_MODE_PER_REQUEST, label: t('admin.channels.billingMode.perRequest') },
    { value: BILLING_MODE_IMAGE, label: t('admin.channels.billingMode.image') },
    { value: BILLING_MODE_VIDEO, label: t('admin.channels.billingMode.videoSeconds') },
    { value: BILLING_MODE_VIDEO_TOKEN, label: t('admin.channels.billingMode.videoToken') },
    { value: BILLING_MODE_VIDEO_PER_REQUEST, label: t('admin.channels.billingMode.videoPerRequest') },
  ]
}

export function getBillingModeLabel(mode: string | null | undefined, t: (key: string) => string): string {
  switch (mode) {
    case BILLING_MODE_PER_REQUEST: return t('admin.usage.billingModePerRequest')
    case BILLING_MODE_IMAGE: return t('admin.usage.billingModeImage')
    case BILLING_MODE_VIDEO_TOKEN: return t('admin.channels.billingMode.videoToken')
    case BILLING_MODE_VIDEO_PER_REQUEST: return t('admin.channels.billingMode.videoPerRequest')
    case BILLING_MODE_VIDEO_SECOND: return t('admin.usage.billingModeVideoSecond')
    case BILLING_MODE_VIDEO_REQUEST: return t('admin.usage.billingModeVideoRequest')
    case BILLING_MODE_VIDEO: return t('admin.usage.billingModeVideo')
    default: return t('admin.usage.billingModeToken')
  }
}

export function getBillingModeBadgeClass(mode: string | null | undefined): string {
  switch (mode) {
    case BILLING_MODE_PER_REQUEST: return 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-300'
    case BILLING_MODE_IMAGE: return 'bg-pink-100 text-pink-700 dark:bg-pink-900/30 dark:text-pink-300'
    case BILLING_MODE_VIDEO_TOKEN:
    case BILLING_MODE_VIDEO_PER_REQUEST:
    case BILLING_MODE_VIDEO_SECOND:
    case BILLING_MODE_VIDEO_REQUEST:
    case BILLING_MODE_VIDEO: return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    default: return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  }
}

interface ImageBillingRow {
  image_count: number
  billing_mode?: string | null
  total_cost?: number | null
  video_billing?: { unit: 'second' | 'million_tokens' | 'request' } | null
}

export function isImageUsage(row: Pick<ImageBillingRow, 'image_count' | 'billing_mode'> | null | undefined): boolean {
  return (row?.image_count ?? 0) > 0 && row?.billing_mode !== BILLING_MODE_TOKEN && !isVideoUsage(row)
}

// 只依据已记录的计费模式识别视频，不把历史按次请求按模型名称改为按秒。
export function isVideoUsage(row: Pick<ImageBillingRow, 'billing_mode'> | null | undefined): boolean {
  return row?.billing_mode === BILLING_MODE_VIDEO || row?.billing_mode === BILLING_MODE_VIDEO_TOKEN || row?.billing_mode === BILLING_MODE_VIDEO_PER_REQUEST
}

export function getDisplayBillingMode(row: Pick<ImageBillingRow, 'billing_mode' | 'image_count' | 'video_billing'> | null | undefined): string | null | undefined {
  if (isImageUsage(row)) {
    return BILLING_MODE_IMAGE
  }
  // 旧 Grok 按次计费也曾保存为 video，缺少快照时只显示中性“视频”。
  if (isVideoUsage(row) && row?.video_billing) {
    if (row.video_billing.unit === 'second') return BILLING_MODE_VIDEO_SECOND
    if (row.video_billing.unit === 'request') return BILLING_MODE_VIDEO_REQUEST
    if (row.video_billing.unit === 'million_tokens') return BILLING_MODE_VIDEO_TOKEN
  }
  return row?.billing_mode
}

export function imageUnitPrice(row: Pick<ImageBillingRow, 'image_count' | 'total_cost'> | null): number {
  if (!row || row.image_count <= 0) return 0
  const total = row.total_cost ?? 0
  const price = total / row.image_count
  return Number.isFinite(price) ? price : 0
}
