<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.imageModelCooldowns.title')"
    width="normal"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <p class="text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.imageModelCooldowns.description') }}
      </p>

      <div v-if="loading" class="flex items-center justify-center py-8">
        <Icon name="refresh" size="md" class="animate-spin text-gray-400" />
      </div>

      <div
        v-else-if="entries.length === 0"
        class="rounded-lg border border-gray-200 p-4 text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400"
      >
        {{ t('admin.accounts.imageModelCooldowns.empty') }}
      </div>

      <div v-else class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-600">
        <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-600">
          <thead class="bg-gray-50 dark:bg-dark-700">
            <tr>
              <th class="px-3 py-2 text-left font-medium text-gray-600 dark:text-gray-300">{{ t('admin.accounts.imageModelCooldowns.model') }}</th>
              <th class="px-3 py-2 text-left font-medium text-gray-600 dark:text-gray-300">{{ t('admin.accounts.imageModelCooldowns.operation') }}</th>
              <th class="px-3 py-2 text-left font-medium text-gray-600 dark:text-gray-300">{{ t('admin.accounts.imageModelCooldowns.remaining') }}</th>
              <th class="px-3 py-2 text-left font-medium text-gray-600 dark:text-gray-300">{{ t('admin.accounts.imageModelCooldowns.failures') }}</th>
              <th class="px-3 py-2 text-right font-medium text-gray-600 dark:text-gray-300">{{ t('admin.accounts.imageModelCooldowns.actions') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="entry in entries" :key="entry.model + '|' + entry.operation">
              <td class="px-3 py-2 font-mono text-xs text-gray-900 dark:text-gray-100">{{ entry.model }}</td>
              <td class="px-3 py-2 text-gray-700 dark:text-gray-300">{{ operationLabel(entry.operation) }}</td>
              <td class="px-3 py-2 text-gray-700 dark:text-gray-300">{{ remainingText(entry.remaining_seconds) }}</td>
              <td class="px-3 py-2 text-gray-700 dark:text-gray-300">{{ entry.failures || '-' }}</td>
              <td class="px-3 py-2 text-right">
                <button
                  type="button"
                  class="btn btn-secondary btn-sm"
                  :disabled="clearingKey !== ''"
                  @click="handleClear(entry)"
                >
                  {{ t('admin.accounts.imageModelCooldowns.clear') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="loading" @click="loadEntries">
          {{ t('common.refresh') }}
        </button>
        <button type="button" class="btn btn-primary" @click="emit('close')">
          {{ t('common.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { ImageModelCooldownEntry } from '@/api/admin/accounts'
import type { Account } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { Icon } from '@/components/icons'

const props = defineProps<{
  show: boolean
  account: Account | null
}>()

const emit = defineEmits<{
  close: []
}>()

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const entries = ref<ImageModelCooldownEntry[]>([])
const clearingKey = ref('')
// 切换账号或关闭弹窗后，丢弃旧账号迟到的结果。
let requestVersion = 0

const operationLabel = (operation: string) => {
  const key = `admin.accounts.imageModelCooldowns.operations.${operation}`
  const label = t(key)
  return label === key ? operation : label
}

// 剩余时间按分钟展示，不足一分钟显示秒。
const remainingText = (seconds: number) => {
  if (seconds < 60) return t('admin.accounts.imageModelCooldowns.seconds', { seconds })
  return t('admin.accounts.imageModelCooldowns.minutes', { minutes: Math.ceil(seconds / 60) })
}

const loadEntries = async () => {
  if (!props.account) return
  const version = ++requestVersion
  loading.value = true
  try {
    const result = await adminAPI.accounts.getImageModelCooldowns(props.account.id)
    if (version === requestVersion) entries.value = result
  } catch (error: any) {
    if (version !== requestVersion) return
    entries.value = []
    appStore.showError(error?.message || t('admin.accounts.imageModelCooldowns.loadFailed'))
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

const handleClear = async (entry: ImageModelCooldownEntry) => {
  if (!props.account) return
  clearingKey.value = entry.model + '|' + entry.operation
  try {
    await adminAPI.accounts.clearImageModelCooldown(props.account.id, entry.model, entry.operation)
    appStore.showSuccess(t('admin.accounts.imageModelCooldowns.cleared'))
    await loadEntries()
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accounts.imageModelCooldowns.clearFailed'))
  } finally {
    clearingKey.value = ''
  }
}

watch(
  () => [props.show, props.account?.id],
  ([visible], _, onCleanup) => {
    onCleanup(() => { requestVersion++ })
    entries.value = []
    if (visible && props.account) loadEntries()
  }
)
</script>
