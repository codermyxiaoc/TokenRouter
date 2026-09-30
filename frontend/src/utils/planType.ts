// 仅用于套餐展示匹配；不得使用规范化结果覆盖后端返回的原始套餐值。
export function normalizePlanType(value?: string | null): string {
  return (value || '').trim().toLowerCase().replace(/[\s_-]+/g, '')
}

// 比较时只合并同一 SKU 的别名，不能用相同展示标签合并不同套餐。
export function openAIPlanTypeKey(value?: string | null): string {
  const key = normalizePlanType(value)
  return key === 'chatgptpro' ? 'pro' : key
}

// 保留 Codex 协议的套餐原值，选择和保存时不改写未知 SKU。
export const openAIPlanTypes = [
  'free', 'go', 'plus', 'prolite', 'pro', 'promax', 'team',
  'self_serve_business_usage_based', 'self_serve_business_prolite', 'business',
  'enterprise', 'ent26', 'enterprise_cbp_usage_based', 'enterprise_cbp_automation',
  'edu', 'edu_plus', 'edu_pro', 'unknown'
] as const

// OpenAI 的展示名称独立于其他平台；状态标签保留 SKU，统计标签按产品族归并。
export function openAIPlanTypeLabel(value?: string | null, display: 'status' | 'analytics' = 'status'): string {
  switch (openAIPlanTypeKey(value)) {
    case 'free': return 'Free'
    case 'go': return 'Go'
    case 'plus': return 'Plus'
    case 'prolite': return 'Pro 100'
    case 'pro': return 'Pro 200'
    case 'promax': return 'Pro 500'
    case 'team':
    case 'selfservebusinessusagebased': return 'Business'
    case 'business': return display === 'status' ? 'Enterprise' : 'Business'
    case 'selfservebusinessprolite': return display === 'status' ? 'Business Premium' : 'Business'
    case 'enterprisecbpautomation': return display === 'status' ? 'Enterprise (Automation)' : 'Enterprise'
    case 'enterprise':
    case 'ent26':
    case 'enterprisecbpusagebased': return 'Enterprise'
    case 'edu': return display === 'status' ? 'Edu' : 'Education'
    case 'eduplus': return display === 'status' ? 'Edu Plus' : 'Education'
    case 'edupro': return display === 'status' ? 'Edu Pro' : 'Education'
    case 'unknown': return display === 'status' ? 'Unknown' : 'Account'
    default: return ''
  }
}
