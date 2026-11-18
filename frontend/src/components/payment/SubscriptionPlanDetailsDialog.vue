<template>
  <BaseDialog
    :show="plan !== null"
    :title="plan ? t('payment.planPreview.title', { plan: plan.name }) : ''"
    :z-index="60"
    close-on-click-outside
    @close="emit('close')"
  >
    <p v-if="plan?.description" class="mb-4 whitespace-pre-line break-words text-sm text-gray-600 dark:text-dark-300">
      {{ plan.description }}
    </p>
    <div v-if="failed" class="flex flex-wrap items-center justify-between gap-3 py-2">
      <p role="alert" class="text-sm text-gray-600 dark:text-dark-300">{{ t('payment.planPreview.loadFailed') }}</p>
      <button type="button" class="btn btn-secondary btn-sm" @click="retryCount++">{{ t('payment.planPreview.retry') }}</button>
    </div>
    <p v-else-if="!preview" role="status" class="py-3 text-sm text-gray-500 dark:text-dark-400">
      {{ t('common.loading') }}
    </p>
    <template v-else>
      <p class="mb-2 text-sm text-gray-600 dark:text-dark-300">
        {{ preview.groups_restricted ? t('payment.planPreview.restricted') : t('payment.planPreview.unrestricted') }}
      </p>
      <p v-if="!preview.groups.length" class="py-3 text-sm text-gray-500 dark:text-dark-400">
        {{ t('payment.planPreview.noGroups') }}
      </p>
      <div v-else class="divide-y divide-gray-100 dark:divide-dark-700">
        <details v-for="(group, index) in preview.groups" :key="group.id" :open="index === 0" class="group">
          <summary class="flex cursor-pointer list-none items-center justify-between gap-2 py-3 [&::-webkit-details-marker]:hidden">
            <GroupBadge
              :name="group.name || `#${group.id}`"
              :platform="group.platform"
              :display-brand="group.display_brand?.trim() || resolveProviderBrandKey(group.platform)"
              :rate-multiplier="group.rate_multiplier"
              :truncate="false"
              class="min-w-0 [&>span:last-child]:shrink-0"
            />
            <span class="flex shrink-0 items-center gap-1 text-xs text-gray-500 dark:text-dark-400">
              {{ t('payment.planPreview.modelCount', { count: group.models.length }, group.models.length) }}
              <Icon name="chevronDown" size="sm" aria-hidden="true" class="transition-transform group-open:rotate-180" />
            </span>
          </summary>
          <ul v-if="group.models.length" class="flex flex-wrap gap-1.5 pb-3">
            <li
              v-for="model in group.models" :key="model"
              class="max-w-full rounded-md bg-gray-100 px-2 py-1 font-mono text-xs text-gray-700 [overflow-wrap:anywhere] dark:bg-dark-700 dark:text-dark-200"
            >
              {{ model }}
            </li>
          </ul>
          <p v-else class="pb-3 text-xs text-gray-500 dark:text-dark-400">{{ t('payment.planPreview.noModels') }}</p>
        </details>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { paymentAPI } from '@/api/payment'
import BaseDialog from '@/components/common/BaseDialog.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import type { SubscriptionPlan, SubscriptionPlanGroupPreview } from '@/types/payment'
import { resolveProviderBrandKey } from '@/utils/providerBrand'

const props = defineProps<{ plan: SubscriptionPlan | null }>()
const emit = defineEmits<{ close: [] }>()
const { t, locale } = useI18n()
const preview = ref<SubscriptionPlanGroupPreview | null>(null)
const failed = ref(false)
const retryCount = ref(0)

// 仅在查看详情时加载。切换套餐或语言、关闭弹窗时丢弃旧请求，避免显示另一套餐的权益。
watch([() => props.plan?.id, locale, retryCount], async ([id], _previous, onCleanup) => {
  preview.value = null
  failed.value = false
  if (id === undefined) return

  const controller = new AbortController()
  onCleanup(() => controller.abort())
  try {
    const { data } = await paymentAPI.getPlanGroups(id, controller.signal)
    if (!controller.signal.aborted) preview.value = data
  } catch {
    if (!controller.signal.aborted) failed.value = true
  }
}, { immediate: true })
</script>
