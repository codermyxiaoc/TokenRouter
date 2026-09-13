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

  const pageDescription = computed(() => {
    const descriptionKey = route.meta.descriptionKey as string
    if (descriptionKey) return t(descriptionKey)
    return (route.meta.description as string) || ''
  })

  return { pageTitle, pageDescription }
}
