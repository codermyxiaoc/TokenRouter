<template>
  <AppLayout>
    <div class="min-w-0 space-y-6">
      <div class="flex flex-wrap items-start justify-between gap-3"><div><h1 class="page-title">{{ t('intelligence.title') }}</h1><p class="page-description">{{ t('intelligence.description') }}</p></div><button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load">{{ t('common.refresh') }}</button></div>
      <div v-if="loading && !tests.length" role="status" class="py-16 text-center text-gray-500">{{ t('common.loading') }}</div>
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>
      <section v-if="!loading && !error && !groups.length" class="card px-6 py-16 text-center"><h2 class="font-semibold">{{ t('intelligence.empty') }}</h2><p class="mt-2 text-sm text-gray-500">{{ t('intelligence.emptyHint') }}</p></section>
      <section v-for="group in groups" :key="group.id" class="card min-w-0 p-4 sm:p-6" :data-group-id="group.id">
        <h2 class="break-words text-xl font-semibold text-gray-900 dark:text-white">{{ group.name }}</h2>
        <div v-for="model in group.models" :key="model.name" class="mt-6 min-w-0 border-t border-gray-100 pt-5 dark:border-dark-700">
          <h3 class="mb-4 break-all text-base font-semibold text-gray-800 dark:text-gray-100">{{ model.name }}</h3>
          <div v-for="test in model.tests" :key="test.id" class="mb-6 min-w-0 last:mb-0" :data-test-id="test.id">
            <h4 class="mb-2 text-sm font-medium text-gray-500 dark:text-gray-400">{{ t(`intelligence.${test.benchmark}`) }}</h4>
            <IntelligenceResultBar :runs="test.runs || []" @select="openRun" />
            <template v-if="test.benchmark === 'drawing' && recentArtifacts(test.artifacts || []).length">
              <p class="mb-3 mt-5 text-xs text-gray-500">{{ t('intelligence.artworksHint') }}</p>
              <!-- 隐藏样例滚动条，保留横向滚动与触摸滑动。 -->
              <div class="scrollbar-hide flex min-w-0 snap-x gap-4 overflow-x-auto overscroll-x-contain pb-3" data-testid="drawing-gallery">
                <article v-for="artifact in recentArtifacts(test.artifacts || [])" :key="artifact.id" class="relative w-[min(78vw,360px)] shrink-0 snap-start overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-600">
                  <div class="pointer-events-none h-56 overflow-hidden"><IntelligenceDrawing :run-id="artifact.id" :title="`${model.name} · ${formatDateTime(artifact.created_at)}`" /></div>
                  <span class="absolute right-2 top-2 rounded-md px-2 py-1 text-xs font-semibold text-white shadow-sm" :class="resultColor(artifact)">{{ t(`intelligence.statuses.${resultStatus(artifact)}`) }}</span>
                  <button type="button" class="flex w-full items-center justify-between gap-3 border-t border-gray-100 px-3 py-3 text-left text-xs hover:bg-gray-50 dark:border-dark-700 dark:bg-dark-800 dark:hover:bg-dark-700" @click="openRun(artifact)"><span class="text-gray-500">{{ formatDateTime(artifact.created_at) }}</span><span class="font-medium text-primary-600 dark:text-primary-400">{{ t('intelligence.preview') }}</span></button>
                </article>
              </div>
            </template>
          </div>
        </div>
      </section>
      <IntelligenceRunDialog :show="showDetail" :run="selectedRun" :loading="detailLoading" :error="detailError" @close="closeDetail" />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import IntelligenceResultBar from '@/components/intelligence/IntelligenceResultBar.vue'
import IntelligenceDrawing from '@/components/intelligence/IntelligenceDrawing.vue'
import IntelligenceRunDialog from '@/components/intelligence/IntelligenceRunDialog.vue'
import { groupTests, recentArtifacts, resultColor, resultStatus } from '@/components/intelligence/results'
import { intelligenceAPI, type IntelligenceRun, type IntelligenceTest } from '@/api/intelligence'
import { formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import { provideIntelligenceResults } from '@/components/intelligence/useIntelligenceResults'

const { t } = useI18n()
const results = provideIntelligenceResults()
const tests = ref<IntelligenceTest[]>([])
const loading = ref(false)
const error = ref('')
const groups = computed(() => groupTests(tests.value))
const showDetail = ref(false)
const selectedRun = ref<IntelligenceRun | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
let listController: AbortController | null = null
let detailController: AbortController | null = null
let refreshTimer: ReturnType<typeof setInterval> | undefined

async function load() {
  listController?.abort()
  const controller = new AbortController()
  listController = controller
  loading.value = true
  error.value = ''
  try { const result = await intelligenceAPI.list(controller.signal); if (!controller.signal.aborted) tests.value = result }
  catch (err) { if (!controller.signal.aborted) { tests.value = []; error.value = extractApiErrorMessage(err, t('intelligence.loadFailed')) } }
  finally { if (!controller.signal.aborted) loading.value = false }
}

async function openRun(run: IntelligenceRun) {
  detailController?.abort()
  const controller = new AbortController()
  detailController = controller
  selectedRun.value = null
  detailError.value = ''
  detailLoading.value = true
  showDetail.value = true
  // 切换记录或关闭弹窗后，旧响应不能覆盖当前详情。
  try { const result = await results.detail(run.id); if (!controller.signal.aborted) selectedRun.value = result }
  catch (err) { if (!controller.signal.aborted) detailError.value = extractApiErrorMessage(err, t('intelligence.loadFailed')) }
  finally { if (!controller.signal.aborted) detailLoading.value = false }
}
function closeDetail() { detailController?.abort(); showDetail.value = false; selectedRun.value = null }
onMounted(() => {
  void load()
  // 自动刷新仅读取结果；隐藏页面及查看详情时暂停，避免打断作品阅读。
  refreshTimer = setInterval(() => { if (!document.hidden && !showDetail.value && !loading.value) void load() }, 30000)
})
onBeforeUnmount(() => { listController?.abort(); detailController?.abort(); if (refreshTimer) clearInterval(refreshTimer) })
</script>
