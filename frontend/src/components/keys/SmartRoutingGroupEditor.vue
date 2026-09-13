<template>
  <div class="space-y-3" data-test="smart-routing-group-editor">
    <div
      v-for="(item, index) in selectedGroups"
      :key="item.id"
      class="flex min-w-0 items-center gap-2 rounded-lg border border-gray-200 px-3 py-3 dark:border-dark-600 sm:gap-3"
      :data-test="`smart-routing-row-${index}`"
    >
      <span class="shrink-0 text-sm text-gray-400 dark:text-dark-400">
        {{ t('keys.smartRouting.route', { index: index + 1 }) }}
      </span>
      <div class="min-w-0 flex-1">
        <GroupBadge
          v-if="item.group"
          class="max-w-full"
          :name="item.group.name"
          :platform="item.group.platform"
          :display-brand="item.group.display_brand"
          :rate-multiplier="item.group.rate_multiplier"
          :user-rate-multiplier="userGroupRates[item.id]"
          :peak-rate-enabled="item.group.peak_rate_enabled"
          :peak-start="item.group.peak_start"
          :peak-end="item.group.peak_end"
          :peak-rate-multiplier="item.group.peak_rate_multiplier"
        />
        <span v-else class="text-sm text-gray-500">{{ t('keys.smartRouting.unavailableGroup', { id: item.id }) }}</span>
      </div>
      <div class="flex shrink-0 items-center gap-0.5">
        <button
          type="button"
          class="rounded p-1 text-gray-400 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-30 dark:hover:bg-dark-700"
          :disabled="disabled || index === 0"
          :title="t('keys.smartRouting.moveUp')"
          :aria-label="t('keys.smartRouting.moveUp')"
          :data-test="`smart-routing-up-${index}`"
          @click="moveGroup(index, -1)"
        ><Icon name="arrowUp" size="sm" /></button>
        <button
          type="button"
          class="rounded p-1 text-gray-400 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-30 dark:hover:bg-dark-700"
          :disabled="disabled || index === modelValue.length - 1"
          :title="t('keys.smartRouting.moveDown')"
          :aria-label="t('keys.smartRouting.moveDown')"
          :data-test="`smart-routing-down-${index}`"
          @click="moveGroup(index, 1)"
        ><Icon name="arrowDown" size="sm" /></button>
        <button
          type="button"
          class="rounded p-1 text-gray-400 hover:bg-red-50 hover:text-red-500 disabled:opacity-30 dark:hover:bg-red-900/20"
          :disabled="disabled"
          :title="t('common.delete')"
          :aria-label="t('common.delete')"
          :data-test="`smart-routing-remove-${index}`"
          @click="removeGroup(index)"
        ><Icon name="x" size="sm" /></button>
      </div>
    </div>

    <Select
      :model-value="null"
      :options="availableOptions"
      :placeholder="t('keys.smartRouting.addGroup')"
      :search-placeholder="t('keys.searchGroup')"
      :aria-label="t('keys.smartRouting.addGroup')"
      :searchable="true"
      :disabled="disabled || modelValue.length >= 10 || availableOptions.length === 0"
      data-test="smart-routing-add-group"
      @update:model-value="addGroup"
    >
      <template #option="{ option, selected }">
        <GroupOptionItem
          :name="(option as unknown as RoutingGroupOption).group.name"
          :platform="(option as unknown as RoutingGroupOption).group.platform"
          :display-brand="(option as unknown as RoutingGroupOption).group.display_brand"
          :rate-multiplier="(option as unknown as RoutingGroupOption).group.rate_multiplier"
          :user-rate-multiplier="userGroupRates[(option as unknown as RoutingGroupOption).value]"
          :peak-rate-enabled="(option as unknown as RoutingGroupOption).group.peak_rate_enabled"
          :peak-start="(option as unknown as RoutingGroupOption).group.peak_start"
          :peak-end="(option as unknown as RoutingGroupOption).group.peak_end"
          :peak-rate-multiplier="(option as unknown as RoutingGroupOption).group.peak_rate_multiplier"
          :description="(option as unknown as RoutingGroupOption).group.description"
          :selected="selected"
        />
      </template>
    </Select>
    <p v-if="modelValue.length >= 10" class="text-xs text-gray-500 dark:text-dark-400">
      {{ t('keys.smartRouting.limitReached') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import GroupOptionItem from '@/components/common/GroupOptionItem.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Group } from '@/types'

const props = withDefaults(defineProps<{
  modelValue: number[]
  groups: Group[]
  existingGroups?: Group[]
  userGroupRates?: Record<number, number>
  disabled?: boolean
}>(), {
  existingGroups: () => [],
  userGroupRates: () => ({}),
  disabled: false
})
const emit = defineEmits<{ 'update:modelValue': [value: number[]] }>()
const { t } = useI18n()

interface RoutingGroupOption extends Record<string, unknown> {
  value: number
  label: string
  description: string | null
  displayBrand?: string | null
  group: Group
}

// 候选显示始终按持久化顺序排列；旧摘要仅用于展示，不能加入当前无权限的分组。
const groupByID = computed(() => new Map([...props.existingGroups, ...props.groups].map((group) => [group.id, group])))
const selectedGroups = computed(() => props.modelValue.map((id) => ({ id, group: groupByID.value.get(id) })))
const availableOptions = computed<RoutingGroupOption[]>(() => props.groups
  .filter((group) => !props.modelValue.includes(group.id))
  .map((group) => ({ value: group.id, label: group.name, description: group.description, displayBrand: group.display_brand, group })))

// 同时约束交互入口，避免重复事件或组件事件绕过最多十组的限制。
const addGroup = (value: string | number | boolean | null) => {
  if (props.disabled || typeof value !== 'number' || props.modelValue.length >= 10) return
  if (!availableOptions.value.some((option) => option.value === value)) return
  emit('update:modelValue', [...props.modelValue, value])
}

const moveGroup = (index: number, offset: -1 | 1) => {
  const target = index + offset
  if (props.disabled || target < 0 || target >= props.modelValue.length) return
  const next = [...props.modelValue]
  const [id] = next.splice(index, 1)
  if (id === undefined) return
  next.splice(target, 0, id)
  emit('update:modelValue', next)
}

const removeGroup = (index: number) => {
  if (props.disabled) return
  emit('update:modelValue', props.modelValue.filter((_, itemIndex) => itemIndex !== index))
}
</script>
