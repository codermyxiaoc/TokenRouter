import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAdminSettingsStore, useAppStore, useAuthStore } from '@/stores'

/**
 * 统一解析应用页面标题和描述，供全局布局在内容区展示。
 */
export function usePageMeta() {
  const route = useRoute()
  const { t } = useI18n()
  const appStore = useAppStore()
  const authStore = useAuthStore()
  const adminSettingsStore = useAdminSettingsStore()

  const pageTitle = computed(() => {
    if (route.meta.hidePageHeading) return ''

    // 自定义页面优先显示菜单配置的名称，而不是通用路由标题。
    if (route.name === 'CustomPage') {
      const id = route.params.id as string
      const publicItems = appStore.cachedPublicSettings?.custom_menu_items ?? []
      const menuItem = publicItems.find((item) => item.id === id)
        ?? (authStore.isAdmin ? adminSettingsStore.customMenuItems.find((item) => item.id === id) : undefined)
      if (menuItem?.label) return menuItem.label
    }

    const titleKey = route.meta.titleKey as string
    if (titleKey) return t(titleKey)
    return (route.meta.title as string) || ''
  })

  // 创作台描述里的素材保存期取后台公开设置；缺失或旧后端未返回时按 168 小时（7 天），不能沿用写死的文案。
  const creativeAssetRetentionHours = computed(() => {
    const hours = appStore.cachedPublicSettings?.creative_canvas_asset_retention_hours
    return typeof hours === 'number' && Number.isFinite(hours) && hours > 0 ? Math.round(hours) : 168
  })
  // 整天才按天描述，不足一天的原样显示小时，避免 1/12/25 小时都被说成 1 天。
  const creativeAssetRetention = computed(() => {
    const hours = creativeAssetRetentionHours.value
    return hours % 24 === 0 ? { days: hours / 24 } : { hours }
  })

  const pageDescription = computed(() => {
    const descriptionKey = route.meta.descriptionKey as string
    if (descriptionKey) {
      if (descriptionKey !== 'creative.description') return t(descriptionKey)
      // 整天用“{days} 天”，不足一天用“{hours} 小时”，避免 1/12/25 小时都被说成 1 天。
      const retention = creativeAssetRetention.value
      return 'days' in retention ? t('creative.description', retention) : t('creative.descriptionHours', retention)
    }
    return (route.meta.description as string) || ''
  })

  return { pageTitle, pageDescription }
}
