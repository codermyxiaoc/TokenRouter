// 仅用于套餐展示匹配；不得使用规范化结果覆盖后端返回的原始套餐值。
export function normalizePlanType(value?: string | null): string {
  return (value || '').trim().toLowerCase().replace(/[\s_-]+/g, '')
}

// OpenAI 的展示名称独立于其他平台，未知档位由调用方保留原文。
export function openAIPlanTypeLabel(value?: string | null): string {
  switch (normalizePlanType(value)) {
    case 'plus':
      return 'Plus'
    case 'pro':
    case 'chatgptpro':
      return 'Pro 20x'
    case 'prolite':
      return 'Pro 5x'
    case 'selfservebusinessprolite':
      return 'Business Premium'
    case 'team':
      return 'Business Standard'
    case 'free':
      return 'Free'
    default:
      return ''
  }
}
