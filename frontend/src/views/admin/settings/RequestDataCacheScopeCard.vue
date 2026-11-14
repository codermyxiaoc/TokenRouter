<template>
  <!-- 一类请求（普通/错误）的缓存配置：开关、缓存时间、分组范围与手动清除 -->
  <div class="rounded-lg border border-gray-200 p-5 dark:border-dark-600" :data-testid="`data-cache-${kind}`">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ title }}</h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ hint }}</p>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400" :data-testid="`data-cache-${kind}-count`">
          {{ t('admin.requestDataCacheSettings.cachedCount', { count }) }}
        </p>
      </div>
      <div class="flex shrink-0 items-center gap-3">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="disabled" :data-testid="`data-cache-${kind}-clear`" @click="emit('clear')">
          {{ t('admin.requestDataCacheSettings.clear') }}
        </button>
        <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-dark-200">
          {{ t('admin.requestDataCacheSettings.enabled') }}
          <Toggle :model-value="modelValue.enabled" :disabled="disabled" @update:model-value="patch({ enabled: $event })" />
        </label>
      </div>
    </div>

    <div v-if="modelValue.enabled" class="mt-4 space-y-4">
      <div>
        <label class="input-label">{{ t('admin.requestDataCacheSettings.ttl') }}</label>
        <div class="flex max-w-sm gap-2">
          <input
            v-model.number="ttlValue"
            type="number"
            min="1"
            step="1"
            class="input flex-1"
            :disabled="disabled"
            :data-testid="`data-cache-${kind}-ttl`"
          />
          <div class="w-32">
            <Select v-model="ttlUnit" :options="unitOptions" :disabled="disabled" />
          </div>
        </div>
        <p v-if="ttlInvalid" class="mt-1 text-xs text-red-500">{{ t('admin.requestDataCacheSettings.ttlInvalid') }}</p>
      </div>

      <div>
        <label class="input-label">{{ t('admin.requestDataCacheSettings.scope') }}</label>
        <div class="max-w-sm">
          <Select
            :model-value="modelValue.all_groups ? 'all' : 'selected'"
            :options="scopeOptions"
            :disabled="disabled"
            @update:model-value="patch({ all_groups: $event === 'all' })"
          />
        </div>
      </div>

      <template v-if="!modelValue.all_groups">
        <GroupSelector
          :model-value="modelValue.group_ids"
          :groups="groups"
          :disabled="disabled"
          @update:model-value="patch({ group_ids: $event })"
        />
        <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-dark-200">
          <input
            type="checkbox"
            class="h-4 w-4 rounded border-gray-300 text-primary-500 focus:ring-primary-500"
            :checked="modelValue.include_ungrouped"
            :disabled="disabled"
            @change="patch({ include_ungrouped: ($event.target as HTMLInputElement).checked })"
          />
          {{ t('admin.requestDataCacheSettings.includeUngrouped') }}
        </label>
        <p v-if="scopeEmpty" class="text-xs text-red-500">{{ t('admin.requestDataCacheSettings.groupsRequired') }}</p>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import Select from '@/components/common/Select.vue'
import GroupSelector from '@/components/common/GroupSelector.vue'
import type { AdminGroup } from '@/types'
import type { RequestDataCacheScope } from '@/api/admin/requestDataCache'

const props = defineProps<{
  kind: 'normal' | 'error'
  title: string
  hint: string
  modelValue: RequestDataCacheScope
  groups: AdminGroup[]
  count: number
  disabled?: boolean
}>()
const emit = defineEmits<{
  'update:modelValue': [value: RequestDataCacheScope]
  clear: []
}>()

const { t } = useI18n()

// 缓存时间上限与后端一致：365 天。
const MAX_TTL_MINUTES = 365 * 24 * 60
type TTLUnit = 'minutes' | 'hours' | 'days'
const UNIT_MINUTES: Record<TTLUnit, number> = { minutes: 1, hours: 60, days: 24 * 60 }

const unitOptions = computed(() => (['minutes', 'hours', 'days'] as TTLUnit[]).map((value) => ({
  value,
  label: t(`admin.requestDataCacheSettings.units.${value}`)
})))
const scopeOptions = computed(() => [
  { value: 'all', label: t('admin.requestDataCacheSettings.allGroups') },
  { value: 'selected', label: t('admin.requestDataCacheSettings.selectedGroups') }
])

// 按能整除的最大单位展示缓存时间，例如 1440 分钟显示为 1 天。
function pickUnit(minutes: number): TTLUnit {
  if (minutes > 0 && minutes % UNIT_MINUTES.days === 0) return 'days'
  if (minutes > 0 && minutes % UNIT_MINUTES.hours === 0) return 'hours'
  return 'minutes'
}

const ttlUnit = ref<TTLUnit>(pickUnit(props.modelValue.ttl_minutes))
const ttlValue = ref<number>(props.modelValue.ttl_minutes / UNIT_MINUTES[ttlUnit.value])

// 外部数据（加载/保存后）变化时同步本地输入；与当前输入等价时不打断编辑。
watch(() => props.modelValue.ttl_minutes, (minutes) => {
  if (ttlValue.value * UNIT_MINUTES[ttlUnit.value] === minutes) return
  ttlUnit.value = pickUnit(minutes)
  ttlValue.value = minutes / UNIT_MINUTES[ttlUnit.value]
})

const ttlMinutes = computed(() => Math.round(Number(ttlValue.value) * UNIT_MINUTES[ttlUnit.value]))
const ttlInvalid = computed(() => !Number.isFinite(ttlMinutes.value) || ttlMinutes.value < 1 || ttlMinutes.value > MAX_TTL_MINUTES)
const scopeEmpty = computed(() => !props.modelValue.all_groups && props.modelValue.group_ids.length === 0 && !props.modelValue.include_ungrouped)

watch([ttlValue, ttlUnit], () => {
  if (!ttlInvalid.value && ttlMinutes.value !== props.modelValue.ttl_minutes) patch({ ttl_minutes: ttlMinutes.value })
})

function patch(partial: Partial<RequestDataCacheScope>) {
  emit('update:modelValue', { ...props.modelValue, ...partial })
}

// 供父组件在保存前判断本卡片输入是否有效。
defineExpose({ invalid: computed(() => props.modelValue.enabled && (ttlInvalid.value || scopeEmpty.value)) })
</script>
