<template>
  <Teleport to="body">
    <div v-if="show && group && position">
      <div class="fixed inset-0 z-[9998]" aria-hidden="true" @click="emit('close')"></div>
      <div
        :id="`group-action-menu-${group.id}`"
        class="fixed z-[9999] w-48 overflow-y-auto rounded-control bg-white shadow-lg ring-1 ring-black/5 dark:bg-dark-800 dark:ring-white/10"
        :style="{ top: `${position.top}px`, left: `${position.left}px`, maxHeight: `calc(100dvh - ${position.top + 8}px)` }"
        role="menu"
        :aria-label="t('common.actions')"
        @click.stop
      >
        <div class="py-1">
          <button
            type="button"
            data-testid="group-duplicate"
            class="menu-item disabled:cursor-not-allowed disabled:opacity-50"
            role="menuitem"
            :title="duplicating ? t('admin.groups.duplicating') : t('admin.groups.duplicate')"
            :disabled="duplicating"
            @click="emitAction('duplicate')"
          >
            <Icon name="copy" size="sm" class="text-blue-500" />
            {{ duplicating ? t('admin.groups.duplicating') : t('admin.groups.duplicate') }}
          </button>
          <button type="button" class="menu-item" role="menuitem" @click="emitAction('rate-multipliers')">
            <Icon name="dollar" size="sm" class="text-violet-500" />
            {{ t('admin.groups.rateMultipliers') }}
          </button>
          <button type="button" class="menu-item" role="menuitem" @click="emitAction('rpm-overrides')">
            <Icon name="bolt" size="sm" class="text-amber-500" />
            {{ t('admin.groups.rpmOverrides') }}
          </button>
          <div class="my-1 border-t border-gray-100 dark:border-dark-700"></div>
          <button type="button" class="menu-item menu-item-danger" role="menuitem" @click="emitAction('delete')">
            <Icon name="trash" size="sm" />
            {{ t('common.delete') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { AdminGroup } from '@/types'

const props = defineProps<{
  show: boolean
  group: AdminGroup | null
  position: { top: number; left: number } | null
  duplicating: boolean
}>()

type GroupAction = 'duplicate' | 'rate-multipliers' | 'rpm-overrides' | 'delete'
const emit = defineEmits<{
  (event: 'close'): void
  (event: GroupAction, group: AdminGroup): void
}>()
const { t } = useI18n()

// 动作仍由页面处理；关闭浮层后不会遮挡后续设置弹窗或删除确认框。
const emitAction = (event: GroupAction) => {
  if (!props.group || (event === 'duplicate' && props.duplicating)) return
  emit(event, props.group)
  emit('close')
}

const handleEscape = (event: KeyboardEvent) => {
  if (props.show && event.key === 'Escape') emit('close')
}

watch(() => props.show, (visible) => {
  if (visible) window.addEventListener('keydown', handleEscape)
  else window.removeEventListener('keydown', handleEscape)
}, { immediate: true })

onUnmounted(() => window.removeEventListener('keydown', handleEscape))
</script>

<style scoped>
.menu-item {
  @apply flex min-h-9 w-full items-center gap-2 px-4 py-2 text-left text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700;
}

.menu-item-danger {
  @apply text-red-600 dark:text-red-400;
}
</style>
