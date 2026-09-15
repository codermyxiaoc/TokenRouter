import { apiClient } from './client'
import type { MarketplaceGroup, MarketplaceStats } from '@/types'

// 弹窗关闭时可取消目录请求，模型广场原有无参数调用保持兼容。
export async function getMarketplaceModels(signal?: AbortSignal): Promise<MarketplaceGroup[]> {
  const { data } = await apiClient.get<MarketplaceGroup[]>('/marketplace/models', { signal })
  return data
}

export async function getMarketplaceStats(): Promise<MarketplaceStats> {
  const { data } = await apiClient.get<MarketplaceStats>('/marketplace/stats')
  return data
}

export const marketplaceAPI = {
  getMarketplaceModels,
  getMarketplaceStats,
}

export default marketplaceAPI
