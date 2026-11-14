<template>
  <!-- 数据缓存设置：普通请求与错误请求的端点信息缓存分开配置，支持手动清除 -->
  <section class="card" data-testid="request-data-cache-settings">
    <div class="flex flex-wrap items-start justify-between gap-4 border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <div class="min-w-0">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.requestDataCacheSettings.title') }}</h2>
        <p class="mt-1 text-sm text-gray-500">{{ t('admin.requestDataCacheSettings.description') }}</p>
      </div>
      <div class="flex shrink-0 gap-2">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || busy" @click="load">
          <Icon name="refresh" size="sm" class="mr-1" />{{ t('common.refresh') }}
        </button>
        <button type="button" class="btn btn-danger btn-sm" :disabled="loading || busy" data-testid="data-cache-clear-all" @click="askClear('all')">
          {{ t('admin.requestDataCacheSettings.clearAll') }}
        </button>
      </div>
    </div>

    <div v-if="loading" class="p-10 text-center text-gray-500" role="status">{{ t('common.loading') }}</div>
    <div v-else-if="form" class="space-y-6 p-6">
      <RequestDataCacheScopeCard
        ref="normalCard"
        v-model="form.normal"
        kind="normal"
        :title="t('admin.requestDataCacheSettings.normalTitle')"
        :hint="t('admin.requestDataCacheSettings.normalHint')"
        :groups="groups"
        :count="stats.normal"
        :disabled="busy"
        @clear="askClear('normal')"
      />
      <RequestDataCacheScopeCard
        ref="errorCard"
        v-model="form.error"
        kind="error"
        :title="t('admin.requestDataCacheSettings.errorTitle')"
        :hint="t('admin.requestDataCacheSettings.errorHint')"
        :groups="groups"
        :count="stats.error"
        :disabled="busy"
        @clear="askClear('error')"
      />
      <div class="flex justify-end">
        <button type="button" class="btn btn-primary" :disabled="busy || invalid" data-testid="data-cache-save" @click="save">
          {{ t('common.save') }}
        </button>
      </div>
    </div>

    <ConfirmDialog
      :show="clearTarget !== null"
      :title="t('admin.requestDataCacheSettings.clearConfirmTitle')"
      :message="clearTarget === 'all' ? t('admin.requestDataCacheSettings.clearAllConfirm') : t('admin.requestDataCacheSettings.clearConfirm', { target: clearTargetLabel })"
      :confirm-text="t('admin.requestDataCacheSettings.clear')"
      :loading="busy"
      danger
      @confirm="confirmClear"
      @cancel="clearTarget = null"
    />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import RequestDataCacheScopeCard from './RequestDataCacheScopeCard.vue'
import { requestDataCacheAPI, type RequestDataCacheKind, type RequestDataCacheSettings, type RequestDataCacheStats, type RequestDataCacheView } from '@/api/admin/requestDataCache'
import { groupsAPI } from '@/api/admin/groups'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { AdminGroup } from '@/types'

const { t } = useI18n()
const app = useAppStore()

const loading = ref(true)
const busy = ref(false)
const form = ref<RequestDataCacheSettings | null>(null)
const stats = ref<RequestDataCacheStats>({ normal: 0, error: 0 })
const groups = ref<AdminGroup[]>([])
const clearTarget = ref<RequestDataCacheKind | null>(null)
const normalCard = ref<{ invalid: boolean } | null>(null)
const errorCard = ref<{ invalid: boolean } | null>(null)

const invalid = computed(() => Boolean(normalCard.value?.invalid || errorCard.value?.invalid))
const clearTargetLabel = computed(() => {
  if (clearTarget.value === 'normal') return t('admin.requestDataCacheSettings.targetNormal')
  if (clearTarget.value === 'error') return t('admin.requestDataCacheSettings.targetError')
  return t('admin.requestDataCacheSettings.targetAll')
})

// 用服务端返回的规范化结果覆盖本地表单，保证分组列表等字段始终有值。
function applyView(view: RequestDataCacheView) {
  form.value = {
    normal: { ...view.settings.normal, group_ids: [...(view.settings.normal.group_ids ?? [])] },
    error: { ...view.settings.error, group_ids: [...(view.settings.error.group_ids ?? [])] }
  }
  stats.value = view.stats
}

async function load() {
  loading.value = true
  try {
    const [view, allGroups] = await Promise.all([requestDataCacheAPI.get(), groupsAPI.getAllIncludingInactive()])
    applyView(view)
    groups.value = allGroups
  } catch (err) {
    app.showError(extractApiErrorMessage(err, t('admin.requestDataCacheSettings.loadFailed')))
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!form.value || invalid.value) return
  busy.value = true
  try {
    applyView(await requestDataCacheAPI.update(form.value))
    app.showSuccess(t('admin.requestDataCacheSettings.saved'))
  } catch (err) {
    app.showError(extractApiErrorMessage(err, t('admin.requestDataCacheSettings.saveFailed')))
  } finally {
    busy.value = false
  }
}

function askClear(kind: RequestDataCacheKind) {
  clearTarget.value = kind
}

// 清除只刷新统计，不覆盖尚未保存的表单修改。
async function confirmClear() {
  const kind = clearTarget.value
  if (!kind) return
  busy.value = true
  try {
    const result = await requestDataCacheAPI.clear(kind)
    stats.value = result.stats
    // 清除全部为整表 TRUNCATE，条数是统计估算值。
    app.showSuccess(t(kind === 'all' ? 'admin.requestDataCacheSettings.clearedAll' : 'admin.requestDataCacheSettings.cleared', { count: result.deleted }))
    clearTarget.value = null
  } catch (err) {
    app.showError(extractApiErrorMessage(err, t('admin.requestDataCacheSettings.clearFailed')))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>
